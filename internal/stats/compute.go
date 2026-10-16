// Package stats turns recording digests into per-mission player statistics and aggregates
// them over date ranges.
package stats

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/smitt14ua/ocap-statistics/internal/digest"
)

// ComputeVersion changes whenever the per-mission rules below change. Stored missions computed
// with another version or nickname format are recomputed from their digests.
const ComputeVersion = 1

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
	Shots        int    `json:"shotsFired"`
	Hits         int    `json:"hits"`
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
}

type MissionResult struct {
	Duration   float64
	EndSide    string
	EndMessage string
	Players    []PlayerRow
	Kills      []KillRow
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

// Rules is a compiled Nickname.
type Rules struct {
	squadRe *regexp.Regexp
	aliases map[string]string
	ignore  map[string]bool
	key     string
}

// NewRules validates and compiles a nickname format.
func NewRules(n Nickname) (*Rules, error) {
	r := &Rules{aliases: map[string]string{}, ignore: map[string]bool{}}
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
	b, _ := json.Marshal(n) // map keys are sorted, so the encoding is stable
	sum := sha256.Sum256(append([]byte(fmt.Sprintf("v%d:", ComputeVersion)), b...))
	r.key = hex.EncodeToString(sum[:8])
	return r, nil
}

// DefaultRules returns compiled DefaultNickname rules.
func DefaultRules() *Rules {
	r, err := NewRules(DefaultNickname())
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

// Compute derives per-player statistics for one mission.
//
// Rules:
//   - a player is an entity with isPlayer=1; entities with the same clean name are merged;
//   - kill: "killed" event whose killer is a player and not the victim; killing a vehicle
//     counts as "vehicles destroyed" instead of a kill;
//   - team kill: a kill where killer and victim (both players) are on the same side;
//   - death: "killed" event whose victim is a player (suicides included);
//   - hits: "hit" events by a player on a non-vehicle target; shots: fired frames.
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

	for _, k := range d.Kills {
		victimEnt := entities[k.Victim]
		isVehicle := victimEnt != nil && victimEnt.Type == "vehicle"
		victim := playerOf[k.Victim]
		var killer *PlayerRow
		if k.Killer >= 0 && k.Killer != k.Victim {
			killer = playerOf[k.Killer]
		}
		teamKill := false
		if killer != nil {
			if isVehicle {
				killer.VehicleKills++
			} else {
				killer.Kills++
				ks, vs := sideOf(k.Killer), sideOf(k.Victim)
				if victim != nil && ks != "" && ks == vs {
					killer.TeamKills++
					teamKill = true
				}
			}
		}
		if victim != nil && !isVehicle {
			victim.Deaths++
		}
		if killer == nil && victim == nil {
			continue
		}

		row := KillRow{
			Frame: k.Frame, Weapon: k.Weapon, Distance: k.Distance, TeamKill: teamKill,
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
		res.Kills = append(res.Kills, row)
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
