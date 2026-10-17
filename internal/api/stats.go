package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/smitt14ua/ocap-statistics/internal/report"
	"github.com/smitt14ua/ocap-statistics/internal/settings"
	"github.com/smitt14ua/ocap-statistics/internal/store"
)

// --- query parameters ---------------------------------------------------------------

const dayLayout = "2006-01-02"

// filter reads from, to, tag and min_duration.
func (s *Server) filter(r *http.Request) (store.Filter, error) {
	q := r.URL.Query()
	set := s.settings.Get()
	f := store.Filter{From: "0000-01-01", To: "9999-12-31", Tag: set.DefaultTag, MinDuration: set.MinDuration}
	for _, p := range []struct {
		name string
		dst  *string
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := q.Get(p.name); v != "" {
			if _, err := time.Parse(dayLayout, v); err != nil {
				return f, badRequest("%s: expected YYYY-MM-DD", p.name)
			}
			*p.dst = v
		}
	}
	if q.Has("tag") {
		f.Tag = q.Get("tag")
		if f.Tag == "*" || strings.EqualFold(f.Tag, "all") {
			f.Tag = ""
		}
	}
	if v := q.Get("min_duration"); v != "" {
		d, err := strconv.ParseFloat(v, 64)
		if err != nil || d < 0 || d > 1e7 {
			return f, badRequest("min_duration: expected seconds")
		}
		f.MinDuration = d
	}
	return f, nil
}

type filterOut struct {
	From        string  `json:"from"`
	To          string  `json:"to"`
	Tag         string  `json:"tag"`
	MinDuration float64 `json:"minDuration"`
}

func outFilter(f store.Filter) filterOut {
	return filterOut{From: f.From, To: f.To, Tag: f.Tag, MinDuration: f.MinDuration}
}

// report returns the aggregation for a filter, computed once per data generation.
func (s *Server) report(ctx context.Context, f store.Filter) (built, error) {
	return s.results.get(ctx, s.store.Generation(), f, func(ctx context.Context) (built, error) {
		missions, err := s.store.Missions(ctx, f)
		if err != nil {
			return built{}, err
		}
		rows, err := s.store.Rows(ctx, f, "")
		if err != nil {
			return built{}, err
		}
		longest, err := s.store.LongestKill(ctx, f)
		if err != nil {
			return built{}, err
		}
		set := s.settings.Get()
		res := report.Build(missions, rows)
		res.ApplyColumns(set.Columns)
		res.Hide(set.MinMissions)
		return built{res: res, longest: longest}, nil
	})
}

type listParams struct {
	sort   string
	desc   bool
	limit  int
	offset int
	search string
	squads []string // display names, "" for no squad
	csv    bool
}

// parseList reads list parameters; def is the configured default sort of the table.
func parseList(r *http.Request, def settings.SortOrder) (listParams, error) {
	q := r.URL.Query()
	p := listParams{sort: def.Field, desc: def.Order != "asc", search: strings.ToLower(strings.TrimSpace(q.Get("search")))}
	if v := q.Get("sort"); v != "" {
		p.sort = v
		p.desc = true // an explicit sort without order is descending
	}
	switch q.Get("order") {
	case "":
	case "desc":
		p.desc = true
	case "asc":
		p.desc = false
	default:
		return p, badRequest("order: asc or desc")
	}
	for _, name := range []string{"limit", "offset"} {
		if v := q.Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return p, badRequest("%s: expected a non-negative integer", name)
			}
			if name == "limit" {
				p.limit = n
			} else {
				p.offset = n
			}
		}
	}
	for _, v := range q["squad"] {
		for sq := range strings.SplitSeq(v, ",") {
			sq = strings.TrimSpace(sq)
			if sq == report.NoSquad {
				sq = ""
			}
			p.squads = append(p.squads, sq)
		}
	}
	switch q.Get("format") {
	case "", "json":
	case "csv":
		p.csv = true
	default:
		return p, badRequest("format: json or csv")
	}
	return p, nil
}

func (p listParams) wantSquad(name string) bool {
	if len(p.squads) == 0 {
		return true
	}
	for _, s := range p.squads {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}

func page[T any](items []T, p listParams) []T {
	if p.offset >= len(items) {
		return []T{}
	}
	items = items[p.offset:]
	if p.limit > 0 && p.limit < len(items) {
		items = items[:p.limit]
	}
	return items
}

func csvResponse(filename string, header []string, rows [][]string) (response, error) {
	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF") // BOM so that spreadsheet apps detect UTF-8
	w := csv.NewWriter(&buf)
	_ = w.Write(header)
	_ = w.WriteAll(rows)
	return response{contentType: "text/csv; charset=utf-8", body: buf.Bytes(), filename: filename}, w.Error()
}

// publicColumn is a custom column as published to the web page and API clients.
type publicColumn struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Formula  string `json:"formula"`
	Decimals int    `json:"decimals"`
	Target   string `json:"target"`
}

