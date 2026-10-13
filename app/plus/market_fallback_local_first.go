package plus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

// A hívó a progressNetworkMu zárolással sorosítja a Market-műveleteket.
// A trackingMu csak a befoglalás és a helyi lezárás alatt él: a GET/PATCH
// közben új helyi Add ugyanahhoz a v2 csomaghoz is érkezhet.
func (a *ItemWindowApp) processLocalFirstMarketFallback(ctx context.Context, key string, opts ItemOptions) error {
	if ctx == nil || key == "" || a.tracker == nil || a.localStore == nil {
		return errMarketFallbackSyncInvalid
	}
	marketFallbackSyncMu.Lock()
	defer marketFallbackSyncMu.Unlock()
	if err := a.marketFallbackLocalFirstContext(ctx, key, opts); err != nil {
		return err
	}
	intents, err := a.readMarketFallbackSyncIntents(opts)
	if err != nil {
		return err
	}
	if len(intents) == 0 {
		stock, err := a.loadMarketFallback(opts)
		if err != nil {
			return err
		}
		hasStock := false
		for _, item := range stock.Items {
			hasStock = hasStock || item.LocalAvailableQuantity > 0
		}
		if !hasStock {
			return nil
		}
	}
	remote, err := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
	if err != nil {
		if len(intents) != 0 {
			return errors.Join(errMarketFallbackSyncPending, err)
		}
		return err
	}
	if !marketFallbackSyncSnapshot(remote, opts) || remote.FIRInventorySeparate {
		return errMarketFallbackSyncInvalid
	}
	if err := a.marketFallbackLocalFirstContext(ctx, key, opts); err != nil {
		return err
	}
	excluded := make(map[string]bool)
	var pendingErr error
	status := ""
	for _, pending := range intents {
		excluded[pending.ItemID] = true
		settled, err := a.settleLocalFirstMarketFallback(ctx, key, opts, remote, pending)
		pendingErr = errors.Join(pendingErr, err)
		if settled != "" {
			status = settled
		}
	}
	if len(intents) != 0 {
		// A másik tárgy új CAS-írása a rendezések után új GET-et használ.
		// Az előző napló tárgyát ebben a futásban sohasem küldjük újra.
		stock, err := a.loadMarketFallback(opts)
		if err != nil {
			return errors.Join(pendingErr, err)
		}
		hasOther := false
		for _, item := range stock.Items {
			hasOther = hasOther || item.LocalAvailableQuantity > 0 && !excluded[item.ItemID]
		}
		if !hasOther {
			return errors.Join(pendingErr, a.publishLocalFirstMarketFallback(ctx, key, opts, remote, status))
		}
		remote, err = a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
		if err != nil || !marketFallbackSyncSnapshot(remote, opts) || remote.FIRInventorySeparate {
			return errors.Join(pendingErr, err, errMarketFallbackSyncPending)
		}
	}
	// A friss távoli állapot után a rövid lock alatt a legújabb teljes
	// fallback kerül a csomagba. A korábbi Load nem befoglalás.
	pending, err := a.prepareLocalFirstMarketFallback(ctx, key, opts, remote, excluded)
	if err != nil || pending == nil {
		return errors.Join(pendingErr, err, a.publishLocalFirstMarketFallback(ctx, key, opts, remote, status))
	}
	a.trackingMu.Lock()
	if a.marketFallbackLocalFirstContext(ctx, key, opts) == nil {
		a.publishLocalFirstProgress(key, opts, "Market supplementary stock saved locally. Uploading…")
	}
	a.trackingMu.Unlock()
	// Eddig még biztosan nem indult PATCH. Kontextusváltás esetén ez az új
	// szándék törölhető, és az összes helyi darab megmarad.
	if err := a.marketFallbackLocalFirstContext(ctx, key, opts); err != nil {
		a.trackingMu.Lock()
		err = a.cancelUnsentMarketFallbackSync(opts, err, pending.ItemID)
		a.trackingMu.Unlock()
		return err
	}
	a.markLocalFirstFallbackAttempt(opts, pending)
	defer func() { a.queueNextLocalFirstFallback(opts, remote) }()
	if err := a.marketFallbackLocalFirstContext(ctx, key, opts); err != nil {
		a.trackingMu.Lock()
		err = a.cancelUnsentMarketFallbackSync(opts, err, pending.ItemID)
		a.trackingMu.Unlock()
		a.mu.Lock()
		delete(a.fallbackSyncAt, localFirstFallbackAttemptKey(opts, pending.ItemID))
		delete(a.fallbackSyncAt, localFirstFallbackAmountKey(opts, pending.ItemID, pending.Fallback))
		a.mu.Unlock()
		return err
	}
	result, writeErr := a.tracker.SetTrackedQuantity(ctx, key, opts.CharacterUID, pending.ItemID, pending.Revision, pending.Target)
	if progressWriteDefinitelyNotApplied(writeErr) {
		return a.cancelLocalFirstRejectedUpload(ctx, key, opts, remote, pending,
			"Market did not accept the supplementary stock upload. Supplementary stock was kept locally.")
	}
	observed, readErr := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
	if readErr != nil {
		if marketFallbackSyncNoOp(result, writeErr, pending) {
			return a.cancelLocalFirstRejectedUpload(ctx, key, opts, remote, pending,
				"Market reported no change. Supplementary stock was kept locally.")
		}
		return errors.Join(errMarketFallbackSyncPending, writeErr, readErr)
	}
	if err := a.marketFallbackLocalFirstContext(ctx, key, opts); err != nil {
		return errors.Join(errMarketFallbackSyncPending, err)
	}
	status, settleErr := a.settleLocalFirstMarketFallback(ctx, key, opts, observed, pending)
	if settleErr != nil && marketFallbackSyncNoOp(result, writeErr, pending) {
		row, unique := marketFallbackSyncItem(observed, pending.ItemID)
		if marketFallbackSyncSnapshot(observed, opts) && !observed.FIRInventorySeparate && unique &&
			observed.Revision == pending.Revision && row.AvailableQuantity == pending.Before {
			return a.cancelLocalFirstRejectedUpload(ctx, key, opts, observed, pending,
				"Market reported no change. Supplementary stock was kept locally.")
		}
	}
	publishErr := a.publishLocalFirstMarketFallback(ctx, key, opts, observed, status)
	if settleErr != nil {
		return errors.Join(settleErr, writeErr, publishErr)
	}
	return errors.Join(pendingErr, publishErr)
}

