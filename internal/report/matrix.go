package report

import (
	"cmp"
	"slices"
	"strings"

	"github.com/smitt14ua/ocap-statistics/internal/store"
)

// MatrixCell counts the kills of one squad's players by another's ("" = without a squad).
type MatrixCell struct {
	Killer    string `json:"killer"`
	Victim    string `json:"victim"`
	Kills     int    `json:"kills"` // kills of enemies
	TeamKills int    `json:"teamKills"`
}

// SquadMatrix is who killed whom between squads over a period.
type SquadMatrix struct {
	// Squads that killed or lost a player, most kills first; "" (no squad) last.
	Squads []string     `json:"squads"`
	Cells  []MatrixCell `json:"cells"`
}

// SquadMatrix merges squad kills under the display names of the report's squads, leaving out
// squads that Hide removed.
func (r *Result) SquadMatrix(kills []store.SquadKill) SquadMatrix {
	shown := map[string]bool{}
	for _, s := range r.Squads {
		shown[s.Name] = true
	}
	type pair struct{ killer, victim string }
	cells := map[pair]*MatrixCell{}
	total := map[string]int{} // enemy kills of every squad in the matrix
	for _, k := range kills {
		p := pair{r.SquadName(k.KillerSquad), r.SquadName(k.VictimSquad)}
		if !shown[p.killer] || !shown[p.victim] {
			continue
		}
		c := cells[p]
		if c == nil {
			c = &MatrixCell{Killer: p.killer, Victim: p.victim}
			cells[p] = c
		}
		c.Kills += k.Kills
		c.TeamKills += k.TeamKills
		total[p.killer] += k.Kills
		total[p.victim] += 0 // a squad that killed nobody is listed too
	}
	m := SquadMatrix{Squads: make([]string, 0, len(total)), Cells: make([]MatrixCell, 0, len(cells))}
	for s := range total {
		m.Squads = append(m.Squads, s)
	}
	slices.SortFunc(m.Squads, func(a, b string) int {
		if (a == "") != (b == "") {
			if a == "" {
				return 1
			}
			return -1
		}
		return cmp.Or(cmp.Compare(total[b], total[a]), strings.Compare(strings.ToLower(a), strings.ToLower(b)))
	})
	for _, c := range cells {
		m.Cells = append(m.Cells, *c)
	}
	slices.SortFunc(m.Cells, func(a, b MatrixCell) int {
		return cmp.Or(cmp.Compare(b.Kills, a.Kills), strings.Compare(a.Killer, b.Killer), strings.Compare(a.Victim, b.Victim))
	})
	return m
}

// Rivals returns the squads a squad killed most and those that killed it most, with counts
// of enemy kills.
func (m SquadMatrix) Rivals(squad string) (killed, killedBy []SquadCount) {
	for _, c := range m.Cells {
		if c.Kills == 0 {
			continue
		}
		if strings.EqualFold(c.Killer, squad) {
			killed = append(killed, SquadCount{c.Victim, c.Kills})
		}
		if strings.EqualFold(c.Victim, squad) {
			killedBy = append(killedBy, SquadCount{c.Killer, c.Kills})
		}
	}
	return killed, killedBy
}

// SquadCount is a squad with a number.
type SquadCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}
