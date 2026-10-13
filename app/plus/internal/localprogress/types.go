// Package localprogress a Market követett igényeit egészíti ki a tarkov.dev
// katalógusából. A Market készlete és a saját helyi készletrész külön forrás;
// ugyanahhoz a tárgyhoz tartozó mennyiségük csak az élő nézetben adódik össze.
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
	OriginDev          = "tarkov.dev"
	MaxQuantity  int64 = 1_000_000_000
)

var (
	ErrInvalidSnapshot     = errors.New("A helyi előrehaladás pillanatképe hiányos vagy hibás")
	ErrSnapshotMismatch    = errors.New("A karakter, a játékmód vagy a Market revíziója eltér a pillanatképekben")
	ErrInvalidIdentity     = errors.New("A helyi előrehaladás karakter- vagy tárgyazonosítója hibás")
	ErrInvalidQuantity     = errors.New("A helyi készlet mennyisége 0 és 1000000000 közötti egész lehet")
	ErrItemNotFound        = errors.New("A tárgy nem szerepel a helyi követési listában")
	ErrMarketItem          = errors.New("A tárgy a Tarkov Market követési listájában szerepel; készletét a Marketen kell módosítani")
	ErrAmbiguousItem       = errors.New("Azonos tárgyazonosítóhoz több készletváltozat tartozik; a helyi hozzáadás nem egyértelmű")
	ErrSeparateInventory   = errors.New("A külön Found in Raid készlet helyi módosítása nem támogatott")
	ErrCorruptStore        = errors.New("A helyi előrehaladás fájlja hibás; a mentett adatok nem lettek felülírva")
	ErrStoreTooLarge       = errors.New("A helyi előrehaladás fájlja túllépi a megengedett méretet")
	ErrHandoverConflict    = errors.New("Ehhez a leadási művelethez már eltérő elszámolás tartozik")
	ErrHandoverUnavailable = errors.New("A kiválasztott típusból nincs elegendő felhasználható helyi készlet")
)

type Source struct {
	market.TrackedSource
	Origin                 string `json:"origin"`
	GroupID                string `json:"groupId,omitempty"`
	Alternatives           int    `json:"alternatives,omitempty"`
	GroupAmount            int64  `json:"groupAmount,omitempty"`
	AllocationKnown        bool   `json:"allocationKnown"`
	AllocatedQuantity      int64  `json:"allocatedQuantity"`
	GroupRemainingQuantity int64  `json:"groupRemainingQuantity"`
}

type Item struct {
	ItemID                             string   `json:"itemId"`
	MarketTracked                      bool     `json:"marketTracked"`
	MarketRequiredQuantity             int64    `json:"marketRequiredQuantity"`
	MarketAvailableQuantity            int64    `json:"marketAvailableQuantity"`
	LocalAvailableQuantity             int64    `json:"localAvailableQuantity"`
	MarketRegularQuantity              int64    `json:"marketRegularQuantity"`
	MarketFIRQuantity                  int64    `json:"marketFIRQuantity"`
	MarketFallbackAvailableQuantity    int64    `json:"marketFallbackAvailableQuantity"`
	MarketFallbackRegularQuantity      int64    `json:"marketFallbackRegularQuantity"`
	MarketFallbackFIRQuantity          int64    `json:"marketFallbackFIRQuantity"`
	MarketFallbackUsedQuantity         int64    `json:"marketFallbackUsedQuantity"`
	MarketFallbackUsedRegularQuantity  int64    `json:"marketFallbackUsedRegularQuantity"`
	MarketFallbackUsedFIRQuantity      int64    `json:"marketFallbackUsedFIRQuantity"`
	LocalRegularQuantity               int64    `json:"localRegularQuantity"`
	LocalFIRQuantity                   int64    `json:"localFIRQuantity"`
	LocalCollectedQuantity             int64    `json:"localCollectedQuantity"`
	LocalCollectedRegularQuantity      int64    `json:"localCollectedRegularQuantity"`
	LocalCollectedFIRQuantity          int64    `json:"localCollectedFIRQuantity"`
	LocalUsedQuantity                  int64    `json:"localUsedQuantity"`
	LocalUsedRegularQuantity           int64    `json:"localUsedRegularQuantity"`
	LocalUsedFIRQuantity               int64    `json:"localUsedFIRQuantity"`
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
	// Csak memóriában tartjuk: a Merge által normalizált és lezárt
	// források mellett az eredeti Market-követelmény változását is észleljük.
	MarketRawSources []market.TrackedSource `json:"-"`
}

type Snapshot struct {
	CharUID              string `json:"charUid"`
	GameMode             string `json:"gameMode"`
	Revision             string `json:"revision"`
	FIRInventorySeparate bool   `json:"firInventorySeparate"`
	Items                []Item `json:"items"`
}

