package localprogress

import (
	"errors"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// HandoverEntry a jóváhagyott leadás vagy megmaradó készlet típusbontása.
// A három típus külön készletrész: egyik sem helyettesíti a másikat.
type HandoverEntry struct {
	ItemID  string `json:"itemId"`
	Regular int64  `json:"regular"`
	FIR     int64  `json:"fir"`
	Unknown int64  `json:"unknown"`
}

// RecordHandover a gyűjtött mennyiséget megtartja, és atomikusan rögzíti
// a saját készletből felhasznált darabokat. Ugyanaz a művelet ugyanazzal
// a tartalommal ismételve nem von le újra; eltérő tartalmat nem ír felül.
func (s *Store) RecordHandover(charUID, gameMode, operationID string, entries []HandoverEntry) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validHandoverOperation(operationID) {
		return Snapshot{}, ErrInvalidIdentity
	}
	canonical, err := canonicalHandoverEntries(entries)
	if err != nil {
		return Snapshot{}, err
	}
	path, err := s.path(charUID, gameMode)
	if err != nil {
		return Snapshot{}, err
	}
	state, err := readState(path, charUID, gameMode)
	if errors.Is(err, os.ErrNotExist) {
		state = emptyState(charUID, gameMode)
	} else if err != nil {
		return Snapshot{}, err
	}
	if previous, exists := state.HandoverReceipts[operationID]; exists {
		if !sameHandoverEntries(previous, canonical) {
			return Snapshot{}, ErrHandoverConflict
		}
		return stockSnapshot(state), nil
	}
	for _, entry := range canonical {
		stock, exists := state.Stock[entry.ItemID]
		if !exists {
			return Snapshot{}, ErrItemNotFound
		}
		used := state.LocalUsed[entry.ItemID]
		if entry.Regular > state.RegularStock[entry.ItemID]-used.Regular || entry.FIR > state.FIRStock[entry.ItemID]-used.FIR || entry.Unknown > stock-state.RegularStock[entry.ItemID]-state.FIRStock[entry.ItemID]-used.Unknown {
			return Snapshot{}, ErrHandoverUnavailable
		}
		used.Regular += entry.Regular
		used.FIR += entry.FIR
		used.Unknown += entry.Unknown
		state.LocalUsed[entry.ItemID] = used
	}
	state.HandoverReceipts[operationID] = canonical
	if err := s.commit(path, state); err != nil {
		return Snapshot{}, err
	}
	return stockSnapshot(state), nil
}

// RecordMarketRemoval kizárólag a már megerősített API-csökkentés minőségi
// jelölését módosítja. Sem Market teljes készletet, sem helyi felhasználást
// nem ment; a tranzakció ismétlését a hívó tartós műveletnaplója kezeli.
// A megmaradó típusbontás csak a művelet előtt rögzített jelölésből és már
// megerősített API-eredményből adható meg; köztes frissítés után is helyreállítja.
func (s *Store) RecordMarketRemoval(charUID, gameMode string, removed HandoverEntry, before, after int64, remaining ...HandoverEntry) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validID(removed.ItemID) {
		return Snapshot{}, ErrInvalidIdentity
	}
	total, err := handoverEntryQuantity(removed)
	if err != nil || !validQuantity(before) || !validQuantity(after) || total <= 0 || total != before-after {
		return Snapshot{}, ErrInvalidQuantity
	}
	if len(remaining) > 1 {
		return Snapshot{}, ErrInvalidQuantity
	}
	var expected HandoverEntry
	if len(remaining) == 1 {
		expected = remaining[0]
		if !validID(expected.ItemID) || expected.ItemID != removed.ItemID {
			return Snapshot{}, ErrInvalidIdentity
		}
		totalRemaining, err := handoverEntryQuantity(expected)
		if err != nil || totalRemaining != after {
			return Snapshot{}, ErrInvalidQuantity
		}
	}
	path, err := s.path(charUID, gameMode)
	if err != nil {
		return Snapshot{}, err
	}
	state, err := readState(path, charUID, gameMode)
	if errors.Is(err, os.ErrNotExist) {
		state = emptyState(charUID, gameMode)
	} else if err != nil {
		return Snapshot{}, err
	}
	annotation, annotated := state.MarketKinds[removed.ItemID]
	if after == 0 && !annotated {
		return stockSnapshot(state), nil
	}
	afterHash := marketStockHash(removed.ItemID, after)
	if annotation.StockHash == afterHash && (len(remaining) == 0 || annotation.Regular == expected.Regular && annotation.FIR == expected.FIR) {
		if after == 0 {
			delete(state.MarketKinds, removed.ItemID)
			if err := s.commit(path, state); err != nil {
				return Snapshot{}, err
			}
		}
		return stockSnapshot(state), nil
	}
	if annotation.StockHash == marketStockHash(removed.ItemID, before) {
		if removed.Regular > annotation.Regular || removed.FIR > annotation.FIR || removed.Unknown > before-annotation.Regular-annotation.FIR {
			return Snapshot{}, ErrHandoverUnavailable
		}
		annotation.Regular -= removed.Regular
		annotation.FIR -= removed.FIR
		if len(remaining) == 1 && (annotation.Regular != expected.Regular || annotation.FIR != expected.FIR) {
			return Snapshot{}, ErrHandoverUnavailable
		}
	} else if len(remaining) == 0 {
		// Külső készletváltozás után a korábbi jelölés nem bizonyított.
		annotation = marketKindAnnotation{}
	}
	if len(remaining) == 1 {
		// A tartós műveletnapló megerősített célértéke az elveszett jelölést
		// is pótolja. Az API-készlet és az unknown darabszám nincs a fájlban.
		annotation.Regular, annotation.FIR = expected.Regular, expected.FIR
	}
	if after == 0 {
		delete(state.MarketKinds, removed.ItemID)
	} else {
		annotation.StockHash = afterHash
		state.MarketKinds[removed.ItemID] = annotation
	}
	if err := s.commit(path, state); err != nil {
		return Snapshot{}, err
	}
	return stockSnapshot(state), nil
}

