package report

import (
	"testing"

	"github.com/smitt14ua/ocap-statistics/internal/stats"
	"github.com/smitt14ua/ocap-statistics/internal/store"
)

// A player who left a squad is listed without one, while the squad keeps their missions.
func TestBuildSquadOfLatestMission(t *testing.T) {
	rows := []store.Row{
		{MissionID: 1, Date: "2026-01-01", PlayerRow: stats.PlayerRow{Name: "Quiet", Squad: "ALPHA", SquadRaw: "ALPHA", Kills: 5}},
		{MissionID: 2, Date: "2026-01-02", PlayerRow: stats.PlayerRow{Name: "Quiet", Squad: "ALPHA", SquadRaw: "ALPHA", Kills: 4}},
		{MissionID: 3, Date: "2026-01-03", PlayerRow: stats.PlayerRow{Name: "Quiet", Kills: 1}},
	}
	r := Build([]store.Mission{{ID: 1}, {ID: 2}, {ID: 3}}, rows)
	if p := r.Players[0]; p.Squad != "" || p.SquadRaw != "" || p.Kills != 10 {
		t.Errorf("player = %+v, want no squad and 10 kills", p)
	}
	alpha, ok := r.FindSquad("ALPHA")
	if !ok || alpha.Kills != 9 || alpha.Missions != 2 || alpha.Players != 1 {
		t.Errorf("ALPHA = %+v (found %v), want 9 kills in 2 missions", alpha, ok)
	}
	solo, _ := r.FindSquad(NoSquad)
	if solo.Kills != 1 || solo.Missions != 1 {
		t.Errorf("no squad = %+v, want 1 kill in 1 mission", solo)
	}

	// Rejoining a squad shows it again.
	rows = append(rows, store.Row{MissionID: 4, Date: "2026-01-04", PlayerRow: stats.PlayerRow{Name: "Quiet", Squad: "BRAVO", SquadRaw: "BRAVO"}})
	if p := Build(nil, rows).Players[0]; p.Squad != "BRAVO" {
		t.Errorf("squad after rejoining = %q, want BRAVO", p.Squad)
	}
}

func TestAchievementsTopAssists(t *testing.T) {
	r := &Result{Players: []Player{
		{Name: "Alpha", KillAssists: 3},
		{Name: "Bravo", KillAssists: 5, Squad: "B", SquadRaw: "B"},
		{Name: "Charlie", KillAssists: 5}, // tie: the first name wins
	}}
	got := r.Achievements(nil).TopAssists
	if got == nil || got.Name != "Bravo" || got.Value != 5 || got.SquadRaw != "B" {
		t.Errorf("top assists = %+v, want Bravo with 5", got)
	}

	r = &Result{Players: []Player{{Name: "Alpha"}}}
	if got := r.Achievements(nil).TopAssists; got != nil {
		t.Errorf("top assists without assists = %+v, want nil", got)
	}
}
