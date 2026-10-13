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
	"reflect"
	"strings"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
)

const maxStockEditorPendingBytes = 8 << 10

var (
	errStockEditorPendingInvalid  = errors.New("The pending stock edit is invalid. It has been kept; discard it before changing stock.")
	errStockEditorPendingConflict = errors.New("A different stock edit is already pending. Refresh or discard it before changing stock.")
)

// A függő fájl csak a kézi Market-írás célját őrzi az ellenőrzésig. Nem
// tartalmaz kiinduló készletet, követelményt, API-kulcsot vagy készletmásolatot.
type stockEditorPending struct {
	Version  int                         `json:"version"`
	CharUID  string                      `json:"charUid"`
	Mode     string                      `json:"mode"`
	ItemID   string                      `json:"itemId"`
	Amounts  localprogress.StockAmounts  `json:"amounts"`
	Revision string                      `json:"revision"`
	Stage    string                      `json:"stage,omitempty"`
	Baseline *localprogress.StockAmounts `json:"baseline,omitempty"`
}

func (a *ItemWindowApp) stockEditorPendingPath(charUID, mode string) string {
	hash := sha256.Sum256([]byte(charUID + "\x00" + strings.ToUpper(mode)))
	return filepath.Join(a.dir, "stock-edits", hex.EncodeToString(hash[:])+".json")
}