// Clone a hívó kezében lévő pillanatképet a forráslistákig másolja.
func Clone(snapshot Snapshot) Snapshot {
	snapshot.Items = append([]Item{}, snapshot.Items...)
	for i := range snapshot.Items {
		snapshot.Items[i].Sources = append([]Source{}, snapshot.Items[i].Sources...)
		if snapshot.Items[i].MarketRawSources != nil {
			snapshot.Items[i].MarketRawSources = append([]market.TrackedSource{}, snapshot.Items[i].MarketRawSources...)
		}
	}
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
	for _, item := range snapshot.Items {
		if !validID(item.ItemID) {
			return ErrInvalidIdentity
		}
		if !validQuantity(item.RequiredQuantity) || !validQuantity(item.SpentQuantity) || !validQuantity(item.AvailableQuantity) || !validQuantity(item.RemainingQuantity) {
			return ErrInvalidQuantity
		}
		if !validQuantity(item.MarketRequiredQuantity) || !validQuantity(item.MarketAvailableQuantity) || !validQuantity(item.LocalAvailableQuantity) {
			return ErrInvalidQuantity
		}
		if !validQuantity(item.MarketRegularQuantity) || !validQuantity(item.MarketFIRQuantity) || !validQuantity(item.LocalRegularQuantity) || !validQuantity(item.LocalFIRQuantity) {
			return ErrInvalidQuantity
		}
		if err := validateMarketFallbackItem(item); err != nil {
			return err
		}
		if item.MarketRegularQuantity > item.MarketAvailableQuantity-item.MarketFIRQuantity || item.LocalRegularQuantity > item.LocalAvailableQuantity-item.LocalFIRQuantity || item.MarketRegularQuantity+item.MarketFIRQuantity+item.LocalRegularQuantity+item.LocalFIRQuantity+item.MarketFallbackRegularQuantity+item.MarketFallbackFIRQuantity > item.AvailableQuantity {
			return ErrInvalidQuantity
		}
		if !validQuantity(item.LocalCollectedQuantity) || !validQuantity(item.LocalCollectedRegularQuantity) || !validQuantity(item.LocalCollectedFIRQuantity) || !validQuantity(item.LocalUsedQuantity) || !validQuantity(item.LocalUsedRegularQuantity) || !validQuantity(item.LocalUsedFIRQuantity) {
			return ErrInvalidQuantity
		}
		if item.LocalCollectedQuantity > 0 || item.LocalCollectedRegularQuantity > 0 || item.LocalCollectedFIRQuantity > 0 || item.LocalUsedQuantity > 0 || item.LocalUsedRegularQuantity > 0 || item.LocalUsedFIRQuantity > 0 {
			if item.LocalCollectedRegularQuantity > item.LocalCollectedQuantity-item.LocalCollectedFIRQuantity || item.LocalUsedRegularQuantity > item.LocalUsedQuantity-item.LocalUsedFIRQuantity || item.LocalUsedRegularQuantity > item.LocalCollectedRegularQuantity || item.LocalUsedFIRQuantity > item.LocalCollectedFIRQuantity || item.LocalUsedQuantity-item.LocalUsedRegularQuantity-item.LocalUsedFIRQuantity > item.LocalCollectedQuantity-item.LocalCollectedRegularQuantity-item.LocalCollectedFIRQuantity || item.LocalAvailableQuantity != item.LocalCollectedQuantity-item.LocalUsedQuantity || item.LocalRegularQuantity != item.LocalCollectedRegularQuantity-item.LocalUsedRegularQuantity || item.LocalFIRQuantity != item.LocalCollectedFIRQuantity-item.LocalUsedFIRQuantity {
				return ErrInvalidQuantity
			}
		}
		for _, source := range item.Sources {
			if source.QuestID != "" && !validID(source.QuestID) || source.UpgradeKey != "" && !validUpgradeKey(source.UpgradeKey) {
				return ErrInvalidSnapshot
			}
			if !validQuantity(source.Amount) || !validQuantity(source.RemainingRequiredQuantity) || !validQuantity(source.GroupAmount) {
				return ErrInvalidQuantity
			}
			if source.Origin != OriginMarket && source.Origin != OriginDev {
				return ErrInvalidSnapshot
			}
			if source.Alternatives < 0 || source.Alternatives > 100_000 || (source.Alternatives > 1 && source.GroupID == "") {
				return ErrInvalidSnapshot
			}
		}
	}
	return nil
}

func active(source Source) bool {
	return !source.Completed && !source.Canceled && !source.Optional
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
