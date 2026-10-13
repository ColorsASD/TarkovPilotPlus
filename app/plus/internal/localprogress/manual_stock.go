package localprogress

import (
	"errors"
	"os"
)

// StockAmounts a különálló Regular, FiR és ismeretlen készletrészek darabszáma.
// Kézi korrekciónál egyik rész sem helyettesíti a másikat.
type StockAmounts struct {
	Regular int64 `json:"regular"`
	FIR     int64 `json:"fir"`
	Unknown int64 `json:"unknown"`
}

// ManualStockCorrection a még elérhető saját készletet, illetve a Market élő
// készletéhez tartozó helyi típusjelölést állítja. A nil rész változatlan marad.
// ObservedMarketQuantity csak ellenőrzéshez és a jelölés lenyomatához kell;
// a Market teljes készlete nem kerül a helyi fájlba.
type ManualStockCorrection struct {
	ItemID                 string        `json:"itemId"`
	LocalAvailable         *StockAmounts `json:"localAvailable,omitempty"`
	MarketKinds            *StockAmounts `json:"marketKinds,omitempty"`
	ObservedMarketQuantity int64         `json:"observedMarketQuantity"`
}

// CorrectStock egyetlen atomikus mentéssel korrigálja a kiválasztott készletrészt.
// A leadott darabok és nyugtáik változatlanok: az új gyűjtött készlet típusonként
// a már felhasznált és a kívánt elérhető mennyiség összege. Nulla elérhető készlet
// sem hoz létre leadást. A hívó ellenőrzi az aktuális követési listát és a Market
// frissen kiolvasott teljes készletét; ez a tároló nem végez API-hívást.
func (s *Store) CorrectStock(charUID, gameMode string, correction ManualStockCorrection) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validID(correction.ItemID) {
		return Snapshot{}, ErrInvalidIdentity
	}
	if correction.LocalAvailable == nil && correction.MarketKinds == nil {
		return Snapshot{}, ErrInvalidQuantity
	}
	var local, market StockAmounts
	var localTotal int64
	if correction.LocalAvailable != nil {
		local = *correction.LocalAvailable
		var err error
		localTotal, err = stockAmountsQuantity(local)
		if err != nil {
			return Snapshot{}, err
		}
	}
	if correction.MarketKinds != nil {
		market = *correction.MarketKinds
		marketTotal, err := stockAmountsQuantity(market)
		if err != nil || !validQuantity(correction.ObservedMarketQuantity) || marketTotal != correction.ObservedMarketQuantity {
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
	if correction.LocalAvailable != nil {
		used := state.LocalUsed[correction.ItemID]
		collected := used.Regular + used.FIR + used.Unknown
		if err := addQuantity(&collected, localTotal); err != nil {
			return Snapshot{}, err
		}
		state.Stock[correction.ItemID] = collected
		state.RegularStock[correction.ItemID] = used.Regular + local.Regular
		state.FIRStock[correction.ItemID] = used.FIR + local.FIR
	}
	if correction.MarketKinds != nil {
		if correction.ObservedMarketQuantity == 0 {
			delete(state.MarketKinds, correction.ItemID)
		} else {
			state.MarketKinds[correction.ItemID] = marketKindAnnotation{
				Regular: market.Regular, FIR: market.FIR,
				StockHash: marketStockHash(correction.ItemID, correction.ObservedMarketQuantity),
			}
		}
	}
	if err := s.commit(path, state); err != nil {
		return Snapshot{}, err
	}
	return stockSnapshot(state), nil
}

func stockAmountsQuantity(amounts StockAmounts) (int64, error) {
	return handoverEntryQuantity(HandoverEntry{Regular: amounts.Regular, FIR: amounts.FIR, Unknown: amounts.Unknown})
}
