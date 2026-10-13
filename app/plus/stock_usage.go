package plus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
)

const maxStockUsageBytes = 8 << 20

var (
	stockUsageMu          sync.Mutex
	errInvalidStockUsage  = errors.New("The saved Market hand-in history is invalid. It has been kept; stock changes are disabled.")
	errStockUsageConflict = errors.New("This Market hand-in was already recorded with different quantities.")
)

// A nyugta csak a megerősített leadás darabjait őrzi. Az élő Market-készlet,
// revízió, követelmény és API-kulcs nem része az előzménynek.
type stockUsageHistory struct {
	Version  int                                               `json:"version"`
	CharUID  string                                            `json:"charUid"`
	GameMode string                                            `json:"gameMode"`
	Receipts map[string]map[string]localprogress.HandoverEntry `json:"receipts"`
}

func (a *ItemWindowApp) stockUsagePath(charUID, mode string) (string, string, error) {
	mode = strings.ToUpper(mode)
	if a.dir == "" || !handoverItemID(charUID) || mode != "PVP" && mode != "PVE" && mode != "SEASON" {
		return "", "", errInvalidStockUsage
	}
	hash := sha256.Sum256([]byte(charUID + "\x00" + mode))
	return filepath.Join(a.dir, "stock-usage", hex.EncodeToString(hash[:])+".json"), mode, nil
}

// A tervezett nyugták összeg- és fájlkorlátját még bármilyen készletlevonás
// előtt ellenőrzi. Nem ír fájlt, és a már rögzített részeket nem számolja újra.
func (a *ItemWindowApp) validateStockEditorMarketUsage(tx *handoverTransaction) error {
	if tx == nil || tx.Version != 1 && tx.Version != 2 || !stockUsageOperationID(tx.ID) || len(tx.Items) == 0 || len(tx.Items) > 128 {
		return errInvalidStockUsage
	}
	path, mode, err := a.stockUsagePath(tx.CharUID, tx.Mode)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(tx.Items))
	for _, op := range tx.Items {
		if !handoverItemID(op.ItemID) || seen[op.ItemID] || op.Market.ItemID != op.ItemID || !validHandoverEntry(op.Market) {
			return errInvalidStockUsage
		}
		seen[op.ItemID] = true
		quantity := handoverQuantity(op.Market)
		if op.RegularToMarket < 0 || op.FIRToMarket < 0 || op.RegularToMarket > quantity || op.FIRToMarket != quantity-op.RegularToMarket {
			return errInvalidStockUsage
		}
	}
	stockUsageMu.Lock()
	defer stockUsageMu.Unlock()
	history, err := readStockUsage(path, tx.CharUID, mode)
	if err != nil {
		return err
	}
	for _, op := range tx.Items {
		if handoverQuantity(op.Market) == 0 {
			continue
		}
		if err := appendStockUsage(&history, tx.ID, op.Market); err != nil {
			return err
		}
	}
	_, err = encodeStockUsage(history)
	return err
}

// recordStockEditorMarketUsage tárgyanként ment, így a több tárgyas leadás
// részsikere is tartós. A hívó csak ellenőrzött API-csökkentés és sikeres
// minőségi mentés után, a művelet lezárása előtt hívhatja.
func (a *ItemWindowApp) recordStockEditorMarketUsage(tx *handoverTransaction, op *handoverOperation) error {
	if tx == nil || op == nil || tx.Version != 1 && tx.Version != 2 || !tx.LocalDone || !stockUsageOperationID(tx.ID) ||
		!handoverItemID(op.ItemID) || op.Market.ItemID != op.ItemID || !validHandoverEntry(op.Market) {
		return errInvalidStockUsage
	}
	path, mode, err := a.stockUsagePath(tx.CharUID, tx.Mode)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(tx.Items))
	found := false
	for _, candidate := range tx.Items {
		if !handoverItemID(candidate.ItemID) || seen[candidate.ItemID] {
			return errInvalidStockUsage
		}
		seen[candidate.ItemID] = true
		if candidate.ItemID == op.ItemID {
			found = candidate == *op
		}
	}
	if !found || len(tx.Items) > 128 {
		return errInvalidStockUsage
	}
	quantity := handoverQuantity(op.Market)
	if op.RegularToMarket < 0 || op.FIRToMarket < 0 ||
		op.RegularToMarket > quantity || op.FIRToMarket != quantity-op.RegularToMarket {
		return errInvalidStockUsage
	}
	if quantity == 0 {
		// Helyi leadásból nem készül Market-nyugta vagy üres előzményfájl.
		return nil
	}
	if op.Stage != "confirmed" && op.Stage != "done" {
		return errInvalidStockUsage
	}
	stockUsageMu.Lock()
	defer stockUsageMu.Unlock()
	history, err := readStockUsage(path, tx.CharUID, mode)
	if err != nil {
		return err
	}
	if previous, exists := history.Receipts[tx.ID][op.ItemID]; exists {
		if previous != op.Market {
			return errStockUsageConflict
		}
		return nil
	}
	if err := appendStockUsage(&history, tx.ID, op.Market); err != nil {
		return err
	}
	data, err := encodeStockUsage(history)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writePrivateFile(path, data)
}