func (a *ItemWindowApp) readStockEditorPending(opts ItemOptions) (*stockEditorPending, error) {
	mode := strings.ToUpper(opts.Mode)
	if a.dir == "" || !handoverItemID(opts.CharacterUID) || !stockEditorPendingMode(mode) {
		return nil, errStockEditorPendingInvalid
	}
	file, err := os.Open(a.stockEditorPendingPath(opts.CharacterUID, mode))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("The pending stock edit could not be read. Stock changes are disabled.")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStockEditorPendingBytes+1))
	if err != nil || len(data) > maxStockEditorPendingBytes || !stockUsageUniqueJSON(data) || !localBackupJSONShape(data, reflect.TypeOf(stockEditorPending{}), 0) {
		return nil, errStockEditorPendingInvalid
	}
	// A null vagy hiányzó típusmező nem jelenthet észrevétlenül nulla célértéket.
	var wire struct {
		Version int    `json:"version"`
		CharUID string `json:"charUid"`
		Mode    string `json:"mode"`
		ItemID  string `json:"itemId"`
		Amounts *struct {
			Regular *int64 `json:"regular"`
			FIR     *int64 `json:"fir"`
			Unknown *int64 `json:"unknown"`
		} `json:"amounts"`
		Revision string `json:"revision"`
		Stage    string `json:"stage"`
		Baseline *struct {
			Regular *int64 `json:"regular"`
			FIR     *int64 `json:"fir"`
			Unknown *int64 `json:"unknown"`
		} `json:"baseline"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.Amounts == nil ||
		wire.Amounts.Regular == nil || wire.Amounts.FIR == nil || wire.Amounts.Unknown == nil {
		return nil, errStockEditorPendingInvalid
	}
	pending := stockEditorPending{
		Version: wire.Version, CharUID: wire.CharUID, Mode: wire.Mode, ItemID: wire.ItemID, Revision: wire.Revision,
		Amounts: localprogress.StockAmounts{Regular: *wire.Amounts.Regular, FIR: *wire.Amounts.FIR, Unknown: *wire.Amounts.Unknown},
	}
	if wire.Baseline != nil {
		if wire.Baseline.Regular == nil || wire.Baseline.FIR == nil || wire.Baseline.Unknown == nil {
			return nil, errStockEditorPendingInvalid
		}
		pending.Baseline = &localprogress.StockAmounts{Regular: *wire.Baseline.Regular, FIR: *wire.Baseline.FIR, Unknown: *wire.Baseline.Unknown}
	}
	pending.Stage = wire.Stage
	if !validStockEditorPending(pending) || pending.CharUID != opts.CharacterUID || pending.Mode != mode {
		return nil, errStockEditorPendingInvalid
	}
	return &pending, nil
}

func (a *ItemWindowApp) saveStockEditorPending(pending *stockEditorPending) error {
	if pending == nil || a.dir == "" || !validStockEditorPending(*pending) {
		return errStockEditorPendingInvalid
	}
	previous, err := a.readStockEditorPending(ItemOptions{CharacterUID: pending.CharUID, Mode: pending.Mode})
	if err != nil {
		return err
	}
	if previous != nil {
		if reflect.DeepEqual(previous, pending) {
			return nil
		}
		if previous.Version != 2 || pending.Version != 2 || previous.CharUID != pending.CharUID || previous.Mode != pending.Mode || previous.ItemID != pending.ItemID || previous.Amounts != pending.Amounts || previous.Revision != pending.Revision || !reflect.DeepEqual(previous.Baseline, pending.Baseline) || !(previous.Stage == "unsent" && (pending.Stage == "sent" || pending.Stage == "conflict") || previous.Stage == "sent" && (pending.Stage == "conflict" || pending.Stage == "unsent")) {
			return errStockEditorPendingConflict
		}
	}
	data, err := json.MarshalIndent(pending, "", "  ")
	if err != nil || len(data) > maxStockEditorPendingBytes {
		return errStockEditorPendingInvalid
	}
	path := a.stockEditorPendingPath(pending.CharUID, pending.Mode)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("The pending stock edit could not be saved. No stock request was made.")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".stock-edit-*.tmp")
	if err != nil {
		return errors.New("The pending stock edit could not be saved. No stock request was made.")
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil || os.Rename(temp, path) != nil {
		return errors.New("The pending stock edit could not be saved. No stock request was made.")
	}
	return nil
}

// A törlés csak az ellenőrzött befejezés vagy az explicit elvetés fájlművelete.
// Nincs API-írás és a megmaradt típusjelölés sem változik tőle.
func (a *ItemWindowApp) removeStockEditorPending(opts ItemOptions) error {
	mode := strings.ToUpper(opts.Mode)
	if a.dir == "" || !handoverItemID(opts.CharacterUID) || !stockEditorPendingMode(mode) {
		return errStockEditorPendingInvalid
	}
	err := os.Remove(a.stockEditorPendingPath(opts.CharacterUID, mode))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("The pending stock edit could not be removed. Stock changes are disabled.")
	}
	return nil
}

func stockEditorPendingMode(mode string) bool {
	return mode == "PVP" || mode == "PVE" || mode == "SEASON"
}

func validStockEditorPending(pending stockEditorPending) bool {
	if (pending.Version != 1 && pending.Version != 2) || !handoverItemID(pending.CharUID) || !handoverItemID(pending.ItemID) ||
		!stockEditorPendingMode(pending.Mode) || pending.Revision == "" || len(pending.Revision) > 256 ||
		!validHandoverEntry(localprogress.HandoverEntry{ItemID: pending.ItemID, Regular: pending.Amounts.Regular, FIR: pending.Amounts.FIR, Unknown: pending.Amounts.Unknown}) {
		return false
	}
	if pending.Version == 1 && (pending.Stage != "" || pending.Baseline != nil) {
		return false
	}
	if pending.Version == 2 {
		if pending.Stage != "unsent" && pending.Stage != "sent" && pending.Stage != "conflict" || pending.Baseline == nil {
			return false
		}
		if _, err := stockEditorTotal(*pending.Baseline); err != nil {
			return false
		}
	}
	for _, character := range pending.Revision {
		if character < 33 || character > 126 || character == '"' || character == '\\' {
			return false
		}
	}
	return true
}

func sameStockEditorPending(left, right *stockEditorPending) bool {
	return left != nil && right != nil && reflect.DeepEqual(left, right)
}
