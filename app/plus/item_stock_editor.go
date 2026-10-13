package plus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

type ItemStockEditorState struct {
	Open          bool                 `json:"open"`
	Loading       bool                 `json:"loading"`
	Saving        bool                 `json:"saving"`
	Pending       bool                 `json:"pending"`
	Background    bool                 `json:"background"`
	PendingItemID string               `json:"pendingItemId"`
	Generation    uint64               `json:"generation"`
	CharUID       string               `json:"charUid"`
	GameMode      string               `json:"gameMode"`
	Rows          []ItemStockEditorRow `json:"rows"`
	Status        string               `json:"status"`
	Error         string               `json:"error"`
	Warning       string               `json:"warning"`
}

type ItemStockEditorRow struct {
	ItemID                 string                     `json:"itemId"`
	Name                   string                     `json:"name"`
	RequiredQuantity       int64                      `json:"requiredQuantity"`
	AvailableQuantity      int64                      `json:"availableQuantity"`
	MarketTracked          bool                       `json:"marketTracked"`
	MarketEditable         bool                       `json:"marketEditable"`
	MarketFallbackEditable bool                       `json:"marketFallbackEditable"`
	MarketFallbackStock    localprogress.StockAmounts `json:"marketFallbackStock"`
	MarketFallbackUsed     localprogress.StockAmounts `json:"marketFallbackUsed"`
	LocalEditable          bool                       `json:"localEditable"`
	MarketStock            localprogress.StockAmounts `json:"marketStock"`
	LocalStock             localprogress.StockAmounts `json:"localStock"`
	LocalUsed              localprogress.StockAmounts `json:"localUsed"`
	MarketHandedIn         localprogress.StockAmounts `json:"marketHandedIn"`
	MarketSpentQuantity    int64                      `json:"marketSpentQuantity"`
	UsedQuantity           int64                      `json:"usedQuantity"`
	EditError              string                     `json:"editError"`
}

type ItemStockEditRequest struct {
	Generation uint64 `json:"generation"`
	ItemID     string `json:"itemId"`
	Source     string `json:"source"`
	FIR        int64  `json:"fir"`
	Regular    int64  `json:"regular"`
	Unknown    int64  `json:"unknown"`
}

func cloneStockEditorState(st *ItemStockEditorState) *ItemStockEditorState {
	if st == nil {
		return nil
	}
	copy := *st
	copy.Rows = append([]ItemStockEditorRow{}, st.Rows...)
	return &copy
}

func stockEditorTotal(amounts localprogress.StockAmounts) (int64, error) {
	var total int64
	for _, value := range []int64{amounts.FIR, amounts.Regular, amounts.Unknown} {
		if value < 0 || value > localprogress.MaxQuantity-total {
			return 0, localprogress.ErrInvalidQuantity
		}
		total += value
	}
	return total, nil
}

func (a *ItemWindowApp) stockEditorBusyLocked() bool {
	return a.localBackupBusyLocked() || a.state.StockEditor != nil && (a.state.StockEditor.Saving || a.state.StockEditor.Loading)
}

func (a *ItemWindowApp) stockEditorBusy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stockEditorBusyLocked()
}

func (a *ItemWindowApp) stockEditorBlocksStockLocked() bool {
	if a.localBackupBusyLocked() || a.state.MarketFallbackInvalid || a.stockEditorBusyLocked() {
		return true
	}
	st := a.state.StockEditor
	return st != nil && (st.Open || st.Pending) && !(st.Pending && handoverItemID(st.PendingItemID))
}

func (a *ItemWindowApp) stockEditorBlocksItemLocked(id string) bool {
	if a.stockEditorBlocksStockLocked() {
		return true
	}
	st := a.state.StockEditor
	return st != nil && st.Pending && st.PendingItemID == id
}

func (a *ItemWindowApp) restoreStockEditorLocked() {
	a.stockEditorGeneration++
	a.stockEditorSnapshot = localprogress.Snapshot{}
	a.stockEditorRevision = 0
	a.state.StockEditor = nil
	opts := a.state.ItemOptions
	a.state.MarketFallbackPending = false
	a.state.MarketFallbackItemID = ""
	a.state.MarketFallbackInvalid = false
	if opts.CharacterUID == "" {
		return
	}
	_ = a.refreshMarketFallbackPendingLocked(opts)
	pending, err := a.readStockEditorPending(opts)
	if pending != nil || err != nil {
		a.state.StockEditor = &ItemStockEditorState{Pending: true, Generation: a.stockEditorGeneration, CharUID: opts.CharacterUID, GameMode: strings.ToUpper(opts.Mode), Rows: []ItemStockEditorRow{}, Error: "A Market stock edit is awaiting verification. Open Items Tracker settings and refresh before changing stock."}
		if pending != nil {
			a.state.StockEditor.PendingItemID = pending.ItemID
			a.state.StockEditor.Background = pending.Version == 2 && pending.Stage != "conflict"
		}
	}
}

func (a *ItemWindowApp) stockEditorFailure(key string, opts ItemOptions, message string) ItemState {
	a.mu.Lock()
	if a.localCurrentLocked(key, opts) && a.state.StockEditor != nil {
		a.state.StockEditor.Loading = false
		a.state.StockEditor.Saving = false
		a.state.StockEditor.Error = message
		a.localAt = time.Time{}
	}
	a.mu.Unlock()
	a.emit()
	return a.GetState()
}

// Opening the editor reads the whole character; no inspected item is required.
func (a *ItemWindowApp) OpenStockEditor() ItemState {
	a.mu.Lock()
	if a.localBackupBusyLocked() {
		a.mu.Unlock()
		return a.GetState()
	}
	if a.state.StockEditor == nil {
		a.stockEditorGeneration++
		a.state.StockEditor = &ItemStockEditorState{Generation: a.stockEditorGeneration, Rows: []ItemStockEditorRow{}}
	}
	a.state.StockEditor.Open = true
	key, opts := a.apiKey, a.state.ItemOptions
	a.mu.Unlock()
	a.configureTrackedAddHotkey()
	if a.trackingMu.TryLock() {
		snapshot, warning, err := a.localFirstSnapshot(key, opts)
		if err == nil {
			result := a.publishStockEditor(ctxOrBackground(a.ctx), key, opts, snapshot, warning, "Items Tracker stock loaded from confirmed local progress.", "")
			a.trackingMu.Unlock()
			return result
		}
		a.trackingMu.Unlock()
	}
	return a.RefreshStockEditor()
}

func (a *ItemWindowApp) CloseStockEditor() ItemState {
	a.mu.Lock()
	if a.state.StockEditor != nil && !a.stockEditorBusyLocked() {
		a.state.StockEditor.Open = false
	}
	a.mu.Unlock()
	a.configureTrackedAddHotkey()
	a.emit()
	return a.GetState()
}