func appendStockUsage(history *stockUsageHistory, operationID string, entry localprogress.HandoverEntry) error {
	entries := history.Receipts[operationID]
	if previous, exists := entries[entry.ItemID]; exists {
		if previous != entry {
			return errStockUsageConflict
		}
		return nil
	}
	if entries == nil {
		entries = make(map[string]localprogress.HandoverEntry)
		history.Receipts[operationID] = entries
	}
	entries[entry.ItemID] = entry
	return nil
}

func encodeStockUsage(history stockUsageHistory) ([]byte, error) {
	if _, err := aggregateStockUsage(history); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil || len(data) > maxStockUsageBytes {
		return nil, errInvalidStockUsage
	}
	return data, nil
}

// Az előzmény összesített saját Market-leadás. A Market API spentQuantity
// mezőjével nem adható össze, mert ugyanaz a használat mindkettőben szerepelhet.
func (a *ItemWindowApp) loadStockEditorMarketUsage(opts ItemOptions) (map[string]localprogress.StockAmounts, error) {
	path, mode, err := a.stockUsagePath(opts.CharacterUID, opts.Mode)
	if err != nil {
		return nil, err
	}
	stockUsageMu.Lock()
	defer stockUsageMu.Unlock()
	history, err := readStockUsage(path, opts.CharacterUID, mode)
	if err != nil {
		return nil, err
	}
	return aggregateStockUsage(history)
}

func readStockUsage(path, charUID, mode string) (stockUsageHistory, error) {
	history := stockUsageHistory{Version: 1, CharUID: charUID, GameMode: mode,
		Receipts: make(map[string]map[string]localprogress.HandoverEntry)}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return history, nil
	}
	if err != nil {
		return stockUsageHistory{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStockUsageBytes+1))
	if err != nil || len(data) > maxStockUsageBytes || !stockUsageUniqueJSON(data) {
		return stockUsageHistory{}, errInvalidStockUsage
	}
	var wire struct {
		Version  int    `json:"version"`
		CharUID  string `json:"charUid"`
		GameMode string `json:"gameMode"`
		Receipts map[string]map[string]struct {
			ItemID  string `json:"itemId"`
			Regular *int64 `json:"regular"`
			FIR     *int64 `json:"fir"`
			Unknown *int64 `json:"unknown"`
		} `json:"receipts"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF ||
		wire.Version != 1 || wire.CharUID != charUID || wire.GameMode != mode || wire.Receipts == nil {
		return stockUsageHistory{}, errInvalidStockUsage
	}
	for operationID, entries := range wire.Receipts {
		history.Receipts[operationID] = make(map[string]localprogress.HandoverEntry, len(entries))
		for id, entry := range entries {
			if entry.Regular == nil || entry.FIR == nil || entry.Unknown == nil {
				return stockUsageHistory{}, errInvalidStockUsage
			}
			history.Receipts[operationID][id] = localprogress.HandoverEntry{
				ItemID: entry.ItemID, Regular: *entry.Regular, FIR: *entry.FIR, Unknown: *entry.Unknown,
			}
		}
	}
	if _, err := aggregateStockUsage(history); err != nil {
		return stockUsageHistory{}, err
	}
	return history, nil
}

// Az ismételt JSON-kulcs nem írhatja felül egy korábbi nyugta tartalmát.
func stockUsageUniqueJSON(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 8 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return true
		}
		if delimiter != '{' {
			return false
		}
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			name, valid := key.(string)
			if err != nil || !valid || seen[name] {
				return false
			}
			seen[name] = true
			if !value(depth + 1) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	}
	if !value(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func stockUsageOperationID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func aggregateStockUsage(history stockUsageHistory) (map[string]localprogress.StockAmounts, error) {
	result := make(map[string]localprogress.StockAmounts)
	for operationID, entries := range history.Receipts {
		if !stockUsageOperationID(operationID) || len(entries) == 0 || len(entries) > 128 {
			return nil, errInvalidStockUsage
		}
		for id, entry := range entries {
			if !handoverItemID(id) || entry.ItemID != id || !validHandoverEntry(entry) || handoverQuantity(entry) == 0 {
				return nil, errInvalidStockUsage
			}
			total := result[id]
			quantity := total.Regular + total.FIR + total.Unknown
			if handoverQuantity(entry) > localprogress.MaxQuantity-quantity {
				return nil, errInvalidStockUsage
			}
			total.Regular += entry.Regular
			total.FIR += entry.FIR
			total.Unknown += entry.Unknown
			result[id] = total
		}
	}
	return result, nil
}
