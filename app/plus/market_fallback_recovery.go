package plus

import (
	"context"
	"errors"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

// Csak már létező függő feltöltést egyeztetünk. A háttérfrissítés ezen az
// útvonalon nem hozhat létre új feltöltést és nem küld PATCH-et.
// A hívó trackingMu-t tart; a snapshotot a sync ellenőrzése frissen olvassa.
func (a *ItemWindowApp) checkPendingMarketFallback(ctx context.Context, key string, opts ItemOptions) error {
	pending, err := a.readMarketFallbackSync(opts)
	if err == nil && pending != nil {
		_, _, err = a.syncMarketFallback(ctx, key, opts, market.TrackedSnapshot{}, "")
	}
	a.setMarketFallbackPending(key, opts, pending != nil || err != nil)
	return err
}

// RetryMarketUpload kizárólag a felhasználó külön kérésére ismétli a mentett
// abszolút célt. A háttérfrissítés továbbra is csak ellenőrzi a függő írást.
func (a *ItemWindowApp) RetryMarketUpload() ItemState {
	// A hálózati sort a worker előtt és a helyi lock nélkül szerezzük meg.
	// Futó szinkron esetén csak ez a Retry tér vissza; a helyi műveletek élnek.
	if !a.progressNetworkMu.TryLock() {
		a.mu.Lock()
		if a.state.StockEditor != nil {
			a.state.StockEditor.Status = progressBusyMessage
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
	locked := true
	defer func() {
		if locked {
			a.trackingMu.Unlock()
		}
	}()
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	if a.localBackupBusyLocked() || a.state.Busy || a.stockEditorBusyLocked() ||
		a.state.StockEditor != nil && a.state.StockEditor.Pending ||
		a.state.Handover != nil && (a.state.Handover.Open || a.state.Handover.Pending || a.state.Handover.Busy) {
		a.mu.Unlock()
		return a.GetState()
	}
	if a.state.StockEditor == nil {
		a.stockEditorGeneration++
		a.state.StockEditor = &ItemStockEditorState{Generation: a.stockEditorGeneration, Rows: []ItemStockEditorRow{}}
	}
	if a.ctx == nil || a.ctx.Err() != nil || key == "" || opts.CharacterUID == "" || a.localStore == nil || a.tracker == nil {
		a.state.StockEditor.Error = "Enter your API key and select a character to retry the saved Market upload."
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	a.state.StockEditor.Status = "Checking the saved Market upload."
	a.state.StockEditor.Error = ""
	ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
	a.progressWriteCancel = cancel
	a.mu.Unlock()
	a.trackingMu.Unlock()
	locked = false
	active := true
	a.refreshProgressSyncState(opts, "Checking the saved Market upload.", "", &active)
	a.emit()
	defer func() {
		cancel()
		a.mu.Lock()
		a.progressWriteCancel = nil
		a.mu.Unlock()
		active := false
		a.refreshProgressSyncState(opts, "", "", &active)
		a.emit()
	}()
	remote, status, retryErr := a.retrySavedMarketFallback(ctx, key, opts)
	a.trackingMu.Lock()
	locked = true
	if ctx.Err() != nil || !a.handoverCurrent(key, opts) {
		return a.GetState()
	}
	if marketFallbackSyncSnapshot(remote, opts) {
		retryErr = errors.Join(retryErr, a.updateLocalFirstMarketSnapshot(key, opts, remote))
	}
	pending, pendingErr := a.marketFallbackSyncPending(opts)
	a.setMarketFallbackPending(key, opts, pending || pendingErr != nil)
	message := ""
	if retryErr != nil {
		message = localProgressErrorText(retryErr)
	} else if pendingErr != nil {
		message = localProgressErrorText(pendingErr)
	}
	snapshot, warning, err := a.localFirstSnapshot(key, opts)
	if err != nil {
		if message != "" {
			message += "\n"
		}
		return a.stockEditorFailure(key, opts, message+"Items Tracker stock could not be refreshed. "+localProgressErrorText(err))
	}
	if status == "" {
		status = "Saved Market upload checked."
	}
	return a.publishStockEditor(ctx, key, opts, snapshot, warning, status, message)
}
