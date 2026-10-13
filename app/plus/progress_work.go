package plus

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

type ItemProgressSyncState struct {
	Running      bool                        `json:"running"`
	Pending      bool                        `json:"pending"`
	PendingCount int                         `json:"pendingCount"`
	PendingItems int64                       `json:"pendingItems"`
	Status       string                      `json:"status"`
	Error        string                      `json:"error"`
	Operations   []ItemProgressSyncOperation `json:"operations"`
}

type ItemProgressSyncOperation struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	ItemID   string `json:"itemId"`
	ItemName string `json:"itemName"`
	Quantity int64  `json:"quantity"`
	Status   string `json:"status"`
	Error    string `json:"error"`
}

func (a *ItemWindowApp) queueProgressWork() {
	if a.progressWorkRequests == nil || a.ctx == nil || a.ctx.Err() != nil {
		return
	}
	select {
	case a.progressWorkRequests <- struct{}{}:
	default:
	}
}

// A darabszám a tartós helyi szándékokból készül. A kilépés vagy egy új
// frissítési kérés nem üríti a sort; a következő indítás ugyanazokat ellenőrzi.
func (a *ItemWindowApp) refreshProgressSyncState(opts ItemOptions, status, errorText string, running *bool) {
	quantity := int64(0)
	operations := []ItemProgressSyncOperation{}
	addRows := map[string]int{}
	invalid := false
	a.mu.Lock()
	metadata := make(map[string]string)
	knownRows := make(map[string][]market.TrackedItem)
	for _, row := range a.progressBaseSnapshot.Items {
		metadata[row.ItemID] = row.Name
	}
	if a.state.Item != nil {
		metadata[a.state.Item.BSGID] = a.state.Item.Name
	}
	if a.state.StockEditor != nil {
		for _, row := range a.state.StockEditor.Rows {
			metadata[row.ItemID] = row.Name
		}
	}
	knownRemote := a.trackedRevision == opts.revision && a.trackedSnapshot.CharUID == opts.CharacterUID
	if knownRemote {
		for _, row := range a.trackedSnapshot.Items {
			knownRows[row.ItemID] = append(knownRows[row.ItemID], row)
		}
	}
	unsafeItems := make(map[string]string, len(a.progressUnsafeItems))
	for id, reason := range a.progressUnsafeItems {
		unsafeItems[id] = reason
	}
	attempts := make(map[string]time.Time, len(a.fallbackSyncAt))
	for id, at := range a.fallbackSyncAt {
		attempts[id] = at
	}
	a.mu.Unlock()
	if opts.CharacterUID != "" && a.dir != "" {
		stock, err := a.loadMarketFallback(opts)
		if err != nil {
			invalid = true
		} else {
			for _, row := range stock.Items {
				if row.LocalAvailableQuantity > 0 {
					quantity += row.LocalAvailableQuantity
					op := ItemProgressSyncOperation{ID: "add:" + row.ItemID, Kind: "add", ItemID: row.ItemID, Quantity: row.LocalAvailableQuantity, Status: "Waiting for Market upload."}
					if reason := unsafeItems[row.ItemID]; reason != "" {
						op.Error = reason
					} else if knownRemote && len(knownRows[row.ItemID]) != 1 {
						op.Error = "The current Market list has no unique stock entry for this item. Stock is kept locally."
					} else if knownRemote && knownRows[row.ItemID][0].RequiredQuantity == 0 {
						active := false
						for _, source := range knownRows[row.ItemID][0].Sources {
							active = active || !source.Completed && !source.Canceled && source.RemainingRequiredQuantity > 0
						}
						if !active {
							op.Error = "No active Market requirement remains for this item. Stock is kept locally."
						}
					}
					if op.Error == "" {
						if at := attempts[opts.CharacterUID+"\x00"+strings.ToUpper(opts.Mode)+"\x00"+row.ItemID]; !at.IsZero() && time.Since(at) < time.Minute {
							op.Status = "The last upload was not confirmed. Stock is kept locally; another item can upload first."
						}
					}
					addRows[row.ItemID] = len(operations)
					operations = append(operations, op)
				}
			}
		}
		pending, err := a.readMarketFallbackSyncIntents(opts)
		if err != nil {
			invalid = true
		} else {
			for _, intent := range pending {
				if index, found := addRows[intent.ItemID]; found {
					operations[index].Status = "Awaiting Market verification. No automatic repeat is sent."
				} else {
					operations = append(operations, ItemProgressSyncOperation{ID: "add:" + intent.ItemID, Kind: "add", ItemID: intent.ItemID, Status: "Awaiting Market verification. No automatic repeat is sent."})
				}
			}
		}
		edit, err := a.readStockEditorPending(opts)
		if err != nil {
			invalid = true
		} else if edit != nil {
			q, _ := stockEditorTotal(edit.Amounts)
			if edit.Baseline != nil {
				before, _ := stockEditorTotal(*edit.Baseline)
				q -= before
				if q < 0 {
					q = -q
				}
			}
			quantity += q
			op := ItemProgressSyncOperation{ID: "stock-edit:" + edit.ItemID, Kind: "stock-edit", ItemID: edit.ItemID, Quantity: q, Status: "Waiting for Market stock correction."}
			if edit.Stage == "sent" || edit.Version == 1 {
				op.Status = "Awaiting Market stock correction verification."
			} else if edit.Stage == "conflict" {
				op.Error = "Market stock changed. Review or discard the saved correction."
			}
			operations = append(operations, op)
		}
		tx, err := readHandover(a.handoverPath(opts.CharacterUID, opts.Mode), opts.CharacterUID, opts.Mode)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			invalid = true
		} else if tx != nil {
			for _, op := range tx.Items {
				if op.Stage != "done" {
					q := handoverQuantity(op.Market)
					quantity += q
					entry := ItemProgressSyncOperation{ID: "handover:" + op.ItemID, Kind: "handover", ItemID: op.ItemID, Quantity: q, Status: "Waiting for Market hand-in."}
					if op.Stage == "sent" || tx.Version == 1 {
						entry.Status = "Awaiting Market hand-in verification."
					} else if op.Stage == "conflict" {
						entry.Error = "Market stock changed. Resume the saved hand-in to review it."
					}
					operations = append(operations, entry)
				}
			}
		}
	}
	// A tárgynevek a már letöltött katalógusból is feloldhatók; ez soha
	// nem indít hálózati kérést és nem tartja az alkalmazás állapotzárját.
	needNames := false
	for _, op := range operations {
		needNames = needNames || metadata[op.ItemID] == "" || metadata[op.ItemID] == op.ItemID
	}
	if needNames && a.client != nil {
		if catalog, ok := a.client.CachedCatalog(opts.Mode, opts.Language); ok {
			for _, item := range catalog.Items {
				if metadata[item.BSGID] == "" || metadata[item.BSGID] == item.BSGID {
					metadata[item.BSGID] = item.Name
				}
			}
		}
	}
	for i := range operations {
		operations[i].ItemName = metadata[operations[i].ItemID]
		if operations[i].ItemName == "" {
			operations[i].ItemName = operations[i].ItemID
		}
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].ID < operations[j].ID })
	a.mu.Lock()
	defer a.mu.Unlock()
	if opts.revision != a.state.ItemOptions.revision || opts.CharacterUID != a.state.CharacterUID || !strings.EqualFold(opts.Mode, a.state.Mode) {
		return
	}
	if a.state.ProgressSync == nil {
		a.state.ProgressSync = &ItemProgressSyncState{}
	}
	st := a.state.ProgressSync
	st.Operations = operations
	st.PendingCount, st.PendingItems, st.Pending = len(operations), quantity, len(operations) > 0 || invalid
	if running != nil {
		st.Running = *running
	}
	if status != "" {
		st.Status = status
	}
	if errorText != "" {
		st.Error = errorText
	} else if running != nil {
		st.Error = ""
	}
	if invalid {
		st.Error = "A saved progress operation could not be read. The files have been kept."
	}
	if !st.Pending && !st.Running {
		st.Status, st.Error = "All Market changes have been verified.", ""
	}
}

