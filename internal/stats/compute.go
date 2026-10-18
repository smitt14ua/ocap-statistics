// Package stats turns recording digests into per-mission player statistics and aggregates
// them over date ranges.
package stats

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/smitt14ua/ocap-statistics/internal/digest"
)

// ComputeVersion changes whenever the per-mission rules below change. Stored missions computed
// with another version or nickname format are recomputed from their digests.
// Version 3 added kill assists for hits; version 4 records who got each assist; version 5
// credits deaths without a killer to the enemy who damaged the victim last.
const ComputeVersion = 5

// Victim kinds stored with kills.
const (
	VictimPlayer  = "player"
	VictimAI      = "ai"
	VictimVehicle = "vehicle"
)

// PlayerRow is one player's result in one mission.
type PlayerRow struct {
	Name         string `json:"name"`
	RawName      string `json:"rawName"`
	SquadRaw     string `json:"squadRaw"`
	Squad        string `json:"squad"`
	Side         string `json:"side"`
	Kills        int    `json:"kills"`
	Deaths       int    `json:"deaths"`
	TeamKills    int    `json:"teamKills"`
	VehicleKills int    `json:"vehiclesDestroyed"`
	// KillAssists counts kills made by another crew member of the vehicle the player sat in
	// and deaths of enemies the player hit shortly before (see Compute).
	KillAssists int `json:"killAssists"`
	Shots       int `json:"shotsFired"`
	Hits        int `json:"hits"`
}

// KillRow is a kill where the killer or the victim is a player.
type KillRow struct {
	Frame          int     `json:"frame"`
	Killer         string  `json:"killer"`
	KillerSquad    string  `json:"killerSquad"`
	KillerSide     string  `json:"killerSide"`
	KillerIsPlayer bool    `json:"killerIsPlayer"`
	Victim         string  `json:"victim"`
	VictimSquad    string  `json:"victimSquad"`
	VictimSide     string  `json:"victimSide"`
	VictimKind     string  `json:"victimKind"`
	VehicleClass   string  `json:"vehicleClass,omitempty"`
	Weapon         string  `json:"weapon"`
	Distance       float64 `json:"distance"`
	TeamKill       bool    `json:"teamKill"`
	// Cause is empty for a killer the recording names, otherwise why the kill was credited
	// (CauseVehicle, CauseLastHit).
	Cause string `json:"cause,omitempty"`
}

// Causes of credited kills: the recording has no killer for the death (a suicide, no killer
// or a wreck), and the kill goes to the enemy who last damaged the victim (see Compute).
const (
	CauseVehicle = "vehicle" // the victim died with the vehicle the killer destroyed
	CauseLastHit = "lastHit" // the killer hit the victim last
)

// Assist kinds stored with assists.
const (
	AssistCrew = "crew" // a kill by another crew member of the player's vehicle
	AssistHit  = "hit"  // the player hit the victim shortly before its death
)

// AssistRow is a kill assist one player got for one death.
type AssistRow struct {
	Frame       int    `json:"frame"`
	Player      string `json:"player"`
	PlayerSquad string `json:"playerSquad"`
	Kind        string `json:"kind"`
	Killer      string `json:"killer"`
	Victim      string `json:"victim"`
	VictimSquad string `json:"victimSquad"`
	VictimKind  string `json:"victimKind"`
}

type MissionResult struct {
	Duration float64
	// CaptureDelay is the length of a frame in seconds.
	CaptureDelay float64
	EndSide      string
	EndMessage   string
	Players      []PlayerRow
	Kills        []KillRow
	Assists      []AssistRow
}

// DefaultSquadPattern matches "[MILF] Smith". Recruits are written "[~MILF~] Smith"; the
// tildes are outside the capture group, so recruits count in their squad like everyone else.
const DefaultSquadPattern = `\[~?(.*?)~?\]\s*`

// Nickname describes how squads are written in nicknames. It is editable in the admin panel.
type Nickname struct {
	// SquadPattern finds the squad in a nickname: capture group 1 is the squad, and the whole
	// match is removed to get the player's name.
	SquadPattern string `json:"squadPattern"`
	// Aliases maps squad spellings (case-insensitive) to one name, e.g. "S.T.A.R.S." -> "STARS".
	Aliases map[string]string `json:"aliases"`
	// IgnoreNames are extra player names (without squad, case-insensitive) that are not counted.
	IgnoreNames []string `json:"ignoreNames"`
}

// DefaultNickname returns the "[SQUAD] Name" nickname format.
func DefaultNickname() Nickname {
	return Nickname{SquadPattern: DefaultSquadPattern}
}

// Assists configures kill assists for hits. It is editable in the admin panel.
type Assists struct {
	// Hits gives an assist for hitting an enemy shortly before its death.
	Hits bool `json:"hits"`
	// Window (seconds) is how long before the death a hit still counts.
	Window int `json:"window"`
}

