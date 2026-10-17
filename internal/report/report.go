// Package report aggregates stored per-mission statistics into player, squad, achievement
// and rotation reports for a date range.
package report

import (
	"cmp"
	"math"
	"slices"
	"strings"

	"github.com/smitt14ua/ocap-statistics/internal/config"
	"github.com/smitt14ua/ocap-statistics/internal/store"
)

// NoSquad stands for "players without a squad" in URLs, query parameters and CSV.
const NoSquad = "-"

type Player struct {
	Name              string  `json:"name"`
	Squad             string  `json:"squad"`    // normalised squad of the latest mission with a squad
	SquadRaw          string  `json:"squadRaw"` // squad as captured from the nickname, before aliases
	Kills             int     `json:"kills"`
	Deaths            int     `json:"deaths"`
	TeamKills         int     `json:"teamKills"`
	VehiclesDestroyed int     `json:"vehiclesDestroyed"`
	KillAssists       int     `json:"killAssists"`
	ShotsFired        int     `json:"shotsFired"`
	Hits              int     `json:"hits"`
	Attendance        int     `json:"attendance"`
	Accuracy          float64 `json:"accuracy"`
	KD                float64 `json:"kd"` // kills / deaths (kills when there are no deaths)
	FirstSeen         string  `json:"firstSeen"`
	LastSeen          string  `json:"lastSeen"`
	// Custom holds custom column values by column id (null when undefined, e.g. x/0).
	Custom map[string]*float64 `json:"custom,omitempty"`
}

type Squad struct {
	Name              string  `json:"name"` // "" for players without a squad
	Players           int     `json:"players"`
	Missions          int     `json:"missions"`
	Attendance        int     `json:"attendance"` // player-missions
	AvgAttendance     float64 `json:"avgAttendance"`
	Kills             int     `json:"kills"`
	Deaths            int     `json:"deaths"`
	TeamKills         int     `json:"teamKills"`
	VehiclesDestroyed int     `json:"vehiclesDestroyed"`
	KillAssists       int     `json:"killAssists"`
	ShotsFired        int     `json:"shotsFired"`
	Hits              int     `json:"hits"`
	Accuracy          float64 `json:"accuracy"`
	KD                float64 `json:"kd"`
	Score             float64 `json:"score"` // confidence-weighted K/D used for the "best squad" award
	// Custom holds custom column values by column id.
	Custom map[string]*float64 `json:"custom,omitempty"`
}

// Totals sums all players.
type Totals struct {
	Missions          int `json:"missions"`
	Players           int `json:"players"`
	Squads            int `json:"squads"`
	Kills             int `json:"kills"`
	Deaths            int `json:"deaths"`
	TeamKills         int `json:"teamKills"`
	VehiclesDestroyed int `json:"vehiclesDestroyed"`
	KillAssists       int `json:"killAssists"`
}

// Result is the full aggregation for a filter.
type Result struct {
	Missions []store.Mission
	Players  []Player // sorted by name
	Squads   []Squad  // sorted by name, solo squad last
	Totals   Totals
	// squadKey maps a lower-cased squad to its display name.
	squadKey map[string]string
	rows     []store.Row
}

