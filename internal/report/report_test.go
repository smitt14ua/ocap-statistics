package report

import "testing"

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
