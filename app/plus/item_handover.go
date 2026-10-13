package plus

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/scanner"
)

type ItemHandoverEntry struct {
	ID       string `json:"id"`
	ItemID   string `json:"itemId"`
	ItemUID  string `json:"itemUid,omitempty"`
	Name     string `json:"name"`
	Quantity int64  `json:"quantity"`
	Kind     string `json:"kind"`
}

type ItemHandoverState struct {
	Open       bool                `json:"open"`
	Busy       bool                `json:"busy"`
	Pending    bool                `json:"pending"`
	Background bool                `json:"background"`
	Scoped     bool                `json:"scoped"`
	Completed  bool                `json:"completed"`
	Automatic  bool                `json:"automatic"`
	CharUID    string              `json:"charUid"`
	Items      []ItemHandoverEntry `json:"items"`
	Error      string              `json:"error"`
	Status     string              `json:"status"`
	generation uint64
}

type handoverTransaction struct {
	Version   int                 `json:"version"`
	ID        string              `json:"id"`
	CharUID   string              `json:"charUid"`
	Mode      string              `json:"mode"`
	Draft     []ItemHandoverEntry `json:"draft"`
	LocalDone bool                `json:"localDone"`
	Items     []handoverOperation `json:"items"`
}

type handoverOperation struct {
	ItemID          string                      `json:"itemId"`
	Name            string                      `json:"name"`
	Local           localprogress.HandoverEntry `json:"local"`
	Fallback        localprogress.HandoverEntry `json:"fallback"`
	Market          localprogress.HandoverEntry `json:"market"`
	MarketRemaining localprogress.HandoverEntry `json:"marketRemaining"`
	RegularToMarket int64                       `json:"regularToMarket"`
	FIRToMarket     int64                       `json:"firToMarket"`
	// Csak a függő művelet abszolút írási szándéka marad lemezen. Ez nem
	// Market-készletmásolat; a befejezett művelet naplófájlja törlődik.
	Stage    string `json:"stage"`
	Target   int64  `json:"target"`
	Revision string `json:"revision"`
}

func cloneHandoverState(st *ItemHandoverState) *ItemHandoverState {
	if st == nil {
		return nil
	}
	copy := *st
	copy.Items = append([]ItemHandoverEntry{}, st.Items...)
	return &copy
}

func handoverAutomaticScan(st ItemState) bool {
	if st.Handover != nil && st.Handover.Open {
		return st.Handover.Automatic && !st.Handover.Busy && !st.Handover.Pending && !st.Handover.Completed
	}
	return st.Automatic
}

func handoverScanGeneration(st ItemState) uint64 {
	if st.Handover != nil && st.Handover.Open && !st.Handover.Pending && !st.Handover.Completed {
		return st.Handover.generation
	}
	return 0
}

func handoverCaptureCurrent(st *ItemHandoverState, generation []uint64) bool {
	return len(generation) == 0 || st != nil && generation[0] != 0 && st.generation == generation[0]
}

func (a *ItemWindowApp) handoverBusy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state.Handover != nil && a.state.Handover.Busy
}

func (a *ItemWindowApp) handoverPath(charUID, mode string) string {
	hash := sha256.Sum256([]byte(charUID + "\x00" + strings.ToUpper(mode)))
	return filepath.Join(a.dir, "handovers", hex.EncodeToString(hash[:])+".json")
}

func readHandover(path, charUID, mode string) (*handoverTransaction, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 256*1024+1))
	if err != nil || len(data) > 256*1024 || !localBackupUniqueJSON(data) || !localBackupJSONShape(data, reflect.TypeOf(handoverTransaction{}), 0) {
		return nil, errors.New("The pending hand-in file could not be read.")
	}
	var tx handoverTransaction
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&tx) != nil || decoder.Decode(new(any)) != io.EOF || (tx.Version != 1 && tx.Version != 2) || len(tx.ID) != 32 ||
		tx.CharUID != charUID || tx.Mode != strings.ToUpper(mode) || len(tx.Items) == 0 || len(tx.Items) > 128 || len(tx.Draft) == 0 || len(tx.Draft) > 128 {
		return nil, errors.New("The pending hand-in file is invalid. It has been kept; stock changes are disabled.")
	}
	if _, err := hex.DecodeString(tx.ID); err != nil {
		return nil, errors.New("The pending hand-in identifier is invalid.")
	}
	seen := map[string]bool{}
	for i := range tx.Items {
		if tx.Items[i].Fallback == (localprogress.HandoverEntry{}) {
			tx.Items[i].Fallback.ItemID = tx.Items[i].ItemID
		}
	}
	for _, op := range tx.Items {
		q := handoverQuantity(op.Market)
		if !handoverItemID(op.ItemID) || seen[op.ItemID] || op.Local.ItemID != op.ItemID || op.Market.ItemID != op.ItemID ||
			!validHandoverEntry(op.Local) || !validHandoverEntry(op.Market) || !validHandoverEntry(op.Fallback) || op.Fallback.ItemID != op.ItemID || q != op.RegularToMarket+op.FIRToMarket ||
			op.RegularToMarket < 0 || op.FIRToMarket < 0 || op.Target < 0 || op.Target > localprogress.MaxQuantity ||
			(!validHandoverStage(tx.Version, op.Stage)) ||
			(op.Stage != "" && op.Stage != "done" && (q == 0 || op.Revision == "" || len(op.Revision) > 256 || op.Target > localprogress.MaxQuantity-q)) {
			return nil, errors.New("The pending hand-in file is invalid. It has been kept; stock changes are disabled.")
		}
		seen[op.ItemID] = true
		if op.MarketRemaining.ItemID != "" && (!validHandoverEntry(op.MarketRemaining) || op.MarketRemaining.ItemID != op.ItemID || handoverQuantity(op.MarketRemaining) != op.Target) {
			return nil, errors.New("The pending hand-in remaining types are invalid.")
		}
	}
	if tx.Version == 2 {
		for _, op := range tx.Items {
			if handoverQuantity(op.Market) > 0 && (op.Stage == "" || op.MarketRemaining.ItemID != op.ItemID) || handoverQuantity(op.Market) == 0 && op.Stage != "" || !tx.LocalDone && op.Stage != "" && op.Stage != "unsent" {
				return nil, errors.New("The pending hand-in phase is invalid.")
			}
		}
	}
	requested := map[string][2]int64{}
	for _, row := range tx.Draft {
		if !seen[row.ItemID] || row.Quantity <= 0 || row.Quantity > localprogress.MaxQuantity || row.Kind != "regular" && row.Kind != "fir" {
			return nil, errors.New("The pending hand-in list is invalid.")
		}
		q := requested[row.ItemID]
		if row.Quantity > localprogress.MaxQuantity-q[0]-q[1] {
			return nil, errors.New("The pending hand-in quantity is invalid.")
		}
		index := 0
		if row.Kind == "fir" {
			index = 1
		}
		q[index] += row.Quantity
		requested[row.ItemID] = q
	}
	for _, op := range tx.Items {
		q := requested[op.ItemID]
		if q[0]+q[1] != handoverQuantity(op.Local)+handoverQuantity(op.Fallback)+handoverQuantity(op.Market) ||
			op.Local.Regular+op.Fallback.Regular+op.RegularToMarket > q[0] || op.Local.FIR+op.Fallback.FIR+op.FIRToMarket > q[1] {
			return nil, errors.New("The pending hand-in quantities do not match the selected list.")
		}
	}
	return &tx, nil
}