// Build aggregates rows (ordered by date) of the given missions.
func Build(missions []store.Mission, rows []store.Row) *Result {
	r := &Result{Missions: missions, rows: rows, squadKey: normaliseSquads(rows)}

	type acc struct {
		p        *Player
		missions map[int64]bool
	}
	byName := map[string]*acc{}
	var order []string
	for _, row := range rows {
		a := byName[row.Name]
		if a == nil {
			a = &acc{p: &Player{Name: row.Name, FirstSeen: row.Date}, missions: map[int64]bool{}}
			byName[row.Name] = a
			order = append(order, row.Name)
		}
		p := a.p
		p.Kills += row.Kills
		p.Deaths += row.Deaths
		p.TeamKills += row.TeamKills
		p.VehiclesDestroyed += row.VehicleKills
		p.KillAssists += row.KillAssists
		p.ShotsFired += row.Shots
		p.Hits += row.Hits
		a.missions[row.MissionID] = true
		p.LastSeen = row.Date
		if row.Squad != "" {
			p.Squad = r.SquadName(row.Squad)
			p.SquadRaw = row.SquadRaw
		}
	}

	maxAtt := 1
	for _, name := range order {
		a := byName[name]
		a.p.Attendance = len(a.missions)
		maxAtt = max(maxAtt, a.p.Attendance)
	}
	for _, name := range order {
		p := byName[name].p
		p.Accuracy = accuracy(p.Hits, p.ShotsFired)
		p.KD = round2(float64(p.Kills) / float64(max(p.Deaths, 1)))
		r.Players = append(r.Players, *p)
		r.Totals.Kills += p.Kills
		r.Totals.Deaths += p.Deaths
		r.Totals.TeamKills += p.TeamKills
		r.Totals.VehiclesDestroyed += p.VehiclesDestroyed
		r.Totals.KillAssists += p.KillAssists
	}
	slices.SortFunc(r.Players, func(a, b Player) int { return strings.Compare(a.Name, b.Name) })

	r.Squads = r.buildSquads(rows, nil)
	r.Totals.Missions = len(missions)
	r.Totals.Players = len(r.Players)
	for _, s := range r.Squads {
		if s.Name != "" {
			r.Totals.Squads++
		}
	}
	return r
}

// SquadName returns the display form of a squad (most frequent spelling in the range).
func (r *Result) SquadName(squad string) string {
	if squad == "" {
		return ""
	}
	if n, ok := r.squadKey[strings.ToLower(squad)]; ok {
		return n
	}
	return squad
}

// normaliseSquads picks the most frequent spelling for each case-insensitive squad tag.
func normaliseSquads(rows []store.Row) map[string]string {
	counts := map[string]map[string]int{}
	for _, row := range rows {
		if row.Squad == "" {
			continue
		}
		k := strings.ToLower(row.Squad)
		if counts[k] == nil {
			counts[k] = map[string]int{}
		}
		counts[k][row.Squad]++
	}
	out := make(map[string]string, len(counts))
	for k, variants := range counts {
		best, bestN := "", -1
		for v, n := range variants {
			if n > bestN || (n == bestN && v < best) {
				best, bestN = v, n
			}
		}
		out[k] = best
	}
	return out
}

