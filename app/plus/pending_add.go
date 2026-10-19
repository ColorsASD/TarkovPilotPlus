package plus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

const maxPendingMarketAdds = 1000

type pendingMarketAdd struct {
	ID             uint64
	Key            string
	Opts           ItemOptions
	Item           market.Item
	Kind           string
	Stage          string
	BeforeRevision string
	Before         int64
	Target         int64
	ACK            market.ProgressWriteResult
	WriteError     error
	Progress       localprogress.Snapshot
	Error          string
}

type PendingAddState struct {
	Count   int    `json:"count"`
	Running bool   `json:"running"`
	Status  string `json:"status"`
	Error   string `json:"error"`
}

func clonePendingAddState(state *PendingAddState) *PendingAddState {
	if state == nil {
		return nil
	}
	copy := *state
	return &copy
}

func (a *ItemWindowApp) pendingAddCountLocked() int { return len(a.pendingAddQueue) }

func (a *ItemWindowApp) GetPendingAddCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pendingAddCountLocked()
}

func (a *ItemWindowApp) pendingAddAmountsLocked(opts ItemOptions, itemID string) (regular, fir int64) {
	for _, entry := range a.pendingAddQueue {
		if entry.Key != a.apiKey || entry.Opts.CharacterUID != opts.CharacterUID || !strings.EqualFold(entry.Opts.Mode, opts.Mode) || entry.Item.BSGID != itemID {
			continue
		}
		if entry.Kind == "fir" {
			fir++
		} else {
			regular++
		}
	}
	return
}

func (a *ItemWindowApp) updatePendingAddStateLocked() {
	status := "All added items are confirmed on Tarkov Market."
	if len(a.pendingAddQueue) > 0 {
		status = "Waiting for Tarkov Market confirmation."
		if a.pendingAddRunning {
			status = "Saving added items to Tarkov Market…"
		}
	}
	a.state.PendingAdds = &PendingAddState{Count: len(a.pendingAddQueue), Running: a.pendingAddRunning, Status: status, Error: a.pendingAddError}
}

func (a *ItemWindowApp) startPendingAddWorkerLocked() {
	if a.pendingAddRunning || len(a.pendingAddQueue) == 0 || a.ctx == nil || a.ctx.Err() != nil || a.tracker == nil {
		return
	}
	a.pendingAddRunning = true
	a.updatePendingAddStateLocked()
	go a.runPendingMarketAdds()
}