func publicColumns(cols []report.Column) []publicColumn {
	out := make([]publicColumn, 0, len(cols))
	for _, c := range cols {
		out = append(out, publicColumn{ID: c.ID, Name: c.Name, Formula: c.Formula, Decimals: c.Decimals, Target: c.Target})
	}
	return out
}

// withCustomCSV appends custom columns (except those for the excluded target) to a CSV.
func (s *Server) withCustomCSV(header []string, rows [][]string, exclude string, custom func(int) map[string]*float64) ([]string, [][]string) {
	for _, c := range s.settings.Get().Columns {
		if c.Target == exclude {
			continue
		}
		header = append(header, c.Name)
		for i := range rows {
			v := ""
			if p := custom(i)[c.ID]; p != nil {
				v = ff(*p)
			}
			rows[i] = append(rows[i], v)
		}
	}
	return header, rows
}

func ff(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
func fi(v int) string     { return strconv.Itoa(v) }

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// --- service endpoints ------------------------------------------------------------------

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	counts, err := s.store.Counts(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"missions":   counts,
		"sync":       s.syncer.Status(),
		"generation": s.store.Generation(),
		"build":      s.build,
	})
}

func (s *Server) publicConfig(w http.ResponseWriter, r *http.Request) {
	set := s.settings.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"title":       s.cfg.UI.Title,
		"logoUrl":     s.cfg.UI.LogoURL,
		"ocapUrl":     s.cfg.UI.OCAPURL,
		"defaultTag":  set.DefaultTag,
		"columns":     publicColumns(set.Columns),
		"sort":        set.Sort,
		"minDuration": set.MinDuration,
		"minMissions": set.MinMissions,
		"rotation":    set.Rotation,
	})
}