func (a *ItemWindowApp) marketFallbackLocalFirstContext(ctx context.Context, key string, opts ItemOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !a.handoverCurrent(key, opts) {
		return context.Canceled
	}
	return nil
}

func (a *ItemWindowApp) prepareLocalFirstMarketFallback(ctx context.Context, key string, opts ItemOptions, remote market.TrackedSnapshot, exclusions ...map[string]bool) (*marketFallbackSyncIntent, error) {
	a.trackingMu.Lock()
	defer a.trackingMu.Unlock()
	if err := a.marketFallbackLocalFirstContext(ctx, key, opts); err != nil {
		return nil, err
	}
	intents, err := a.readMarketFallbackSyncIntents(opts)
	if err != nil {
		return nil, err
	}
	excluded := make(map[string]bool)
	for _, pending := range intents {
		excluded[pending.ItemID] = true
	}
	for _, ids := range exclusions {
		for id := range ids {
			excluded[id] = true
		}
	}
	stock, err := a.loadMarketFallback(opts)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	attempts := make(map[string]time.Time, len(stock.Items))
	cooldown := make(map[string]time.Time, len(stock.Items))
	for _, item := range stock.Items {
		attempts[item.ItemID] = a.fallbackSyncAt[localFirstFallbackAttemptKey(opts, item.ItemID)]
		cooldown[item.ItemID] = a.fallbackSyncAt[localFirstFallbackAmountKey(opts, item.ItemID, stockEditorLocalAmounts(item))]
	}
	a.mu.Unlock()
	sort.Slice(stock.Items, func(i, j int) bool {
		left, right := stock.Items[i].ItemID, stock.Items[j].ItemID
		if attempts[left].Equal(attempts[right]) {
			return left < right
		}
		return attempts[left].Before(attempts[right])
	})
	inactiveStock := false
	for _, item := range stock.Items {
		if item.LocalAvailableQuantity <= 0 || excluded[item.ItemID] || time.Since(cooldown[item.ItemID]) < time.Minute {
			continue
		}
		row, unique := marketFallbackSyncItem(remote, item.ItemID)
		if !unique {
			continue
		}
		a.mu.Lock()
		blocked := a.stockEditorBlocksItemLocked(item.ItemID) || a.handoverBlocksItemLocked(item.ItemID)
		a.mu.Unlock()
		if blocked {
			continue
		}
		if row.RequiredQuantity == 0 {
			active := false
			for _, source := range row.Sources {
				active = active || !source.Completed && !source.Canceled && source.RemainingRequiredQuantity > 0
			}
			if !active {
				inactiveStock = true
				continue
			}
		}
		captured := stockEditorLocalAmounts(item)
		quantity, err := stockEditorTotal(captured)
		if err != nil || row.AvailableQuantity < 0 || row.AvailableQuantity > localprogress.MaxQuantity-quantity {
			return nil, localprogress.ErrInvalidQuantity
		}
		projected, err := a.localStore.Apply(marketProgressOnly(remote))
		if err != nil {
			return nil, err
		}
		current, unique := stockEditorItem(projected, item.ItemID)
		if !unique {
			return nil, errMarketFallbackSyncInvalid
		}
		var operation [16]byte
		if _, err := rand.Read(operation[:]); err != nil {
			return nil, err
		}
		pending := &marketFallbackSyncIntent{Version: 2, OperationID: hex.EncodeToString(operation[:]), CharUID: opts.CharacterUID,
			Mode: strings.ToUpper(opts.Mode), ItemID: item.ItemID, Revision: remote.Revision, Before: row.AvailableQuantity,
			Target: row.AvailableQuantity + quantity, Fallback: captured,
			MarketAfter: localprogress.StockAmounts{Regular: current.MarketRegularQuantity + captured.Regular,
				FIR: current.MarketFIRQuantity + captured.FIR, Unknown: row.AvailableQuantity - current.MarketRegularQuantity - current.MarketFIRQuantity + captured.Unknown}}
		if err := a.marketFallbackLocalFirstContext(ctx, key, opts); err != nil {
			return nil, err
		}
		if err := a.marketFallbackStock().ValidateTransferAvailable(opts.CharacterUID, strings.ToUpper(opts.Mode), pending.OperationID, pending.ItemID, captured); err != nil {
			return nil, err
		}
		if err := a.saveMarketFallbackSync(opts, pending); err != nil {
			return nil, err
		}
		return pending, nil
	}
	if inactiveStock {
		return nil, errors.New("No active Market requirement remains for the saved supplementary stock. The stock was kept locally.")
	}
	return nil, nil
}

