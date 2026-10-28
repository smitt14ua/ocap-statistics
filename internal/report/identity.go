package report

import (
	"cmp"
	"slices"

	"github.com/smitt14ua/ocap-statistics/internal/store"
)

// Identities merges the names a Steam account played under into one player, shown under the
// most recent of those names. Missions without Steam ids (older OCAP versions) join that
// player by name when the name belongs to one Steam account only; a name several accounts
// played under stays a player of its own there. Steam ids never leave this type.
type Identities struct {
	bySteam map[string]string   // Steam id → player name
	byName  map[string]string   // name of exactly one Steam account → player name
	names   map[string][]string // player name → its names, the player name first
}

// NewIdentities builds the identities from every name of every Steam account.
func NewIdentities(aliases []store.SteamAlias) *Identities {
	ids := &Identities{bySteam: map[string]string{}, byName: map[string]string{}, names: map[string][]string{}}
	// Most recent first: the first name seen for an account is its player name.
	aliases = slices.Clone(aliases)
	slices.SortFunc(aliases, func(a, b store.SteamAlias) int {
		return cmp.Or(cmp.Compare(b.LastDate, a.LastDate), cmp.Compare(b.LastID, a.LastID), cmp.Compare(a.Name, b.Name))
	})
	accounts := map[string]int{} // name → number of Steam accounts
	for _, a := range aliases {
		accounts[a.Name]++
		if _, ok := ids.bySteam[a.SteamID]; !ok {
			ids.bySteam[a.SteamID] = a.Name
		}
	}
	for _, a := range aliases {
		player := ids.bySteam[a.SteamID]
		if accounts[a.Name] == 1 {
			ids.byName[a.Name] = player
		}
		if !slices.Contains(ids.names[player], a.Name) {
			if a.Name == player {
				ids.names[player] = slices.Insert(ids.names[player], 0, a.Name)
			} else {
				ids.names[player] = append(ids.names[player], a.Name)
			}
		}
	}
	return ids
}

// Name returns the player a name with an optional Steam id belongs to.
func (ids *Identities) Name(name, steamID string) string {
	if p, ok := ids.bySteam[steamID]; ok && steamID != "" {
		return p
	}
	if p, ok := ids.byName[name]; ok {
		return p
	}
	return name
}

// Names returns every name merged into a player, the player name first.
func (ids *Identities) Names(player string) []string {
	if n := ids.names[player]; len(n) > 0 {
		return n
	}
	return []string{player}
}

// Aliases returns the other names of a player, the most recent first, or nil.
func (ids *Identities) Aliases(player string) []string {
	if n := ids.Names(player); len(n) > 1 {
		return slices.Clone(n[1:])
	}
	return nil
}

// Renamer renames the players of a set of missions, also in their kills.
type Renamer struct {
	ids   *Identities
	steam map[missionName]string // a player's name in a mission → Steam id ("" if none)
}

type missionName struct {
	mission int64
	name    string
}

// Merge renames rows to their players in place and returns the renamer of those missions.
func (ids *Identities) Merge(rows []store.Row) *Renamer {
	r := &Renamer{ids: ids, steam: make(map[missionName]string, len(rows))}
	for i := range rows {
		row := &rows[i]
		r.steam[missionName{row.MissionID, row.Name}] = row.SteamID
		row.Name = ids.Name(row.Name, row.SteamID)
	}
	return r
}

// Of returns the player a name of a mission belongs to; names that are not players of the
// mission (AI units, vehicles) are kept.
func (r *Renamer) Of(missionID int64, name string) string {
	steamID, ok := r.steam[missionName{missionID, name}]
	if !ok {
		return name
	}
	return r.ids.Name(name, steamID)
}

// Kill renames the killer and the victim of a kill.
func (r *Renamer) Kill(k *store.Kill) {
	k.Killer = r.Of(k.MissionID, k.Killer)
	k.Victim = r.Of(k.MissionID, k.Victim)
}