// buildSquads aggregates rows per squad of the mission they were played in. When only is
// non-nil, rows of other squads are skipped.
func (r *Result) buildSquads(rows []store.Row, only func(string) bool) []Squad {
	type acc struct {
		s        *Squad
		players  map[string]bool
		missions map[int64]bool
	}
	by := map[string]*acc{}
	for _, row := range rows {
		name := r.SquadName(row.Squad)
		if only != nil && !only(name) {
			continue
		}
		a := by[name]
		if a == nil {
			a = &acc{s: &Squad{Name: name}, players: map[string]bool{}, missions: map[int64]bool{}}
			by[name] = a
		}
		s := a.s
		s.Kills += row.Kills
		s.Deaths += row.Deaths
		s.TeamKills += row.TeamKills
		s.VehiclesDestroyed += row.VehicleKills
		s.KillAssists += row.KillAssists
		s.ShotsFired += row.Shots
		s.Hits += row.Hits
		s.Attendance++
		a.players[row.Name] = true
		a.missions[row.MissionID] = true
	}

	totalMissions := float64(max(len(r.Missions), 1))
	out := make([]Squad, 0, len(by))
	for _, a := range by {
		s := a.s
		s.Players = len(a.players)
		s.Missions = len(a.missions)
		avg := float64(s.Attendance) / float64(max(s.Players, 1))
		s.AvgAttendance = round2(avg)
		s.Accuracy = accuracy(s.Hits, s.ShotsFired)
		s.KD = round2(float64(s.Kills) / float64(max(s.Deaths, 1)))
		out = append(out, *s)
	}

	// Confidence-weighted K/D: small squads are pulled towards half of the average K/D.
	const confWeight = 10.0
	var gKills, gDeaths float64
	for _, s := range out {
		if s.Name != "" {
			gKills += netKills(s.Kills, s.TeamKills, s.VehiclesDestroyed)
			gDeaths += float64(s.Deaths + 1)
		}
	}
	consAvg := 0.0
	if gDeaths > 0 {
		consAvg = gKills / gDeaths * 0.5
	}
	for i := range out {
		s := &out[i]
		att := attendanceMultiplier(math.Max(s.AvgAttendance, 1), totalMissions)
		s.Score = round2((netKills(s.Kills, s.TeamKills, s.VehiclesDestroyed) + confWeight*consAvg) / (float64(s.Deaths) + confWeight) * att)
	}

	slices.SortFunc(out, func(a, b Squad) int {
		if (a.Name == "") != (b.Name == "") {
			if a.Name == "" {
				return 1
			}
			return -1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out
}

// SquadMembers returns statistics of players counting only missions played in the squad
// ("" = without a squad), with custom columns.
func (r *Result) SquadMembers(squad string, cols []Column) []Player {
	var rows []store.Row
	for _, row := range r.rows {
		if strings.EqualFold(r.SquadName(row.Squad), squad) {
			rows = append(rows, row)
		}
	}
	sub := Build(r.Missions, rows)
	sub.ApplyColumns(cols)
	return sub.Players
}

// FindSquad looks a squad up case-insensitively ("" or NoSquad = no squad).
func (r *Result) FindSquad(name string) (Squad, bool) {
	if name == NoSquad {
		name = ""
	}
	for _, s := range r.Squads {
		if strings.EqualFold(s.Name, name) {
			return s, true
		}
	}
	return Squad{}, false
}

// --- formulas ----------------------------------------------------------------

func netKills(kills, teamKills, vehicles int) float64 {
	// Team kills are penalised twice, destroyed vehicles give half a kill.
	return float64(kills) - float64(teamKills)*2 + float64(vehicles)*0.5
}

// attendanceMultiplier scales from 0.7 (rare player) to 1.0 (attended the most missions).
func attendanceMultiplier(att, maxAtt float64) float64 {
	if maxAtt <= 0 {
		maxAtt = 1
	}
	return 0.7 + 0.3*(math.Log(att+1)/math.Log(maxAtt+1))
}

func accuracy(hits, shots int) float64 {
	if shots <= 0 {
		return 0
	}
	return round2(math.Min(100, float64(hits)/float64(shots)*100))
}

func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}

// --- sorting -----------------------------------------------------------------

// SortPlayers sorts by a field name used in the API ("kills", "kd", "custom.<id>", ...).
func SortPlayers(ps []Player, field string, desc bool) {
	key := func(p Player) float64 {
		switch field {
		case "deaths":
			return float64(p.Deaths)
		case "teamKills":
			return float64(p.TeamKills)
		case "vehiclesDestroyed":
			return float64(p.VehiclesDestroyed)
		case "attendance":
			return float64(p.Attendance)
		case "accuracy":
			return p.Accuracy
		case "kd":
			return p.KD
		case "killAssists":
			return float64(p.KillAssists)
		case "shotsFired":
			return float64(p.ShotsFired)
		case "hits":
			return float64(p.Hits)
		default:
			return float64(p.Kills)
		}
	}
	slices.SortStableFunc(ps, func(a, b Player) int {
		var c int
		switch field {
		case "name":
			c = cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		case "squad":
			c = cmp.Compare(strings.ToLower(a.SquadRaw), strings.ToLower(b.SquadRaw))
		default:
			if id, ok := strings.CutPrefix(field, "custom."); ok {
				c = compareCustom(a.Custom, b.Custom, id)
			} else {
				c = cmp.Compare(key(a), key(b))
			}
		}
		if desc {
			c = -c
		}
		if c == 0 {
			c = cmp.Compare(a.Name, b.Name)
		}
		return c
	})
}

// SortSquads sorts squads like SortPlayers.
func SortSquads(ss []Squad, field string, desc bool) {
	key := func(s Squad) float64 {
		switch field {
		case "deaths":
			return float64(s.Deaths)
		case "teamKills":
			return float64(s.TeamKills)
		case "vehiclesDestroyed":
			return float64(s.VehiclesDestroyed)
		case "attendance", "avgAttendance":
			return s.AvgAttendance
		case "players":
			return float64(s.Players)
		case "accuracy":
			return s.Accuracy
		case "kd":
			return s.KD
		case "killAssists":
			return float64(s.KillAssists)
		case "score":
			return s.Score
		default:
			return float64(s.Kills)
		}
	}
	slices.SortStableFunc(ss, func(a, b Squad) int {
		var c int
		if field == "name" {
			c = cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		} else if id, ok := strings.CutPrefix(field, "custom."); ok {
			c = compareCustom(a.Custom, b.Custom, id)
		} else {
			c = cmp.Compare(key(a), key(b))
		}
		if desc {
			c = -c
		}
		if c == 0 {
			c = cmp.Compare(a.Name, b.Name)
		}
		return c
	})
}

// --- achievements --------------------------------------------------------------

type PlayerAward struct {
	Name     string  `json:"name"`
	Squad    string  `json:"squad"`
	SquadRaw string  `json:"squadRaw"`
	Value    float64 `json:"value"`
}

type SquadAward struct {
	Name  string  `json:"name"`
	KD    float64 `json:"kd"`
	Score float64 `json:"score"`
}

type KillAward struct {
	PlayerAward
	Victim    string `json:"victim"`
	Weapon    string `json:"weapon"`
	MissionID int64  `json:"missionId"`
	Date      string `json:"date"`
}

type Achievements struct {
	TopKiller     *PlayerAward `json:"topKiller"`     // most kills
	TopAssists    *PlayerAward `json:"topAssists"`    // most kill assists
	TopTeamKiller *PlayerAward `json:"topTeamKiller"` // most team kills
	TopVehicles   *PlayerAward `json:"topVehicles"`   // most vehicles destroyed
	TopSquad      *SquadAward  `json:"topSquad"`      // best confidence-weighted squad K/D
	LongestKill   *KillAward   `json:"longestKill"`   // longest kill of a player or AI soldier
}

// Achievements computes awards; longest is the longest kill in the range (may be nil).
func (r *Result) Achievements(longest *store.Kill) Achievements {
	var a Achievements
	award := func(p Player, v int) *PlayerAward {
		return &PlayerAward{Name: p.Name, Squad: p.Squad, SquadRaw: p.SquadRaw, Value: float64(v)}
	}
	for _, p := range r.Players { // sorted by name: ties go to the first name
		if p.Kills > 0 && (a.TopKiller == nil || float64(p.Kills) > a.TopKiller.Value) {
			a.TopKiller = award(p, p.Kills)
		}
		if p.TeamKills > 0 && (a.TopTeamKiller == nil || float64(p.TeamKills) > a.TopTeamKiller.Value) {
			a.TopTeamKiller = award(p, p.TeamKills)
		}
		if p.VehiclesDestroyed > 0 && (a.TopVehicles == nil || float64(p.VehiclesDestroyed) > a.TopVehicles.Value) {
			a.TopVehicles = award(p, p.VehiclesDestroyed)
		}
		if p.KillAssists > 0 && (a.TopAssists == nil || float64(p.KillAssists) > a.TopAssists.Value) {
			a.TopAssists = award(p, p.KillAssists)
		}
	}
	for _, s := range r.Squads {
		if s.Name != "" && (a.TopSquad == nil || s.Score > a.TopSquad.Score) {
			a.TopSquad = &SquadAward{Name: s.Name, KD: s.KD, Score: s.Score}
		}
	}
	if k := longest; k != nil {
		a.LongestKill = &KillAward{
			PlayerAward: PlayerAward{Name: k.Killer, Squad: r.SquadName(k.KillerSquad), Value: k.Distance},
			Victim:      k.Victim, Weapon: k.Weapon, MissionID: k.MissionID, Date: k.Date,
		}
		for _, p := range r.Players {
			if p.Name == a.LongestKill.Name {
				a.LongestKill.SquadRaw = p.SquadRaw
				break
			}
		}
	}
	return a
}

// --- rotation --------------------------------------------------------------------

type RotationSide struct {
	Name   string   `json:"name"`
	Color  string   `json:"color"`
	Squads []string `json:"squads"`
	Wins   int      `json:"wins"`
}

type RotationMatch struct {
	MissionID int64   `json:"missionId"`
	Date      string  `json:"date"`
	Name      string  `json:"name"`
	World     string  `json:"world"`
	Duration  float64 `json:"duration"`
	EndSide   string  `json:"endSide"`
	// Winner is the index into Sides, or -1 for a draw.
	Winner int `json:"winner"`
	// GameSides maps each rotation side to the in-game side (EAST, WEST, ...) most of its
	// players were on.
	GameSides []string `json:"gameSides"`
}

type Rotation struct {
	Since   string          `json:"since"`
	Sides   []RotationSide  `json:"sides"`
	Draws   int             `json:"draws"`
	Matches []RotationMatch `json:"matches"` // newest first
}

var gameSides = []string{"EAST", "WEST", "GUER", "CIV"}

// Rotation scores rotation sides. A side wins a mission when the endMission winner names the
// in-game side most of its squads played on; everything else is a draw.
func (r *Result) Rotation(cfg config.Rotation) Rotation {
	rot := Rotation{Since: cfg.Since, Matches: []RotationMatch{}}
	for _, s := range cfg.Sides {
		rot.Sides = append(rot.Sides, RotationSide{Name: s.Name, Color: s.Color, Squads: s.Squads})
	}

	byMission := map[int64][]store.Row{}
	for _, row := range r.rows {
		byMission[row.MissionID] = append(byMission[row.MissionID], row)
	}

	for _, m := range r.Missions {
		if cfg.Since != "" && m.Date < cfg.Since {
			continue
		}
		match := RotationMatch{
			MissionID: m.ID, Date: m.Date, Name: m.Name, World: m.World, Duration: m.Duration,
			EndSide: m.EndSide, Winner: -1, GameSides: make([]string, len(cfg.Sides)),
		}
		winner := strings.ToUpper(m.EndSide)
		for i, side := range cfg.Sides {
			counts := map[string]int{}
			for _, row := range byMission[m.ID] {
				if row.Squad != "" && row.Side != "" && squadMatches(row.Squad, side.Squads) {
					counts[strings.ToUpper(row.Side)]++
				}
			}
			best := 0
			for _, gs := range gameSides {
				if counts[gs] > best {
					best, match.GameSides[i] = counts[gs], gs
				}
			}
			if match.Winner < 0 && winner != "" && match.GameSides[i] != "" && strings.Contains(winner, match.GameSides[i]) {
				match.Winner = i
			}
		}
		if match.Winner >= 0 {
			rot.Sides[match.Winner].Wins++
		} else {
			rot.Draws++
		}
		rot.Matches = append(rot.Matches, match)
	}
	slices.Reverse(rot.Matches)
	return rot
}

// squadMatches reports whether squad contains one of the configured tags (case-insensitive),
// the same loose matching the original page uses.
func squadMatches(squad string, tags []string) bool {
	u := strings.ToUpper(squad)
	for _, t := range tags {
		if t != "" && strings.Contains(u, strings.ToUpper(t)) {
			return true
		}
	}
	return false
}
