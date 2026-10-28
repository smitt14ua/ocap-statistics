package report

import (
	"slices"
	"testing"

	"github.com/smitt14ua/ocap-statistics/internal/store"
)

func TestIdentities(t *testing.T) {
	ids := NewIdentities([]store.SteamAlias{
		{SteamID: "1", Name: "Old", LastDate: "2026-10-01", LastID: 1},
		{SteamID: "1", Name: "New", LastDate: "2026-10-05", LastID: 5},
		{SteamID: "2", Name: "Shared", LastDate: "2026-10-02", LastID: 2},
		{SteamID: "2", Name: "Second", LastDate: "2026-10-03", LastID: 3},
		{SteamID: "3", Name: "Shared", LastDate: "2026-10-04", LastID: 4},
	})
	for _, c := range []struct{ name, steam, want string }{
		{"Old", "1", "New"},
		{"Old", "", "New"}, // a mission without Steam ids
		{"New", "", "New"},
		{"Shared", "2", "Second"},
		{"Shared", "3", "Shared"},
		{"Shared", "", "Shared"}, // two accounts played under it
		{"Stranger", "", "Stranger"},
	} {
		if got := ids.Name(c.name, c.steam); got != c.want {
			t.Errorf("Name(%q, %q) = %q, want %q", c.name, c.steam, got, c.want)
		}
	}
	if got := ids.Aliases("New"); !slices.Equal(got, []string{"Old"}) {
		t.Errorf("aliases of New: %v", got)
	}
	if got := ids.Names("Second"); !slices.Equal(got, []string{"Second", "Shared"}) {
		t.Errorf("names of Second: %v", got)
	}
	if got := ids.Aliases("Stranger"); got != nil {
		t.Errorf("aliases of a player without Steam id: %v", got)
	}

	// Kills keep names that are not players of the mission (AI units, vehicles).
	rows := []store.Row{{MissionID: 7}}
	rows[0].Name, rows[0].SteamID = "Old", "1"
	r := ids.Merge(rows)
	k := store.Kill{MissionID: 7}
	k.Killer, k.Victim = "Old", "Rifleman"
	r.Kill(&k)
	if rows[0].Name != "New" || k.Killer != "New" || k.Victim != "Rifleman" {
		t.Errorf("merged row %q, kill %q → %q", rows[0].Name, k.Killer, k.Victim)
	}
}