func (a *ItemWindowApp) runProgressWork() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.mu.Lock()
			a.progressRefreshRequested = true
			a.mu.Unlock()
		case <-a.progressWorkRequests:
			// A gyors egymás utáni hozzáadások egyetlen készletcsomagba férnek.
			select {
			case <-a.ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
		_ = a.processProgressWork(a.ctx)
	}
}

// Csak ez a worker birtokolja a hálózati sort. A helyi Add/Hand in/Edit nem
// vár erre a mutexre, és a refresh nem cancelálhat futó feltöltést.
func (a *ItemWindowApp) processProgressWork(parent context.Context) error {
	a.progressNetworkMu.Lock()
	defer a.progressNetworkMu.Unlock()
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	refresh := a.progressRefreshRequested || a.progressBaseRevision != opts.revision || a.progressBaseSnapshot.CharUID != opts.CharacterUID || a.progressBaseSnapshot.Revision == ""
	a.progressRefreshRequested = false
	if a.localBackupBusyLocked() || parent == nil || key == "" || a.tracker == nil || a.localStore == nil {
		a.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	a.progressWriteCancel = cancel
	a.mu.Unlock()
	defer func() { cancel(); a.mu.Lock(); a.progressWriteCancel = nil; a.mu.Unlock() }()
	active := true
	a.refreshProgressSyncState(opts, "Synchronizing Market progress in the background…", "", &active)
	a.emit()
	var workErr error
	if opts.CharacterUID != "" {
		// A debitek/abszolút szerkesztések megelőzik az új feltöltést.
		for _, process := range []func(context.Context, string, ItemOptions) error{a.processLocalFirstHandover, a.processLocalFirstStockEdit, a.processLocalFirstMarketFallback} {
			if ctx.Err() != nil {
				break
			}
			if err := process(ctx, key, opts); err != nil {
				workErr = errors.Join(workErr, err)
			}
		}
	}
	if refresh && ctx.Err() == nil {
		workErr = errors.Join(workErr, a.refreshLocalFirstSources(ctx, key, opts))
	}
	a.mu.Lock()
	currentOpts := a.state.ItemOptions
	sameKey := key == a.apiKey
	a.mu.Unlock()
	if sameKey && currentOpts.revision == opts.revision {
		active = false
		message := ""
		if workErr != nil && !errors.Is(workErr, context.Canceled) {
			message = "Market synchronization is waiting for verification or a connection. Saved local changes have been kept."
		}
		a.refreshProgressSyncState(currentOpts, "Local changes saved. Pending uploads will continue in the background.", message, &active)
		a.emit()
	}
	return workErr
}

func (a *ItemWindowApp) refreshLocalFirstSources(ctx context.Context, key string, opts ItemOptions) error {
	a.mu.Lock()
	characters := cloneCharacters(a.state.Characters)
	loadCharacters := a.charactersRevision != opts.revision || a.charactersAt.IsZero() || time.Since(a.charactersAt) >= time.Minute
	if loadCharacters {
		// A megerősített karakter gráfjának háttérfrissítése nem tilthatja
		// a hálózatmentes készletműveleteket. Csak az első betöltés várakozó.
		warm := a.progressBaseRevision == opts.revision && a.progressBaseSnapshot.CharUID == opts.CharacterUID &&
			strings.EqualFold(a.progressBaseSnapshot.GameMode, opts.Mode) && a.progressBaseSnapshot.Revision != ""
		a.state.CharactersLoading = !warm
	}
	a.mu.Unlock()
	if loadCharacters {
		loaded, err := a.tracker.ListCharacters(ctx, key)
		a.mu.Lock()
		if key != a.apiKey || opts.revision != a.state.ItemOptions.revision {
			a.mu.Unlock()
			return context.Canceled
		}
		a.state.CharactersLoading = false
		if err != nil {
			a.state.CharactersError = trackingError(err)
			a.mu.Unlock()
			return err
		}
		characters = loaded
		a.state.Characters, a.state.CharactersError = cloneCharacters(loaded), ""
		a.charactersAt, a.charactersRevision = time.Now(), opts.revision
		a.mu.Unlock()
	}
	compatible := []market.Character{}
	valid := false
	for _, ch := range characters {
		if strings.EqualFold(ch.GameMode, opts.Mode) {
			compatible = append(compatible, ch)
			valid = valid || ch.CharUID == opts.CharacterUID
		}
	}
	if !valid {
		opts.CharacterUID = ""
		if len(compatible) == 1 {
			opts.CharacterUID = compatible[0].CharUID
		}
	}
	a.mu.Lock()
	if key != a.apiKey || opts.revision != a.state.ItemOptions.revision {
		a.mu.Unlock()
		return context.Canceled
	}
	if a.state.CharacterUID != opts.CharacterUID {
		a.state.CharacterUID = opts.CharacterUID
		a.restoreHandoverLocked()
		a.restoreStockEditorLocked()
		a.progressBaseSnapshot = localprogress.Snapshot{}
		a.localSnapshot = localprogress.Snapshot{}
	}
	if opts.CharacterUID == "" {
		a.state.CharactersError = "Select a Tracked Items character in settings."
		a.mu.Unlock()
		a.emit()
		return market.ErrProgressNotFound
	}
	var item market.Item
	if a.state.Item != nil {
		item = *a.state.Item
	}
	warm := a.progressBaseRevision == opts.revision && a.progressBaseSnapshot.CharUID == opts.CharacterUID && a.progressBaseSnapshot.Revision != ""
	if !warm {
		a.state.Local = &LocalProgressState{Loading: true}
	}
	a.mu.Unlock()
	a.emit()
	if item.UID != "" && item.BSGID == "" {
		resolved, err := a.tracker.LookupItem(ctx, key, item.UID, opts.Mode, opts.Language)
		if err != nil {
			return err
		}
		a.mu.Lock()
		if a.localCurrentLocked(key, opts) && a.localItemCurrentLocked(item) {
			copy := *a.state.Item
			copy.BSGID = resolved.BSGID
			a.state.Item = &copy
		}
		a.mu.Unlock()
	}
	snapshot, warning, err := a.loadLocalSources(ctx, key, opts)
	if err != nil {
		a.mu.Lock()
		if a.localCurrentLocked(key, opts) {
			if warm {
				a.progressBaseWarning = "Showing the last confirmed requirements while Market refresh is unavailable."
			} else {
				a.state.Local = &LocalProgressState{Error: localProgressErrorText(err)}
			}
		}
		a.mu.Unlock()
		if warm {
			a.trackingMu.Lock()
			a.publishLocalFirstProgress(key, opts, "")
			a.trackingMu.Unlock()
		} else {
			a.emit()
		}
		return err
	}
	a.trackingMu.Lock()
	defer a.trackingMu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = a.rememberProgressSources(key, opts, snapshot, warning); err != nil {
		return err
	}
	a.publishLocalFirstProgress(key, opts, "")
	// Első betöltés után a már lemezen lévő szándékok is sorra kerülnek.
	a.refreshProgressSyncState(opts, "", "", nil)
	a.mu.Lock()
	pending := a.state.ProgressSync != nil && a.state.ProgressSync.Pending
	a.mu.Unlock()
	if pending {
		a.queueProgressWork()
	}
	return nil
}