func projectLocalStock(item *Item, state storedProgress) {
	used := state.LocalUsed[item.ItemID]
	item.LocalCollectedQuantity = state.Stock[item.ItemID]
	item.LocalCollectedRegularQuantity, item.LocalCollectedFIRQuantity = state.RegularStock[item.ItemID], state.FIRStock[item.ItemID]
	item.LocalUsedRegularQuantity, item.LocalUsedFIRQuantity = used.Regular, used.FIR
	item.LocalUsedQuantity = used.Regular + used.FIR + used.Unknown
	item.LocalAvailableQuantity = item.LocalCollectedQuantity - item.LocalUsedQuantity
	item.LocalRegularQuantity = item.LocalCollectedRegularQuantity - used.Regular
	item.LocalFIRQuantity = item.LocalCollectedFIRQuantity - used.FIR
}

func validHandoverOperation(value string) bool {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func handoverEntryQuantity(entry HandoverEntry) (int64, error) {
	if !validQuantity(entry.Regular) || !validQuantity(entry.FIR) || !validQuantity(entry.Unknown) {
		return 0, ErrInvalidQuantity
	}
	total := entry.Regular
	if err := addQuantity(&total, entry.FIR); err != nil {
		return 0, err
	}
	if err := addQuantity(&total, entry.Unknown); err != nil {
		return 0, err
	}
	return total, nil
}

func canonicalHandoverEntries(entries []HandoverEntry) ([]HandoverEntry, error) {
	byItem := make(map[string]HandoverEntry)
	for _, entry := range entries {
		if !validID(entry.ItemID) {
			return nil, ErrInvalidIdentity
		}
		total, err := handoverEntryQuantity(entry)
		if err != nil {
			return nil, err
		}
		if total == 0 {
			continue
		}
		combined := byItem[entry.ItemID]
		combined.ItemID = entry.ItemID
		if err := addQuantity(&combined.Regular, entry.Regular); err != nil {
			return nil, err
		}
		if err := addQuantity(&combined.FIR, entry.FIR); err != nil {
			return nil, err
		}
		if err := addQuantity(&combined.Unknown, entry.Unknown); err != nil {
			return nil, err
		}
		if _, err := handoverEntryQuantity(combined); err != nil {
			return nil, err
		}
		byItem[entry.ItemID] = combined
	}
	canonical := make([]HandoverEntry, 0, len(byItem))
	for _, entry := range byItem {
		canonical = append(canonical, entry)
	}
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].ItemID < canonical[j].ItemID })
	return canonical, nil
}

func sameHandoverEntries(left, right []HandoverEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func validateHandovers(state storedProgress) error {
	summed := make(map[string]usedLocalStock)
	for operationID, entries := range state.HandoverReceipts {
		if !validHandoverOperation(operationID) || entries == nil {
			return ErrCorruptStore
		}
		canonical, err := canonicalHandoverEntries(entries)
		if err != nil || !sameHandoverEntries(entries, canonical) {
			return ErrCorruptStore
		}
		for _, entry := range entries {
			used := summed[entry.ItemID]
			if addQuantity(&used.Regular, entry.Regular) != nil || addQuantity(&used.FIR, entry.FIR) != nil || addQuantity(&used.Unknown, entry.Unknown) != nil {
				return ErrCorruptStore
			}
			summed[entry.ItemID] = used
		}
	}
	for id, used := range state.LocalUsed {
		stock, exists := state.Stock[id]
		if !exists || !validID(id) || !validQuantity(used.Regular) || !validQuantity(used.FIR) || !validQuantity(used.Unknown) || used.Regular > state.RegularStock[id] || used.FIR > state.FIRStock[id] || used.Unknown > stock-state.RegularStock[id]-state.FIRStock[id] || summed[id] != used {
			return ErrCorruptStore
		}
	}
	for id, used := range summed {
		if state.LocalUsed[id] != used {
			return ErrCorruptStore
		}
	}
	return nil
}