// DefaultAssists counts hits within five minutes before a death.
func DefaultAssists() Assists {
	return Assists{Hits: true, Window: 300}
}

// MaxAssistWindow is the largest allowed Assists.Window (seconds).
const MaxAssistWindow = 3600

// Rules is a compiled Nickname together with the other per-mission counting settings.
type Rules struct {
	squadRe *regexp.Regexp
	aliases map[string]string
	ignore  map[string]bool
	assists Assists
	key     string
}

// NewRules validates and compiles a nickname format and the assist settings.
func NewRules(n Nickname, a Assists) (*Rules, error) {
	if a.Window < 1 || a.Window > MaxAssistWindow {
		return nil, fmt.Errorf("assists: window must be between 1 and %d seconds", MaxAssistWindow)
	}
	r := &Rules{aliases: map[string]string{}, ignore: map[string]bool{}, assists: a}
	var err error
	if r.squadRe, err = regexp.Compile(n.SquadPattern); err != nil {
		return nil, fmt.Errorf("squad pattern: %w", err)
	}
	if r.squadRe.NumSubexp() < 1 {
		return nil, errors.New(`squad pattern: needs a capture group for the squad, e.g. \[(.*?)\]`)
	}
	for from, to := range n.Aliases {
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if from != "" && to != "" {
			r.aliases[strings.ToLower(from)] = to
		}
	}
	for _, name := range n.IgnoreNames {
		if name = strings.TrimSpace(name); name != "" {
			r.ignore[strings.ToLower(name)] = true
		}
	}
	// The key identifies the rules: statistics computed with another key are recomputed.
	b, _ := json.Marshal(struct { // map keys are sorted, so the encoding is stable
		N Nickname
		A Assists
	}{n, a})
	sum := sha256.Sum256(append([]byte(fmt.Sprintf("v%d:", ComputeVersion)), b...))
	r.key = hex.EncodeToString(sum[:8])
	return r, nil
}

// DefaultRules returns compiled DefaultNickname and DefaultAssists rules.
func DefaultRules() *Rules {
	r, err := NewRules(DefaultNickname(), DefaultAssists())
	if err != nil {
		panic(err)
	}
	return r
}

// Key identifies the rules together with ComputeVersion.
func (r *Rules) Key() string { return r.key }

// ParseName splits a nickname like "[MILF] Smith" into the clean name ("Smith"), the squad
// as captured by the pattern ("MILF") and the squad with aliases applied.
func (r *Rules) ParseName(raw string) (name, squadRaw, squad string) {
	name = raw
	if loc := r.squadRe.FindStringSubmatchIndex(raw); loc != nil {
		if loc[2] >= 0 {
			squadRaw = raw[loc[2]:loc[3]]
		}
		name = raw[:loc[0]] + raw[loc[1]:]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(raw)
	}
	squadRaw = strings.TrimSpace(squadRaw)
	squad = squadRaw
	if alias, ok := r.aliases[strings.ToLower(squad)]; ok {
		squad = alias
	}
	return name, squadRaw, squad
}

// ValidPlayer reports whether a clean name is counted (not a headless client, server or
// ignored name).
func (r *Rules) ValidPlayer(name string) bool {
	if name == "" {
		return false
	}
	l := strings.ToLower(name)
	if l == "hc" || strings.HasPrefix(l, "hc ") || strings.HasPrefix(l, "hc_") ||
		l == "headlessclient" || strings.Contains(l, "headless client") ||
		l == "unknown" || l == "__server__" {
		return false
	}
	return !r.ignore[l]
}

// credits collects the assists of one death: at most one per player, none for the killer.
type credits struct {
	done    map[*PlayerRow]bool
	players []*PlayerRow
	kinds   []string
}

func (c *credits) add(p *PlayerRow, kind string) {
	if p == nil || c.done[p] {
		return
	}
	c.done[p] = true
	p.KillAssists++
	c.players = append(c.players, p)
	c.kinds = append(c.kinds, kind)
}

// creditCrewAssists gives a kill assist to every other player in the vehicle the killer was in
// at a frame. Crews are recorded per frame, so the frame before is checked too (the killer may
// have left the vehicle in the same frame).
func creditCrewAssists(vehicles []*digest.Entity, playerOf map[int]*PlayerRow, killer, frame int, c *credits) {
	for _, f := range []int{frame, frame - 1} {
		for _, v := range vehicles {
			crew := v.CrewAt(f)
			if !slices.Contains(crew, killer) {
				continue
			}
			for _, id := range crew {
				c.add(playerOf[id], AssistCrew)
			}
			return
		}
	}
}