func validHandoverStage(version int, stage string) bool {
	if version == 1 {
		return stage == "" || stage == "prepared" || stage == "rejected" || stage == "confirmed" || stage == "done"
	}
	return stage == "" || stage == "unsent" || stage == "sent" || stage == "conflict" || stage == "confirmed" || stage == "done"
}

func handoverItemID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

func handoverQuantity(entry localprogress.HandoverEntry) int64 {
	return entry.Regular + entry.FIR + entry.Unknown
}
func validHandoverEntry(entry localprogress.HandoverEntry) bool {
	return handoverItemID(entry.ItemID) && entry.Regular >= 0 && entry.FIR >= 0 && entry.Unknown >= 0 &&
		entry.Regular <= localprogress.MaxQuantity-entry.FIR && entry.Unknown <= localprogress.MaxQuantity-entry.Regular-entry.FIR
}

func (a *ItemWindowApp) saveHandover(tx *handoverTransaction) error {
	path := a.handoverPath(tx.CharUID, tx.Mode)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tx, "", "  ")
	if err != nil {
		return err
	}
	return handoverJournalWrite(path, data)
}

var handoverJournalWrite = atomicHandoverJournalWrite

func atomicHandoverJournalWrite(path string, data []byte) error {
	// Flush the intent before making a request; a crash must leave a readable
	// operation identifier and target rather than another relative deduction.
	file, err := os.CreateTemp(filepath.Dir(path), ".handover-*.tmp")
	if err != nil {
		return err
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
	if err != nil {
		return err
	}
	return os.Rename(temp, path)
}

// restoreHandoverLocked runs before shortcuts can add stock after a restart or
// character change. The journal stores neither an API key nor a Market cache.
func (a *ItemWindowApp) restoreHandoverLocked() {
	a.handover = nil
	a.state.Handover = nil
	if a.state.CharacterUID == "" || a.dir == "" {
		return
	}
	tx, err := readHandover(a.handoverPath(a.state.CharacterUID, a.state.Mode), a.state.CharacterUID, a.state.Mode)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	a.state.Handover = &ItemHandoverState{CharUID: a.state.CharacterUID, Automatic: true, Pending: true, Items: []ItemHandoverEntry{}, Status: "Resume the previous hand-in before changing stock."}
	if err != nil {
		a.state.Handover.Error = err.Error()
		return
	}
	a.handover = tx
	a.state.Handover.Items = append([]ItemHandoverEntry{}, tx.Draft...)
	if tx.Version == 2 && tx.LocalDone {
		a.state.Handover.Scoped = true
		a.state.Handover.Background = true
		for _, op := range tx.Items {
			if op.Stage == "conflict" {
				a.state.Handover.Background = false
			}
		}
	}
}

func (a *ItemWindowApp) OpenHandover() ItemState {
	a.mu.Lock()
	if a.stockEditorBlocksStockLocked() || a.state.Handover != nil && a.state.Handover.Busy {
		a.mu.Unlock()
		return a.GetState()
	}
	if a.state.Handover == nil || a.state.Handover.Completed {
		a.restoreHandoverLocked()
	}
	if a.state.Handover == nil {
		a.handoverGeneration++
		a.state.Handover = &ItemHandoverState{CharUID: a.state.CharacterUID, Automatic: true, Items: []ItemHandoverEntry{}, generation: a.handoverGeneration}
	}
	a.state.Handover.Open = true
	if a.state.CharacterUID == "" || a.apiKey == "" || a.localStore == nil {
		a.state.Handover.Error = "Enter your API key and select a character before handing in items."
	} else if !a.state.Handover.Pending {
		a.state.Handover.Error = ""
		a.state.Handover.Status = "Open item Inspect windows in the game or search by name, then check quantities and type before confirming."
	}
	a.mu.Unlock()
	a.emit()
	return a.GetState()
}

func (a *ItemWindowApp) SetHandoverAutomatic(enabled bool) ItemState {
	a.mu.Lock()
	if st := a.state.Handover; st != nil && st.Open && !st.Busy && !st.Pending && !st.Completed {
		st.Automatic = enabled
	}
	a.mu.Unlock()
	a.emit()
	return a.GetState()
}

func (a *ItemWindowApp) CloseHandover() ItemState {
	a.mu.Lock()
	if st := a.state.Handover; st != nil && !st.Busy {
		if st.Pending {
			st.Open = false
		} else {
			a.state.Handover = nil
			a.handover = nil
		}
	}
	a.mu.Unlock()
	a.emit()
	return a.GetState()
}

func (a *ItemWindowApp) UpdateHandoverItem(id string, quantity int64, kind string) ItemState {
	a.mu.Lock()
	if st := a.state.Handover; st != nil && st.Open && !st.Busy && !st.Pending && !st.Completed &&
		st.CharUID == a.state.CharacterUID && quantity > 0 && quantity <= localprogress.MaxQuantity && (kind == "regular" || kind == "fir") {
		for i := range st.Items {
			if st.Items[i].ID == id {
				st.Items[i].Quantity = quantity
				st.Items[i].Kind = kind
				st.Error = ""
				break
			}
		}
	}
	a.mu.Unlock()
	a.emit()
	return a.GetState()
}

func (a *ItemWindowApp) RemoveHandoverItem(id string) ItemState {
	a.mu.Lock()
	if st := a.state.Handover; st != nil && st.Open && !st.Busy && !st.Pending && !st.Completed {
		for i, row := range st.Items {
			if row.ID == id {
				st.Items = append(st.Items[:i], st.Items[i+1:]...)
				break
			}
		}
	}
	a.mu.Unlock()
	a.emit()
	return a.GetState()
}

// SplitHandoverItem permits both physical qualities of one item in a batch.
// The extra row is explicit; inspecting the same title never adds more stock.
func (a *ItemWindowApp) SplitHandoverItem(id string) ItemState {
	a.mu.Lock()
	if st := a.state.Handover; st != nil && st.Open && !st.Busy && !st.Pending && !st.Completed && len(st.Items) < 128 {
		for _, row := range st.Items {
			if row.ID == id {
				count := 0
				for _, other := range st.Items {
					if other.ItemID == row.ItemID {
						count++
					}
				}
				if count >= 2 {
					break
				}
				kind := "fir"
				if row.Kind == "fir" {
					kind = "regular"
				}
				st.Items = append(st.Items, ItemHandoverEntry{ID: row.ID + "-other", ItemID: row.ItemID, ItemUID: row.ItemUID, Name: row.Name, Quantity: 1, Kind: kind})
				break
			}
		}
	}
	a.mu.Unlock()
	a.emit()
	return a.GetState()
}

// Each item type is captured once. OCR variants and repeated clicks on an
// already open Inspect are not evidence of additional physical items.
func (a *ItemWindowApp) captureHandoverScan(result scanner.Result, items []market.Item, opts ItemOptions, generation ...uint64) {
	if !result.InspectOpen {
		return
	}
	windows := result.TitleVariants
	if len(windows) == 0 && len(result.Groups) > 0 {
		for i := 0; i < len(result.Groups); i += 2 {
			variants := []string{strings.Join(result.Groups[i], "\n")}
			if i+1 < len(result.Groups) {
				variants = append(variants, strings.Join(result.Groups[i+1], "\n"))
			}
			windows = append(windows, variants)
		}
	}
	if len(windows) == 0 {
		windows = [][]string{{strings.Join(result.Lines, "\n")}}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.state.Handover
	if st == nil || !st.Open || st.Busy || st.Pending || st.Completed || !handoverCaptureCurrent(st, generation) || st.CharUID != opts.CharacterUID || a.state.ItemOptions.revision != opts.revision {
		return
	}
	for _, variants := range windows {
		var selected market.Item
		best := -1.0
		for _, title := range variants {
			if item, score, ok := market.MatchInspection([]string{title}, items); ok && score > best {
				selected, best = item, score
			}
		}
		if best < 0 || selected.UID == "" {
			continue
		}
		a.captureHandoverItemLocked(selected, opts)
	}
}

func (a *ItemWindowApp) captureHandoverItemLocked(item market.Item, opts ItemOptions) {
	st := a.state.Handover
	if st == nil || !st.Open || st.Busy || st.Pending || st.Completed || st.CharUID != opts.CharacterUID || a.state.ItemOptions.revision != opts.revision || item.UID == "" {
		return
	}
	id := item.BSGID
	if id == "" {
		id = item.UID
	}
	for _, row := range st.Items {
		if row.ID == id || row.ItemUID == item.UID || item.BSGID != "" && row.ItemID == item.BSGID {
			return
		}
	}
	if len(st.Items) >= 128 {
		return
	}
	st.Items = append(st.Items, ItemHandoverEntry{ID: id, ItemID: item.BSGID, ItemUID: item.UID, Name: item.Name, Quantity: 1, Kind: "regular"})
	st.Error = ""
	st.Status = "Items recognized. Check quantities and type, then confirm the hand-in."
}

func (a *ItemWindowApp) handoverCurrent(key string, opts ItemOptions) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.apiKey == key && a.state.ItemOptions.revision == opts.revision && a.state.CharacterUID == opts.CharacterUID
}

func (a *ItemWindowApp) failHandover(message string) {
	a.mu.Lock()
	if st := a.state.Handover; st != nil {
		st.Busy = false
		st.Background = false
		st.Error = message
		st.Pending = a.handover != nil
		st.Scoped = a.handoverScopeLocked()
		if st.Pending {
			st.Status = "The hand-in is saved. Resume it to verify the remaining operations."
		}
	}
	a.localAt = time.Time{}
	a.mu.Unlock()
	a.emit()
}

func (a *ItemWindowApp) planHandover(ctx context.Context, key string, opts ItemOptions, draft []ItemHandoverEntry) (*handoverTransaction, error) {
	_ = a.checkPendingMarketFallback(ctx, key, opts)
	resolved := map[string]string{}
	for i := range draft {
		if draft[i].ItemID == "" && draft[i].ItemUID != "" {
			id := resolved[draft[i].ItemUID]
			if id == "" {
				item, err := a.tracker.LookupItem(ctx, key, draft[i].ItemUID, opts.Mode, opts.Language)
				if err != nil {
					return nil, err
				}
				id = item.BSGID
				resolved[draft[i].ItemUID] = id
			}
			draft[i].ItemID = id
		}
	}
	snapshot, _, err := a.loadLocalSources(ctx, key, opts)
	if err != nil {
		return nil, err
	}
	if !a.handoverCurrent(key, opts) {
		return nil, context.Canceled
	}
	snapshot, err = a.applyProgressStock(snapshot)
	if err != nil {
		return nil, err
	}
	return a.planHandoverSnapshot(opts, draft, snapshot)
}

func (a *ItemWindowApp) planHandoverSnapshot(opts ItemOptions, draft []ItemHandoverEntry, snapshot localprogress.Snapshot) (*handoverTransaction, error) {
	stock, err := a.localStore.Load(opts.CharacterUID, strings.ToUpper(opts.Mode))
	if err != nil {
		return nil, err
	}
	rows := map[string][]localprogress.Item{}
	for _, row := range snapshot.Items {
		rows[row.ItemID] = append(rows[row.ItemID], row)
	}
	for _, row := range stock.Items {
		if len(rows[row.ItemID]) == 0 {
			rows[row.ItemID] = []localprogress.Item{row}
		}
	}
	requested := map[string][2]int64{}
	names := map[string]string{}
	for _, row := range draft {
		if !handoverItemID(row.ItemID) || row.Quantity <= 0 || row.Quantity > localprogress.MaxQuantity || row.Kind != "regular" && row.Kind != "fir" {
			return nil, errors.New("A recognized item could not be linked to stock. Remove it and scan it again.")
		}
		q := requested[row.ItemID]
		index := 0
		if row.Kind == "fir" {
			index = 1
		}
		if row.Quantity > localprogress.MaxQuantity-q[0]-q[1] {
			return nil, errors.New("The hand-in quantity is too large.")
		}
		q[index] += row.Quantity
		requested[row.ItemID] = q
		names[row.ItemID] = row.Name
	}
	if len(requested) == 0 || len(requested) > 128 {
		return nil, errors.New("Scan at least one item before confirming the hand-in.")
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	tx := &handoverTransaction{Version: 1, ID: hex.EncodeToString(id), CharUID: opts.CharacterUID, Mode: strings.ToUpper(opts.Mode), Draft: append([]ItemHandoverEntry{}, draft...)}
	ids := make([]string, 0, len(requested))
	for id := range requested {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a.mu.Lock()
		unsafe := a.progressUnsafeItemLocked(id)
		a.mu.Unlock()
		if unsafe != "" {
			return nil, errors.New(unsafe)
		}
		if len(rows[id]) != 1 {
			return nil, fmt.Errorf("%s: stock is missing or shares an ambiguous item ID. No items were deducted.", names[id])
		}
		q := requested[id]
		blocked, err := a.marketFallbackBlocksItem(opts, id)
		if err != nil {
			return nil, err
		}
		available := rows[id][0]
		if blocked {
			// A rögzített feltöltés csak a Market és fallback forrásra
			// vonatkozik. A független dev/local készlet leadható marad.
			available.MarketAvailableQuantity, available.MarketRegularQuantity, available.MarketFIRQuantity = 0, 0, 0
			available.MarketFallbackAvailableQuantity, available.MarketFallbackRegularQuantity, available.MarketFallbackFIRQuantity = 0, 0, 0
			available.AvailableQuantity = available.LocalAvailableQuantity
		}
		plan, err := localprogress.PlanHandoverItem(available, q[0], q[1])
		if err != nil {
			if blocked {
				return nil, fmt.Errorf("%s: the saved Market upload is awaiting verification. Only independent Local Progress stock can be handed in for this item. No items were deducted.", names[id])
			}
			return nil, fmt.Errorf("%s: not enough stock of the selected FiR/non-FiR type. No items were deducted.", names[id])
		}
		if handoverQuantity(plan.Market) > 0 && snapshot.FIRInventorySeparate {
			return nil, errors.New("The Market character uses separate FiR stock, which this API cannot update safely.")
		}
		target := rows[id][0].MarketAvailableQuantity - handoverQuantity(plan.Market)
		remaining := handoverRemainingKinds(rows[id][0], plan.Market, target)
		tx.Items = append(tx.Items, handoverOperation{ItemID: id, Name: names[id], Local: plan.Local, Fallback: plan.Fallback, Market: plan.Market, MarketRemaining: remaining, Target: target, RegularToMarket: plan.RegularToMarket, FIRToMarket: plan.FIRToMarket})
	}
	return tx, nil
}

// ConfirmHandover never marks a quest complete. Actual selected stock is
// recorded first, and later Market quest completion only removes requirements.
func (a *ItemWindowApp) confirmHandoverLegacy() ItemState {
	if !a.progressNetworkMu.TryLock() {
		a.notifyHandoverBusy()
		return a.GetState()
	}
	defer a.progressNetworkMu.Unlock()
	if !a.trackingMu.TryLock() {
		a.notifyHandoverBusy()
		return a.GetState()
	}
	defer a.trackingMu.Unlock()
	a.mu.Lock()
	st := a.state.Handover
	if a.stockEditorBlocksStockLocked() || st == nil || !st.Open || st.Busy || st.Completed || a.state.Busy || a.apiKey == "" || a.state.CharacterUID == "" || a.localStore == nil || st.CharUID != a.state.CharacterUID {
		a.mu.Unlock()
		return a.GetState()
	}
	if st.Pending && a.handover == nil {
		a.mu.Unlock()
		return a.GetState()
	}
	key, opts := a.apiKey, a.state.ItemOptions
	draft := append([]ItemHandoverEntry{}, st.Items...)
	tx := a.handover
	st.Busy = true
	st.Error = ""
	st.Status = "Checking stock…"
	a.mu.Unlock()
	a.emit()
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
	defer cancel()
	if tx == nil {
		var err error
		tx, err = a.planHandover(ctx, key, opts, draft)
		if err != nil {
			a.failHandover(handoverError(err))
			return a.GetState()
		}
		if err = a.checkHandoverFallbackLanes(opts, tx); err != nil {
			a.failHandover(handoverError(err))
			return a.GetState()
		}
		if err = a.saveHandover(tx); err != nil {
			a.failHandover("The hand-in could not be saved. No items were deducted.")
			return a.GetState()
		}
		a.mu.Lock()
		a.handover = tx
		a.state.Handover.Pending = true
		a.mu.Unlock()
	}
	if err := a.checkHandoverFallbackLanes(opts, tx); err != nil {
		a.failHandover(handoverError(err))
		return a.GetState()
	}
	if !tx.LocalDone {
		if err := a.validateStockEditorMarketUsage(tx); err != nil {
			a.failHandover("Hand-in history could not accept this operation. The saved hand-in was kept; no new stock deduction was sent.")
			return a.GetState()
		}
		entries := []localprogress.HandoverEntry{}
		for _, op := range tx.Items {
			if handoverQuantity(op.Local) > 0 {
				entries = append(entries, op.Local)
			}
		}
		if len(entries) > 0 {
			if _, err := a.localStore.RecordHandover(tx.CharUID, tx.Mode, tx.ID, entries); err != nil {
				a.failHandover("Local stock could not be updated. The saved hand-in can be resumed.")
				return a.GetState()
			}
		}
		fallbackEntries := []localprogress.HandoverEntry{}
		for _, op := range tx.Items {
			if handoverQuantity(op.Fallback) > 0 {
				fallbackEntries = append(fallbackEntries, op.Fallback)
			}
		}
		if len(fallbackEntries) > 0 {
			if _, err := a.marketFallbackStock().RecordHandover(tx.CharUID, tx.Mode, tx.ID, fallbackEntries); err != nil {
				a.failHandover("Market's local fallback stock could not be updated. The saved hand-in can be resumed.")
				return a.GetState()
			}
		}
		tx.LocalDone = true
		if err := a.saveHandover(tx); err != nil {
			a.failHandover("Local stock was recorded, but the hand-in status could not be saved. Resume to verify it.")
			return a.GetState()
		}
	}
	for i := range tx.Items {
		if !a.handoverCurrent(key, opts) {
			a.failHandover("The character or settings changed. The saved hand-in was kept.")
			return a.GetState()
		}
		a.mu.Lock()
		a.state.Handover.Status = fmt.Sprintf("Recording hand-in: %d/%d…", i+1, len(tx.Items))
		a.mu.Unlock()
		a.emit()
		if err := a.processHandoverMarket(ctx, key, opts, tx, &tx.Items[i]); err != nil {
			a.failHandover(handoverError(err))
			a.refreshAfterHandover(ctx, key, opts)
			return a.GetState()
		}
	}
	if err := os.Remove(a.handoverPath(tx.CharUID, tx.Mode)); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.failHandover("The hand-in was recorded, but its saved status could not be cleared. Resume to finish.")
		return a.GetState()
	}
	a.refreshAfterHandover(ctx, key, opts)
	a.mu.Lock()
	a.handover = nil
	a.state.Handover.Busy = false
	a.state.Handover.Pending = false
	a.state.Handover.Completed = true
	a.state.Handover.Status = "Hand-in recorded. Hand the items in in the game, then mark the quest or hideout upgrade complete on Tarkov Market."
	a.state.Handover.Error = ""
	a.mu.Unlock()
	a.emit()
	return a.GetState()
}

// Egy mentett hand-in listáját nem tervezzük újra. Mielőtt bármelyik helyi
// nyugtát rögzítenénk, az összes művelet védett Market/fallback sávját nézzük.
func (a *ItemWindowApp) checkHandoverFallbackLanes(opts ItemOptions, tx *handoverTransaction) error {
	for _, op := range tx.Items {
		blocked, err := a.marketFallbackBlocksItem(opts, op.ItemID)
		if err != nil {
			return err
		}
		if blocked && (handoverQuantity(op.Fallback) > 0 || handoverQuantity(op.Market) > 0) {
			return fmt.Errorf("%s: %s No further items were deducted.", op.Name, marketFallbackItemBlockedMessage)
		}
	}
	return nil
}

func (a *ItemWindowApp) processHandoverMarket(ctx context.Context, key string, opts ItemOptions, tx *handoverTransaction, op *handoverOperation) error {
	quantity := handoverQuantity(op.Market)
	if quantity == 0 {
		return nil
	}
	if op.Stage == "done" {
		return a.recordStockEditorMarketUsage(tx, op)
	}
	if op.Stage != "confirmed" {
		remote, err := a.tracker.LoadTracked(ctx, key, tx.CharUID, opts.Language)
		if err != nil {
			return err
		}
		if remote.CharUID != tx.CharUID || remote.GameMode != tx.Mode || remote.FIRInventorySeparate || remote.Revision == "" {
			return market.ErrProgressConflict
		}
		var matches []market.TrackedItem
		for _, row := range remote.Items {
			if row.ItemID == op.ItemID {
				matches = append(matches, row)
			}
		}
		if len(matches) != 1 {
			return market.ErrProgressConflict
		}
		current := matches[0].AvailableQuantity
		if op.Stage == "prepared" && current == op.Target {
			if remote.Revision == op.Revision {
				return errors.New("The Market hand-in readback was outdated. Resume after refreshing stock; no repeated deduction was sent.")
			}
			op.Stage = "confirmed"
		} else {
			if op.Stage == "prepared" && (current != op.Target+quantity || remote.Revision != op.Revision) {
				return errors.New("Market stock changed while this hand-in was pending. Check it on Tarkov Market; no repeated deduction was sent.")
			}
			if current < quantity {
				return errors.New("Market stock is no longer sufficient. The completed local part was kept; check the pending hand-in.")
			}
			// Reconcile confirmed type metadata against this fresh Market count.
			projected, err := a.applyProgressStock(marketProgressOnly(remote))
			if err != nil {
				return err
			}
			var item localprogress.Item
			for _, row := range projected.Items {
				if row.ItemID == op.ItemID {
					item = row
					break
				}
			}
			item.LocalAvailableQuantity, item.LocalRegularQuantity, item.LocalFIRQuantity = 0, 0, 0
			item.MarketFallbackAvailableQuantity, item.MarketFallbackRegularQuantity, item.MarketFallbackFIRQuantity = 0, 0, 0
			plan, err := localprogress.PlanHandoverItem(item, op.RegularToMarket, op.FIRToMarket)
			if err != nil {
				return errors.New("Market stock of the selected FiR/non-FiR type changed. No further deduction was sent.")
			}
			op.Market = plan.Market
			op.Target = current - quantity
			op.MarketRemaining = handoverRemainingKinds(item, plan.Market, op.Target)
			op.Revision = remote.Revision
			if err := a.validateStockEditorMarketUsage(tx); err != nil {
				return errors.New("Hand-in history could not accept the current Market quantities. The saved hand-in was kept; no Market change was sent.")
			}
			op.Stage = "prepared"
			if err := a.saveHandover(tx); err != nil {
				return errors.New("The Market operation could not be saved. No Market change was sent.")
			}
			if !a.handoverCurrent(key, opts) {
				return context.Canceled
			}
			result, err := a.tracker.SetTrackedQuantity(ctx, key, tx.CharUID, op.ItemID, op.Revision, op.Target)
			if err != nil && !errors.Is(err, market.ErrProgressWriteUncertain) {
				diagnostic := progressWriteDiagnostic(result, err, op.Revision, "", nil)
				// A definite rejection did not modify stock. Keep this distinct
				// from an interrupted request whose result remains unknown.
				op.Stage = "rejected"
				if saveErr := a.saveHandover(tx); saveErr != nil {
					return errors.New("The rejected Market operation status could not be saved. Resume to verify stock.")
				}
				return controlledHandoverWriteError(err, diagnostic)
			}
			// Az abszolút setter OK/no-op válasza nem igazol készletlevonást.
			// A napló csak egy új revíziójú, pontos target GET után zárható le.
			observed, readErr := a.tracker.LoadTracked(ctx, key, tx.CharUID, opts.Language)
			diagnostic := progressWriteDiagnostic(result, err, op.Revision, observed.Revision, readErr)
			count, observedQuantity := 0, int64(0)
			valid := readErr == nil && observed.CharUID == tx.CharUID && observed.GameMode == tx.Mode && !observed.FIRInventorySeparate && observed.Revision != ""
			if valid {
				for _, row := range observed.Items {
					if row.ItemID == op.ItemID {
						count++
						observedQuantity = row.AvailableQuantity
					}
				}
			}
			if !valid || count != 1 || observedQuantity != op.Target || observed.Revision == op.Revision {
				change := "Market did not confirm the deduction."
				if err == nil && result.OK && !result.Changed {
					change = "Tarkov Market reported no change."
				}
				if valid && count == 1 {
					diagnostic += progressItemMismatch(market.Item{BSGID: op.ItemID, Name: op.Name}, observed)
					return fmt.Errorf("%s Requested stock: %d; current stock: %d. The operation was saved; resume after checking stock. No automatic retry was sent. %s", change, op.Target, observedQuantity, diagnostic)
				}
				return fmt.Errorf("%s Requested stock: %d; current stock: unavailable. The operation was saved; resume after checking stock. No automatic retry was sent. %s", change, op.Target, diagnostic)
			}
			op.Stage = "confirmed"
		}
		if err := a.saveHandover(tx); err != nil {
			return errors.New("Market stock was recorded, but the operation status could not be saved. Resume to verify it.")
		}
	}
	remaining := []localprogress.HandoverEntry{}
	if op.MarketRemaining.ItemID != "" {
		remaining = append(remaining, op.MarketRemaining)
	}
	if _, err := a.localStore.RecordMarketRemoval(tx.CharUID, tx.Mode, op.Market, op.Target+quantity, op.Target, remaining...); err != nil {
		return errors.New("Market stock was recorded, but its local FiR annotation could not be saved. Resume to finish.")
	}
	if err := a.recordStockEditorMarketUsage(tx, op); err != nil {
		return errors.New("Market stock was recorded, but its hand-in history could not be saved. Resume to finish.")
	}
	op.Stage = "done"
	return a.saveHandover(tx)
}

func handoverRemainingKinds(item localprogress.Item, removed localprogress.HandoverEntry, target int64) localprogress.HandoverEntry {
	regular, fir := item.MarketRegularQuantity-removed.Regular, item.MarketFIRQuantity-removed.FIR
	return localprogress.HandoverEntry{ItemID: item.ItemID, Regular: regular, FIR: fir, Unknown: target - regular - fir}
}

func handoverError(err error) string {
	if err == nil {
		return ""
	}
	var controlled *progressWriteFeedbackError
	if errors.As(err, &controlled) {
		return controlled.Error()
	}
	if errors.Is(err, market.ErrRateLimited) {
		return "The Market request limit was reached. Wait a minute, then resume the saved hand-in."
	}
	if errors.Is(err, market.ErrProgressConflict) {
		return "Market progress changed. Resume the saved hand-in to check current stock before continuing."
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "The hand-in was interrupted. Resume the saved operation before changing stock."
	}
	var failure *localProgressLoadError
	if errors.As(err, &failure) {
		return localProgressErrorText(err)
	}
	if errors.Is(err, market.ErrUnauthorized) || errors.Is(err, market.ErrProgressForbidden) || errors.Is(err, market.ErrAPIKeyRequired) {
		return trackingError(err)
	}
	return err.Error()
}

func (a *ItemWindowApp) refreshAfterHandover(ctx context.Context, key string, opts ItemOptions) {
	snapshot, warning, err := a.loadLocalSources(ctx, key, opts)
	if err == nil {
		snapshot, err = a.applyProgressStock(snapshot)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.apiKey != key || a.state.ItemOptions.revision != opts.revision {
		return
	}
	a.localAt = time.Time{}
	if err != nil {
		if a.state.Local != nil {
			a.state.Local.Error = localProgressErrorText(err)
		}
		return
	}
	a.localSnapshot = snapshot
	a.localRevision = opts.revision
	a.localAt = time.Now()
	a.state.Local = localLists(snapshot, a.localAt, warning)
	if a.state.Item != nil {
		a.state.Tracking = localTrackedView(*a.state.Item, snapshot)
	}
}

// A háttérben ellenőrzött Market-terv csak a változtathatatlan listában lévő
// tárgyakat védi. A beolvasás alatt álló új draft továbbra globális művelet.
func (a *ItemWindowApp) handoverBlocksItemLocked(id string) bool {
	st := a.state.Handover
	if st == nil || st.Completed {
		return false
	}
	if !st.Background && !a.handoverScopeLocked() {
		return st.Open || st.Pending || st.Busy
	}
	for _, row := range st.Items {
		if row.ItemID == id {
			return true
		}
	}
	return false
}

// Csak validált, tartósan elszámolt v2 terv oldhatja fel a profil többi
// tárgyát. A frontend jelző nem helyettesíti a backend naplóazonosságát.
func (a *ItemWindowApp) handoverScopeLocked() bool {
	st, tx := a.state.Handover, a.handover
	return st != nil && tx != nil && tx.Version == 2 && tx.LocalDone && tx.CharUID == st.CharUID && tx.CharUID == a.state.CharacterUID && strings.EqualFold(tx.Mode, a.state.Mode)
}

func (a *ItemWindowApp) ConfirmHandover() (result ItemState) {
	if !a.trackingMu.TryLock() {
		a.notifyHandoverBusy()
		return a.GetState()
	}
	a.mu.Lock()
	st := a.state.Handover
	if a.stockEditorBlocksStockLocked() || st == nil || !st.Open || st.Busy || st.Completed || a.state.Busy || a.state.CharacterUID == "" || a.localStore == nil || st.CharUID != a.state.CharacterUID || st.Pending && a.handover == nil {
		a.mu.Unlock()
		a.trackingMu.Unlock()
		return a.GetState()
	}
	key, opts := a.apiKey, a.state.ItemOptions
	tx := a.handover
	draft := append([]ItemHandoverEntry{}, st.Items...)
	// A korábbi, bizonytalan v1 napló explicit folytatása változatlan protokoll.
	if tx != nil && tx.Version == 1 {
		a.mu.Unlock()
		a.trackingMu.Unlock()
		return a.confirmHandoverLegacy()
	}
	for _, row := range draft {
		if message := a.progressUnsafeItemLocked(row.ItemID); message != "" {
			st.Error = message
			a.mu.Unlock()
			a.trackingMu.Unlock()
			a.emit()
			return a.GetState()
		}
	}
	st.Busy = true
	st.Error = ""
	st.Status = "Saving the hand-in locally…"
	a.mu.Unlock()
	// A rövid tartós commit idején a profil/draft nem váltható. A háttér
	// Market-kérés előtt már false, és minden visszatérés a kész állapotot adja.
	defer func() {
		a.mu.Lock()
		if a.state.Handover == st {
			st.Busy = false
		}
		a.mu.Unlock()
		a.trackingMu.Unlock()
		a.configureTrackedAddHotkey()
		a.emit()
		result = a.GetState()
	}()
	currentLocked := func() bool {
		return a.localCurrentLocked(key, opts) && a.state.Handover == st && st.CharUID == opts.CharacterUID && a.ctx != nil && a.ctx.Err() == nil
	}
	current := func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return currentLocked()
	}
	fail := func(message string) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if currentLocked() {
			st.Busy, st.Background = false, false
			st.Error = message
			st.Pending = a.handover != nil
			st.Scoped = a.handoverScopeLocked()
			if st.Pending {
				st.Status = "The hand-in is saved. Resume it to verify the remaining operations."
			}
		}
	}
	if tx == nil {
		snapshot, _, err := a.localFirstSnapshot(key, opts)
		if err != nil {
			fail("Refresh Items Tracker before handing in stock. " + localProgressErrorText(err))
			return a.GetState()
		}
		tx, err = a.planHandoverSnapshot(opts, draft, snapshot)
		if err != nil {
			fail(handoverError(err))
			return a.GetState()
		}
		tx.Version = 2
		for i := range tx.Items {
			if handoverQuantity(tx.Items[i].Market) > 0 {
				tx.Items[i].Stage, tx.Items[i].Revision = "unsent", snapshot.Revision
			}
		}
		if err = a.checkHandoverFallbackLanes(opts, tx); err != nil {
			fail(handoverError(err))
			return a.GetState()
		}
		if err = a.validateStockEditorMarketUsage(tx); err != nil {
			fail("Hand-in history could not accept this operation. No stock was deducted.")
			return a.GetState()
		}
		if !current() {
			return a.GetState()
		}
		if err = a.saveHandover(tx); err != nil {
			fail("The hand-in could not be saved. No stock was deducted.")
			return a.GetState()
		}
		a.mu.Lock()
		if !currentLocked() {
			a.mu.Unlock()
			return a.GetState()
		}
		a.handover = tx
		st.Pending = true
		a.mu.Unlock()
	}
	if !current() {
		return a.GetState()
	}
	if err := a.checkHandoverFallbackLanes(opts, tx); err != nil {
		fail(handoverError(err))
		return a.GetState()
	}
	if err := a.commitLocalFirstHandover(tx); err != nil {
		fail(handoverError(err))
		return a.GetState()
	}
	if !current() {
		return a.GetState()
	}
	hasMarket := false
	for _, op := range tx.Items {
		hasMarket = hasMarket || handoverQuantity(op.Market) > 0 && op.Stage != "done"
	}
	if !hasMarket {
		if err := a.finishLocalFirstHandover(tx); err != nil {
			fail(handoverError(err))
			return a.GetState()
		}
	} else {
		a.mu.Lock()
		if !currentLocked() {
			a.mu.Unlock()
			return a.GetState()
		}
		st.Busy = false
		st.Pending = true
		st.Background = true
		st.Scoped = true
		st.Error = ""
		st.Status = "Local stock recorded. Market hand-in is being checked in the background."
		a.mu.Unlock()
	}
	a.publishLocalFirstProgress(key, opts, "Hand-in stock recorded locally.")
	if hasMarket {
		a.queueProgressWork()
	}
	return a.GetState()
}

