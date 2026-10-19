package report

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/smitt14ua/ocap-statistics/internal/formula"
)

// Column targets.
const (
	TargetPlayers = "players"
	TargetSquads  = "squads"
	TargetBoth    = "both"
)

// Sort fields accepted for players and squads, besides "custom.<column id>".
var (
	PlayerSortFields = []string{"kills", "deaths", "teamKills", "vehiclesDestroyed", "killAssists", "attendance", "accuracy", "kd", "shotsFired", "hits", "treatments", "revives", "medicalPoints", "name", "squad"}
	SquadSortFields  = []string{"kills", "deaths", "teamKills", "vehiclesDestroyed", "killAssists", "attendance", "players", "accuracy", "kd", "score", "treatments", "revives", "medicalPoints", "name"}
)

// MaxColumns limits how many custom columns can be defined.
const MaxColumns = 20

// Column is a custom statistics column computed with a formula (see package formula).
type Column struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Formula  string `json:"formula"`
	Decimals int    `json:"decimals"`
	Target   string `json:"target"`

	expr *formula.Expr
}

// Variables are the names formulas can use. For squads, the statistics are the squad's
// totals, attendance is its average attendance per player and squadMissions the missions
// it took part in; for players, players is 1 and squadMissions equals attendance.
var Variables = []string{
	"kills", "deaths", "teamkills", "vehiclesdestroyed", "killassists", "shotsfired", "hits",
	"accuracy", "kd", "attendance", "players", "squadmissions",
	"maxattendance", "missions", "issquad", "treatments", "revives", "medicalpoints",
}

var columnIDRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// NormalizeColumns validates and compiles columns, assigning ids to new ones.
func NormalizeColumns(cols []Column) ([]Column, error) {
	if len(cols) > MaxColumns {
		return nil, fmt.Errorf("at most %d custom columns", MaxColumns)
	}
	out := make([]Column, 0, len(cols))
	seen := map[string]bool{}
	for i, c := range cols {
		c.Name = strings.TrimSpace(c.Name)
		c.Formula = strings.TrimSpace(c.Formula)
		if c.Name == "" || utf8.RuneCountInString(c.Name) > 40 {
			return nil, fmt.Errorf("column %d: name must be 1-40 characters", i+1)
		}
		if c.ID == "" {
			c.ID = newColumnID()
		}
		if !columnIDRe.MatchString(c.ID) || seen[c.ID] {
			return nil, fmt.Errorf("column %q: invalid or duplicate id", c.Name)
		}
		seen[c.ID] = true
		c.Decimals = min(max(c.Decimals, 0), 6)
		switch c.Target {
		case TargetPlayers, TargetSquads, TargetBoth:
		case "":
			c.Target = TargetBoth
		default:
			return nil, fmt.Errorf("column %q: target must be players, squads or both", c.Name)
		}
		expr, err := formula.Compile(c.Formula, Variables)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", c.Name, err)
		}
		c.expr = expr
		out = append(out, c)
	}
	return out, nil
}

func newColumnID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "c" + hex.EncodeToString(b)
}

// CompileFormula checks a formula against the column variables.
func CompileFormula(src string) (*formula.Expr, error) {
	return formula.Compile(src, Variables)
}

// value evaluates expr and rounds it; NaN and infinities (e.g. division by zero) become nil.
func value(expr *formula.Expr, vars map[string]float64, decimals int) *float64 {
	v := expr.Eval(vars)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	p := math.Pow(10, float64(decimals))
	v = math.Round(v*p) / p
	return &v
}

func (r *Result) playerVars(p *Player, maxAtt float64) map[string]float64 {
	return map[string]float64{
		"kills": float64(p.Kills), "deaths": float64(p.Deaths), "teamkills": float64(p.TeamKills),
		"vehiclesdestroyed": float64(p.VehiclesDestroyed), "killassists": float64(p.KillAssists),
		"shotsfired": float64(p.ShotsFired), "hits": float64(p.Hits), "accuracy": p.Accuracy, "kd": p.KD,
		"attendance": float64(p.Attendance), "players": 1, "squadmissions": float64(p.Attendance),
		"maxattendance": maxAtt, "missions": float64(len(r.Missions)), "issquad": 0,
		"treatments": float64(p.Treatments), "revives": float64(p.Revives), "medicalpoints": p.MedicalPoints,
	}
}