// A már létező csomag csak visszaolvasással zárható le. Ez a függvény
// nem küld új PATCH-et, és más mennyiségből nem talál ki elszámolást.
func (a *ItemWindowApp) settleLocalFirstMarketFallback(ctx context.Context, key string, opts ItemOptions, remote market.TrackedSnapshot, expected *marketFallbackSyncIntent) (string, error) {
	a.trackingMu.Lock()
	defer a.trackingMu.Unlock()
	if err := a.marketFallbackLocalFirstContext(ctx, key, opts); err != nil {
		return "", err
	}
	pending, err := a.readMarketFallbackSync(opts, expected.ItemID)
	if err != nil {
		return "", err
	}
	if pending == nil || *pending != *expected {
		return "", errMarketFallbackSyncPending
	}
	store := a.marketFallbackStock()
	if marketFallbackSyncVerified(remote, opts, pending) {
		if err := a.finishMarketFallbackSync(opts, pending, store); err != nil {
			return "", err
		}
		a.mu.Lock()
		delete(a.fallbackSyncAt, localFirstFallbackAttemptKey(opts, pending.ItemID))
		delete(a.fallbackSyncAt, localFirstFallbackAmountKey(opts, pending.ItemID, pending.Fallback))
		a.mu.Unlock()
		return "Market supplementary stock uploaded and verified.", nil
	}
	if released, err := a.releaseObsoleteMarketFallbackSync(remote, opts, pending, store); released || err != nil {
		return marketFallbackSyncKeptMessage, err
	}
	return "", errMarketFallbackSyncPending
}

