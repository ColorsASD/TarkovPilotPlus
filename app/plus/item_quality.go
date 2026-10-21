package plus

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/nextitem"
)

// A fájl csak minőségi megjegyzéseket tárol. A Total az egyeztetés legutóbbi
// API-alapja; sem betöltéskor, sem szerkesztéskor nem írja a Market készletét.
type qualityRecord struct {
	Total   int64 `json:"total"`
	FIR     int64 `json:"fir"`
	Regular int64 `json:"regular"`
}

type qualityOperation struct {
	Profile, Item, BeforeRevision string
	Before                        qualityRecord
	Delta                         int64
	FIR                           bool
}

type qualityData struct {
	Version  int                                 `json:"version"`
	Profiles map[string]map[string]qualityRecord `json:"profiles"`
	Pending  map[string]qualityOperation         `json:"pending"`
	Applied  map[string]bool                     `json:"applied"`
}

type qualityStore struct {
	path, session string
	data          qualityData
}

func qualityProfile(key string, opts ItemOptions) string {
	digest := sha256.Sum256([]byte(key + "\x00" + opts.CharacterUID + "\x00" + strings.ToUpper(opts.Mode)))
	return hex.EncodeToString(digest[:])
}

func (a *ItemWindowApp) qualityStoreLocked() (*qualityStore, error) {
	if a.qualities != nil {
		return a.qualities, nil
	}
	store := &qualityStore{data: qualityData{Version: 1, Profiles: map[string]map[string]qualityRecord{}, Pending: map[string]qualityOperation{}, Applied: map[string]bool{}}}
	var session [16]byte
	if _, err := rand.Read(session[:]); err != nil {
		return nil, err
	}
	store.session = hex.EncodeToString(session[:])
	if a.dir != "" {
		store.path = filepath.Join(a.dir, "item-quality.json")
		body, err := os.ReadFile(store.path)
		if err == nil {
			if len(body) > 8<<20 || json.Unmarshal(body, &store.data) != nil || store.data.Version != 1 || store.data.Profiles == nil || store.data.Pending == nil || store.data.Applied == nil {
				return nil, errors.New("The saved item quality data is invalid. Its file was preserved.")
			}
			for _, profile := range store.data.Profiles {
				for _, row := range profile {
					if !validQualityRecord(row) {
						return nil, errors.New("The saved item quality quantities are invalid. Its file was preserved.")
					}
				}
			}
			for _, op := range store.data.Pending {
				after := op.Before
				after.Total += op.Delta
				if op.FIR {
					after.FIR += op.Delta
				} else {
					after.Regular += op.Delta
				}
				if op.Profile == "" || op.Item == "" || op.BeforeRevision == "" || (op.Delta != 1 && op.Delta != -1) || !validQualityRecord(op.Before) || !validQualityRecord(after) {
					return nil, errors.New("The saved item quality reservation is invalid. Its file was preserved.")
				}
			}
			// Másik futás már nem hívhatja újra egy lezárt művelet callbackjét.
			store.data.Applied = map[string]bool{}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	a.qualities = store
	return store, nil
}

func validQualityRecord(row qualityRecord) bool {
	return row.Total >= 0 && row.Total <= localprogress.MaxQuantity && row.FIR >= 0 && row.FIR <= row.Total && row.Regular >= 0 && row.Regular <= row.Total-row.FIR
}

func (store *qualityStore) update(change func(*qualityData) error) error {
	before, err := json.Marshal(store.data)
	if err != nil {
		return err
	}
	var next qualityData
	if err := json.Unmarshal(before, &next); err != nil {
		return err
	}
	if err := change(&next); err != nil {
		return err
	}
	after, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	compact, _ := json.Marshal(next)
	if string(before) == string(compact) {
		return nil
	}
	if store.path != "" {
		if err := os.MkdirAll(filepath.Dir(store.path), 0700); err != nil {
			return err
		}
		if err := writePrivateFile(store.path, after); err != nil {
			return err
		}
	}
	store.data = next
	return nil
}

func qualityRows(data *qualityData, profile string) map[string]qualityRecord {
	if data.Profiles[profile] == nil {
		data.Profiles[profile] = map[string]qualityRecord{}
	}
	return data.Profiles[profile]
}

func clampQuality(row qualityRecord, total int64) qualityRecord {
	// Külső növekedés kategorizálatlan. Csökkenéskor az API nem mondja
	// meg, melyik típus fogyott: csak a biztosan megmaradó minimum őrizhető.
	decrease := max(0, row.Total-total)
	row.FIR, row.Regular = max(0, row.FIR-decrease), max(0, row.Regular-decrease)
	row.Total = total
	row.FIR = min(max(0, row.FIR), total)
	row.Regular = min(max(0, row.Regular), total-row.FIR)
	return row
}

func applyQualityOperation(data *qualityData, id string, op qualityOperation) {
	if data.Applied[id] {
		return
	}
	row := op.Before
	row.Total += op.Delta
	if op.FIR {
		row.FIR += op.Delta
	} else {
		row.Regular += op.Delta
	}
	qualityRows(data, op.Profile)[op.Item] = clampQuality(row, max(0, row.Total))
	data.Applied[id] = true
	if len(data.Applied) > 4096 {
		data.Applied = map[string]bool{id: true}
	}
	delete(data.Pending, id)
}

func (a *ItemWindowApp) qualityOperationIDLocked(entry pendingMarketAdd) string {
	return a.qualities.session + ":" + fmt.Sprint(entry.ID)
}

func (a *ItemWindowApp) qualityReconcileSnapshotLocked(key string, opts ItemOptions, snapshot localprogress.Snapshot) (localprogress.Snapshot, error) {
	if key == "" || snapshot.CharUID != opts.CharacterUID || !strings.EqualFold(snapshot.GameMode, opts.Mode) || snapshot.Revision == "" {
		return snapshot, errors.New("The Market profile could not be matched to local item quality.")
	}
	for _, row := range snapshot.Items {
		if row.MarketAvailableQuantity < 0 || row.MarketAvailableQuantity > localprogress.MaxQuantity {
			return snapshot, errors.New("The Market inventory quantity is invalid.")
		}
	}
	store, err := a.qualityStoreLocked()
	if err != nil {
		return snapshot, err
	}
	profile := qualityProfile(key, opts)
	counts := map[string]int{}
	for _, row := range snapshot.Items {
		counts[row.ItemID]++
	}
	active := map[string]bool{}
	for _, entry := range a.pendingAddQueue {
		if entry.Typed {
			active[a.qualityOperationIDLocked(entry)] = true
		}
	}
	err = store.update(func(data *qualityData) error {
		rows := qualityRows(data, profile)
		for id, op := range data.Pending {
			if op.Profile != profile || active[id] {
				continue
			}
			for _, row := range snapshot.Items {
				if row.ItemID != op.Item || counts[row.ItemID] != 1 {
					continue
				}
				if row.MarketAvailableQuantity != op.Before.Total || snapshot.Revision != op.BeforeRevision {
					// Megszakadt kérés után bizonytalan a darab minősége.
					// Új API-írás helyett a helyi besorolást hagyjuk nyitva.
					rows[row.ItemID] = qualityRecord{Total: row.MarketAvailableQuantity}
				}
				delete(data.Pending, id)
			}
		}
		for _, row := range snapshot.Items {
			if counts[row.ItemID] != 1 {
				continue
			}
			held := false
			for id, op := range data.Pending {
				if active[id] && op.Profile == profile && op.Item == row.ItemID {
					held = true
				}
			}
			if !held {
				rows[row.ItemID] = clampQuality(rows[row.ItemID], row.MarketAvailableQuantity)
			}
		}
		return nil
	})
	if err != nil {
		return snapshot, err
	}
	out := localprogress.Clone(snapshot)
	for i := range out.Items {
		row := &out.Items[i]
		if counts[row.ItemID] != 1 {
			row.QualityKnown, row.FIRQuantity, row.RegularQuantity = true, 0, 0
			continue
		}
		quality := clampQuality(store.data.Profiles[profile][row.ItemID], row.MarketAvailableQuantity)
		row.QualityKnown, row.FIRQuantity, row.RegularQuantity = true, quality.FIR, quality.Regular
	}
	return out, nil
}

func (a *ItemWindowApp) qualityChangeAllowedLocked(snapshot localprogress.Snapshot, entry pendingMarketAdd) bool {
	if !entry.Typed {
		return false
	}
	typed, known := snapshot, true
	for _, row := range snapshot.Items {
		if !row.QualityKnown {
			known = false
			break
		}
	}
	if !known {
		var err error
		typed, err = a.qualityReconcileSnapshotLocked(entry.Key, entry.Opts, snapshot)
		if err != nil {
			return false
		}
	}
	row, unique := stockItem(typed, entry.Item.BSGID)
	if !unique || typed.FIRInventorySeparate {
		return false
	}
	if pendingMarketDelta(entry) < 0 {
		if entry.FIR {
			return row.FIRQuantity > 0
		}
		return row.RegularQuantity > 0
	}
	if row.AvailableQuantity >= localprogress.MaxQuantity {
		return false
	}
	view := nextitem.CalculateTyped(typed, entry.Item.BSGID, true)
	if entry.FIR {
		return view.Known && view.AddFIRAllowed
	}
	return view.Known && view.AddRegularAllowed
}

func (a *ItemWindowApp) qualityMarkSentLocked(entry pendingMarketAdd) error {
	if !entry.Typed {
		return nil
	}
	store, err := a.qualityStoreLocked()
	if err != nil {
		return err
	}
	id, profile := a.qualityOperationIDLocked(entry), qualityProfile(entry.Key, entry.Opts)
	return store.update(func(data *qualityData) error {
		if data.Applied[id] {
			return nil
		}
		before := clampQuality(qualityRows(data, profile)[entry.Item.BSGID], entry.Before)
		after := before
		after.Total += pendingMarketDelta(entry)
		if entry.FIR {
			after.FIR += pendingMarketDelta(entry)
		} else {
			after.Regular += pendingMarketDelta(entry)
		}
		if !validQualityRecord(after) {
			return errors.New("The selected stock quality is no longer available.")
		}
		data.Pending[id] = qualityOperation{Profile: profile, Item: entry.Item.BSGID, BeforeRevision: entry.BeforeRevision, Before: before, Delta: pendingMarketDelta(entry), FIR: entry.FIR}
		return nil
	})
}

func (a *ItemWindowApp) qualityCommitPendingLocked(entry pendingMarketAdd) error {
	if !entry.Typed {
		return nil
	}
	store, err := a.qualityStoreLocked()
	if err != nil {
		return err
	}
	id := a.qualityOperationIDLocked(entry)
	return store.update(func(data *qualityData) error {
		if data.Applied[id] {
			return nil
		}
		op, exists := data.Pending[id]
		if !exists {
			return errors.New("The local stock quality reservation could not be found.")
		}
		applyQualityOperation(data, id, op)
		return nil
	})
}

func (a *ItemWindowApp) qualityCancelPendingLocked(entry pendingMarketAdd) error {
	if !entry.Typed {
		return nil
	}
	store, err := a.qualityStoreLocked()
	if err != nil {
		return err
	}
	id := a.qualityOperationIDLocked(entry)
	return store.update(func(data *qualityData) error { delete(data.Pending, id); return nil })
}

type TrackerQualityRow struct {
	ItemID        string `json:"itemId"`
	Name          string `json:"name"`
	Total         int64  `json:"total"`
	FIR           int64  `json:"fir"`
	Regular       int64  `json:"regular"`
	Uncategorized int64  `json:"uncategorized"`
	ReadOnly      bool   `json:"readOnly"`
}

type TrackerQualityInventory struct {
	Profile  string              `json:"profile"`
	Revision string              `json:"revision"`
	Rows     []TrackerQualityRow `json:"rows"`
	Error    string              `json:"error"`
}

func (a *ItemWindowApp) GetTrackerInventory() TrackerQualityInventory {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := TrackerQualityInventory{Rows: []TrackerQualityRow{}}
	if a.apiKey == "" || a.confirmedProgress.CharUID != a.state.CharacterUID || a.confirmedProgress.Revision == "" {
		result.Error = "Refresh Market progress before editing item quality."
		return result
	}
	snapshot, err := a.qualityReconcileSnapshotLocked(a.apiKey, a.state.ItemOptions, a.confirmedProgress)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Profile, result.Revision = qualityProfile(a.apiKey, a.state.ItemOptions), snapshot.Revision
	counts := map[string]int{}
	for _, row := range snapshot.Items {
		counts[row.ItemID]++
	}
	for _, row := range snapshot.Items {
		if row.MarketAvailableQuantity <= 0 {
			continue
		}
		pending := false
		for _, entry := range a.pendingAddQueue {
			if entry.Key == a.apiKey && entry.Opts.CharacterUID == snapshot.CharUID && entry.Item.BSGID == row.ItemID {
				pending = true
			}
		}
		result.Rows = append(result.Rows, TrackerQualityRow{ItemID: row.ItemID, Name: row.Name, Total: row.MarketAvailableQuantity,
			FIR: row.FIRQuantity, Regular: row.RegularQuantity, Uncategorized: row.MarketAvailableQuantity - row.FIRQuantity - row.RegularQuantity,
			ReadOnly: counts[row.ItemID] != 1 || snapshot.FIRInventorySeparate || pending})
	}
	sort.SliceStable(result.Rows, func(i, j int) bool {
		return strings.ToLower(result.Rows[i].Name) < strings.ToLower(result.Rows[j].Name)
	})
	return result
}

// Csak a helyi minőségi bontás változik. A Market teljes készletéhez nem
// tartozik írható mező, és ez a metódus hálózati kérést sem indít.
func (a *ItemWindowApp) SetTrackerItemQuality(profile, itemID string, expectedTotal, fir, regular int64) ItemState {
	a.mu.Lock()
	row, unique := stockItem(a.confirmedProgress, itemID)
	valid := a.apiKey != "" && profile == qualityProfile(a.apiKey, a.state.ItemOptions) && a.confirmedProgress.CharUID == a.state.CharacterUID && unique &&
		!a.confirmedProgress.FIRInventorySeparate && row.MarketAvailableQuantity == expectedTotal && validQualityRecord(qualityRecord{Total: expectedTotal, FIR: fir, Regular: regular})
	for _, entry := range a.pendingAddQueue {
		if entry.Key == a.apiKey && entry.Opts.CharacterUID == a.state.CharacterUID && entry.Item.BSGID == itemID {
			valid = false
		}
	}
	var err error
	if !valid {
		err = errors.New("Stock changed or has pending saves. Reload Items Tracker before saving its quality.")
	} else {
		var store *qualityStore
		store, err = a.qualityStoreLocked()
		if err == nil {
			err = store.update(func(data *qualityData) error {
				qualityRows(data, profile)[itemID] = qualityRecord{Total: expectedTotal, FIR: fir, Regular: regular}
				return nil
			})
		}
	}
	if a.state.Local == nil {
		a.state.Local = &LocalProgressState{}
	}
	if err != nil {
		a.state.Local.Error = err.Error()
	} else {
		a.state.Local.Error = ""
		a.confirmedProgress, err = a.qualityReconcileSnapshotLocked(a.apiKey, a.state.ItemOptions, a.confirmedProgress)
		if err == nil {
			a.rebuildPendingViewLocked()
		} else {
			a.state.Local.Error = err.Error()
		}
	}
	a.mu.Unlock()
	a.configureTrackedAddHotkey()
	a.emit()
	return a.GetState()
}