func (a *ItemWindowApp) refreshStockEditorLegacy() ItemState {
	if !a.progressNetworkMu.TryLock() {
		a.mu.Lock()
		if a.state.StockEditor != nil {
			a.state.StockEditor.Error = progressBusyMessage
		}
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	defer a.progressNetworkMu.Unlock()
	if !a.trackingMu.TryLock() {
		a.mu.Lock()
		if a.state.StockEditor != nil {
			a.state.StockEditor.Error = progressBusyMessage
		}
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	defer a.trackingMu.Unlock()
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	if a.state.StockEditor == nil {
		a.mu.Unlock()
		return a.GetState()
	}
	if a.ctx == nil || a.ctx.Err() != nil || key == "" || opts.CharacterUID == "" || a.localStore == nil || a.tracker == nil {
		a.state.StockEditor.Error = "Enter your API key and select a character to edit Items Tracker stock."
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	if a.state.Handover != nil && a.state.Handover.Busy {
		a.state.StockEditor.Error = progressBusyMessage
		a.mu.Unlock()
		return a.GetState()
	}
	a.state.StockEditor.Loading = true
	a.state.StockEditor.Error = ""
	a.mu.Unlock()
	a.emit()
	ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
	defer cancel()
	a.mu.Lock()
	canSync := a.state.Handover == nil || !a.state.Handover.Open && !a.state.Handover.Pending
	a.mu.Unlock()
	fallbackMessage := ""
	if canSync {
		if _, err := a.recoverMarketFallback(ctx, key, opts, "", true); err != nil {
			a.mu.Lock()
			pending := a.state.MarketFallbackPending
			a.mu.Unlock()
			if pending {
				fallbackMessage = "Market upload is awaiting verification. Use Retry saved upload to check and retry the saved quantity."
			}
		}
	}
	snapshot, warning, err := a.loadStockEditorSnapshot(ctx, key, opts)
	if err != nil {
		return a.stockEditorFailure(key, opts, "Items Tracker stock could not be loaded. "+localProgressErrorText(err))
	}
	pending, pendingErr := a.readStockEditorPending(opts)
	message := ""
	if pendingErr != nil {
		message = "The pending Market edit file is invalid. It has been kept; stock changes are disabled."
	} else if pending != nil {
		if pending.Version == 2 && pending.Stage == "unsent" {
			a.mu.Lock()
			if a.localCurrentLocked(key, opts) && a.state.StockEditor != nil {
				a.state.StockEditor.Background = true
			}
			a.mu.Unlock()
			a.queueProgressWork()
		} else {
			message, err = a.verifyStockEditorPending(key, opts, snapshot, pending)
		}
		if err != nil {
			message = "The Market edit could not be completed locally. The pending edit has been kept."
		}
		if message == "" {
			snapshot, err = a.applyProgressStock(snapshot)
			if err != nil {
				return a.stockEditorFailure(key, opts, "The stock types could not be refreshed. The local file has been kept.")
			}
		}
	}
	if fallbackMessage != "" {
		if message != "" {
			message += "\n"
		}
		message += fallbackMessage
	}
	if projected, e := a.projectPendingProgress(snapshot); e == nil {
		snapshot = projected
	}
	return a.publishStockEditor(ctx, key, opts, snapshot, warning, "Items Tracker stock refreshed.", message)
}

func (a *ItemWindowApp) loadStockEditorSnapshot(ctx context.Context, key string, opts ItemOptions) (localprogress.Snapshot, string, error) {
	snapshot, warning, err := a.loadLocalSources(ctx, key, opts)
	if err != nil {
		return snapshot, warning, err
	}
	if !a.handoverCurrent(key, opts) {
		return snapshot, warning, context.Canceled
	}
	if _, err := a.loadStockEditorMarketUsage(opts); err != nil {
		return snapshot, warning, localProgressFailure("save", err)
	}
	if err = a.rememberProgressSources(key, opts, snapshot, warning); err != nil {
		return snapshot, warning, err
	}
	snapshot, err = a.applyProgressStockRaw(snapshot)
	if err != nil {
		return snapshot, warning, localProgressFailure("save", err)
	}
	return snapshot, warning, nil
}

func (a *ItemWindowApp) stockEditorRows(ctx context.Context, key string, opts ItemOptions, snapshot localprogress.Snapshot) ([]ItemStockEditorRow, error) {
	allocated, err := localprogress.Allocate(snapshot)
	if err != nil {
		return nil, err
	}
	snapshot = allocated
	stock, err := a.localStore.Load(opts.CharacterUID, strings.ToUpper(opts.Mode))
	if err != nil {
		return nil, err
	}
	usage, err := a.loadStockEditorMarketUsage(opts)
	if err != nil {
		return nil, err
	}
	pendingUploads, err := a.readMarketFallbackSyncIntents(opts)
	if err != nil {
		return nil, err
	}
	protectedUploads := make(map[string]bool)
	for _, pending := range pendingUploads {
		protectedUploads[pending.ItemID] = true
	}
	remembered := make(map[string]bool)
	for _, row := range stock.Items {
		remembered[row.ItemID] = row.LocalCollectedQuantity > 0 || row.LocalUsedQuantity > 0
	}
	items := append([]localprogress.Item{}, snapshot.Items...)
	counts := make(map[string]int)
	for _, row := range items {
		counts[row.ItemID]++
	}
	for _, row := range stock.Items {
		if counts[row.ItemID] == 0 {
			items = append(items, row)
			counts[row.ItemID]++
		}
	}
	for id := range usage {
		if counts[id] == 0 {
			items = append(items, localprogress.Item{ItemID: id})
			counts[id]++
		}
	}
	metadata := make(map[string]string)
	a.mu.Lock()
	if a.state.Item != nil {
		metadata[a.state.Item.BSGID] = a.state.Item.Name
	}
	if a.state.StockEditor != nil {
		for _, row := range a.state.StockEditor.Rows {
			metadata[row.ItemID] = row.Name
		}
	}
	a.mu.Unlock()
	seen := make(map[string]bool)
	rows := []ItemStockEditorRow{}
	for _, item := range items {
		if seen[item.ItemID] {
			continue
		}
		seen[item.ItemID] = true
		name := item.Name
		if name == "" || name == item.ItemID {
			name = metadata[item.ItemID]
		}
		if name == "" {
			name = item.ItemID
		}
		localEditable := remembered[item.ItemID]
		for _, source := range item.Sources {
			if source.Origin == localprogress.OriginDev {
				localEditable = true
			}
		}
		required := item.RequiredQuantity
		if item.CollectionCalculated {
			required = item.CollectionRequiredQuantity
		}
		localUsed := localprogress.StockAmounts{FIR: item.LocalUsedFIRQuantity, Regular: item.LocalUsedRegularQuantity, Unknown: item.LocalUsedQuantity - item.LocalUsedFIRQuantity - item.LocalUsedRegularQuantity}
		marketUsed := usage[item.ItemID]
		marketUsedTotal, e := stockEditorTotal(marketUsed)
		if e != nil {
			return nil, e
		}
		if item.MarketFallbackUsedQuantity > localprogress.MaxQuantity-marketUsedTotal || item.LocalUsedQuantity > localprogress.MaxQuantity-marketUsedTotal-item.MarketFallbackUsedQuantity {
			return nil, localprogress.ErrInvalidQuantity
		}
		row := ItemStockEditorRow{ItemID: item.ItemID, Name: name, RequiredQuantity: required, AvailableQuantity: item.AvailableQuantity,
			MarketFallbackEditable: counts[item.ItemID] == 1 && (item.MarketTracked || item.MarketFallbackAvailableQuantity > 0 || item.MarketFallbackUsedQuantity > 0),
			MarketFallbackStock:    fallbackAmounts(item),
			MarketFallbackUsed:     localprogress.StockAmounts{FIR: item.MarketFallbackUsedFIRQuantity, Regular: item.MarketFallbackUsedRegularQuantity, Unknown: item.MarketFallbackUsedQuantity - item.MarketFallbackUsedRegularQuantity - item.MarketFallbackUsedFIRQuantity},
			MarketTracked:          item.MarketTracked, MarketEditable: item.MarketTracked && !snapshot.FIRInventorySeparate && counts[item.ItemID] == 1,
			LocalEditable: localEditable && counts[item.ItemID] == 1,
			MarketStock:   localprogress.StockAmounts{FIR: item.MarketFIRQuantity, Regular: item.MarketRegularQuantity, Unknown: item.MarketAvailableQuantity - item.MarketFIRQuantity - item.MarketRegularQuantity},
			LocalStock:    localprogress.StockAmounts{FIR: item.LocalFIRQuantity, Regular: item.LocalRegularQuantity, Unknown: item.LocalAvailableQuantity - item.LocalFIRQuantity - item.LocalRegularQuantity},
			LocalUsed:     localUsed, MarketHandedIn: marketUsed, MarketSpentQuantity: item.SpentQuantity, UsedQuantity: item.LocalUsedQuantity + marketUsedTotal + item.MarketFallbackUsedQuantity}
		if counts[item.ItemID] != 1 {
			row.EditError = "Multiple item variants share this ID. Stock editing is unavailable."
		}
		if snapshot.FIRInventorySeparate && item.MarketTracked {
			row.EditError = "Separate Market FiR inventory cannot be edited through this API."
		}
		if protectedUploads[item.ItemID] {
			row.MarketEditable, row.MarketFallbackEditable = false, false
			row.EditError = marketFallbackItemBlockedMessage
		}
		a.mu.Lock()
		unsafe := a.progressUnsafeItemLocked(item.ItemID)
		a.mu.Unlock()
		if unsafe != "" {
			row.MarketEditable, row.MarketFallbackEditable, row.LocalEditable = false, false, false
			row.EditError = unsafe
		}
		if len(item.Sources) == 0 && row.AvailableQuantity == 0 && row.UsedQuantity == 0 && row.MarketSpentQuantity == 0 {
			continue
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name) })
	return rows, nil
}

func (a *ItemWindowApp) publishStockEditor(ctx context.Context, key string, opts ItemOptions, snapshot localprogress.Snapshot, warning, status, message string) ItemState {
	rows, err := a.stockEditorRows(ctx, key, opts, snapshot)
	if err != nil {
		return a.stockEditorFailure(key, opts, "Stock or hand-in history could not be read. The existing files were kept.")
	}
	pending, pendingErr := a.readStockEditorPending(opts)
	a.mu.Lock()
	if a.localCurrentLocked(key, opts) && a.state.StockEditor != nil {
		open := a.state.StockEditor.Open
		background := a.state.StockEditor.Background
		a.stockEditorGeneration++
		a.stockEditorSnapshot = localprogress.Clone(snapshot)
		a.stockEditorRevision = opts.revision
		a.state.StockEditor = &ItemStockEditorState{Open: open, Pending: pending != nil || pendingErr != nil, Generation: a.stockEditorGeneration, CharUID: opts.CharacterUID, GameMode: strings.ToUpper(opts.Mode), Rows: rows, Status: status, Error: message, Warning: warning}
		if pending != nil {
			a.state.StockEditor.PendingItemID = pending.ItemID
			a.state.StockEditor.Background = background && (pending.Version == 1 || pending.Stage != "conflict")
		}
		a.localSnapshot, a.localRevision, a.localAt = snapshot, opts.revision, time.Now()
		a.state.Local = localLists(snapshot, a.localAt, warning)
		if a.state.Item != nil {
			a.state.Tracking = localTrackedView(*a.state.Item, snapshot)
		}
	}
	a.mu.Unlock()
	a.emit()
	return a.GetState()
}

func stockEditorItem(snapshot localprogress.Snapshot, id string) (localprogress.Item, bool) {
	var item localprogress.Item
	count := 0
	for _, candidate := range snapshot.Items {
		if candidate.ItemID == id {
			item = candidate
			count++
		}
	}
	return item, count == 1
}

func stockEditorLocalAmounts(item localprogress.Item) localprogress.StockAmounts {
	return localprogress.StockAmounts{FIR: item.LocalFIRQuantity, Regular: item.LocalRegularQuantity, Unknown: item.LocalAvailableQuantity - item.LocalFIRQuantity - item.LocalRegularQuantity}
}

func (a *ItemWindowApp) saveStockEditorItemLegacy(request ItemStockEditRequest) ItemState {
	amounts := localprogress.StockAmounts{FIR: request.FIR, Regular: request.Regular, Unknown: request.Unknown}
	target, quantityErr := stockEditorTotal(amounts)
	if !a.trackingMu.TryLock() {
		a.mu.Lock()
		if a.state.StockEditor != nil {
			a.state.StockEditor.Error = progressBusyMessage
		}
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	defer a.trackingMu.Unlock()
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	st := a.state.StockEditor
	if st == nil || !st.Open {
		a.mu.Unlock()
		return a.GetState()
	}
	if quantityErr != nil || !handoverItemID(request.ItemID) || request.Source != "market" && request.Source != "local" && request.Source != "market-fallback" {
		st.Error = "Enter whole, nonnegative quantities with a total no greater than 1000000000."
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	if a.ctx == nil || a.ctx.Err() != nil || key == "" || opts.CharacterUID == "" || a.localStore == nil || a.tracker == nil || st.Loading || st.Saving || st.Pending || a.state.MarketFallbackInvalid || a.state.Busy || a.state.Handover != nil && (a.state.Handover.Open || a.state.Handover.Pending || a.state.Handover.Busy) {
		st.Error = "Finish pending operations before editing stock."
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	if request.Source != "local" && a.marketFallbackBlocksItemLocked(request.ItemID) {
		st.Error = marketFallbackItemBlockedMessage
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	if request.Generation != st.Generation || a.stockEditorRevision != opts.revision || st.CharUID != opts.CharacterUID || st.GameMode != strings.ToUpper(opts.Mode) {
		st.Error = "This stock list is outdated. Refresh it before saving."
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	if request.Source != "local" && a.marketFallbackBlocksItemLocked(request.ItemID) {
		st.Error = marketFallbackItemBlockedMessage
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	var selected ItemStockEditorRow
	found := false
	for _, row := range st.Rows {
		if row.ItemID == request.ItemID {
			selected = row
			found = true
			break
		}
	}
	if !found || request.Source == "market" && !selected.MarketEditable || request.Source == "local" && !selected.LocalEditable || request.Source == "market-fallback" && !selected.MarketFallbackEditable {
		st.Error = "This stock source cannot be edited for the selected item."
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	baseline := localprogress.Clone(a.stockEditorSnapshot)
	st.Saving = true
	st.Error = ""
	st.Status = "Checking stock…"
	a.mu.Unlock()
	a.emit()
	ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
	defer cancel()
	blocked, gateErr := a.marketFallbackBlocksItem(opts, request.ItemID)
	if gateErr != nil {
		return a.stockEditorFailure(key, opts, localProgressErrorText(gateErr))
	}
	if request.Source != "local" && blocked {
		return a.stockEditorFailure(key, opts, marketFallbackItemBlockedMessage)
	}
	snapshot, warning, err := a.loadStockEditorSnapshot(ctx, key, opts)
	if err != nil {
		return a.stockEditorFailure(key, opts, "Stock could not be checked. "+localProgressErrorText(err))
	}
	current, unique := stockEditorItem(snapshot, request.ItemID)
	missing := true
	for _, candidate := range snapshot.Items {
		if candidate.ItemID == request.ItemID {
			missing = false
			break
		}
	}
	if missing && request.Source == "local" {
		stock, e := a.localStore.Load(opts.CharacterUID, strings.ToUpper(opts.Mode))
		if e == nil {
			current, unique = stockEditorItem(stock, request.ItemID)
		}
	}
	if !unique {
		return a.stockEditorFailure(key, opts, "The tracked item is missing or ambiguous. Refresh the list.")
	}
	if request.Source == "market-fallback" {
		if fallbackAmounts(current) != selected.MarketFallbackStock || current.MarketFallbackUsedQuantity != selected.MarketFallbackUsed.FIR+selected.MarketFallbackUsed.Regular+selected.MarketFallbackUsed.Unknown {
			return a.publishStockEditor(ctx, key, opts, snapshot, warning, "", "Pending upload stock changed. Review the refreshed quantities before saving.")
		}
		if target > localprogress.MaxQuantity-current.MarketAvailableQuantity-current.LocalAvailableQuantity {
			return a.stockEditorFailure(key, opts, "The combined stock exceeds the supported quantity limit.")
		}
		if !a.handoverCurrent(key, opts) {
			return a.stockEditorFailure(key, opts, "The selected character changed. No correction was saved.")
		}
		_, err = a.marketFallbackStock().CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode), localprogress.ManualStockCorrection{ItemID: request.ItemID, LocalAvailable: &amounts})
		if err != nil {
			return a.stockEditorFailure(key, opts, "Pending upload stock could not be corrected. The existing file was kept.")
		}
	} else if request.Source == "local" {
		if stockEditorLocalAmounts(current) != selected.LocalStock || current.LocalUsedQuantity != selected.LocalUsed.FIR+selected.LocalUsed.Regular+selected.LocalUsed.Unknown {
			return a.publishStockEditor(ctx, key, opts, snapshot, warning, "", "Local stock changed. Review the refreshed quantities before saving.")
		}
		if current.MarketAvailableQuantity+current.MarketFallbackAvailableQuantity > localprogress.MaxQuantity-target {
			return a.stockEditorFailure(key, opts, "The combined stock exceeds the supported quantity limit.")
		}
		if !a.handoverCurrent(key, opts) {
			return a.stockEditorFailure(key, opts, "The selected character changed. No correction was saved.")
		}
		_, err = a.localStore.CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode), localprogress.ManualStockCorrection{ItemID: request.ItemID, LocalAvailable: &amounts})
		if err != nil {
			return a.stockEditorFailure(key, opts, "Local stock could not be corrected. The existing file was kept.")
		}
	} else {
		marketAmounts := localprogress.StockAmounts{FIR: current.MarketFIRQuantity, Regular: current.MarketRegularQuantity, Unknown: current.MarketAvailableQuantity - current.MarketFIRQuantity - current.MarketRegularQuantity}
		if !current.MarketTracked || snapshot.FIRInventorySeparate || snapshot.Revision != baseline.Revision || marketAmounts != selected.MarketStock {
			return a.publishStockEditor(ctx, key, opts, snapshot, warning, "", "Market progress changed. Review the refreshed quantities before saving.")
		}
		if target > localprogress.MaxQuantity-current.LocalAvailableQuantity-current.MarketFallbackAvailableQuantity {
			return a.stockEditorFailure(key, opts, "The combined stock exceeds the supported quantity limit.")
		}
		if target != current.MarketAvailableQuantity {
			pending := &stockEditorPending{Version: 1, CharUID: opts.CharacterUID, Mode: strings.ToUpper(opts.Mode), ItemID: request.ItemID, Amounts: amounts, Revision: snapshot.Revision}
			if err = a.saveStockEditorPending(pending); err != nil {
				return a.stockEditorFailure(key, opts, "The stock edit could not be prepared. No Market change was sent.")
			}
			a.mu.Lock()
			if a.localCurrentLocked(key, opts) {
				a.state.StockEditor.Pending = true
			}
			a.mu.Unlock()
			if !a.handoverCurrent(key, opts) {
				return a.stockEditorFailure(key, opts, "The selected character changed. The pending edit was kept.")
			}
			ack, writeErr := a.tracker.SetTrackedQuantity(ctx, key, opts.CharacterUID, request.ItemID, snapshot.Revision, target)
			// Exactly one write is sent. Refresh only reads and verifies this intent.
			if writeErr != nil && !errors.Is(writeErr, market.ErrProgressWriteUncertain) {
				if err = a.removeStockEditorPending(opts); err != nil {
					return a.stockEditorFailure(key, opts, "Market rejected the edit, but its pending file could not be cleared.")
				}
				a.mu.Lock()
				if a.localCurrentLocked(key, opts) {
					a.state.StockEditor.Pending = false
				}
				a.mu.Unlock()
				return a.stockEditorFailure(key, opts, "Market did not save the stock correction. "+progressWriteDiagnostic(ack, writeErr, snapshot.Revision, "", nil))
			}
			readback, readErr := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
			if readErr != nil || readback.CharUID != opts.CharacterUID || !strings.EqualFold(readback.GameMode, opts.Mode) || readback.FIRInventorySeparate {
				return a.stockEditorFailure(key, opts, "The Market edit is awaiting verification. Refresh before changing stock. "+progressWriteDiagnostic(ack, writeErr, snapshot.Revision, readback.Revision, readErr))
			}
			observed := marketProgressOnly(readback)
			message, verifyErr := a.verifyStockEditorPending(key, opts, observed, pending)
			if verifyErr != nil || message != "" {
				return a.stockEditorFailure(key, opts, message+" "+progressWriteDiagnostic(ack, writeErr, snapshot.Revision, readback.Revision, readErr))
			}
			// Requirements are refreshed against the new revision before publication.
			snapshot, warning, err = a.loadStockEditorSnapshot(ctx, key, opts)
			if err != nil {
				return a.stockEditorFailure(key, opts, "Market stock was verified, but progress could not be refreshed.")
			}
		} else {
			// A quality-only correction uses the observed Market stock; no PATCH.
			if !a.handoverCurrent(key, opts) {
				return a.stockEditorFailure(key, opts, "The selected character changed. No correction was saved.")
			}
			_, err = a.localStore.CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode), localprogress.ManualStockCorrection{ItemID: request.ItemID, MarketKinds: &amounts, ObservedMarketQuantity: target})
			if err != nil {
				return a.stockEditorFailure(key, opts, "Market stock is unchanged, but its local type labels could not be saved.")
			}
		}
	}
	snapshot, err = a.applyProgressStock(snapshot)
	if err != nil {
		return a.stockEditorFailure(key, opts, "Stock was corrected, but the local view could not be refreshed.")
	}
	return a.publishStockEditor(ctx, key, opts, snapshot, warning, "Stock corrected. Hand-in history is unchanged.", "")
}

func (a *ItemWindowApp) verifyStockEditorPending(key string, opts ItemOptions, snapshot localprogress.Snapshot, pending *stockEditorPending) (string, error) {
	if pending.Version == 2 && pending.Stage != "sent" {
		return "The saved Market edit has not been sent or its original baseline changed. No Market change was inferred.", nil
	}
	target, err := stockEditorTotal(pending.Amounts)
	if err != nil {
		return "The pending Market edit is invalid.", err
	}
	item, unique := stockEditorItem(snapshot, pending.ItemID)
	qualityOnly := false
	if pending.Version == 2 && pending.Baseline != nil {
		before, _ := stockEditorTotal(*pending.Baseline)
		qualityOnly = before == target
	}
	if snapshot.CharUID != opts.CharacterUID || !strings.EqualFold(snapshot.GameMode, opts.Mode) || snapshot.FIRInventorySeparate || !unique || !item.MarketTracked || snapshot.Revision == pending.Revision && !qualityOnly || item.MarketAvailableQuantity != target {
		return fmt.Sprintf("Market did not confirm the stock correction. Requested stock: %d. Check Market stock, then refresh or discard the pending edit.", target), nil
	}
	if !a.handoverCurrent(key, opts) {
		return "The selected character changed; the pending edit was kept.", context.Canceled
	}
	_, err = a.localStore.CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode), localprogress.ManualStockCorrection{ItemID: pending.ItemID, MarketKinds: &pending.Amounts, ObservedMarketQuantity: target})
	if err != nil {
		return "Market stock was verified, but its local type labels could not be saved. Refresh to finish.", err
	}
	if err = a.removeStockEditorPending(opts); err != nil {
		return "The confirmed pending edit could not be cleared. Refresh to finish.", err
	}
	return "", nil
}

func (a *ItemWindowApp) discardStockEditorLegacy() ItemState {
	if !a.progressNetworkMu.TryLock() {
		return a.GetState()
	}
	defer a.progressNetworkMu.Unlock()
	if !a.trackingMu.TryLock() {
		return a.GetState()
	}
	defer a.trackingMu.Unlock()
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	st := a.state.StockEditor
	if a.ctx == nil || a.ctx.Err() != nil || key == "" || opts.CharacterUID == "" || st == nil || !st.Open || !st.Pending || st.Saving || st.Loading {
		a.mu.Unlock()
		return a.GetState()
	}
	st.Loading = true
	a.mu.Unlock()
	a.emit()
	ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
	defer cancel()
	snapshot, warning, err := a.loadStockEditorSnapshot(ctx, key, opts)
	if err != nil {
		return a.stockEditorFailure(key, opts, "Current stock could not be read. The pending edit was kept.")
	}
	if err = a.removeStockEditorPending(opts); err != nil {
		return a.stockEditorFailure(key, opts, "The pending edit could not be discarded.")
	}
	return a.publishStockEditor(ctx, key, opts, snapshot, warning, "Pending edit discarded. Current Market stock was kept.", "")
}

// A helyi korrekció saját diskállapoton dolgozik; a Market abszolút intent
// előbb tartósan unsent fázisba kerül, a hálózat a soros worker dolga.
func (a *ItemWindowApp) SaveStockEditorItem(request ItemStockEditRequest) ItemState {
	amounts := localprogress.StockAmounts{Regular: request.Regular, FIR: request.FIR, Unknown: request.Unknown}
	target, quantityErr := stockEditorTotal(amounts)
	if !a.trackingMu.TryLock() {
		a.mu.Lock()
		if a.state.StockEditor != nil {
			a.state.StockEditor.Error = progressBusyMessage
		}
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	defer a.trackingMu.Unlock()
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	st := a.state.StockEditor
	if st == nil || !st.Open {
		a.mu.Unlock()
		return a.GetState()
	}
	if quantityErr != nil || !handoverItemID(request.ItemID) || request.Source != "local" && request.Source != "market" && request.Source != "market-fallback" {
		st.Error = "Enter whole, nonnegative quantities with a total no greater than 1000000000."
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	if message := a.progressUnsafeItemLocked(request.ItemID); message != "" {
		st.Error = message
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	if a.ctx == nil || a.ctx.Err() != nil || a.localBackupBusyLocked() || a.localStore == nil || opts.CharacterUID == "" || st.Saving || st.Loading || a.state.MarketFallbackInvalid || a.state.Busy || a.handoverBlocksItemLocked(request.ItemID) || st.Pending && (st.PendingItemID == "" || st.PendingItemID == request.ItemID) {
		st.Error = "Finish the pending operation for this item before editing its stock."
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	if request.Generation != st.Generation || a.stockEditorRevision != opts.revision || st.CharUID != opts.CharacterUID || st.GameMode != strings.ToUpper(opts.Mode) {
		st.Error = "This stock list is outdated. Refresh it before saving."
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	var selected ItemStockEditorRow
	found := false
	for _, row := range st.Rows {
		if row.ItemID == request.ItemID {
			selected = row
			found = true
			break
		}
	}
	if request.Source != "local" && a.marketFallbackBlocksItemLocked(request.ItemID) {
		st.Error = marketFallbackItemBlockedMessage
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	if !found || request.Source == "local" && !selected.LocalEditable || request.Source == "market" && !selected.MarketEditable || request.Source == "market-fallback" && !selected.MarketFallbackEditable {
		st.Error = "This stock source cannot be edited for the selected item."
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	st.Error = ""
	a.mu.Unlock()
	blocked, gateErr := a.marketFallbackBlocksItem(opts, request.ItemID)
	if gateErr != nil || request.Source != "local" && blocked {
		if gateErr != nil {
			return a.stockEditorFailure(key, opts, localProgressErrorText(gateErr))
		}
		return a.stockEditorFailure(key, opts, marketFallbackItemBlockedMessage)
	}
	snapshot, warning, err := a.localFirstSnapshot(key, opts)
	if err != nil {
		return a.stockEditorFailure(key, opts, "Refresh Items Tracker before correcting stock. "+localProgressErrorText(err))
	}
	current, unique := stockEditorItem(snapshot, request.ItemID)
	if !unique && request.Source == "local" {
		missing := true
		for _, row := range snapshot.Items {
			if row.ItemID == request.ItemID {
				missing = false
			}
		}
		if missing {
			own, e := a.localStore.Load(opts.CharacterUID, strings.ToUpper(opts.Mode))
			if e != nil {
				return a.stockEditorFailure(key, opts, "Local stock could not be read.")
			}
			current, unique = stockEditorItem(own, request.ItemID)
		}
	}
	if !unique {
		return a.stockEditorFailure(key, opts, "The tracked item is missing or ambiguous. Refresh the list.")
	}
	if !a.handoverCurrent(key, opts) {
		return a.GetState()
	}
	if request.Source == "local" {
		if stockEditorLocalAmounts(current) != selected.LocalStock || current.LocalUsedQuantity != selected.LocalUsed.Regular+selected.LocalUsed.FIR+selected.LocalUsed.Unknown {
			return a.publishStockEditor(a.ctx, key, opts, snapshot, warning, "", "Local stock changed. Review the refreshed quantities before saving.")
		}
		if target > localprogress.MaxQuantity-current.MarketAvailableQuantity-current.MarketFallbackAvailableQuantity {
			return a.stockEditorFailure(key, opts, "The combined stock exceeds the supported quantity limit.")
		}
		_, err = a.localStore.CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode), localprogress.ManualStockCorrection{ItemID: request.ItemID, LocalAvailable: &amounts})
	} else if request.Source == "market-fallback" {
		if fallbackAmounts(current) != selected.MarketFallbackStock || current.MarketFallbackUsedQuantity != selected.MarketFallbackUsed.Regular+selected.MarketFallbackUsed.FIR+selected.MarketFallbackUsed.Unknown {
			return a.publishStockEditor(a.ctx, key, opts, snapshot, warning, "", "Pending upload stock changed. Review the refreshed quantities before saving.")
		}
		if target > localprogress.MaxQuantity-current.MarketAvailableQuantity-current.LocalAvailableQuantity {
			return a.stockEditorFailure(key, opts, "The combined stock exceeds the supported quantity limit.")
		}
		_, err = a.marketFallbackStock().CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode), localprogress.ManualStockCorrection{ItemID: request.ItemID, LocalAvailable: &amounts})
	} else {
		baseline := localprogress.StockAmounts{Regular: current.MarketRegularQuantity, FIR: current.MarketFIRQuantity, Unknown: current.MarketAvailableQuantity - current.MarketRegularQuantity - current.MarketFIRQuantity}
		if !current.MarketTracked || snapshot.FIRInventorySeparate || baseline != selected.MarketStock {
			return a.publishStockEditor(a.ctx, key, opts, snapshot, warning, "", "Market stock changed. Review the refreshed quantities before saving.")
		}
		if target > localprogress.MaxQuantity-current.LocalAvailableQuantity-current.MarketFallbackAvailableQuantity {
			return a.stockEditorFailure(key, opts, "The combined stock exceeds the supported quantity limit.")
		}
		pending := &stockEditorPending{Version: 2, CharUID: opts.CharacterUID, Mode: strings.ToUpper(opts.Mode), ItemID: request.ItemID, Amounts: amounts, Revision: snapshot.Revision, Stage: "unsent", Baseline: &baseline}
		if err = a.saveStockEditorPending(pending); err != nil {
			return a.stockEditorFailure(key, opts, "The stock edit could not be prepared. No Market change was sent.")
		}
		a.mu.Lock()
		if a.localCurrentLocked(key, opts) {
			st = a.state.StockEditor
			st.Pending = true
			st.PendingItemID = request.ItemID
			st.Background = true
			st.Saving = false
			st.Status = "Stock correction saved. Market is being checked in the background."
		}
		a.mu.Unlock()
		a.publishLocalFirstProgress(key, opts, "")
		projected, _, e := a.localFirstSnapshot(key, opts)
		if e == nil {
			a.publishStockEditor(a.ctx, key, opts, projected, warning, "Stock correction saved. Market is being checked in the background.", "")
		}
		a.queueProgressWork()
		a.configureTrackedAddHotkey()
		a.emit()
		return a.GetState()
	}
	if err != nil {
		return a.stockEditorFailure(key, opts, "Local stock could not be corrected. The existing file was kept.")
	}
	a.publishLocalFirstProgress(key, opts, "Stock corrected. Hand-in history is unchanged.")
	snapshot, warning, err = a.localFirstSnapshot(key, opts)
	if err != nil {
		return a.stockEditorFailure(key, opts, "Stock was corrected, but its local view could not be refreshed.")
	}
	return a.publishStockEditor(a.ctx, key, opts, snapshot, warning, "Stock corrected. Hand-in history is unchanged.", "")
}

func (a *ItemWindowApp) processLocalFirstStockEdit(ctx context.Context, key string, opts ItemOptions) error {
	a.trackingMu.Lock()
	pending, err := a.readStockEditorPending(opts)
	if err != nil || pending == nil {
		a.trackingMu.Unlock()
		return err
	}
	// A tartós napló a jogosultság forrása. A sent és régi v1 szándék
	// csak GET-tel ellenőrizhető akkor is, ha a panel háttérjelzője hamis.
	if !a.handoverCurrent(key, opts) || ctx.Err() != nil {
		a.trackingMu.Unlock()
		return nil
	}
	if pending.Version == 1 {
		a.trackingMu.Unlock()
		return a.processLegacyStockEditReadback(ctx, key, opts, pending)
	}
	if pending.Stage == "conflict" {
		a.trackingMu.Unlock()
		return nil
	}
	a.trackingMu.Unlock()
	remote, err := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
	if err != nil {
		a.finishLocalFirstStockEditFailure(key, opts, "Market stock could not be read. The saved correction was kept.")
		return err
	}
	a.trackingMu.Lock()
	if !a.handoverCurrent(key, opts) || ctx.Err() != nil {
		a.trackingMu.Unlock()
		return context.Canceled
	}
	currentIntent, intentErr := a.readStockEditorPending(opts)
	if intentErr != nil || !sameStockEditorPending(currentIntent, pending) {
		a.trackingMu.Unlock()
		return context.Canceled
	}
	blocked, gateErr := a.marketFallbackBlocksItem(opts, pending.ItemID)
	if gateErr != nil || blocked {
		a.trackingMu.Unlock()
		if gateErr == nil {
			gateErr = errors.New(marketFallbackItemBlockedMessage)
		}
		a.finishLocalFirstStockEditFailure(key, opts, handoverError(gateErr))
		return gateErr
	}
	projected, e := a.applyProgressStockRaw(marketProgressOnly(remote))
	current, unique := stockEditorItem(projected, pending.ItemID)
	target, _ := stockEditorTotal(pending.Amounts)
	if e != nil || remote.CharUID != opts.CharacterUID || !strings.EqualFold(remote.GameMode, opts.Mode) || remote.FIRInventorySeparate || remote.Revision == "" || !unique {
		a.trackingMu.Unlock()
		if e == nil {
			e = market.ErrProgressConflict
		}
		a.finishLocalFirstStockEditFailure(key, opts, "Market stock could not be checked safely. The correction was kept.")
		return e
	}
	if pending.Stage == "unsent" {
		baseline := localprogress.StockAmounts{Regular: current.MarketRegularQuantity, FIR: current.MarketFIRQuantity, Unknown: current.MarketAvailableQuantity - current.MarketRegularQuantity - current.MarketFIRQuantity}
		if remote.Revision != pending.Revision || baseline != *pending.Baseline {
			if e = a.updateLocalFirstMarketSnapshot(key, opts, remote); e != nil {
				a.trackingMu.Unlock()
				return e
			}
			pending.Stage = "conflict"
			e = a.saveStockEditorPending(pending)
			a.trackingMu.Unlock()
			a.finishLocalFirstStockEditFailure(key, opts, "Market stock changed before the saved correction was sent. No Market change was sent. Refresh or discard the saved edit.")
			if e != nil {
				return e
			}
			return market.ErrProgressConflict
		}
		// A quality-only intent is also checked against live stock, but has no PATCH.
		if target == current.MarketAvailableQuantity {
			pending.Stage = "sent"
			e = a.saveStockEditorPending(pending)
			if e == nil {
				_, e = a.localStore.CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode), localprogress.ManualStockCorrection{ItemID: pending.ItemID, MarketKinds: &pending.Amounts, ObservedMarketQuantity: target})
			}
			if e == nil {
				e = a.removeStockEditorPending(opts)
			}
			if e == nil {
				e = a.updateLocalFirstMarketSnapshot(key, opts, remote)
			}
			a.trackingMu.Unlock()
			if e != nil {
				a.finishLocalFirstStockEditFailure(key, opts, "The saved type correction could not be finished.")
				return e
			}
			a.finishLocalFirstStockEdit(key, opts)
			return nil
		}
		pending.Stage = "sent"
		if e = a.saveStockEditorPending(pending); e != nil {
			a.trackingMu.Unlock()
			return e
		}
		a.trackingMu.Unlock()
		if !a.handoverCurrent(key, opts) || ctx.Err() != nil {
			a.trackingMu.Lock()
			stored, readErr := a.readStockEditorPending(opts)
			if readErr == nil && sameStockEditorPending(stored, pending) && stored.Stage == "sent" {
				stored.Stage = "unsent"
				if a.saveStockEditorPending(stored) == nil {
					pending.Stage = "unsent"
				}
			}
			a.trackingMu.Unlock()
			return context.Canceled
		}
		ack, writeErr := a.tracker.SetTrackedQuantity(ctx, key, opts.CharacterUID, pending.ItemID, pending.Revision, target)
		if progressWriteDefinitelyNotApplied(writeErr) {
			err := errors.Join(writeErr, a.restoreRejectedLocalFirstStockEdit(pending))
			a.finishLocalFirstStockEditFailure(key, opts, "Market did not accept the stock correction. The saved correction has been kept for a later retry. "+progressWriteDiagnostic(ack, writeErr, pending.Revision, "", nil))
			return err
		}
		observed, readErr := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
		if readErr != nil {
			a.finishLocalFirstStockEditFailure(key, opts, "The Market edit is awaiting verification. No automatic retry was sent. "+progressWriteDiagnostic(ack, writeErr, pending.Revision, observed.Revision, readErr))
			return readErr
		}
		remote = observed
		a.trackingMu.Lock()
		if !a.handoverCurrent(key, opts) || ctx.Err() != nil {
			a.trackingMu.Unlock()
			return context.Canceled
		}
	}
	message, e := a.verifyStockEditorPending(key, opts, marketProgressOnly(remote), pending)
	if e == nil && message == "" {
		e = a.updateLocalFirstMarketSnapshot(key, opts, remote)
	}
	a.trackingMu.Unlock()
	if e != nil || message != "" {
		if message == "" {
			message = "The saved Market correction could not be finished."
		}
		a.finishLocalFirstStockEditFailure(key, opts, message)
		if e == nil {
			e = market.ErrProgressWriteUncertain
		}
		return e
	}
	a.finishLocalFirstStockEdit(key, opts)
	return nil
}

func (a *ItemWindowApp) processLegacyStockEditReadback(ctx context.Context, key string, opts ItemOptions, pending *stockEditorPending) error {
	remote, err := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
	if err != nil {
		a.finishLocalFirstStockEditFailure(key, opts, "The saved Market edit could not be verified. No repeated stock request was sent.")
		return err
	}
	a.trackingMu.Lock()
	if !a.handoverCurrent(key, opts) || ctx.Err() != nil {
		a.trackingMu.Unlock()
		return context.Canceled
	}
	current, intentErr := a.readStockEditorPending(opts)
	if intentErr != nil || !sameStockEditorPending(current, pending) {
		a.trackingMu.Unlock()
		return context.Canceled
	}
	message, err := a.verifyStockEditorPending(key, opts, marketProgressOnly(remote), pending)
	if err == nil && message == "" {
		err = a.updateLocalFirstMarketSnapshot(key, opts, remote)
	}
	a.trackingMu.Unlock()
	if err != nil || message != "" {
		a.finishLocalFirstStockEditFailure(key, opts, message)
		if err == nil {
			err = market.ErrProgressWriteUncertain
		}
		return err
	}
	a.finishLocalFirstStockEdit(key, opts)
	return nil
}

// A Refresh a megerősített alapot azonnal újravetíti a saját diskadatokkal.
// Az új API-alap és a függő írás ellenőrzése a soros háttérworker dolga.
func (a *ItemWindowApp) RefreshStockEditor() ItemState {
	if !a.trackingMu.TryLock() {
		a.mu.Lock()
		if a.state.StockEditor != nil {
			a.state.StockEditor.Error = progressBusyMessage
		}
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	defer a.trackingMu.Unlock()
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	if a.state.StockEditor == nil || a.localBackupBusyLocked() {
		a.mu.Unlock()
		return a.GetState()
	}
	a.progressRefreshRequested = true
	a.mu.Unlock()
	pending, pendingErr := a.readStockEditorPending(opts)
	a.mu.Lock()
	if a.localCurrentLocked(key, opts) && a.state.StockEditor != nil {
		st := a.state.StockEditor
		st.Pending = pending != nil || pendingErr != nil
		st.PendingItemID = ""
		st.Background = false
		st.Saving = false
		if pending != nil {
			st.PendingItemID = pending.ItemID
			st.Background = pending.Version == 1 || pending.Stage != "conflict"
		}
	}
	a.mu.Unlock()
	if pendingErr != nil {
		a.mu.Lock()
		if a.localCurrentLocked(key, opts) && a.state.StockEditor != nil {
			a.state.StockEditor.Loading = false
			a.state.StockEditor.Status = ""
			a.state.StockEditor.Error = "The saved Market edit could not be read. The file was kept; stock changes are disabled."
		}
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	snapshot, warning, err := a.localFirstSnapshot(key, opts)
	if err != nil {
		a.mu.Lock()
		if a.localCurrentLocked(key, opts) && a.state.StockEditor != nil {
			a.state.StockEditor.Loading = true
			a.state.StockEditor.Status = "Loading confirmed requirements in the background…"
			a.state.StockEditor.Error = ""
		}
		a.mu.Unlock()
		a.queueProgressWork()
		a.emit()
		return a.GetState()
	}
	message := ""
	if pendingErr != nil {
		message = "The saved Market edit could not be read. The file was kept; stock changes are disabled."
	}
	state := a.publishStockEditor(ctxOrBackground(a.ctx), key, opts, snapshot, warning, "Showing saved stock. Market refresh continues in the background.", message)
	a.queueProgressWork()
	return state
}

func (a *ItemWindowApp) finishLocalFirstStockEditFailure(key string, opts ItemOptions, message string) {
	a.trackingMu.Lock()
	defer a.trackingMu.Unlock()
	pending, _ := a.readStockEditorPending(opts)
	a.mu.Lock()
	if a.localCurrentLocked(key, opts) && a.state.StockEditor != nil {
		a.state.StockEditor.Background = pending != nil && pending.Version == 2 && pending.Stage != "conflict"
		a.state.StockEditor.Saving = false
		a.state.StockEditor.Error = message
	}
	a.mu.Unlock()
	a.publishLocalFirstProgress(key, opts, "")
	a.emit()
}

func (a *ItemWindowApp) finishLocalFirstStockEdit(key string, opts ItemOptions) {
	a.trackingMu.Lock()
	defer a.trackingMu.Unlock()
	a.mu.Lock()
	if a.localCurrentLocked(key, opts) && a.state.StockEditor != nil {
		a.state.StockEditor.Background = false
		a.state.StockEditor.Pending = false
		a.state.StockEditor.PendingItemID = ""
		a.state.StockEditor.Status = "Stock corrected. Hand-in history is unchanged."
		a.state.StockEditor.Error = ""
	}
	a.mu.Unlock()
	a.publishLocalFirstProgress(key, opts, "")
	snapshot, warning, err := a.localFirstSnapshot(key, opts)
	if err == nil {
		a.publishStockEditor(ctxOrBackground(a.ctx), key, opts, snapshot, warning, "Stock corrected. Hand-in history is unchanged.", "")
	}
	a.configureTrackedAddHotkey()
	a.emit()
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

func (a *ItemWindowApp) DiscardStockEditorEdit() ItemState {
	if !a.progressNetworkMu.TryLock() {
		return a.GetState()
	}
	defer a.progressNetworkMu.Unlock()
	if !a.trackingMu.TryLock() {
		return a.GetState()
	}
	defer a.trackingMu.Unlock()
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	st := a.state.StockEditor
	if st == nil || !st.Open || !st.Pending || st.Saving || st.Loading || a.localBackupBusyLocked() {
		a.mu.Unlock()
		return a.GetState()
	}
	a.mu.Unlock()
	if !a.handoverCurrent(key, opts) {
		return a.GetState()
	}
	if err := a.removeStockEditorPending(opts); err != nil {
		return a.stockEditorFailure(key, opts, "The saved edit could not be discarded. The file was kept.")
	}
	a.mu.Lock()
	if a.localCurrentLocked(key, opts) && a.state.StockEditor != nil {
		a.state.StockEditor.Pending = false
		a.state.StockEditor.Background = false
		a.state.StockEditor.PendingItemID = ""
		a.state.StockEditor.Error = ""
		a.state.StockEditor.Status = "Saved edit discarded. No stock request was sent."
		a.progressRefreshRequested = true
	}
	a.mu.Unlock()
	a.publishLocalFirstProgress(key, opts, "")
	if snapshot, warning, err := a.localFirstSnapshot(key, opts); err == nil {
		a.publishStockEditor(ctxOrBackground(a.ctx), key, opts, snapshot, warning, "Saved edit discarded. Current confirmed stock was kept.", "")
	}
	a.queueProgressWork()
	a.configureTrackedAddHotkey()
	a.emit()
	return a.GetState()
}

// Csak a tartós szándék célja jelenik meg optimistán, saját készlet soha nem
// kerül ide. Sérült/ambiguous napló nem alakíthat át egy stock sort.
func (a *ItemWindowApp) projectPendingProgress(snapshot localprogress.Snapshot) (localprogress.Snapshot, error) {
	copy := localprogress.Clone(snapshot)
	opts := ItemOptions{CharacterUID: copy.CharUID, Mode: strings.ToLower(copy.GameMode)}
	apply := func(id string, target int64, regular int64, fir int64) error {
		indices := []int{}
		for i, row := range copy.Items {
			if row.ItemID == id {
				indices = append(indices, i)
			}
		}
		if len(indices) != 1 {
			return market.ErrProgressConflict
		}
		row := &copy.Items[indices[0]]
		if !row.MarketTracked || target < 0 || regular < 0 || fir < 0 || regular > target-fir || target > localprogress.MaxQuantity-row.LocalAvailableQuantity-row.MarketFallbackAvailableQuantity {
			return localprogress.ErrInvalidQuantity
		}
		row.MarketAvailableQuantity = target
		row.MarketRegularQuantity = regular
		row.MarketFIRQuantity = fir
		row.AvailableQuantity = target + row.LocalAvailableQuantity + row.MarketFallbackAvailableQuantity
		return nil
	}
	tx, err := readHandover(a.handoverPath(copy.CharUID, copy.GameMode), copy.CharUID, copy.GameMode)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return snapshot, err
	}
	if tx != nil && tx.Version == 2 {
		for _, op := range tx.Items {
			if handoverQuantity(op.Market) > 0 && (op.Stage == "unsent" || op.Stage == "sent" || op.Stage == "confirmed") {
				if err := apply(op.ItemID, op.Target, op.MarketRemaining.Regular, op.MarketRemaining.FIR); err != nil {
					return snapshot, err
				}
			}
		}
	}
	pending, err := a.readStockEditorPending(opts)
	if err != nil {
		return snapshot, err
	}
	if pending != nil && pending.Version == 2 && (pending.Stage == "unsent" || pending.Stage == "sent") {
		total, _ := stockEditorTotal(pending.Amounts)
		if err := apply(pending.ItemID, total, pending.Amounts.Regular, pending.Amounts.FIR); err != nil {
			return snapshot, err
		}
	}
	return copy, nil
}