// damage is a hit on a unit, or the destruction of the vehicle the unit sat in.
type damage struct {
	frame, shooter int
	// Destroying a vehicle: its weapon and distance, so a crew kill credited for it shows them.
	vehicle  bool
	weapon   string
	distance float64
}

// enemyDamage returns, latest first, the damage enemies of the victim did to it after `from`
// and up to its death.
func enemyDamage(hits []damage, sideOf func(int) string, k digest.Kill, from int) []damage {
	vs := sideOf(k.Victim)
	if vs == "" {
		return nil
	}
	var out []damage
	for _, h := range slices.Backward(hits) {
		if h.frame <= from || h.frame > k.Frame || h.shooter == k.Victim {
			continue
		}
		if ss := sideOf(h.shooter); ss != "" && ss != vs {
			out = append(out, h)
		}
	}
	return out
}

// noKiller reports whether the recording names nobody who caused a death: no killer, the
// victim itself (OCAP writes deaths in a destroyed vehicle, drowning and falls this way), or
// a vehicle (a wreck or a collision).
func noKiller(k digest.Kill, entities map[int]*digest.Entity) bool {
	if k.Killer < 0 || k.Killer == k.Victim {
		return true
	}
	e := entities[k.Killer]
	return e == nil || e.Type == "vehicle"
}

