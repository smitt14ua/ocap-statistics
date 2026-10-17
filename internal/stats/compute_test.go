package stats

import (
	"os"
	"strings"
	"testing"

	"github.com/smitt14ua/ocap-statistics/internal/digest"
)

// testdata/recording.json is a minimal v1 recording covering the counting rules.
func parse(t *testing.T) *digest.Digest {
	t.Helper()
	f, err := os.Open("testdata/recording.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := digest.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestParseName(t *testing.T) {
	cases := []struct{ raw, name, squadRaw, squad string }{
		{"[MILF] Smith", "Smith", "MILF", "MILF"},
		{"[~FAC~]Arzy", "Arzy", "FAC", "FAC"},
		{"Lonely", "Lonely", "", ""},
		{"[3 Bde] Мирослав", "Мирослав", "3 Bde", "3 Bde"},
		{"[MILF]", "[MILF]", "MILF", "MILF"},
	}
	for _, c := range cases {
		name, squadRaw, squad := DefaultRules().ParseName(c.raw)
		if name != c.name || squadRaw != c.squadRaw || squad != c.squad {
			t.Errorf("ParseName(%q) = %q, %q, %q; want %q, %q, %q", c.raw, name, squadRaw, squad, c.name, c.squadRaw, c.squad)
		}
	}
}

func TestVehicleCrew(t *testing.T) {
	d := parse(t)
	var tank *digest.Entity
	for i := range d.Entities {
		if d.Entities[i].ID == 7 {
			tank = &d.Entities[i]
		}
	}
	if tank == nil || len(tank.Crew) != 2 {
		t.Fatalf("tank crew: %+v", tank)
	}
	if c := tank.CrewAt(120); len(c) != 2 || tank.CrewAt(50) != nil || len(tank.CrewAt(200)) != 1 {
		t.Errorf("CrewAt: %v %v %v", tank.CrewAt(120), tank.CrewAt(50), tank.CrewAt(200))
	}
}

func TestAlternativeEventLayout(t *testing.T) {
	d, err := digest.Parse(strings.NewReader(`{"entities": [], "events": [[5, "killed", 3, 9, "AK-74", 120.5], [6, "hit", 3, 9, "AK-74", 10]]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Kills) != 1 || d.Kills[0].Killer != 3 || d.Kills[0].Victim != 9 || d.Kills[0].Weapon != "AK-74" || d.Kills[0].Distance != 120.5 {
		t.Errorf("kills: %+v", d.Kills)
	}
	if len(d.Hits) != 1 || d.Hits[0].Shooter != 3 || d.Hits[0].Victim != 9 {
		t.Errorf("hits: %+v", d.Hits)
	}
}

func TestDigest(t *testing.T) {
	d := parse(t)
	if d.MissionName != "Test_Mission_1" || d.WorldName != "altis" || d.MissionAuthor != "Smith" {
		t.Errorf("metadata: %+v", d)
	}
	if got := d.Duration(); got != 1300 {
		t.Errorf("duration = %v, want 1300", got)
	}
	if len(d.Entities) != 8 || d.Entities[0].Shots != 4 || !d.Entities[1].IsPlayer {
		t.Errorf("entities: %+v", d.Entities)
	}
	if len(d.Kills) != 7 || len(d.Hits) != 3 {
		t.Errorf("kills=%d hits=%d", len(d.Kills), len(d.Hits))
	}
	if d.Kills[4].Killer != -1 {
		t.Errorf("null killer parsed as %d", d.Kills[4].Killer)
	}
	if d.End == nil || d.End.Side != "WEST" || d.End.Message != "Blufor wins" {
		t.Errorf("end: %+v", d.End)
	}

	blob, err := digest.Encode(d)
	if err != nil {
		t.Fatal(err)
	}
	back, err := digest.Decode(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Kills) != len(d.Kills) || back.Entities[0].Shots != 4 {
		t.Errorf("round trip lost data: %+v", back)
	}
}

func TestCompute(t *testing.T) {
	res := Compute(parse(t), DefaultRules())
	if res.EndSide != "WEST" || res.Duration != 1300 {
		t.Errorf("result meta: %+v", res)
	}
	got := map[string]PlayerRow{}
	for _, p := range res.Players {
		got[p.Name] = p
	}
	if _, ok := got["HC"]; ok {
		t.Error("headless client counted as player")
	}
	if len(got) != 3 {
		t.Fatalf("players = %v", got)
	}

	smith := got["Smith"]
	// Two entities merged: kills of AI (110), enemy (120) and team mate (130); Javelin on the tank.
	want := PlayerRow{Name: "Smith", RawName: "[MILF] Smith", SquadRaw: "MILF", Squad: "MILF", Side: "WEST",
		Kills: 3, Deaths: 1, TeamKills: 1, VehicleKills: 1, Shots: 5, Hits: 2, KillAssists: 0}
	if smith != want {
		t.Errorf("Smith = %+v\nwant   %+v", smith, want)
	}

	rookie := got["Rookie"]
	// Rookie sat in Smith's tank when Smith killed the rifleman (frame 110): one assist. The
	// team kill of Rookie (130) and the tank kill (170, Rookie no longer aboard) give none.
	if rookie.SquadRaw != "milf" || rookie.Squad != "milf" || rookie.Deaths != 1 || rookie.Kills != 0 || rookie.KillAssists != 1 {
		t.Errorf("Rookie = %+v", rookie)
	}

	enemy := got["Enemy"]
	// Suicide counts as a death but not as a kill; the tank kill by RPG counts as a vehicle.
	if enemy.Deaths != 2 || enemy.Kills != 0 || enemy.VehicleKills != 1 || enemy.Shots != 1 {
		t.Errorf("Enemy = %+v", enemy)
	}

	var teamKills, vehicles int
	for _, k := range res.Kills {
		if k.TeamKill {
			teamKills++
		}
		if k.VictimKind == VictimVehicle {
			vehicles++
			if k.VehicleClass != "tank" {
				t.Errorf("vehicle class = %q", k.VehicleClass)
			}
		}
	}
	if teamKills != 1 || vehicles != 2 || len(res.Kills) != 7 {
		t.Errorf("kills: %+v", res.Kills)
	}
}

func TestIgnoreNames(t *testing.T) {
	n := DefaultNickname()
	n.IgnoreNames = []string{"rookie"}
	rules, err := NewRules(n)
	if err != nil {
		t.Fatal(err)
	}
	res := Compute(parse(t), rules)
	for _, p := range res.Players {
		if p.Name == "Rookie" {
			t.Error("ignored name was counted")
		}
	}
}

func TestCustomNickname(t *testing.T) {
	rules, err := NewRules(Nickname{
		SquadPattern: `^(\w+)\|`,
		Aliases:      map[string]string{"stars": "S.T.A.R.S."},
	})
	if err != nil {
		t.Fatal(err)
	}
	name, squadRaw, squad := rules.ParseName("stars-R|Smith")
	if name != "stars-R|Smith" || squad != "" {
		// The pattern needs \w+ before "|", "-" does not match: whole nickname is the name.
		t.Errorf("non-matching nickname: %q %q %q", name, squadRaw, squad)
	}
	name, squadRaw, squad = rules.ParseName("STARS|Smith")
	if name != "Smith" || squadRaw != "STARS" || squad != "S.T.A.R.S." {
		t.Errorf("custom pattern: %q %q %q", name, squadRaw, squad)
	}
	if rules.Key() == DefaultRules().Key() {
		t.Error("different rules share a key")
	}

	for _, bad := range []Nickname{{SquadPattern: `[`}, {SquadPattern: `\[.*\]`}} {
		if _, err := NewRules(bad); err == nil {
			t.Errorf("NewRules(%+v) accepted an invalid format", bad)
		}
	}
}