func (r *Result) squadVars(s *Squad, maxAtt float64) map[string]float64 {
	return map[string]float64{
		"kills": float64(s.Kills), "deaths": float64(s.Deaths), "teamkills": float64(s.TeamKills),
		"vehiclesdestroyed": float64(s.VehiclesDestroyed), "killassists": float64(s.KillAssists),
		"shotsfired": float64(s.ShotsFired), "hits": float64(s.Hits), "accuracy": s.Accuracy, "kd": s.KD,
		"attendance": s.AvgAttendance, "players": float64(s.Players), "squadmissions": float64(s.Missions),
		"maxattendance": maxAtt, "missions": float64(len(r.Missions)), "issquad": 1,
		"treatments": float64(s.Treatments), "revives": float64(s.Revives), "medicalpoints": s.MedicalPoints,
	}
}

// ApplyColumns computes custom columns for players and squads. Columns must come from
// NormalizeColumns.
func (r *Result) ApplyColumns(cols []Column) {
	if len(cols) == 0 {
		return
	}
	var maxPlayerAtt, maxSquadAtt float64
	for _, p := range r.Players {
		maxPlayerAtt = max(maxPlayerAtt, float64(p.Attendance))
	}
	for _, s := range r.Squads {
		maxSquadAtt = max(maxSquadAtt, s.AvgAttendance)
	}
	for i := range r.Players {
		p := &r.Players[i]
		vars := r.playerVars(p, maxPlayerAtt)
		for _, c := range cols {
			if c.expr != nil && c.Target != TargetSquads {
				if p.Custom == nil {
					p.Custom = map[string]*float64{}
				}
				p.Custom[c.ID] = value(c.expr, vars, c.Decimals)
			}
		}
	}
	for i := range r.Squads {
		s := &r.Squads[i]
		vars := r.squadVars(s, maxSquadAtt)
		for _, c := range cols {
			if c.expr != nil && c.Target != TargetPlayers {
				if s.Custom == nil {
					s.Custom = map[string]*float64{}
				}
				s.Custom[c.ID] = value(c.expr, vars, c.Decimals)
			}
		}
	}
}

// Preview evaluates a formula for the first n players (or squads) by kills.
func (r *Result) Preview(expr *formula.Expr, squads bool, decimals, n int) []PreviewRow {
	out := []PreviewRow{}
	if squads {
		var maxAtt float64
		for _, s := range r.Squads {
			maxAtt = max(maxAtt, s.AvgAttendance)
		}
		list := append([]Squad(nil), r.Squads...)
		SortSquads(list, "kills", true)
		for _, s := range list[:min(n, len(list))] {
			out = append(out, PreviewRow{Name: s.Name, Value: value(expr, r.squadVars(&s, maxAtt), decimals)})
		}
		return out
	}
	var maxAtt float64
	for _, p := range r.Players {
		maxAtt = max(maxAtt, float64(p.Attendance))
	}
	list := append([]Player(nil), r.Players...)
	SortPlayers(list, "kills", true)
	for _, p := range list[:min(n, len(list))] {
		out = append(out, PreviewRow{Name: p.Name, Value: value(expr, r.playerVars(&p, maxAtt), decimals)})
	}
	return out
}

// PreviewRow is one value of a formula preview.
type PreviewRow struct {
	Name  string   `json:"name"`
	Value *float64 `json:"value"`
}

// customKey returns the sort key of a custom column; missing values sort lowest.
func customKey(custom map[string]*float64, id string) float64 {
	if v := custom[id]; v != nil {
		return *v
	}
	return math.Inf(-1)
}

// compareCustom compares custom values, used by the sort functions.
func compareCustom(a, b map[string]*float64, id string) int {
	return cmp.Compare(customKey(a, id), customKey(b, id))
}