// Compute derives per-player statistics for one mission.
//
// Rules:
//   - a player is an entity with isPlayer=1; entities with the same clean name are merged;
//   - kill: "killed" event whose killer is a player and not the victim; killing a vehicle
//     counts as "vehicles destroyed" instead of a kill;
//   - a death without a killer (see noKiller) goes to the enemy who last damaged the victim
//     within Assists.Window seconds and after its previous death: who last hit it, or who
//     destroyed the vehicle it sat in (its crew dies "by suicide" in the recording);
//     damage by a team mate does not count;
//   - team kill: a kill where killer and victim (both players) are on the same side;
//   - death: "killed" event whose victim is a player (suicides included);
//   - hits: "hit" events by a player on a non-vehicle target; shots: fired frames;
//   - kill assist: a kill (not a team kill) made by another crew member of the vehicle the
//     player was in at that moment (drivers and commanders of the gunner), or the death of an
//     enemy (player or AI, whoever or whatever killed it) the player damaged (hit, or destroyed
//     its vehicle) within Assists.Window seconds before it and after its previous death, when
//     Assists.Hits is on; at most one
//     assist per death, and never for the player's own kill. Every assist is also listed in
//     MissionResult.Assists with the death it was given for.
func Compute(d *digest.Digest, rules *Rules) MissionResult {
	res := MissionResult{Duration: d.Duration()}
	if d.End != nil {
		res.EndSide, res.EndMessage = d.End.Side, d.End.Message
	}

	entities := make(map[int]*digest.Entity, len(d.Entities))
	for i := range d.Entities {
		entities[d.Entities[i].ID] = &d.Entities[i]
	}

	byName := map[string]int{}       // clean name -> index in res.Players
	playerOf := map[int]*PlayerRow{} // entity id -> row
	for _, e := range d.Entities {
		if !e.IsPlayer {
			continue
		}
		name, squadRaw, squad := rules.ParseName(e.Name)
		if !rules.ValidPlayer(name) {
			continue
		}
		idx, ok := byName[name]
		if !ok {
			idx = len(res.Players)
			byName[name] = idx
			res.Players = append(res.Players, PlayerRow{
				Name: name, RawName: strings.TrimSpace(e.Name), SquadRaw: squadRaw, Squad: squad, Side: e.Side,
			})
		}
		row := &res.Players[idx]
		if row.Squad == "" && squad != "" {
			row.SquadRaw, row.Squad = squadRaw, squad
		}
		row.Shots += e.Shots
	}
	// Pointers are taken only after the slice stopped growing.
	for _, e := range d.Entities {
		if !e.IsPlayer {
			continue
		}
		name, _, _ := rules.ParseName(e.Name)
		if idx, ok := byName[name]; ok && rules.ValidPlayer(name) {
			playerOf[e.ID] = &res.Players[idx]
		}
	}

	sideOf := func(id int) string {
		if e := entities[id]; e != nil {
			return e.Side
		}
		return ""
	}

	var vehicles []*digest.Entity
	for i := range d.Entities {
		if len(d.Entities[i].Crew) > 0 {
			vehicles = append(vehicles, &d.Entities[i])
		}
	}

	delay := d.CaptureDelay
	if delay <= 0 {
		delay = 1
	}
	res.CaptureDelay = delay
	assistFrames := int(math.Ceil(float64(rules.assists.Window) / delay))
	// Damage on each unit in frame order: its hits, and the destruction of the vehicle it sat in.
	hitsOn := map[int][]damage{}
	for _, h := range d.Hits {
		hitsOn[h.Victim] = append(hitsOn[h.Victim], damage{frame: h.Frame, shooter: h.Shooter})
	}
	for _, k := range d.Kills {
		v := entities[k.Victim]
		if v == nil || v.Type != "vehicle" || k.Killer < 0 || k.Killer == k.Victim {
			continue
		}
		crew := v.CrewAt(k.Frame)
		if len(crew) == 0 {
			crew = v.CrewAt(k.Frame - 1)
		}
		for _, id := range crew {
			hitsOn[id] = append(hitsOn[id], damage{frame: k.Frame, shooter: k.Killer, vehicle: true, weapon: k.Weapon, distance: k.Distance})
		}
	}
	for _, hs := range hitsOn {
		slices.SortStableFunc(hs, func(a, b damage) int { return a.frame - b.frame })
	}
	lastDeath := map[int]int{} // entity id -> frame of its previous death (respawned players)

	for _, k := range d.Kills {
		victimEnt := entities[k.Victim]
		isVehicle := victimEnt != nil && victimEnt.Type == "vehicle"
		victim := playerOf[k.Victim]

		var damaged []damage // enemy damage on a soldier before this death, latest first
		if !isVehicle {
			from := k.Frame - assistFrames
			if prev, ok := lastDeath[k.Victim]; ok && prev > from {
				from = prev
			}
			damaged = enemyDamage(hitsOn[k.Victim], sideOf, k, from)
			lastDeath[k.Victim] = k.Frame
		}
		// A death the recording names no killer for goes to whoever damaged the victim last; the
		// assists of their crew are those of the moment of that damage.
		cause, crewFrame := "", k.Frame
		if len(damaged) > 0 && noKiller(k, entities) {
			last := damaged[0]
			k.Killer, k.Weapon, k.Distance, crewFrame = last.shooter, last.weapon, last.distance, last.frame
			cause = CauseLastHit
			if last.vehicle {
				cause = CauseVehicle
			}
		}

		var killer *PlayerRow
		if k.Killer >= 0 && k.Killer != k.Victim {
			killer = playerOf[k.Killer]
		}
		teamKill := false
		credited := &credits{done: map[*PlayerRow]bool{}}
		if killer != nil {
			credited.done[killer] = true
			if isVehicle {
				killer.VehicleKills++
			} else {
				killer.Kills++
				ks, vs := sideOf(k.Killer), sideOf(k.Victim)
				if victim != nil && ks != "" && ks == vs {
					killer.TeamKills++
					teamKill = true
				}
				if !teamKill {
					creditCrewAssists(vehicles, playerOf, k.Killer, crewFrame, credited)
				}
			}
		}
		if rules.assists.Hits {
			for _, h := range slices.Backward(damaged) {
				credited.add(playerOf[h.shooter], AssistHit)
			}
		}
		if victim != nil && !isVehicle {
			victim.Deaths++
		}
		if killer == nil && victim == nil && len(credited.players) == 0 {
			continue
		}

		row := KillRow{
			Frame: k.Frame, Weapon: k.Weapon, Distance: k.Distance, TeamKill: teamKill, Cause: cause,
			KillerSide: sideOf(k.Killer), VictimSide: sideOf(k.Victim),
		}
		switch {
		case killer != nil:
			row.Killer, row.KillerSquad, row.KillerIsPlayer = killer.Name, killer.Squad, true
		case k.Killer == k.Victim:
			if victim != nil {
				row.Killer, row.KillerSquad = victim.Name, victim.Squad
			}
		case k.Killer >= 0 && entities[k.Killer] != nil:
			row.Killer = entities[k.Killer].Name
		}
		switch {
		case isVehicle:
			row.VictimKind, row.Victim, row.VehicleClass = VictimVehicle, victimEnt.Name, victimEnt.Class
		case victim != nil:
			row.VictimKind, row.Victim, row.VictimSquad = VictimPlayer, victim.Name, victim.Squad
		default:
			row.VictimKind = VictimAI
			if victimEnt != nil {
				row.Victim = victimEnt.Name
			}
		}
		for i, p := range credited.players {
			res.Assists = append(res.Assists, AssistRow{
				Frame: k.Frame, Player: p.Name, PlayerSquad: p.Squad, Kind: credited.kinds[i],
				Killer: row.Killer, Victim: row.Victim, VictimSquad: row.VictimSquad, VictimKind: row.VictimKind,
			})
		}
		if killer != nil || victim != nil {
			res.Kills = append(res.Kills, row)
		}
	}

	for _, h := range d.Hits {
		if h.Shooter < 0 || h.Shooter == h.Victim {
			continue
		}
		if e := entities[h.Victim]; e != nil && e.Type == "vehicle" {
			continue
		}
		if shooter := playerOf[h.Shooter]; shooter != nil {
			shooter.Hits++
		}
	}
	return res
}
