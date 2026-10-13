package plus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

// Ez a készlet kizárólag a Marketre még fel nem töltött saját darabokat őrzi.
// Az élő Market-készlet és a dev helyi készlete nem kerül ebbe a fájlba.
func (a *ItemWindowApp) marketFallbackStock() *localprogress.Store {
	a.fallbackStoreMu.Lock()
	defer a.fallbackStoreMu.Unlock()
	if a.fallbackStore == nil {
		a.fallbackStore = localprogress.NewStore(filepath.Join(a.dir, "market-fallback"))
	}
	return a.fallbackStore
}

func (a *ItemWindowApp) loadMarketFallback(opts ItemOptions) (localprogress.Snapshot, error) {
	stock, err := a.marketFallbackStock().Load(opts.CharacterUID, strings.ToUpper(opts.Mode))
	if errors.Is(err, os.ErrNotExist) {
		return localprogress.Snapshot{CharUID: opts.CharacterUID, GameMode: strings.ToUpper(opts.Mode), Revision: "local", Items: []localprogress.Item{}}, nil
	}
	return stock, err
}

func (a *ItemWindowApp) applyProgressStock(snapshot localprogress.Snapshot) (localprogress.Snapshot, error) {
	applied, err := a.applyProgressStockRaw(snapshot)
	if err != nil {
		return snapshot, err
	}
	return a.projectPendingProgress(applied)
}

func (a *ItemWindowApp) applyProgressStockRaw(snapshot localprogress.Snapshot) (localprogress.Snapshot, error) {
	base := localprogress.Clone(snapshot)
	for i := range base.Items {
		row := &base.Items[i]
		row.MarketFallbackAvailableQuantity, row.MarketFallbackRegularQuantity, row.MarketFallbackFIRQuantity = 0, 0, 0
		row.MarketFallbackUsedQuantity, row.MarketFallbackUsedRegularQuantity, row.MarketFallbackUsedFIRQuantity = 0, 0, 0
		row.AvailableQuantity = row.MarketAvailableQuantity + row.LocalAvailableQuantity
		row.RemainingQuantity = max(0, row.RequiredQuantity-row.AvailableQuantity)
	}
	applied, err := a.localStore.Apply(base)
	if err != nil {
		return snapshot, err
	}
	fallback, err := a.loadMarketFallback(ItemOptions{CharacterUID: snapshot.CharUID, Mode: snapshot.GameMode})
	if err != nil {
		return snapshot, err
	}
	opts := ItemOptions{CharacterUID: snapshot.CharUID, Mode: snapshot.GameMode}
	intents, err := a.readMarketFallbackSyncIntents(opts)
	if err != nil {
		return snapshot, err
	}
	for _, pending := range intents {
		if snapshot.Revision == pending.Revision {
			continue
		}
		remote, unique := stockEditorItem(applied, pending.ItemID)
		if unique && remote.MarketTracked && !snapshot.FIRInventorySeparate && remote.MarketAvailableQuantity == pending.Target {
			// A visszaigazolt feltöltés nem számolható kétszer akkor sem,
			// ha a helyi fájl lezárását még lemezhiba akadályozza.
			recorded := false
			if pending.Version == 2 {
				recorded, err = a.marketFallbackStock().TransferRecorded(snapshot.CharUID, snapshot.GameMode, pending.OperationID, pending.ItemID, pending.Fallback)
				if err != nil {
					return snapshot, err
				}
			}
			for i := range fallback.Items {
				row := &fallback.Items[i]
				if row.ItemID != pending.ItemID {
					continue
				}
				if pending.Version == 2 {
					if recorded {
						continue
					}
					q, e := stockEditorTotal(pending.Fallback)
					unknown := row.LocalAvailableQuantity - row.LocalRegularQuantity - row.LocalFIRQuantity
					if e != nil || row.LocalRegularQuantity < pending.Fallback.Regular || row.LocalFIRQuantity < pending.Fallback.FIR || unknown < pending.Fallback.Unknown {
						return snapshot, errMarketFallbackSyncInvalid
					}
					row.LocalAvailableQuantity -= q
					row.LocalRegularQuantity -= pending.Fallback.Regular
					row.LocalFIRQuantity -= pending.Fallback.FIR
					row.AvailableQuantity = row.LocalAvailableQuantity
					row.LocalCollectedQuantity = row.LocalUsedQuantity + row.LocalAvailableQuantity
					row.LocalCollectedRegularQuantity = row.LocalUsedRegularQuantity + row.LocalRegularQuantity
					row.LocalCollectedFIRQuantity = row.LocalUsedFIRQuantity + row.LocalFIRQuantity
					continue
				}
				row.LocalAvailableQuantity, row.LocalRegularQuantity, row.LocalFIRQuantity, row.AvailableQuantity = 0, 0, 0, 0
				row.LocalCollectedQuantity = row.LocalUsedQuantity
				row.LocalCollectedRegularQuantity, row.LocalCollectedFIRQuantity = row.LocalUsedRegularQuantity, row.LocalUsedFIRQuantity
			}
		}
	}
	return localprogress.OverlayMarketFallback(applied, fallback)
}