func (a *ItemWindowApp) commitLocalFirstHandover(tx *handoverTransaction) error {
	if tx.LocalDone {
		return nil
	}
	if err := a.validateStockEditorMarketUsage(tx); err != nil {
		return errors.New("Hand-in history could not accept this operation. The saved plan was kept.")
	}
	dev, fallback := []localprogress.HandoverEntry{}, []localprogress.HandoverEntry{}
	for _, op := range tx.Items {
		if handoverQuantity(op.Local) > 0 {
			dev = append(dev, op.Local)
		}
		if handoverQuantity(op.Fallback) > 0 {
			fallback = append(fallback, op.Fallback)
		}
	}
	if len(dev) > 0 {
		if _, err := a.localStore.RecordHandover(tx.CharUID, tx.Mode, tx.ID, dev); err != nil {
			return errors.New("Local stock could not be updated. Resume the saved hand-in.")
		}
	}
	if len(fallback) > 0 {
		if _, err := a.marketFallbackStock().RecordHandover(tx.CharUID, tx.Mode, tx.ID, fallback); err != nil {
			return errors.New("Market fallback stock could not be updated. Resume the saved hand-in.")
		}
	}
	tx.LocalDone = true
	if err := a.saveHandover(tx); err != nil {
		// A saját nyugták már tartósak lehetnek, de a memóriában sem szabad
		// késznek jelölni az el nem mentett fázist. Az exact nyugta retry-safe.
		tx.LocalDone = false
		return err
	}
	return nil
}

