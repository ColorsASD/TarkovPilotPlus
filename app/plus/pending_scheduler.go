package plus

import "strings"

type pendingMarketProfile struct {
	key       string
	character string
	mode      string
}

// Egy profil elakadt feje megőrzi a mögötte álló műveletek sorrendjét,
// de másik profil mentéseit nem állítja meg. Egy workerfutás nem próbálja
// újra a megmaradt fejet; az egy következő workerfutásra marad.
func nextPendingMarketAdd(queue []pendingMarketAdd, attempted map[uint64]struct{}) (pendingMarketAdd, bool) {
	seen := make(map[pendingMarketProfile]struct{})
	for _, entry := range queue {
		profile := pendingMarketProfile{key: entry.Key, character: entry.Opts.CharacterUID, mode: strings.ToLower(entry.Opts.Mode)}
		if _, exists := seen[profile]; exists {
			continue
		}
		seen[profile] = struct{}{}
		if _, tried := attempted[entry.ID]; tried {
			continue
		}
		return entry, true
	}
	return pendingMarketAdd{}, false
}