func fallbackAmounts(item localprogress.Item) localprogress.StockAmounts {
	return localprogress.StockAmounts{FIR: item.MarketFallbackFIRQuantity, Regular: item.MarketFallbackRegularQuantity,
		Unknown: item.MarketFallbackAvailableQuantity - item.MarketFallbackFIRQuantity - item.MarketFallbackRegularQuantity}
}

func safeMarketFallbackRejection(err error) bool {
	if err == nil || errors.Is(err, market.ErrProgressWriteUncertain) {
		return false
	}
	switch market.ProgressWriteFailure(err).HTTPStatus {
	case 400, 403, 404, 428, 429:
		return true
	}
	return false
}

func temporaryMarketReadFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var failure *localProgressLoadError
	if errors.As(err, &failure) {
		if failure.stage != "tracked items" && failure.stage != "quests" && failure.stage != "hideout" {
			return false
		}
	}
	return errors.Is(err, market.ErrProgressUnavailable) || errors.Is(err, market.ErrRateLimited) || errors.Is(err, context.DeadlineExceeded)
}

// trackingMu kötődik minden készletművelethez. A mentés előtt az aktuális
// karaktert, típust és a teljes kiosztás szerinti fennmaradó igényt ellenőrizzük.
func (a *ItemWindowApp) saveMarketFallbackAdd(key string, opts ItemOptions, item market.Item, kind string, status string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.localCurrentLocked(key, opts) || !a.localItemCurrentLocked(item) || a.stockEditorBlocksStockLocked() || a.marketFallbackBlocksItemLocked(item.BSGID) ||
		a.localRevision != opts.revision || a.localSnapshot.CharUID != opts.CharacterUID || !strings.EqualFold(a.localSnapshot.GameMode, opts.Mode) ||
		a.state.Local == nil || a.state.Local.StockError || !a.allowTrackedItemKindLocked(item.BSGID, kind) {
		return false
	}
	row, unique := stockEditorItem(a.localSnapshot, item.BSGID)
	if !unique || !row.MarketTracked || a.localSnapshot.FIRInventorySeparate || row.AvailableQuantity >= localprogress.MaxQuantity {
		return false
	}
	amounts := fallbackAmounts(row)
	if kind == "fir" {
		amounts.FIR++
	} else if kind == "regular" {
		amounts.Regular++
	} else {
		return false
	}
	_, err := a.marketFallbackStock().CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode), localprogress.ManualStockCorrection{ItemID: item.BSGID, LocalAvailable: &amounts})
	if err == nil {
		a.localSnapshot, err = a.applyProgressStock(a.localSnapshot)
	}
	if err != nil {
		a.state.Local.Error = localProgressErrorText(localProgressFailure("save", err))
		a.state.Local.StockError = true
		if a.state.Tracking != nil {
			a.state.Tracking.Adding = false
			a.state.Tracking.Error = a.state.Local.Error
		}
		return false
	}
	if a.fallbackSyncAt == nil {
		a.fallbackSyncAt = map[string]time.Time{}
	}
	a.fallbackSyncAt[opts.CharacterUID+"\x00"+strings.ToUpper(opts.Mode)+"\x00"+item.BSGID] = time.Now()
	warning := "Saved Market items are waiting for verified upload."
	if strings.Contains(a.state.Local.Warning, "last confirmed requirements") {
		warning += " Market is unavailable; requirements are the last confirmed list."
	}
	a.state.Local = localLists(a.localSnapshot, time.Now(), warning)
	a.state.Tracking = localTrackedView(item, a.localSnapshot)
	a.state.Tracking.Status = status
	return true
}

