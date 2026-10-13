package localprogress

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
)

// TransferAvailable a sikeresen feltöltött, befoglalt készletrészt egyszer
// vezeti ki. A közben hozzáadott darabok és a leadási előzmény megmaradnak;
// a feltöltés nem felhasználás, ezért a Used érték nem változik.
func (s *Store) TransferAvailable(charUID, gameMode, operationID, itemID string, amounts StockAmounts) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, state, recorded, err := s.prepareTransfer(charUID, gameMode, operationID, itemID, amounts)
	if err != nil {
		return Snapshot{}, err
	}
	if !recorded {
		if err := s.commit(path, state); err != nil {
			return Snapshot{}, err
		}
	}
	return stockSnapshot(state), nil
}

// ValidateTransferAvailable a PATCH előtt ellenőrzi a későbbi átadás
// készletét és mentési méretét. A fájlt és a nyugtákat nem változtatja.
func (s *Store) ValidateTransferAvailable(charUID, gameMode, operationID, itemID string, amounts StockAmounts) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, state, _, err := s.prepareTransfer(charUID, gameMode, operationID, itemID, amounts)
	if err != nil {
		return err
	}
	if err := validateStoredState(state); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if int64(len(data)) > maxStoreBytes {
		return ErrStoreTooLarge
	}
	return nil
}

func (s *Store) prepareTransfer(charUID, gameMode, operationID, itemID string, amounts StockAmounts) (string, storedProgress, bool, error) {
	if !validTransferOperation(operationID) || !validID(itemID) {
		return "", storedProgress{}, false, ErrInvalidIdentity
	}
	quantity, err := stockAmountsQuantity(amounts)
	if err != nil || quantity == 0 {
		return "", storedProgress{}, false, ErrInvalidQuantity
	}
	entry := HandoverEntry{ItemID: itemID, Regular: amounts.Regular, FIR: amounts.FIR, Unknown: amounts.Unknown}
	path, err := s.path(charUID, gameMode)
	if err != nil {
		return "", storedProgress{}, false, err
	}
	state, err := readState(path, charUID, gameMode)
	if errors.Is(err, os.ErrNotExist) {
		return "", storedProgress{}, false, ErrItemNotFound
	}
	if err != nil {
		return "", storedProgress{}, false, err
	}
	if previous, exists := state.TransferReceipts[operationID]; exists {
		if previous != entry {
			return "", storedProgress{}, false, ErrHandoverConflict
		}
		return path, state, true, nil
	}
	stock, exists := state.Stock[itemID]
	if !exists {
		return "", storedProgress{}, false, ErrItemNotFound
	}
	used := state.LocalUsed[itemID]
	if amounts.Regular > state.RegularStock[itemID]-used.Regular || amounts.FIR > state.FIRStock[itemID]-used.FIR ||
		amounts.Unknown > stock-state.RegularStock[itemID]-state.FIRStock[itemID]-used.Unknown {
		return "", storedProgress{}, false, ErrHandoverUnavailable
	}
	state.Stock[itemID] -= quantity
	state.RegularStock[itemID] -= amounts.Regular
	state.FIRStock[itemID] -= amounts.FIR
	state.TransferReceipts[operationID] = entry
	return path, state, false, nil
}

// TransferRecorded egy már lezárt átadás pontos nyugtáját ellenőrzi. A
// hívó így nem keveri össze az újonnan gyűjtött darabokat a régi csomaggal.
func (s *Store) TransferRecorded(charUID, gameMode, operationID, itemID string, amounts StockAmounts) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validTransferOperation(operationID) || !validID(itemID) {
		return false, ErrInvalidIdentity
	}
	quantity, err := stockAmountsQuantity(amounts)
	if err != nil || quantity == 0 {
		return false, ErrInvalidQuantity
	}
	path, err := s.path(charUID, gameMode)
	if err != nil {
		return false, err
	}
	state, err := readState(path, charUID, gameMode)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	entry, exists := state.TransferReceipts[operationID]
	if exists && entry != (HandoverEntry{ItemID: itemID, Regular: amounts.Regular, FIR: amounts.FIR, Unknown: amounts.Unknown}) {
		return false, ErrHandoverConflict
	}
	return exists, nil
}

func validTransferOperation(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == value
}

func validateTransfers(state storedProgress) error {
	for operationID, entry := range state.TransferReceipts {
		quantity, err := handoverEntryQuantity(entry)
		if !validTransferOperation(operationID) || !validID(entry.ItemID) || err != nil || quantity == 0 {
			return ErrCorruptStore
		}
	}
	return nil
}