func (a *ItemWindowApp) finishLocalFirstHandover(tx *handoverTransaction) error {
	if err := os.Remove(a.handoverPath(tx.CharUID, tx.Mode)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("The hand-in was recorded, but its saved status could not be cleared.")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.handover != nil && a.handover.ID == tx.ID {
		a.handover = nil
	}
	if st := a.state.Handover; st != nil && st.CharUID == tx.CharUID {
		st.Busy = false
		st.Background = false
		st.Scoped = false
		st.Pending = false
		st.Completed = true
		st.Error = ""
		st.Status = "Hand-in recorded. Hand the items in in the game, then mark the quest or hideout upgrade complete on Tarkov Market."
	}
	return nil
}

// A root soros hálózati workere hívja. A trackingMu csak a rövid tartós
// fázisváltást és saját készletmentést védi, GET/PATCH alatt nincs tartva.
func (a *ItemWindowApp) processLocalFirstHandover(ctx context.Context, key string, opts ItemOptions) error {
	a.trackingMu.Lock()
	tx, err := readHandover(a.handoverPath(opts.CharacterUID, opts.Mode), opts.CharacterUID, opts.Mode)
	if errors.Is(err, os.ErrNotExist) {
		a.trackingMu.Unlock()
		return nil
	}
	if err != nil || tx.Version != 2 {
		a.trackingMu.Unlock()
		return err
	}
	if !a.handoverCurrent(key, opts) || ctx.Err() != nil {
		a.trackingMu.Unlock()
		return context.Canceled
	}
	if !tx.LocalDone {
		a.trackingMu.Unlock()
		return nil
	}
	a.mu.Lock()
	a.handover = tx
	if a.state.Handover != nil {
		a.state.Handover.Scoped = a.handoverScopeLocked()
	}
	a.mu.Unlock()
	a.trackingMu.Unlock()
	for i := range tx.Items {
		if err = a.processLocalFirstHandoverMarket(ctx, key, opts, tx, &tx.Items[i]); err != nil {
			a.trackingMu.Lock()
			if a.handoverCurrent(key, opts) {
				a.failHandover(handoverError(err))
				if tx.Items[i].Stage != "conflict" {
					a.mu.Lock()
					if a.state.Handover != nil {
						a.state.Handover.Background = true
					}
					a.mu.Unlock()
				}
				a.publishLocalFirstProgress(key, opts, "")
			}
			a.trackingMu.Unlock()
			return err
		}
	}
	a.trackingMu.Lock()
	if !a.handoverCurrent(key, opts) {
		a.trackingMu.Unlock()
		return context.Canceled
	}
	err = a.finishLocalFirstHandover(tx)
	a.publishLocalFirstProgress(key, opts, "")
	a.trackingMu.Unlock()
	a.emit()
	return err
}

func (a *ItemWindowApp) processLocalFirstHandoverMarket(ctx context.Context, key string, opts ItemOptions, tx *handoverTransaction, op *handoverOperation) error {
	quantity := handoverQuantity(op.Market)
	if quantity == 0 {
		return nil
	}
	if op.Stage == "conflict" {
		return errors.New("Market stock changed before the saved hand-in was sent. The fixed plan was kept; no Market deduction was sent.")
	}
	if op.Stage != "confirmed" && op.Stage != "done" {
		remote, err := a.tracker.LoadTracked(ctx, key, tx.CharUID, opts.Language)
		if err != nil {
			return err
		}
		if remote.CharUID != tx.CharUID || !strings.EqualFold(remote.GameMode, tx.Mode) || remote.FIRInventorySeparate || remote.Revision == "" {
			return market.ErrProgressConflict
		}
		item, unique := stockEditorItem(marketProgressOnly(remote), op.ItemID)
		if !unique {
			return market.ErrProgressConflict
		}
		a.trackingMu.Lock()
		if !a.handoverCurrent(key, opts) || ctx.Err() != nil {
			a.trackingMu.Unlock()
			return context.Canceled
		}
		if err = a.checkHandoverFallbackLanes(opts, tx); err != nil {
			a.trackingMu.Unlock()
			return err
		}
		if op.Stage == "sent" {
			if item.MarketAvailableQuantity != op.Target || remote.Revision == op.Revision {
				a.trackingMu.Unlock()
				return errors.New("The saved Market hand-in is awaiting verification. No repeated deduction was sent.")
			}
			op.Stage = "confirmed"
		} else {
			projected, e := a.applyProgressStockRaw(marketProgressOnly(remote))
			current, ok := stockEditorItem(projected, op.ItemID)
			before := op.Target + quantity
			regular := op.MarketRemaining.Regular + op.Market.Regular
			fir := op.MarketRemaining.FIR + op.Market.FIR
			if e != nil {
				a.trackingMu.Unlock()
				return e
			}
			if !ok || current.MarketAvailableQuantity != before || remote.Revision != op.Revision || current.MarketRegularQuantity != regular || current.MarketFIRQuantity != fir {
				if e = a.updateLocalFirstMarketSnapshot(key, opts, remote); e != nil {
					a.trackingMu.Unlock()
					return e
				}
				op.Stage = "conflict"
				e = a.saveHandover(tx)
				a.trackingMu.Unlock()
				if e != nil {
					return e
				}
				return market.ErrProgressConflict
			}
			op.Stage = "sent"
			if e = a.saveHandover(tx); e != nil {
				a.trackingMu.Unlock()
				return e
			}
			a.trackingMu.Unlock()
			if !a.handoverCurrent(key, opts) || ctx.Err() != nil {
				a.trackingMu.Lock()
				stored, readErr := readHandover(a.handoverPath(tx.CharUID, tx.Mode), tx.CharUID, tx.Mode)
				if readErr == nil && stored.ID == tx.ID {
					for i := range stored.Items {
						if reflect.DeepEqual(stored.Items[i], *op) && stored.Items[i].Stage == "sent" {
							stored.Items[i].Stage = "unsent"
							if a.saveHandover(stored) == nil {
								op.Stage = "unsent"
							}
							break
						}
					}
				}
				a.trackingMu.Unlock()
				return context.Canceled
			}
			ack, writeErr := a.tracker.SetTrackedQuantity(ctx, key, tx.CharUID, op.ItemID, op.Revision, op.Target)
			if progressWriteDefinitelyNotApplied(writeErr) {
				// A 429 és a többi biztos elutasítás után nincs igazolandó
				// levonás. A gate várakozását nem terheljük azonnali GET-tel.
				return errors.Join(writeErr, a.restoreRejectedLocalFirstHandover(tx, op))
			}
			observed, readErr := a.tracker.LoadTracked(ctx, key, tx.CharUID, opts.Language)
			row, one := stockEditorItem(marketProgressOnly(observed), op.ItemID)
			if readErr != nil || observed.CharUID != tx.CharUID || !strings.EqualFold(observed.GameMode, tx.Mode) || observed.FIRInventorySeparate || !one || row.MarketAvailableQuantity != op.Target || observed.Revision == op.Revision || observed.Revision == "" {
				return fmt.Errorf("Market did not confirm the deduction. Requested stock: %d; current stock: %d. The saved operation will not be sent again automatically. %s", op.Target, row.MarketAvailableQuantity, progressWriteDiagnostic(ack, writeErr, op.Revision, observed.Revision, readErr))
			}
			remote = observed
			a.trackingMu.Lock()
			if !a.handoverCurrent(key, opts) || ctx.Err() != nil {
				a.trackingMu.Unlock()
				return context.Canceled
			}
			op.Stage = "confirmed"
		}
		if err = a.saveHandover(tx); err != nil {
			a.trackingMu.Unlock()
			return err
		}
		if err = a.updateLocalFirstMarketSnapshot(key, opts, remote); err != nil {
			a.trackingMu.Unlock()
			return err
		}
		// Saját, igazolt írás új revíziója nem külső konfliktus a még el
		// nem küldött testvérműveleteknél. Mennyiséget/tervet nem írunk át.
		for i := range tx.Items {
			sibling := &tx.Items[i]
			if sibling.Stage == "unsent" {
				row, one := stockEditorItem(marketProgressOnly(remote), sibling.ItemID)
				if one && row.MarketAvailableQuantity == sibling.Target+handoverQuantity(sibling.Market) {
					sibling.Revision = remote.Revision
				}
			}
		}
		if err = a.saveHandover(tx); err != nil {
			a.trackingMu.Unlock()
			return err
		}
		a.trackingMu.Unlock()
	}
	a.trackingMu.Lock()
	defer a.trackingMu.Unlock()
	if !a.handoverCurrent(key, opts) || ctx.Err() != nil {
		return context.Canceled
	}
	if op.Stage == "done" {
		return a.recordStockEditorMarketUsage(tx, op)
	}
	if _, err := a.localStore.RecordMarketRemoval(tx.CharUID, tx.Mode, op.Market, op.Target+quantity, op.Target, op.MarketRemaining); err != nil {
		return err
	}
	if err := a.recordStockEditorMarketUsage(tx, op); err != nil {
		return err
	}
	op.Stage = "done"
	return a.saveHandover(tx)
}