func (a *ItemWindowApp) cancelLocalFirstRejectedUpload(ctx context.Context, key string, opts ItemOptions, remote market.TrackedSnapshot, expected *marketFallbackSyncIntent, status string) error {
	a.trackingMu.Lock()
	// A bizonyítottan elutasított saját szándék a régi profilban is
	// törölhető. A megváltozott kontextus csak az UI/cache publikálását
	// tiltja; különben egy biztosan nem elküldött írás függőben ragadna.
	pending, err := a.readMarketFallbackSync(opts, expected.ItemID)
	if err == nil && pending != nil && *pending == *expected {
		err = a.removeMarketFallbackSync(opts, expected.ItemID)
	} else if err == nil {
		err = errMarketFallbackSyncPending
	}
	a.trackingMu.Unlock()
	if err != nil {
		return errors.Join(errMarketFallbackSyncPending, err)
	}
	return a.publishLocalFirstMarketFallback(ctx, key, opts, remote, status)
}

func localFirstFallbackAttemptKey(opts ItemOptions, itemID string) string {
	return opts.CharacterUID + "\x00" + strings.ToUpper(opts.Mode) + "\x00" + itemID
}

func localFirstFallbackAmountKey(opts ItemOptions, itemID string, amounts localprogress.StockAmounts) string {
	return fmt.Sprintf("%s\x00%d/%d/%d", localFirstFallbackAttemptKey(opts, itemID), amounts.Regular, amounts.FIR, amounts.Unknown)
}

func (a *ItemWindowApp) markLocalFirstFallbackAttempt(opts ItemOptions, pending *marketFallbackSyncIntent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fallbackSyncAt == nil {
		a.fallbackSyncAt = make(map[string]time.Time)
	}
	key := localFirstFallbackAttemptKey(opts, pending.ItemID)
	// Egy tárgyhoz csak az utolsó csomag cooldownja marad memóriában.
	for previous := range a.fallbackSyncAt {
		if strings.HasPrefix(previous, key+"\x00") {
			delete(a.fallbackSyncAt, previous)
		}
	}
	now := time.Now()
	a.fallbackSyncAt[key] = now
	a.fallbackSyncAt[localFirstFallbackAmountKey(opts, pending.ItemID, pending.Fallback)] = now
}

// Csak új, másik és cooldown nélküli csomag ébresztheti fel azonnal a sort.
// Sérült, inaktív vagy bizonytalan napló miatt nem készítünk gyors retry ciklust.
func (a *ItemWindowApp) queueNextLocalFirstFallback(opts ItemOptions, remote market.TrackedSnapshot) {
	intents, err := a.readMarketFallbackSyncIntents(opts)
	if err != nil {
		return
	}
	pending := make(map[string]bool)
	for _, intent := range intents {
		pending[intent.ItemID] = true
	}
	stock, err := a.loadMarketFallback(opts)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.ItemOptions.revision != opts.revision || a.state.CharacterUID != opts.CharacterUID {
		return
	}
	for _, item := range stock.Items {
		row, unique := marketFallbackSyncItem(remote, item.ItemID)
		if item.LocalAvailableQuantity > 0 && unique && row.RequiredQuantity > 0 && !pending[item.ItemID] &&
			time.Since(a.fallbackSyncAt[localFirstFallbackAmountKey(opts, item.ItemID, stockEditorLocalAmounts(item))]) >= time.Minute &&
			!a.stockEditorBlocksItemLocked(item.ItemID) && !a.handoverBlocksItemLocked(item.ItemID) {
			a.queueProgressWork()
			return
		}
	}
}

func (a *ItemWindowApp) publishLocalFirstMarketFallback(ctx context.Context, key string, opts ItemOptions, remote market.TrackedSnapshot, status string) error {
	a.trackingMu.Lock()
	defer a.trackingMu.Unlock()
	if err := a.marketFallbackLocalFirstContext(ctx, key, opts); err != nil {
		return err
	}
	if marketFallbackSyncSnapshot(remote, opts) {
		if err := a.updateLocalFirstMarketSnapshot(key, opts, remote); err != nil {
			return err
		}
	}
	a.publishLocalFirstProgress(key, opts, status)
	// A siker után megmaradó új Add-ok külön csomaghoz tartoznak. A root
	// worker összevonja az ébresztéseket és a sikertelen próbák cooldownját.
	if status == "Market supplementary stock uploaded and verified." {
		a.queueNextLocalFirstFallback(opts, remote)
	}
	return nil
}
