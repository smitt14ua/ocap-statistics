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
	r := Build([]store.Mission{{ID: 1}, {ID: 2}, {ID: 3}}, rows, stats.DefaultMedical())
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
	if p := Build(nil, rows, stats.DefaultMedical()).Players[0]; p.Squad != "BRAVO" {
		t.Errorf("squad after rejoining = %q, want BRAVO", p.Squad)
	}
}

func TestMedicalPoints(t *testing.T) {
	rows := []store.Row{
		{MissionID: 1, PlayerRow: stats.PlayerRow{Name: "Medic", Squad: "ALPHA", Treatments: 10, Revives: 1}},
		{MissionID: 2, PlayerRow: stats.PlayerRow{Name: "Medic", Squad: "ALPHA", Treatments: 3}},
		{MissionID: 2, PlayerRow: stats.PlayerRow{Name: "Rifleman", Squad: "ALPHA", Treatments: 1, Revives: 1}},
	}
	r := Build([]store.Mission{{ID: 1}, {ID: 2}}, rows, stats.Medical{Treatment: 0.5, Revive: 4})
	if p, _ := r.FindPlayer("Medic"); p.Treatments != 13 || p.Revives != 1 || p.MedicalPoints != 10.5 {
		t.Errorf("Medic = %+v, want 13 treatments, 1 revive, 10.5 points", p)
	}
	if s, _ := r.FindSquad("ALPHA"); s.MedicalPoints != 15 || r.Totals.MedicalPoints != 15 {
		t.Errorf("ALPHA = %v points, totals %v, want 15", s.MedicalPoints, r.Totals.MedicalPoints)
	}
	SortPlayers(r.Players, "medicalPoints", true)
	if r.Players[0].Name != "Medic" {
		t.Errorf("sorted by medical points: %+v", r.Players)
	}
	if a := r.Achievements(nil); a.TopMedic == nil || a.TopMedic.Name != "Medic" || a.TopMedic.Value != 10.5 {
		t.Errorf("top medic: %+v", a.TopMedic)
	}
}

func TestHide(t *testing.T) {
	row := func(id int64, name, squad string) store.Row {
		return store.Row{MissionID: id, PlayerRow: stats.PlayerRow{Name: name, Squad: squad, SquadRaw: squad, Kills: 1}}
	}
	// Veteran played 3 missions in ALPHA; Newbie joined ALPHA for one; Guest played one in BRAVO.
	r := Build([]store.Mission{{ID: 1}, {ID: 2}, {ID: 3}}, []store.Row{
		row(1, "Veteran", "ALPHA"), row(2, "Veteran", "ALPHA"), row(3, "Veteran", "ALPHA"),
		row(3, "Newbie", "ALPHA"), row(2, "Guest", "BRAVO"), row(1, "Solo", ""), row(2, "Solo", ""),
	}, stats.DefaultMedical())
	r.Hide(2)
	if len(r.Players) != 2 || r.Players[0].Name != "Solo" || r.Players[1].Name != "Veteran" || r.Totals.Players != 2 {
		t.Errorf("players = %+v", r.Players)
	}
	if len(r.Squads) != 2 || r.Squads[0].Name != "ALPHA" || r.Squads[1].Name != "" || r.Totals.Squads != 1 {
		t.Errorf("squads = %+v", r.Squads)
	}
	if r.Squads[0].Kills != 4 || r.Squads[0].Players != 2 {
		t.Errorf("ALPHA lost the missions of a hidden player: %+v", r.Squads[0])
	}
	// ALPHA: 4 player-missions by 2 players over 3 missions.
	if s := r.Squads[0]; s.AvgAttendance != 2 || s.AvgPlayers != 1.33 {
		t.Errorf("ALPHA missions per player %v, players per mission %v, want 2 and 1.33", s.AvgAttendance, s.AvgPlayers)
	}
	if _, ok := r.FindPlayer("Newbie"); !ok {
		t.Error("hidden player not found")
	}
	if _, ok := r.FindSquad("BRAVO"); !ok {
		t.Error("hidden squad not found")
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
