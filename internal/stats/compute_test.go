package stats

import (
	"os"
	"slices"
	"strconv"
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
		Kills: 3, Deaths: 1, TeamKills: 1, VehicleKills: 1, Shots: 5, Hits: 2, KillAssists: 0, SteamID: "76561190000000001"}
	if smith != want {
		t.Errorf("Smith = %+v\nwant   %+v", smith, want)
	}

	rookie := got["Rookie"]
	// Rookie sat in Smith's tank when Smith killed the rifleman (frame 110): one assist. The
	// team kill of Rookie (130) and the tank kill (170, Rookie no longer aboard) give none.
	// Rookie bandaged the unconscious Smith, who woke up (175-185): a treatment and a revive.
	if rookie.SquadRaw != "milf" || rookie.Squad != "milf" || rookie.Deaths != 1 || rookie.Kills != 0 || rookie.KillAssists != 1 ||
		rookie.Treatments != 1 || rookie.Revives != 1 {
		t.Errorf("Rookie = %+v", rookie)
	}
	wantAssist := AssistRow{Frame: 110, Player: "Rookie", PlayerSquad: "milf", Kind: AssistCrew, Killer: "Smith", Victim: "Rifleman", VictimKind: VictimAI}
	if len(res.Assists) != 1 || res.Assists[0] != wantAssist {
		t.Errorf("assists = %+v\nwant %+v", res.Assists, wantAssist)
	}
	if res.CaptureDelay != 1 {
		t.Errorf("capture delay = %v", res.CaptureDelay)
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

func TestHitAssists(t *testing.T) {
	d, err := digest.Parse(strings.NewReader(`{"captureDelay": 1, "entities": [
		{"id": 0, "name": "Alpha", "side": "WEST", "isPlayer": 1, "type": "unit"},
		{"id": 1, "name": "Bravo", "side": "WEST", "isPlayer": 1, "type": "unit"},
		{"id": 2, "name": "Enemy", "side": "EAST", "isPlayer": 1, "type": "unit"},
		{"id": 3, "name": "Rifleman", "side": "EAST", "isPlayer": 0, "type": "unit"},
		{"id": 4, "name": "Friend", "side": "WEST", "isPlayer": 0, "type": "unit"}
	], "events": [
		[10, "hit", 3, [1, "M4"], 50], [15, "hit", 3, [1, "M4"], 50], [16, "hit", 3, [0, "M4"], 50],
		[20, "killed", 3, [0, "M4"], 50],
		[30, "hit", 2, [1, "M4"], 50], [30, "hit", 2, [0, "M4"], 50],
		[40, "killed", 2, ["null", ""], 0],
		[500, "killed", 2, [4, "AK"], 50],
		[600, "hit", 2, [1, "M4"], 50],
		[1000, "killed", 2, [2, "Grenade"], 0],
		[1100, "hit", 4, [0, "M4"], 50],
		[1110, "killed", 4, [2, "AK"], 50]
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	players := func(a Assists) map[string]PlayerRow {
		rules, err := NewRules(DefaultNickname(), a)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]PlayerRow{}
		for _, p := range Compute(d, rules).Players {
			got[p.Name] = p
		}
		return got
	}
	assists := func(a Assists) map[string]int {
		got := map[string]int{}
		for name, p := range players(a) {
			got[name] = p.KillAssists
		}
		return got
	}
	// Bravo: the rifleman Alpha killed (two hits, one assist) and the enemy who died without a
	// killer, which goes to Alpha who hit him last. Alpha: no assist for his own kills. None for
	// the death at 500 (hits before the previous death), at 1000 (hit 400 s earlier) or for the
	// friendly AI Alpha hit.
	if got := assists(DefaultAssists()); got["Alpha"] != 0 || got["Bravo"] != 2 || got["Enemy"] != 0 {
		t.Errorf("assists = %v, want Alpha 0, Bravo 2, Enemy 0", got)
	}
	// Each assist is listed with its death.
	var bravo []AssistRow
	for _, a := range Compute(d, DefaultRules()).Assists {
		if a.Player == "Bravo" {
			bravo = append(bravo, a)
		}
	}
	if len(bravo) != 2 ||
		bravo[0] != (AssistRow{Frame: 20, Player: "Bravo", Kind: AssistHit, Killer: "Alpha", Victim: "Rifleman", VictimKind: VictimAI}) ||
		bravo[1] != (AssistRow{Frame: 40, Player: "Bravo", Kind: AssistHit, Killer: "Alpha", Victim: "Enemy", VictimKind: VictimPlayer}) {
		t.Errorf("Bravo's assists = %+v", bravo)
	}
	// With a 500 s window Bravo's hit at 600 reaches the grenade suicide at 1000: his kill.
	if got := players(Assists{Hits: true, Window: 500}); got["Bravo"].KillAssists != 2 || got["Bravo"].Kills != 1 {
		t.Errorf("Bravo with a 500 s window = %+v, want 1 kill, 2 assists", got["Bravo"])
	}
	if got := assists(Assists{Window: 300}); got["Alpha"] != 0 || got["Bravo"] != 0 {
		t.Errorf("assists with hits off = %v, want none", got)
	}
	for _, w := range []int{0, MaxAssistWindow + 1} {
		if _, err := NewRules(DefaultNickname(), Assists{Hits: true, Window: w}); err == nil {
			t.Errorf("window %d accepted", w)
		}
	}
	off, _ := NewRules(DefaultNickname(), Assists{Window: 300})
	if off.Key() == DefaultRules().Key() {
		t.Error("assist settings do not change the rules key")
	}
}

// An AI killed by AI is not a kill row, but an assist for it still names both.
func TestAssistWithoutPlayerKill(t *testing.T) {
	d, err := digest.Parse(strings.NewReader(`{"captureDelay": 0.5, "entities": [
		{"id": 0, "name": "Alpha", "side": "WEST", "isPlayer": 1, "type": "unit"},
		{"id": 1, "name": "Rifleman", "side": "EAST", "isPlayer": 0, "type": "unit"},
		{"id": 2, "name": "Friend", "side": "WEST", "isPlayer": 0, "type": "unit"}
	], "events": [[10, "hit", 1, [0, "M4"], 50], [12, "killed", 1, [2, "M4"], 50]]}`))
	if err != nil {
		t.Fatal(err)
	}
	res := Compute(d, DefaultRules())
	want := AssistRow{Frame: 12, Player: "Alpha", Kind: AssistHit, Killer: "Friend", Victim: "Rifleman", VictimKind: VictimAI}
	if len(res.Kills) != 0 || len(res.Assists) != 1 || res.Assists[0] != want || res.CaptureDelay != 0.5 {
		t.Errorf("kills = %+v, assists = %+v, delay = %v", res.Kills, res.Assists, res.CaptureDelay)
	}
}

// OCAP writes the crew of a destroyed vehicle, drownings and the like as suicides or as
// killed by a wreck; such deaths go to the enemy who damaged the victim last.
func TestCreditedKills(t *testing.T) {
	d, err := digest.Parse(strings.NewReader(`{"captureDelay": 1, "entities": [
		{"id": 0, "name": "Gunner", "side": "WEST", "isPlayer": 1, "type": "unit"},
		{"id": 1, "name": "Driver", "side": "WEST", "isPlayer": 1, "type": "unit"},
		{"id": 2, "name": "Tank", "side": "WEST", "type": "vehicle", "class": "tank",
		 "positions": [[[0, 0], 0, 1, [0, 1], [0, 100]]]},
		{"id": 3, "name": "Crew1", "side": "EAST", "isPlayer": 1, "type": "unit"},
		{"id": 4, "name": "Crew2", "side": "EAST", "isPlayer": 1, "type": "unit"},
		{"id": 5, "name": "BTR", "side": "EAST", "type": "vehicle", "class": "apc",
		 "positions": [[[0, 0], 0, 1, [3, 4], [0, 100]]]},
		{"id": 6, "name": "Swimmer", "side": "EAST", "isPlayer": 1, "type": "unit"},
		{"id": 7, "name": "Rifle", "side": "WEST", "isPlayer": 1, "type": "unit"},
		{"id": 8, "name": "Bravo", "side": "WEST", "isPlayer": 1, "type": "unit"},
		{"id": 9, "name": "Lonely", "side": "EAST", "isPlayer": 1, "type": "unit"},
		{"id": 10, "name": "Mate", "side": "WEST", "isPlayer": 1, "type": "unit"}
	], "events": [
		[10, "killed", 5, [0, "Tank: Cannon"], 800],
		[11, "killed", 3, [3, "BTR: KPVT"], 0],
		[11, "killed", 4, ["null", ""], 0],
		[20, "hit", 6, [8, "M4"], 50], [30, "hit", 6, [7, "M4"], 50],
		[60, "killed", 6, [5, ""], 40],
		[200, "killed", 9, [9, "Grenade"], 0],
		[300, "hit", 10, [7, "M4"], 50],
		[310, "killed", 10, [10, ""], 0]
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	res := Compute(d, DefaultRules())
	got := map[string]PlayerRow{}
	for _, p := range res.Players {
		got[p.Name] = p
	}
	// The crew of the BTR goes to its destroyer, with an assist for his driver; the swimmer the
	// BTR wreck "killed" to Rifle, who hit him last, with an assist for Bravo's earlier hit.
	for name, want := range map[string][3]int{ // kills, assists, vehicles
		"Gunner": {2, 0, 1}, "Driver": {0, 2, 0}, "Rifle": {1, 0, 0}, "Bravo": {0, 1, 0},
	} {
		if p := got[name]; p.Kills != want[0] || p.KillAssists != want[1] || p.VehicleKills != want[2] {
			t.Errorf("%s = %+v, want kills %d, assists %d, vehicles %d", name, p, want[0], want[1], want[2])
		}
	}
	// A suicide without enemy damage stays one; damage by a team mate does not count.
	if got["Rifle"].TeamKills != 0 || got["Lonely"].Deaths != 1 || got["Mate"].Deaths != 1 {
		t.Errorf("Rifle %+v, Lonely %+v, Mate %+v", got["Rifle"], got["Lonely"], got["Mate"])
	}
	causes := map[string]KillRow{}
	for _, k := range res.Kills {
		causes[k.Victim] = k
	}
	if k := causes["Crew1"]; k.Killer != "Gunner" || k.Cause != CauseVehicle || k.Weapon != "Tank: Cannon" || k.Distance != 800 {
		t.Errorf("Crew1 kill = %+v", k)
	}
	if k := causes["Swimmer"]; k.Killer != "Rifle" || k.Cause != CauseLastHit || k.Weapon != "" {
		t.Errorf("Swimmer kill = %+v", k)
	}
	if k := causes["Lonely"]; k.Killer != "Lonely" || k.Cause != "" || causes["Mate"].Killer != "Mate" {
		t.Errorf("suicides = %+v, %+v", k, causes["Mate"])
	}
}

func TestIgnoreNames(t *testing.T) {
	n := DefaultNickname()
	n.IgnoreNames = []string{"rookie"}
	rules, err := NewRules(n, DefaultAssists())
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
	}, DefaultAssists())
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
		if _, err := NewRules(bad, DefaultAssists()); err == nil {
			t.Errorf("NewRules(%+v) accepted an invalid format", bad)
		}
	}
}

// Medical snapshots carry the patient's ACE activity log, which keeps only its last entries;
// treatments are the entries new since the previous snapshot.
func TestMedical(t *testing.T) {
	entry := func(text string) string { return `{"text": "` + text + `", "time": "10:00"}` }
	tourniquet, self := entry("[A] Medic applied a tourniquet"), entry("[A] Patient has bandaged patient")
	other, iv := entry("[B] Other перев'язав пацієнта"), entry("[A] Medic has given an IV")
	morphine, unknown, bandage := entry("[A] Medic used Morphine"), entry("Stranger did something"), entry("[A] Medic has bandaged patient")
	d, err := digest.Parse(strings.NewReader(`{"captureDelay": 1, "entities": [
		{"id": 0, "name": "[A] Medic", "side": "WEST", "isPlayer": 1, "type": "unit"},
		{"id": 1, "name": "[A] Patient", "side": "WEST", "isPlayer": 1, "type": "unit"},
		{"id": 2, "name": "[A]", "side": "WEST", "isPlayer": 0, "type": "unit"},
		{"id": 3, "name": "[B] Other", "side": "WEST", "isPlayer": 1, "type": "unit"},
		{"id": 4, "name": "Rifleman", "side": "EAST", "isPlayer": 0, "type": "unit"}
	], "events": [
		[1, "medicalSnapshot", {"unitId": 1, "playerUid": "765611", "reason": "periodic", "activity": []}],
		[2, "radioSnapshot", {"unitId": 0, "playerUid": "765610", "radios": []}],
		[10, "ace3Unconscious", {"SoldierID": 1, "IsUnconscious": true}],
		[20, "medicalSnapshot", {"diffOf": 1, "unitId": 1, "set": {"activity": [` + tourniquet + `, ` + self + `]}}],
		[30, "medicalSnapshot", {"diffOf": 20, "unitId": 1, "set": {"ace": {"pain": 0}}}],
		[40, "medicalSnapshot", ` + strconv.Quote(`{"diffOf": 20, "unitId": 1, "set": {"activity": [`+tourniquet+`, `+self+`, `+other+`]}}`) + `],
		[50, "ace3Unconscious", {"SoldierID": 1, "IsUnconscious": false}],
		[55, "medicalSnapshot", {"unitId": 1, "activity": [` + self + `, ` + other + `, ` + iv + `]}],
		[100, "ace3Unconscious", {"SoldierID": 1, "IsUnconscious": true}],
		[105, "medicalSnapshot", {"diffOf": 55, "unitId": 1, "set": {"activity": [` + other + `, ` + iv + `, ` + morphine + `, ` + unknown + `]}}],
		[110, "killed", 1, [4, "AK-74"], 50],
		[120, "ace3Unconscious", {"SoldierID": 1, "IsUnconscious": false}],
		[200, "ace3Unconscious", {"SoldierID": 1, "IsUnconscious": true}],
		[205, "ace3Unconscious", {"SoldierID": 1, "IsUnconscious": false}],
		[210, "medicalSnapshot", {"diffOf": 105, "unitId": 1, "set": {"activity": [` + iv + `, ` + morphine + `, ` + unknown + `, ` + bandage + `]}}]
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	type tr struct {
		frame, medic int
		action       string
	}
	var got []tr
	for _, x := range d.Treatments {
		if x.Patient != 1 {
			t.Errorf("patient of %+v", x)
		}
		got = append(got, tr{x.Frame, x.Medic, x.Action})
	}
	want := []tr{
		{20, 0, "applied a tourniquet"}, {20, 1, "has bandaged patient"}, {40, 3, "перев'язав пацієнта"}, {55, 0, "has given an IV"},
		{105, 0, "used Morphine"}, {105, -1, "Stranger did something"}, {210, 0, "has bandaged patient"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("treatments:\n got %v\nwant %v", got, want)
	}
	if len(d.Unconscious) != 6 || !d.Unconscious[0].Down || d.Unconscious[1].Down {
		t.Errorf("unconscious: %+v", d.Unconscious)
	}

	res := Compute(d, DefaultRules())
	rows := map[string]PlayerRow{}
	for _, p := range res.Players {
		rows[p.Name] = p
	}
	// Medic: tourniquet, IV, morphine and bandage; the revive at 205 (treated right after it).
	// Other: the revive at 50 (the last medic before it). The wake-up at 120 follows a death.
	if m := rows["Medic"]; m.Treatments != 4 || m.Revives != 1 || m.SteamID != "765610" {
		t.Errorf("Medic = %+v", m)
	}
	if o := rows["Other"]; o.Treatments != 1 || o.Revives != 1 || o.SteamID != "" {
		t.Errorf("Other = %+v", o)
	}
	if p := rows["Patient"]; p.Treatments != 0 || p.Revives != 0 || p.SteamID != "765611" {
		t.Errorf("Patient = %+v (treating oneself does not count)", p)
	}
}
