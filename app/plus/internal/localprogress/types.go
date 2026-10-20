// Package localprogress a Tarkov Market követelményeit és közös Any csoportjait
// vetíti a gyűjtési nézetbe. A számítások nem végeznek mentést vagy API-hívást.
package localprogress

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

const (
	OriginMarket       = "tarkov-market"
	MaxQuantity  int64 = 1_000_000_000
)

var (
	ErrInvalidSnapshot  = errors.New("A Market előrehaladás pillanatképe hiányos vagy hibás")
	ErrSnapshotMismatch = errors.New("A karakter, a játékmód vagy a Market revíziója eltér a pillanatképekben")
	ErrInvalidIdentity  = errors.New("A Market előrehaladás karakter- vagy tárgyazonosítója hibás")
	ErrInvalidQuantity  = errors.New("A Market készlet mennyisége 0 és 1000000000 közötti egész lehet")
	ErrItemNotFound     = errors.New("A tárgy nem szerepel a Market követési listában")
	ErrAmbiguousItem    = errors.New("Azonos tárgyazonosítóhoz több készletváltozat tartozik; a hozzáadás nem egyértelmű")
)

type Source struct {
	market.TrackedSource
	Origin       string `json:"origin"`
	GroupID      string `json:"groupId,omitempty"`
	Alternatives int    `json:"alternatives,omitempty"`
	GroupAmount  int64  `json:"groupAmount,omitempty"`
	// A legutóbbi Market-válasz csoportadata; a helyi kiosztás nem írja át.
	MarketGroupProgressKnown     bool  `json:"marketGroupProgressKnown"`
	MarketGroupAvailableQuantity int64 `json:"marketGroupAvailableQuantity"`
	AllocationKnown              bool  `json:"allocationKnown"`
	AllocatedQuantity            int64 `json:"allocatedQuantity"`
	GroupRemainingQuantity       int64 `json:"groupRemainingQuantity"`
}

type Item struct {
	ItemID                             string   `json:"itemId"`
	MarketTracked                      bool     `json:"marketTracked"`
	MarketRequiredQuantity             int64    `json:"marketRequiredQuantity"`
	MarketAvailableQuantity            int64    `json:"marketAvailableQuantity"`
	Name                               string   `json:"name"`
	ShortName                          string   `json:"shortName"`
	Slots                              float64  `json:"slots"`
	Icon                               string   `json:"icon"`
	RequiredQuantity                   int64    `json:"requiredQuantity"`
	SpentQuantity                      int64    `json:"spentQuantity"`
	AvailableQuantity                  int64    `json:"availableQuantity"`
	RemainingQuantity                  int64    `json:"remainingQuantity"`
	CollectionCalculated               bool     `json:"collectionCalculated"`
	CollectionRequiredQuantity         int64    `json:"collectionRequiredQuantity"`
	CollectionRemainingQuantity        int64    `json:"collectionRemainingQuantity"`
	CollectionMarketRequiredQuantity   int64    `json:"collectionMarketRequiredQuantity"`
	CollectionFIRAvailableQuantity     int64    `json:"collectionFIRAvailableQuantity"`
	CollectionRegularAvailableQuantity int64    `json:"collectionRegularAvailableQuantity"`
	Sources                            []Source `json:"sources"`
}

type Snapshot struct {
	CharUID              string `json:"charUid"`
	GameMode             string `json:"gameMode"`
	Revision             string `json:"revision"`
	FIRInventorySeparate bool   `json:"firInventorySeparate"`
	Items                []Item `json:"items"`
	// Az eredeti csoportadat a több készletváltozathoz kapcsolódó, csak
	// olvasható nézetben is megmarad; a készletkiosztás nem használja.
	MarketGroups []market.TrackedGroup `json:"marketGroups,omitempty"`
	// A következő darab helyi előrejelzése ugyanannak a revíziónak a teljes
	// célállapotát használja; az API-ból érkező új célok is itt jelennek meg.
	MarketQuests     []market.Quest          `json:"marketQuests,omitempty"`
	MarketHideout    []market.HideoutUpgrade `json:"marketHideout,omitempty"`
	MetadataComplete bool                    `json:"metadataComplete"`
}