func (s *Server) triggerSync(w http.ResponseWriter, r *http.Request) {
	if !s.validAdminToken(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	s.syncer.Trigger()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

// --- statistics endpoints ----------------------------------------------------------------

// overview returns everything the web UI needs for one date range in a single call.
func (s *Server) overview(r *http.Request) (response, error) {
	f, err := s.filter(r)
	if err != nil {
		return response{}, err
	}
	b, err := s.report(r.Context(), f)
	if err != nil {
		return response{}, err
	}
	return jsonResponse(map[string]any{
		"filter":       outFilter(f),
		"totals":       b.res.Totals,
		"players":      nonNil(b.res.Players),
		"squads":       nonNil(b.res.Squads),
		"achievements": b.res.Achievements(b.longest),
		"updatedAt":    time.Now().UTC(),
	})
}

func (s *Server) missions(r *http.Request) (response, error) {
	f, err := s.filter(r)
	if err != nil {
		return response{}, err
	}
	b, err := s.report(r.Context(), f)
	if err != nil {
		return response{}, err
	}
	ms := slices.Clone(b.res.Missions)
	slices.Reverse(ms)
	return jsonResponse(map[string]any{"filter": outFilter(f), "total": len(ms), "missions": nonNil(ms)})
}

func (s *Server) mission(r *http.Request) (response, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return response{}, badRequest("id: expected an OCAP recording id")
	}
	ctx := r.Context()
	m, err := s.store.Mission(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return response{}, notFound("mission")
	} else if err != nil {
		return response{}, err
	}
	rows, err := s.store.MissionRows(ctx, id)
	if err != nil {
		return response{}, err
	}
	res := report.Build([]store.Mission{m}, rows)
	kills, err := s.store.Kills(ctx, store.Filter{}, store.KillQuery{MissionID: id})
	if err != nil {
		return response{}, err
	}
	type missionPlayer struct {
		Name              string `json:"name"`
		Squad             string `json:"squad"`
		SquadRaw          string `json:"squadRaw"`
		Side              string `json:"side"`
		Kills             int    `json:"kills"`
		Deaths            int    `json:"deaths"`
		TeamKills         int    `json:"teamKills"`
		VehiclesDestroyed int    `json:"vehiclesDestroyed"`
		KillAssists       int    `json:"killAssists"`
		ShotsFired        int    `json:"shotsFired"`
		Hits              int    `json:"hits"`
	}
	players := make([]missionPlayer, 0, len(rows))
	for _, row := range rows {
		players = append(players, missionPlayer{
			Name: row.Name, Squad: row.Squad, SquadRaw: row.SquadRaw, Side: row.Side,
			Kills: row.Kills, Deaths: row.Deaths, TeamKills: row.TeamKills, VehiclesDestroyed: row.VehicleKills,
			KillAssists: row.KillAssists, ShotsFired: row.Shots, Hits: row.Hits,
		})
	}
	return jsonResponse(map[string]any{
		"mission": m, "counted": m.Duration >= s.settings.Get().MinDuration,
		"players": players, "squads": nonNil(res.Squads), "kills": nonNil(kills),
	})
}

func (s *Server) players(r *http.Request) (response, error) {
	f, err := s.filter(r)
	if err != nil {
		return response{}, err
	}
	p, err := parseList(r, s.settings.Get().Sort.Players)
	if err != nil {
		return response{}, err
	}
	b, err := s.report(r.Context(), f)
	if err != nil {
		return response{}, err
	}
	list := make([]report.Player, 0, len(b.res.Players))
	for _, pl := range b.res.Players {
		if p.search != "" && !strings.Contains(strings.ToLower(pl.Name), p.search) {
			continue
		}
		if !p.wantSquad(pl.Squad) {
			continue
		}
		list = append(list, pl)
	}
	report.SortPlayers(list, p.sort, p.desc)
	total := len(list)
	list = page(list, p)
	if p.csv {
		rows := make([][]string, 0, len(list))
		for i, pl := range list {
			rows = append(rows, []string{fi(p.offset + i + 1), pl.Name, pl.SquadRaw, fi(pl.Kills), fi(pl.Deaths), fi(pl.TeamKills),
				fi(pl.Attendance), ff(pl.Accuracy), fi(pl.VehiclesDestroyed), fi(pl.KillAssists), ff(pl.KD)})
		}
		header := []string{"rank", "name", "squad", "kills", "deaths", "team_kills", "attendance", "accuracy", "vehicles_destroyed", "kill_assists", "kd"}
		header, rows = s.withCustomCSV(header, rows, report.TargetSquads, func(i int) map[string]*float64 { return list[i].Custom })
		return csvResponse("players.csv", header, rows)
	}
	return jsonResponse(map[string]any{"filter": outFilter(f), "missions": len(b.res.Missions), "total": total, "players": list})
}

type missionRow struct {
	MissionID         int64  `json:"missionId"`
	Filename          string `json:"filename"`
	Date              string `json:"date"`
	Name              string `json:"name"`
	World             string `json:"world"`
	Squad             string `json:"squad"`
	SquadRaw          string `json:"squadRaw"`
	Side              string `json:"side"`
	Kills             int    `json:"kills"`
	Deaths            int    `json:"deaths"`
	TeamKills         int    `json:"teamKills"`
	VehiclesDestroyed int    `json:"vehiclesDestroyed"`
	KillAssists       int    `json:"killAssists"`
	ShotsFired        int    `json:"shotsFired"`
	Hits              int    `json:"hits"`
}

func (s *Server) player(r *http.Request) (response, error) {
	name := r.PathValue("name")
	f, err := s.filter(r)
	if err != nil {
		return response{}, err
	}
	ctx := r.Context()
	b, err := s.report(ctx, f)
	if err != nil {
		return response{}, err
	}
	res := b.res
	pl, ok := res.FindPlayer(name)
	if !ok {
		return response{}, notFound("player")
	}
	rows, err := s.store.Rows(ctx, f, name)
	if err != nil {
		return response{}, err
	}
	kills, err := s.store.Kills(ctx, f, store.KillQuery{Player: name})
	if err != nil {
		return response{}, err
	}

	missionByID := make(map[int64]store.Mission, len(res.Missions))
	for _, m := range res.Missions {
		missionByID[m.ID] = m
	}
	missions := make([]missionRow, 0, len(rows))
	squadCount := counter{}
	for _, row := range slices.Backward(rows) {
		m := missionByID[row.MissionID]
		missions = append(missions, missionRow{
			MissionID: row.MissionID, Filename: m.Filename, Date: row.Date, Name: m.Name, World: m.World,
			Squad: res.SquadName(row.Squad), SquadRaw: row.SquadRaw, Side: row.Side,
			Kills: row.Kills, Deaths: row.Deaths, TeamKills: row.TeamKills, VehiclesDestroyed: row.VehicleKills,
			KillAssists: row.KillAssists, ShotsFired: row.Shots, Hits: row.Hits,
		})
		squadCount.add(res.SquadName(row.Squad))
	}

	weapons, victims, killers := counter{}, counter{}, counter{}
	var longest *store.Kill
	for i, k := range kills {
		if k.Killer == name && k.KillerIsPlayer {
			if k.Weapon != "" {
				weapons.add(k.Weapon)
			}
			if k.VictimKind == "player" {
				victims.add(k.Victim)
			}
			if store.CountsForLongestKill(k.KillRow) && (longest == nil || k.Distance > longest.Distance) {
				longest = &kills[i]
			}
		}
		if k.Victim == name && k.VictimKind == "player" && k.Killer != "" && k.Killer != name {
			killers.add(k.Killer)
		}
	}

	return jsonResponse(map[string]any{
		"filter":      outFilter(f),
		"player":      pl,
		"squads":      squadCount.top(0),
		"weapons":     weapons.top(10),
		"victims":     victims.top(10),
		"killedBy":    killers.top(10),
		"longestKill": longest,
		"missions":    missions,
	})
}

type counter map[string]int

func (c counter) add(k string) { c[k]++ }

type countItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func (c counter) top(n int) []countItem {
	out := make([]countItem, 0, len(c))
	for k, v := range c {
		out = append(out, countItem{k, v})
	}
	slices.SortFunc(out, func(a, b countItem) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Name, b.Name)
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

func (s *Server) squads(r *http.Request) (response, error) {
	f, err := s.filter(r)
	if err != nil {
		return response{}, err
	}
	p, err := parseList(r, s.settings.Get().Sort.Squads)
	if err != nil {
		return response{}, err
	}
	b, err := s.report(r.Context(), f)
	if err != nil {
		return response{}, err
	}
	list := make([]report.Squad, 0, len(b.res.Squads))
	for _, sq := range b.res.Squads {
		if p.search != "" && !strings.Contains(strings.ToLower(sq.Name), p.search) {
			continue
		}
		if !p.wantSquad(sq.Name) {
			continue
		}
		list = append(list, sq)
	}
	report.SortSquads(list, p.sort, p.desc)
	total := len(list)
	list = page(list, p)
	if p.csv {
		rows := make([][]string, 0, len(list))
		for i, sq := range list {
			name := sq.Name
			if name == "" {
				name = report.NoSquad
			}
			rows = append(rows, []string{fi(p.offset + i + 1), name, fi(sq.Players), fi(sq.Kills), fi(sq.Deaths), fi(sq.TeamKills),
				ff(sq.AvgAttendance), ff(sq.Accuracy), fi(sq.VehiclesDestroyed), fi(sq.KillAssists), ff(sq.KD)})
		}
		header := []string{"rank", "squad", "players", "kills", "deaths", "team_kills", "avg_attendance", "accuracy", "vehicles_destroyed", "kill_assists", "kd"}
		header, rows = s.withCustomCSV(header, rows, report.TargetPlayers, func(i int) map[string]*float64 { return list[i].Custom })
		return csvResponse("squads.csv", header, rows)
	}
	return jsonResponse(map[string]any{"filter": outFilter(f), "missions": len(b.res.Missions), "total": total, "squads": list})
}

func (s *Server) squad(r *http.Request) (response, error) {
	f, err := s.filter(r)
	if err != nil {
		return response{}, err
	}
	b, err := s.report(r.Context(), f)
	if err != nil {
		return response{}, err
	}
	sq, ok := b.res.FindSquad(r.PathValue("name"))
	if !ok {
		return response{}, notFound("squad")
	}
	members := b.res.SquadMembers(sq.Name, s.settings.Get().Columns)
	report.SortPlayers(members, "kills", true)
	return jsonResponse(map[string]any{"filter": outFilter(f), "squad": sq, "members": nonNil(members)})
}

func (s *Server) achievements(r *http.Request) (response, error) {
	f, err := s.filter(r)
	if err != nil {
		return response{}, err
	}
	b, err := s.report(r.Context(), f)
	if err != nil {
		return response{}, err
	}
	return jsonResponse(map[string]any{"filter": outFilter(f), "achievements": b.res.Achievements(b.longest)})
}

func (s *Server) rotation(r *http.Request) (response, error) {
	f, err := s.filter(r)
	if err != nil {
		return response{}, err
	}
	b, err := s.report(r.Context(), f)
	if err != nil {
		return response{}, err
	}
	return jsonResponse(map[string]any{"filter": outFilter(f), "rotation": b.res.Rotation(s.settings.Get().Rotation)})
}