// A foglalás kizárólag memóriát módosít. A gyűjtési korlát már a korábban
// befogadott, még nem visszaigazolt darabokkal együtt számol.
func (a *ItemWindowApp) reservePendingMarketAdd(uid string, target *trackedAddHotkeyTarget, kind string) ItemState {
	a.mu.Lock()
	opts := a.state.ItemOptions
	if !a.canAddTrackedItemLocked() || a.state.Item.UID != uid || target != nil &&
		(!a.trackedAddHotkeyTargetCurrent(target) || !a.state.AddHotkeyReady || !trackedAddHotkeyEnabled(opts) ||
			target.UID != uid || target.ItemID != a.state.Item.BSGID || target.CharUID != opts.CharacterUID || target.Revision != opts.revision) {
		a.mu.Unlock()
		return a.GetState()
	}
	if len(a.pendingAddQueue) >= maxPendingMarketAdds {
		a.pendingAddError = "Too many items are waiting for Tarkov Market. Wait for the pending additions to finish."
		a.updatePendingAddStateLocked()
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	if !a.allowTrackedItemKindLocked(a.state.Item.BSGID, kind) {
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	a.pendingAddNextID++
	a.pendingAddQueue = append(a.pendingAddQueue, pendingMarketAdd{ID: a.pendingAddNextID, Key: a.apiKey, Opts: opts, Item: *a.state.Item, Kind: kind, Stage: "queued"})
	a.pendingAddError = ""
	a.rebuildPendingViewLocked()
	a.state.Tracking.Status = "Added 1 item. Waiting for Tarkov Market confirmation."
	a.startPendingAddWorkerLocked()
	a.updatePendingAddStateLocked()
	a.mu.Unlock()
	a.configureTrackedAddHotkey()
	a.emit()
	return a.GetState()
}

// Újraellenőrzéskor az elküldött művelet csak GET-et kaphat. A memória
// tartalma kilépéskor nem kerül lemezre és nem állítható vissza új futásban.
func (a *ItemWindowApp) RecheckPendingAdds() ItemState {
	a.mu.Lock()
	a.startPendingAddWorkerLocked()
	a.updatePendingAddStateLocked()
	a.mu.Unlock()
	a.emit()
	return a.GetState()
}

func (a *ItemWindowApp) pendingAddIndexLocked(id uint64) int {
	for i, entry := range a.pendingAddQueue {
		if entry.ID == id {
			return i
		}
	}
	return -1
}

func (a *ItemWindowApp) pendingAddProfileLocked(entry pendingMarketAdd) bool {
	return entry.Key == a.apiKey && entry.Opts.CharacterUID == a.state.CharacterUID && strings.EqualFold(entry.Opts.Mode, a.state.Mode)
}

func (a *ItemWindowApp) removePendingAddLocked(id uint64) bool {
	i := a.pendingAddIndexLocked(id)
	if i < 0 {
		return false
	}
	copy(a.pendingAddQueue[i:], a.pendingAddQueue[i+1:])
	a.pendingAddQueue[len(a.pendingAddQueue)-1] = pendingMarketAdd{}
	a.pendingAddQueue = a.pendingAddQueue[:len(a.pendingAddQueue)-1]
	return true
}

func (a *ItemWindowApp) recordPendingAddKindLocked(entry pendingMarketAdd) {
	if entry.Key != a.apiKey {
		return
	}
	if a.marketKinds == nil {
		a.marketKinds = make(map[string]marketKindState)
	}
	id := marketKindKey(entry.Opts.CharacterUID, entry.Opts.Mode, entry.Item.BSGID)
	known := a.marketKinds[id]
	if known.stock != entry.Before {
		known = marketKindState{}
	}
	known.stock = entry.Target
	if entry.Kind == "fir" {
		known.fir++
	} else {
		known.regular++
	}
	a.marketKinds[id] = known
}

// Ezt a Market-frissítés a saját állapotzárja alatt, az új alap felépítése
// előtt hívja. A visszaigazolt foglalás így már nem kerül az új készlet fölé.
func (a *ItemWindowApp) reconcilePendingSnapshotLocked(remote market.TrackedSnapshot) {
	for i := 0; i < len(a.pendingAddQueue); {
		entry := a.pendingAddQueue[i]
		if entry.Stage != "sent" || entry.Key != a.apiKey || remote.CharUID != entry.Opts.CharacterUID || !strings.EqualFold(remote.GameMode, entry.Opts.Mode) || remote.Revision == "" || remote.Revision == entry.BeforeRevision {
			i++
			continue
		}
		view := trackedView(entry.Item, remote)
		if !view.Tracked || view.Error != "" || view.InventorySeparate || len(view.Variants) > 0 || view.AvailableQuantity != entry.Target {
			i++
			continue
		}
		if entry.ACK.OK && entry.ACK.Changed && entry.ACK.Revision == remote.Revision {
			a.recordPendingAddKindLocked(entry)
		}
		a.removePendingAddLocked(entry.ID)
		if entry.Error != "" && a.pendingAddError == entry.Error {
			a.pendingAddError = ""
		}
	}
	a.updatePendingAddStateLocked()
}

func (a *ItemWindowApp) publishPendingRemoteLocked(entry pendingMarketAdd, remote market.TrackedSnapshot, status string) {
	if !a.pendingAddProfileLocked(entry) || entry.Progress.Revision != remote.Revision || entry.Progress.CharUID != remote.CharUID || entry.Progress.GameMode != remote.GameMode {
		return
	}
	a.trackedSnapshot = cloneTrackedSnapshot(remote)
	a.trackedAt, a.trackedRevision = time.Now(), a.state.ItemOptions.revision
	item := entry.Item
	if a.state.Item != nil {
		item = *a.state.Item
	}
	a.applyMergedMarketSnapshotLocked(item, remote, entry.Progress, status)
}

func (a *ItemWindowApp) finishPendingAdd(entry pendingMarketAdd, remote market.TrackedSnapshot, message string, failure error, ownConfirmed bool) {
	a.mu.Lock()
	if a.removePendingAddLocked(entry.ID) {
		if ownConfirmed {
			a.recordPendingAddKindLocked(entry)
		}
		if failure != nil {
			a.pendingAddError = message
		} else if entry.Error != "" && a.pendingAddError == entry.Error {
			a.pendingAddError = ""
		}
		if remote.Items != nil && remote.CharUID == entry.Opts.CharacterUID && strings.EqualFold(remote.GameMode, entry.Opts.Mode) && remote.Revision != "" && entry.Progress.Revision == remote.Revision {
			a.publishPendingRemoteLocked(entry, remote, "")
		} else {
			a.rebuildPendingViewLocked()
		}
		if a.pendingAddProfileLocked(entry) && a.state.Item != nil && a.state.Item.UID == entry.Item.UID && a.state.Tracking != nil {
			a.state.Tracking.Status = message
		}
		a.updatePendingAddStateLocked()
	}
	a.mu.Unlock()
	a.configureTrackedAddHotkey()
	a.emit()
}

func (a *ItemWindowApp) blockPendingAdd(entry pendingMarketAdd, message string) {
	a.mu.Lock()
	if i := a.pendingAddIndexLocked(entry.ID); i >= 0 {
		a.pendingAddQueue[i].Error = message
		a.pendingAddError = message
		if a.pendingAddProfileLocked(entry) && a.state.Item != nil && a.state.Item.UID == entry.Item.UID && a.state.Tracking != nil {
			a.state.Tracking.Status = message
		}
		a.updatePendingAddStateLocked()
	}
	a.mu.Unlock()
	a.emit()
}

func (a *ItemWindowApp) runPendingMarketAdds() {
	for {
		a.mu.Lock()
		if len(a.pendingAddQueue) == 0 || a.ctx == nil || a.ctx.Err() != nil {
			a.pendingAddRunning = false
			a.updatePendingAddStateLocked()
			a.mu.Unlock()
			a.emit()
			return
		}
		entry := a.pendingAddQueue[0]
		parent := a.ctx
		a.mu.Unlock()
		progressed := a.processPendingMarketAdd(parent, entry)
		if !progressed {
			a.mu.Lock()
			a.pendingAddRunning = false
			a.updatePendingAddStateLocked()
			a.mu.Unlock()
			a.emit()
			return
		}
	}
}

func (a *ItemWindowApp) processPendingMarketAdd(parent context.Context, entry pendingMarketAdd) bool {
	a.marketNetworkMu.Lock()
	defer a.marketNetworkMu.Unlock()
	a.mu.Lock()
	i := a.pendingAddIndexLocked(entry.ID)
	if i < 0 {
		a.mu.Unlock()
		return true
	}
	entry = a.pendingAddQueue[i]
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(parent, 40*time.Second)
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		remote, readErr := a.tracker.LoadTracked(ctx, entry.Key, entry.Opts.CharacterUID, entry.Opts.Language)
		if readErr != nil {
			a.blockPendingAdd(entry, "Tarkov Market could not be reached. The addition remains pending in memory; refresh progress to retry verification.")
			return false
		}
		if remote.CharUID != entry.Opts.CharacterUID || !strings.EqualFold(remote.GameMode, entry.Opts.Mode) || remote.Revision == "" || remote.Items == nil {
			a.blockPendingAdd(entry, "Tarkov Market returned an incompatible progress snapshot. The addition is still pending.")
			return false
		}
		if entry.Stage == "sent" {
			return a.verifyPendingMarketAdd(ctx, entry, remote)
		}
		snapshot, mergeErr := a.loadPendingProgress(ctx, entry, remote)
		if mergeErr != nil {
			a.blockPendingAdd(entry, "The current Market quest and hideout requirements could not be verified. The addition remains pending; refresh progress to check it.")
			return false
		}
		entry.Progress = snapshot
		a.mu.Lock()
		snapshot = a.annotateMarketKindsLocked(snapshot)
		a.publishPendingRemoteLocked(entry, remote, "")
		a.mu.Unlock()
		regular, fir, gateErr := localprogress.AddOptions(snapshot, entry.Item.BSGID)
		row, unique := stockItem(snapshot, entry.Item.BSGID)
		if gateErr != nil || !unique || snapshot.FIRInventorySeparate || !(entry.Kind == "fir" && fir || entry.Kind == "regular" && regular) || row.MarketAvailableQuantity >= localprogress.MaxQuantity {
			a.finishPendingAdd(entry, remote, "The current Market requirements cannot use this item. Its pending addition was canceled.", market.ErrProgressConflict, false)
			return true
		}
		entry.BeforeRevision, entry.Before, entry.Target = remote.Revision, row.MarketAvailableQuantity, row.MarketAvailableQuantity+1
		entry.Stage, entry.Error = "sent", ""
		a.mu.Lock()
		i = a.pendingAddIndexLocked(entry.ID)
		if i < 0 {
			a.mu.Unlock()
			return true
		}
		a.pendingAddQueue[i] = entry
		a.updatePendingAddStateLocked()
		a.mu.Unlock()
		result, writeErr := a.tracker.SetTrackedQuantity(ctx, entry.Key, entry.Opts.CharacterUID, entry.Item.BSGID, remote.Revision, entry.Target)
		if errors.Is(writeErr, market.ErrProgressConflict) && !errors.Is(writeErr, market.ErrProgressWriteUncertain) && attempt == 0 {
			entry.Stage = "queued"
			a.mu.Lock()
			if i := a.pendingAddIndexLocked(entry.ID); i >= 0 {
				a.pendingAddQueue[i] = entry
			}
			a.mu.Unlock()
			continue
		}
		if writeErr != nil && !errors.Is(writeErr, market.ErrProgressWriteUncertain) {
			message := fmt.Sprintf("Market did not save the increase. Requested stock: %d; current stock: %d. %s %s", entry.Target, entry.Before, trackingError(writeErr), progressWriteDiagnostic(result, writeErr, remote.Revision, "", nil)) + progressItemMismatch(entry.Item, remote)
			a.finishPendingAdd(entry, remote, message, writeErr, false)
			return true
		}
		entry.ACK = result
		entry.WriteError = writeErr
		a.mu.Lock()
		if i := a.pendingAddIndexLocked(entry.ID); i >= 0 {
			a.pendingAddQueue[i] = entry
		}
		a.mu.Unlock()
		observed, readErr := a.tracker.LoadTracked(ctx, entry.Key, entry.Opts.CharacterUID, entry.Opts.Language)
		if readErr != nil {
			a.blockPendingAdd(entry, "The Market add is awaiting verification. No automatic repeat was sent; refresh progress to check it. "+progressWriteDiagnostic(result, writeErr, remote.Revision, observed.Revision, readErr))
			return false
		}
		return a.verifyPendingMarketAdd(ctx, entry, observed)
	}
	return false
}

func (a *ItemWindowApp) loadPendingProgress(ctx context.Context, entry pendingMarketAdd, remote market.TrackedSnapshot) (localprogress.Snapshot, error) {
	quests := market.QuestSnapshot{CharUID: remote.CharUID, GameMode: remote.GameMode, Revision: remote.Revision, Quests: []market.Quest{}}
	hideout := market.HideoutSnapshot{CharUID: remote.CharUID, GameMode: remote.GameMode, Revision: remote.Revision, Modules: []market.HideoutUpgrade{}}
	if progress, ok := a.tracker.(characterProgressAPI); ok {
		var err error
		quests, err = progress.LoadQuests(ctx, entry.Key, entry.Opts.CharacterUID, entry.Opts.Language)
		if err != nil {
			return localprogress.Snapshot{}, err
		}
		hideout, err = progress.LoadHideout(ctx, entry.Key, entry.Opts.CharacterUID, entry.Opts.Language)
		if err != nil {
			return localprogress.Snapshot{}, err
		}
	}
	return localprogress.Merge(remote, quests, hideout)
}

func (a *ItemWindowApp) verifyPendingMarketAdd(ctx context.Context, entry pendingMarketAdd, remote market.TrackedSnapshot) bool {
	progress, err := a.loadPendingProgress(ctx, entry, remote)
	if err != nil {
		a.blockPendingAdd(entry, "The Market stock change is awaiting matching quest and hideout progress. No repeated stock request was sent; refresh progress to check it.")
		return false
	}
	entry.Progress = progress
	view := trackedView(entry.Item, remote)
	valid := remote.CharUID == entry.Opts.CharacterUID && strings.EqualFold(remote.GameMode, entry.Opts.Mode) && remote.Items != nil && remote.Revision != "" &&
		view.Tracked && view.Error == "" && !view.InventorySeparate && len(view.Variants) == 0
	if entry.ACK.OK && !entry.ACK.Changed && valid && remote.Revision == entry.BeforeRevision {
		a.finishPendingAdd(entry, remote, "Tarkov Market reported no change. The pending addition was canceled.", market.ErrProgressWriteUncertain, false)
		return true
	}
	if valid && remote.Revision != entry.BeforeRevision && view.AvailableQuantity == entry.Target {
		own := entry.ACK.OK && entry.ACK.Changed && entry.ACK.Revision == remote.Revision
		message := "Tarkov Market stock verified."
		if own {
			message = "Added 1 item to Tracked Items stock."
			if !entry.ACK.NotificationDelivered {
				message += " Web notification was not confirmed."
			}
		}
		a.finishPendingAdd(entry, remote, message, nil, own)
		return true
	}
	message := fmt.Sprintf("The Market add is awaiting verification for %s. Requested stock: %d; current stock: unavailable. No repeated stock request was sent; refresh progress to check it.", diagnosticItemName(entry.Item.Name), entry.Target)
	if valid {
		message = fmt.Sprintf("Market did not confirm the increase. Requested stock: %d; current stock: %d. No repeated stock request was sent; refresh progress to check it.", entry.Target, view.AvailableQuantity)
	}
	a.blockPendingAdd(entry, message+" "+progressWriteDiagnostic(entry.ACK, entry.WriteError, entry.BeforeRevision, remote.Revision, nil)+progressItemMismatch(entry.Item, remote))
	return false
}