// Clone a hívó kezében lévő pillanatképet a forráslistákig másolja.
func Clone(snapshot Snapshot) Snapshot {
	snapshot.Items = append([]Item{}, snapshot.Items...)
	for i := range snapshot.Items {
		snapshot.Items[i].Sources = append([]Source{}, snapshot.Items[i].Sources...)
	}
	snapshot.MarketGroups = append([]market.TrackedGroup(nil), snapshot.MarketGroups...)
	for i := range snapshot.MarketGroups {
		snapshot.MarketGroups[i].ItemIDs = append([]string(nil), snapshot.MarketGroups[i].ItemIDs...)
	}
	snapshot.MarketQuests = append([]market.Quest(nil), snapshot.MarketQuests...)
	for i := range snapshot.MarketQuests {
		snapshot.MarketQuests[i].QuestIDAliases = append([]string(nil), snapshot.MarketQuests[i].QuestIDAliases...)
	}
	snapshot.MarketHideout = append([]market.HideoutUpgrade(nil), snapshot.MarketHideout...)
	return snapshot
}

func validID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, ch := range value {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

// A Market upgradeKey olvasható modulnévből és szintből állhat. Nem fájlnév
// vagy URL-részlet: az eredeti, szóközt vagy ékezetet tartalmazó kulcsot őrizzük.
func validUpgradeKey(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, ch := range value {
		if unicode.IsControl(ch) {
			return false
		}
	}
	return true
}

func validMode(mode string) bool { return mode == "PVP" || mode == "PVE" || mode == "SEASON" }
func validQuantity(q int64) bool { return q >= 0 && q <= MaxQuantity }

func validateSnapshot(snapshot Snapshot) error {
	if !validID(snapshot.CharUID) || !validMode(snapshot.GameMode) {
		return ErrInvalidIdentity
	}
	if snapshot.Items == nil || snapshot.Revision == "" || len(snapshot.Revision) > 256 {
		return ErrInvalidSnapshot
	}
	type groupProgress struct {
		known     bool
		available int64
	}
	groups := make(map[string]groupProgress)
	for _, item := range snapshot.Items {
		if !validID(item.ItemID) {
			return ErrInvalidIdentity
		}
		if !validQuantity(item.RequiredQuantity) || !validQuantity(item.SpentQuantity) || !validQuantity(item.AvailableQuantity) || !validQuantity(item.RemainingQuantity) {
			return ErrInvalidQuantity
		}
		if !validQuantity(item.MarketRequiredQuantity) || !validQuantity(item.MarketAvailableQuantity) {
			return ErrInvalidQuantity
		}
		for _, source := range item.Sources {
			if source.QuestID != "" && !validID(source.QuestID) || source.UpgradeKey != "" && !validUpgradeKey(source.UpgradeKey) {
				return ErrInvalidSnapshot
			}
			if !validQuantity(source.Amount) || !validQuantity(source.RemainingRequiredQuantity) || !validQuantity(source.GroupAmount) {
				return ErrInvalidQuantity
			}
			if source.Origin != OriginMarket {
				return ErrInvalidSnapshot
			}
			if source.Alternatives < 0 || source.Alternatives > 100_000 || (source.Alternatives > 1 && source.GroupID == "") {
				return ErrInvalidSnapshot
			}
			if !validQuantity(source.MarketGroupAvailableQuantity) {
				return ErrInvalidQuantity
			}
			if !source.MarketGroupProgressKnown && source.MarketGroupAvailableQuantity != 0 {
				return ErrInvalidSnapshot
			}
			if source.MarketGroupProgressKnown {
				if source.GroupID == "" || source.Amount == 0 {
					return ErrInvalidSnapshot
				}
			}
			if source.GroupID != "" {
				reported := groupProgress{source.MarketGroupProgressKnown, source.MarketGroupAvailableQuantity}
				if previous, exists := groups[source.GroupID]; exists && previous != reported {
					return ErrInvalidSnapshot
				}
				groups[source.GroupID] = reported
			}
		}
	}
	return nil
}

func active(source Source) bool {
	return !source.Completed && !source.Canceled && !source.Optional
}

func countRows(snapshot Snapshot) map[string]int {
	counts := make(map[string]int)
	for _, item := range snapshot.Items {
		counts[item.ItemID]++
	}
	return counts
}

// A források fennmaradó követelménye és a tényleges készlet külön adat.
// A közös készletet nem osztjuk szét a FiR és a sima követelmények között.
func recount(item *Item) error {
	var required int64
	for _, source := range item.Sources {
		if active(source) {
			if source.RemainingRequiredQuantity > MaxQuantity-required {
				return ErrInvalidQuantity
			}
			required += source.RemainingRequiredQuantity
		}
	}
	item.RequiredQuantity = required
	item.RemainingQuantity = max(0, required-item.AvailableQuantity)
	return nil
}
