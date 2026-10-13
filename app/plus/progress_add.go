package plus

import (
	"strings"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
)

// A gomb és a gyorsbillentyű egyetlen tartós helyi mentést végez. A Market
// részhez tartozó darab a kiegészítő készletben vár, amíg a worker visszaigazolja.
func (a *ItemWindowApp) addCachedTrackedItemKind(uid string, target *trackedAddHotkeyTarget, kind string) ItemState {
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	_ = a.refreshMarketFallbackPendingLocked(opts)
	if !a.canAddTrackedItemLocked() || a.state.Item.UID != uid || target != nil &&
		(!a.trackedAddHotkeyTargetCurrent(target) || !a.state.AddHotkeyReady || !trackedAddHotkeyEnabled(opts) || target.UID != uid || target.ItemID != a.state.Item.BSGID || target.CharUID != opts.CharacterUID || target.Revision != opts.revision) {
		a.mu.Unlock()
		return a.GetState()
	}
	item := *a.state.Item
	a.mu.Unlock()
	// A saját készletet újraolvassuk, de az API-t nem. Így gyors egymás utáni
	// Add vagy egy háttérben befejezett átadás után sem régi számlálót növelünk.
	snapshot, warning, err := a.localFirstSnapshot(key, opts)
	a.mu.Lock()
	if !a.localCurrentLocked(key, opts) || !a.localItemCurrentLocked(item) {
		a.mu.Unlock()
		return a.GetState()
	}
	if err != nil {
		a.state.Tracking.Error = localProgressErrorText(localProgressFailure("save", err))
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	a.localSnapshot = snapshot
	a.state.Local = localLists(snapshot, a.localAt, warning)
	a.state.Tracking = localTrackedView(item, snapshot)
	if !a.canAddTrackedItemLocked() || !a.allowTrackedItemKindLocked(item.BSGID, kind) {
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	marketDestination := a.state.Tracking.Storage == "market"
	if marketDestination {
		allowed, guardErr := a.marketFallbackIntentAllowsAppend(opts, item.BSGID)
		if guardErr != nil || !allowed {
			a.state.Tracking.Status = marketFallbackItemBlockedMessage
			a.mu.Unlock()
			a.emit()
			return a.GetState()
		}
		row, unique := stockEditorItem(snapshot, item.BSGID)
		if !unique || !row.MarketTracked {
			a.mu.Unlock()
			return a.GetState()
		}
		amounts, readErr := marketFallbackSyncAmounts(a.marketFallbackStock(), opts, item.BSGID)
		if readErr != nil {
			a.state.Tracking.Error = localProgressErrorText(localProgressFailure("save", readErr))
			a.mu.Unlock()
			a.emit()
			return a.GetState()
		}
		if kind == "fir" {
			amounts.FIR++
		} else {
			amounts.Regular++
		}
		_, err = a.marketFallbackStock().CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode), localprogress.ManualStockCorrection{ItemID: item.BSGID, LocalAvailable: &amounts})
	} else {
		_, err = a.localStore.AddKind(opts.CharacterUID, strings.ToUpper(opts.Mode), item.BSGID, kind)
	}
	if err != nil {
		a.state.Tracking.Error = localProgressErrorText(localProgressFailure("save", err))
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	a.mu.Unlock()
	status := "Added 1 item to Tracked Items stock."
	if marketDestination {
		status = "Added 1 item locally. Tarkov Market upload is pending."
	}
	a.publishLocalFirstProgress(key, opts, status)
	if marketDestination {
		a.queueProgressWork()
	}
	return a.GetState()
}