// Egy sikertelen próbálkozás után legfeljebb percenként keresünk helyreállást.
// A kézi Refresh ezt a várakozást kihagyja; a függő írás csak GET-tel oldható fel.
func (a *ItemWindowApp) recoverMarketFallback(ctx context.Context, key string, opts ItemOptions, selectedID string, force bool) (string, error) {
	stock, err := a.loadMarketFallback(opts)
	if err != nil {
		return "", err
	}
	pending, err := a.marketFallbackSyncPending(opts)
	if err != nil {
		a.setMarketFallbackPending(key, opts, true)
		return "", err
	}
	a.mu.Lock()
	competingPending := a.state.StockEditor != nil && a.state.StockEditor.Pending || a.state.Handover != nil && (a.state.Handover.Open || a.state.Handover.Pending || a.state.Handover.Busy)
	a.mu.Unlock()
	if competingPending && !pending {
		return "", nil
	}
	ids := []string{}
	for _, row := range stock.Items {
		if row.LocalAvailableQuantity > 0 && (selectedID == "" || row.ItemID == selectedID) {
			ids = append(ids, row.ItemID)
		}
	}
	if !pending && len(ids) == 0 {
		a.setMarketFallbackPending(key, opts, false)
		return "", nil
	}
	if pending {
		ids = []string{selectedID}
	}
	if !pending && len(ids) > 1 {
		a.mu.Lock()
		sort.SliceStable(ids, func(i, j int) bool {
			prefix := opts.CharacterUID + "\x00" + strings.ToUpper(opts.Mode) + "\x00"
			return a.fallbackSyncAt[prefix+ids[i]].Before(a.fallbackSyncAt[prefix+ids[j]])
		})
		a.mu.Unlock()
	}
	if !force && !pending {
		a.mu.Lock()
		last := a.fallbackSyncAt[opts.CharacterUID+"\x00"+strings.ToUpper(opts.Mode)+"\x00"+ids[0]]
		a.mu.Unlock()
		if time.Since(last) < time.Minute {
			return "", nil
		}
	}
	remote, err := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
	if err != nil {
		return "", err
	}
	// Háttérben egy tárgyat kezelünk, így egy hosszú lista nem terheli az API-t.
	if len(ids) > 1 {
		ids = ids[:1]
	}
	message := ""
	for _, id := range ids {
		if !a.handoverCurrent(key, opts) {
			return "", context.Canceled
		}
		a.mu.Lock()
		if a.fallbackSyncAt == nil {
			a.fallbackSyncAt = map[string]time.Time{}
		}
		a.fallbackSyncAt[opts.CharacterUID+"\x00"+strings.ToUpper(opts.Mode)+"\x00"+id] = time.Now()
		a.mu.Unlock()
		remote, message, err = a.syncMarketFallback(ctx, key, opts, remote, id)
		stillPending, readErr := a.marketFallbackSyncPending(opts)
		a.setMarketFallbackPending(key, opts, stillPending || readErr != nil)
		if err != nil {
			return message, err
		}
	}
	return message, nil
}

func (a *ItemWindowApp) setMarketFallbackPending(key string, opts ItemOptions, _ bool) {
	a.mu.Lock()
	if a.localCurrentLocked(key, opts) {
		_ = a.refreshMarketFallbackPendingLocked(opts)
		a.localAt = time.Time{}
	}
	a.mu.Unlock()
}

func (a *ItemWindowApp) runMarketFallbackRecovery() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if !a.trackingMu.TryLock() {
				continue
			}
			a.mu.Lock()
			key, opts := a.apiKey, a.state.ItemOptions
			blocked := key == "" || opts.CharacterUID == "" || a.state.Busy || a.stockEditorBusyLocked() ||
				a.state.StockEditor != nil && (a.state.StockEditor.Open || a.state.StockEditor.Pending) || a.state.Handover != nil && (a.state.Handover.Open || a.state.Handover.Pending)
			a.mu.Unlock()
			if !blocked {
				ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
				a.mu.Lock()
				a.trackingWriteCancel = cancel
				a.mu.Unlock()
				_, _ = a.recoverMarketFallback(ctx, key, opts, "", false)
				cancel()
				a.mu.Lock()
				a.trackingWriteCancel = nil
				a.mu.Unlock()
				a.requestTracking()
				a.emit()
			}
			a.trackingMu.Unlock()
		}
	}
}
