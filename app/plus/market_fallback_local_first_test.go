package plus

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestLocalFirstMarketFallbackNetworkDoesNotHoldTrackingLockAndKeepsConcurrentAdd(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	var captured *marketFallbackSyncIntent
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		if !a.trackingMu.TryLock() {
			t.Fatal("network PATCH kept the local mutation lock")
		}
		defer a.trackingMu.Unlock()
		var err error
		captured, err = a.readMarketFallbackSync(opts)
		if err != nil || captured == nil || captured.Version != 2 || !stockUsageOperationID(captured.OperationID) || captured.Target != 6 {
			t.Fatalf("PATCH started without durable captured v2 intent: %+v %v", captured, err)
		}
		if allowed, err := a.marketFallbackIntentAllowsAppend(opts, trackingBSGID); err != nil || !allowed {
			t.Fatalf("captured batch blocked new typed Add: %v %v", allowed, err)
		}
		current := fallbackSyncAvailableForTest(t, a, opts)
		current.FIR++
		if _, err := a.marketFallbackStock().CorrectStock(opts.CharacterUID, "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &current}); err != nil {
			t.Fatal(err)
		}
		return fake.commitWrite(write), nil
	}
	if err := a.processLocalFirstMarketFallback(context.Background(), a.apiKey, opts); err != nil {
		t.Fatal(err)
	}
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 6 || fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{FIR: 1}) {
		t.Fatalf("new Add lost or uploaded in wrong batch: %+v %+v", fake.writes, fallbackSyncAvailableForTest(t, a, opts))
	}
	stock, err := a.marketFallbackStock().Load(opts.CharacterUID, "PVP")
	row, unique := stockEditorItem(stock, trackingBSGID)
	if err != nil || !unique || row.LocalUsedQuantity != 2 || row.LocalAvailableQuantity != 1 || row.LocalCollectedQuantity != 3 {
		t.Fatalf("upload became usage or erased new stock: %+v %v", row, err)
	}
	if recorded, err := a.marketFallbackStock().TransferRecorded(opts.CharacterUID, "PVP", captured.OperationID, captured.ItemID, captured.Fallback); err != nil || !recorded {
		t.Fatalf("batch transfer receipt missing: %v %v", recorded, err)
	}
}

func twoItemFallbackFixture(t *testing.T) (*ItemWindowApp, *fakeTrackingAPI, ItemOptions, string) {
	t.Helper()
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	id := trackingBSGID + "b"
	fake.snapshot.Items = append(fake.snapshot.Items, market.TrackedItem{ItemID: id, Name: "Other item", RequiredQuantity: 7, RemainingQuantity: 7,
		Sources: []market.TrackedSource{{Type: "quest", Amount: 7, RemainingRequiredQuantity: 7}}})
	amounts := localprogress.StockAmounts{Regular: 1}
	if _, err := a.marketFallbackStock().CorrectStock(opts.CharacterUID, "PVP", localprogress.ManualStockCorrection{ItemID: id, LocalAvailable: &amounts}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := a.localStore.Apply(marketProgressOnly(fake.snapshot))
	if err != nil || a.rememberProgressSources(a.apiKey, opts, snapshot, "") != nil {
		t.Fatal(err)
	}
	return a, fake, opts, id
}

func commitFallbackItemForTest(fake *fakeTrackingAPI, write stockWrite) market.ProgressWriteResult {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for i := range fake.snapshot.Items {
		if fake.snapshot.Items[i].ItemID == write.Item {
			fake.snapshot.Items[i].AvailableQuantity = write.Quantity
			fake.snapshot.Items[i].RemainingQuantity = max(0, fake.snapshot.Items[i].RequiredQuantity-write.Quantity)
		}
	}
	fake.commits++
	fake.snapshot.Revision = "other-item-committed"
	return market.ProgressWriteResult{OK: true, Changed: true, Revision: fake.snapshot.Revision}
}

func TestLocalFirstMarketFallbackNoOpItemDoesNotStarveOtherItem(t *testing.T) {
	for _, failure := range []string{"no-op", "rejected"} {
		t.Run(failure, func(t *testing.T) {
			a, fake, opts, other := twoItemFallbackFixture(t)
			before := fallbackSyncAvailableForTest(t, a, opts)
			fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
				if write.Item == trackingBSGID {
					if failure == "rejected" {
						return market.ProgressWriteResult{}, market.ErrProgressForbidden
					}
					return market.ProgressWriteResult{OK: true, Changed: false, Revision: write.Revision}, nil
				}
				return commitFallbackItemForTest(fake, write), nil
			}
			for i := 0; i < 3; i++ {
				if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, opts); err != nil {
					t.Fatal(err)
				}
			}
			if len(fake.writes) != 2 || fake.writes[0].Item != trackingBSGID || fake.writes[1].Item != other ||
				fallbackSyncAvailableForTest(t, a, opts) != before {
				t.Fatalf("failed first item starved the next or looped immediately: %+v", fake.writes)
			}
			stock, err := marketFallbackSyncAmounts(a.marketFallbackStock(), opts, other)
			if err != nil || stock != (localprogress.StockAmounts{}) {
				t.Fatalf("second item did not finish independently: %+v %v", stock, err)
			}
		})
	}
}

func TestLocalFirstMarketFallbackUncertainItemDoesNotStarveOtherItem(t *testing.T) {
	a, fake, opts, other := twoItemFallbackFixture(t)
	before := fallbackSyncAvailableForTest(t, a, opts)
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		if write.Item == trackingBSGID {
			return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
		}
		return commitFallbackItemForTest(fake, write), nil
	}
	if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, opts); !errors.Is(err, errMarketFallbackSyncPending) {
		t.Fatalf("uncertainty not retained: %v", err)
	}
	first, err := a.readMarketFallbackSyncItem(opts, trackingBSGID)
	if err != nil || first == nil {
		t.Fatal("first item's intent was not durable")
	}
	if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, opts); !errors.Is(err, errMarketFallbackSyncPending) ||
		len(fake.writes) != 2 || fake.writes[1].Item != other || fake.writes[1].Revision != first.Revision {
		t.Fatalf("unrelated second upload never ran or retried the uncertain one: %v %+v", err, fake.writes)
	}
	if fallbackSyncAvailableForTest(t, a, opts) != before {
		t.Fatal("second upload consumed the first item's typed stock")
	}
	// B új revisionje érvényteleníti a régi A CAS-t. Restart után csak GET
	// törölheti ezt a naplót; A megőrzött darabjait nem küldjük azonnal újra.
	a.fallbackStore = localprogress.NewStore(filepath.Join(a.dir, "market-fallback"))
	if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, opts); err != nil || len(fake.writes) != 2 || fallbackSyncAvailableForTest(t, a, opts) != before {
		t.Fatalf("read-only old intent recovery wrote or lost stock: %v %+v", err, fake.writes)
	}
	if intents, err := a.readMarketFallbackSyncIntents(opts); err != nil || len(intents) != 0 {
		t.Fatalf("obsolete first item stayed globally pending: %+v %v", intents, err)
	}
}

func TestLocalFirstMarketFallbackPerItemFilesPreserveLegacyAndRejectDuplicates(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			a, fake, opts, other := twoItemFallbackFixture(t)
			first := &marketFallbackSyncIntent{Version: version, CharUID: opts.CharacterUID, Mode: "PVP", ItemID: trackingBSGID,
				Revision: fake.snapshot.Revision, Before: 2, Target: 6, Fallback: localprogress.StockAmounts{Regular: 2, FIR: 1, Unknown: 1}, MarketAfter: localprogress.StockAmounts{Regular: 3, FIR: 1, Unknown: 2}}
			if version == 2 {
				first.OperationID = strings.Repeat("a", 32)
			}
			if err := a.saveMarketFallbackSync(opts, first); err != nil {
				t.Fatal(err)
			}
			path, _ := a.marketFallbackSyncPath(opts, first.ItemID)
			legacy, _ := a.marketFallbackSyncLegacyPath(opts)
			if err := os.Rename(path, legacy); err != nil {
				t.Fatal(err)
			}
			second := &marketFallbackSyncIntent{Version: 2, OperationID: strings.Repeat("b", 32), CharUID: opts.CharacterUID, Mode: "PVP", ItemID: other,
				Revision: fake.snapshot.Revision, Target: 1, Fallback: localprogress.StockAmounts{Regular: 1}, MarketAfter: localprogress.StockAmounts{Regular: 1}}
			if err := a.saveMarketFallbackSync(opts, second); err != nil {
				t.Fatal(err)
			}
			if intents, err := a.readMarketFallbackSyncIntents(opts); err != nil || len(intents) != 2 {
				t.Fatalf("legacy profile intent prevented independent new file: %+v %v", intents, err)
			}
			before, _ := os.ReadFile(legacy)
			if err := a.writeMarketFallbackSync(path, first); err != nil {
				t.Fatal(err)
			}
			if _, err := a.readMarketFallbackSyncIntents(opts); err == nil {
				t.Fatal("same item in legacy and new file was silently accepted")
			}
			if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, opts); err == nil || len(fake.writes) != 0 {
				t.Fatal("duplicate intent started a write")
			}
			after, _ := os.ReadFile(legacy)
			if !bytes.Equal(before, after) {
				t.Fatal("duplicate recovery overwrote the legacy journal")
			}
		})
	}
}

func TestMarketFallbackPublicRetryDuringBackgroundNetworkDoesNotDeadlock(t *testing.T) {
	a, fake, opts, _ := pendingFallbackRecoveryFixture(t)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	remote := cloneTrackedSnapshot(fake.snapshot)
	fake.loadHook = func(int) (market.TrackedSnapshot, error) {
		once.Do(func() { close(started); <-release })
		return remote, nil
	}
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	go func() {
		a.progressNetworkMu.Lock()
		defer a.progressNetworkMu.Unlock()
		done <- a.processLocalFirstMarketFallback(a.ctx, a.apiKey, opts)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background did not reach the blocked GET")
	}
	retried := make(chan ItemState, 1)
	go func() { retried <- a.RetryMarketUpload() }()
	select {
	case state := <-retried:
		if state.StockEditor != nil && state.StockEditor.Saving {
			t.Fatal("busy Retry froze all local stock")
		}
	case <-time.After(time.Second):
		t.Fatal("Retry waited holding trackingMu behind the worker sync mutex")
	}
	if !a.trackingMu.TryLock() {
		t.Fatal("busy Retry kept the mutation mutex")
	}
	a.trackingMu.Unlock()
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		if !errors.Is(err, errMarketFallbackSyncPending) {
			t.Fatalf("background discarded uncertainty: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("background never completed after Retry returned")
	}
	if len(fake.writes) != 1 {
		t.Fatal("busy Retry resent the uncertain upload")
	}
}

func TestLocalFirstMarketFallbackMultipleUncertainItemsRestartGETOnlyAndStayScoped(t *testing.T) {
	a, fake, opts, other := twoItemFallbackFixture(t)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	for i := 0; i < 2; i++ {
		if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, opts); !errors.Is(err, errMarketFallbackSyncPending) {
			t.Fatal(err)
		}
	}
	intents, err := a.readMarketFallbackSyncIntents(opts)
	if err != nil || len(intents) != 2 || len(fake.writes) != 2 {
		t.Fatalf("independent uncertain items did not receive separate journals: %+v %v", intents, err)
	}
	before := make(map[string][]byte)
	for _, intent := range intents {
		path, _ := a.marketFallbackSyncPath(opts, intent.ItemID)
		before[path], _ = os.ReadFile(path)
	}
	a.mu.Lock()
	err = a.refreshMarketFallbackPendingLocked(opts)
	firstBlocked := a.marketFallbackBlocksItemLocked(trackingBSGID)
	otherBlocked := a.marketFallbackBlocksItemLocked(other)
	unrelatedBlocked := a.marketFallbackBlocksItemLocked("unrelated-item")
	firstAppend := a.marketFallbackAppendAllowedLocked(trackingBSGID)
	otherAppend := a.marketFallbackAppendAllowedLocked(other)
	a.mu.Unlock()
	if err != nil || !firstBlocked || !otherBlocked || unrelatedBlocked || !firstAppend || !otherAppend {
		t.Fatalf("multi-item scope or captured-subset Add permission is wrong: %v %v %v %v %v %v", err, firstBlocked, otherBlocked, unrelatedBlocked, firstAppend, otherAppend)
	}
	a.fallbackStore = localprogress.NewStore(filepath.Join(a.dir, "market-fallback"))
	if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, opts); !errors.Is(err, errMarketFallbackSyncPending) || len(fake.writes) != 2 {
		t.Fatalf("restart replayed one uncertain item: %v %+v", err, fake.writes)
	}
	for path, data := range before {
		after, _ := os.ReadFile(path)
		if !bytes.Equal(data, after) {
			t.Fatal("read-only restart changed a pending item's immutable payload")
		}
	}
}

func TestMarketFallbackPublicRetryNetworkDoesNotFreezeLocalMutationLock(t *testing.T) {
	a, fake, _, _ := pendingFallbackRecoveryFixture(t)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan ItemState, 1)
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		close(started)
		<-release
		return fake.commitWrite(write), nil
	}
	go func() { done <- a.RetryMarketUpload() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("explicit Retry did not reach the saved PATCH")
	}
	if !a.trackingMu.TryLock() {
		t.Fatal("explicit Retry held trackingMu over its network call")
	}
	a.trackingMu.Unlock()
	state := a.GetState()
	if state.StockEditor.Saving || !state.ProgressSync.Running {
		t.Fatalf("Retry used a global stock-edit busy gate: %+v %+v", state.StockEditor, state.ProgressSync)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case state := <-done:
		if state.MarketFallbackPending || state.StockEditor.Error != "" || state.StockEditor.Saving {
			t.Fatalf("verified Retry did not publish cleanly: %+v", state)
		}
	case <-time.After(time.Second):
		t.Fatal("Retry did not settle after network release")
	}
}

func TestLocalFirstMarketFallbackUncertainBatchRestartsGETOnlyAndDoesNotTakeNewAdds(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	if err := a.processLocalFirstMarketFallback(context.Background(), a.apiKey, opts); !errors.Is(err, errMarketFallbackSyncPending) {
		t.Fatalf("uncertain upload was cleared: %v", err)
	}
	pending, err := a.readMarketFallbackSync(opts)
	if err != nil || pending == nil || pending.Version != 2 || len(fake.writes) != 1 {
		t.Fatalf("uncertain batch not durable: %+v %v", pending, err)
	}
	added := pending.Fallback
	added.Regular += 2
	if _, err := a.marketFallbackStock().CorrectStock(opts.CharacterUID, "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &added}); err != nil {
		t.Fatal(err)
	}
	a.fallbackStore = localprogress.NewStore(filepath.Join(a.dir, "market-fallback"))
	if err := a.processLocalFirstMarketFallback(context.Background(), a.apiKey, opts); !errors.Is(err, errMarketFallbackSyncPending) || len(fake.writes) != 1 {
		t.Fatalf("restart repeated uncertain PATCH: %v %+v", err, fake.writes)
	}
	// Az eredeti kérés későn alkalmazódik; a worker csak GET-tel zárja le.
	fake.commitWrite(fake.writes[0])
	if err := a.processLocalFirstMarketFallback(context.Background(), a.apiKey, opts); err != nil || len(fake.writes) != 1 ||
		fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{Regular: 2}) {
		t.Fatalf("late commit or new additions counted twice: %v %+v", err, fake.writes)
	}
}

func TestLocalFirstMarketFallbackPartialSettlementReceiptKeepsPostTransferAdd(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	pending, err := a.prepareLocalFirstMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot))
	if err != nil || pending == nil {
		t.Fatal(err)
	}
	fake.commitWrite(stockWrite{Character: opts.CharacterUID, Item: pending.ItemID, Revision: pending.Revision, Quantity: pending.Target})
	if _, err := a.marketFallbackStock().TransferAvailable(opts.CharacterUID, "PVP", pending.OperationID, pending.ItemID, pending.Fallback); err != nil {
		t.Fatal(err)
	}
	newStock := localprogress.StockAmounts{FIR: 2}
	if _, err := a.marketFallbackStock().CorrectStock(opts.CharacterUID, "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &newStock}); err != nil {
		t.Fatal(err)
	}
	a.fallbackStore = localprogress.NewStore(filepath.Join(a.dir, "market-fallback"))
	if err := a.processLocalFirstMarketFallback(context.Background(), a.apiKey, opts); err != nil || len(fake.writes) != 0 || fallbackSyncAvailableForTest(t, a, opts) != newStock {
		t.Fatalf("receipt replay consumed post-transfer Add: %v", err)
	}
}

func TestLocalFirstMarketFallbackNoOpLeavesAllFallbackAndNoJournal(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	before := fallbackSyncAvailableForTest(t, a, opts)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{OK: true, Changed: false, Revision: fake.snapshot.Revision}, nil
	}
	if err := a.processLocalFirstMarketFallback(context.Background(), a.apiKey, opts); err != nil || len(fake.writes) != 1 || fallbackSyncAvailableForTest(t, a, opts) != before {
		t.Fatalf("new confirmed no-op changed stock: %v", err)
	}
	if pending, err := a.readMarketFallbackSync(opts); pending != nil || err != nil {
		t.Fatalf("definite no-op retained new journal: %+v %v", pending, err)
	}
}

func TestLocalFirstMarketFallbackLegacyAndMalformedV2FailClosed(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	pending := &marketFallbackSyncIntent{Version: 1, CharUID: opts.CharacterUID, Mode: "PVP", ItemID: trackingBSGID,
		Revision: fake.snapshot.Revision, Before: 2, Target: 6, Fallback: localprogress.StockAmounts{Regular: 2, FIR: 1, Unknown: 1},
		MarketAfter: localprogress.StockAmounts{Regular: 3, FIR: 1, Unknown: 2}}
	if err := a.saveMarketFallbackSync(opts, pending); err != nil {
		t.Fatal(err)
	}
	if allowed, err := a.marketFallbackIntentAllowsAppend(opts, trackingBSGID); allowed || err != nil {
		t.Fatalf("legacy whole-stock intent allowed append: %v %v", allowed, err)
	}
	path, _ := a.marketFallbackSyncPath(opts, trackingBSGID)
	legacy, _ := os.ReadFile(path)
	if err := a.removeMarketFallbackSync(opts); err != nil {
		t.Fatal(err)
	}
	pending.Version, pending.OperationID = 2, strings.Repeat("b", 32)
	if err := a.saveMarketFallbackSync(opts, pending); err != nil {
		t.Fatal(err)
	}
	v2, _ := os.ReadFile(path)
	for _, corrupted := range [][]byte{
		bytes.Replace(v2, []byte(`"operationId"`), []byte(`"OperationId"`), 1),
		bytes.Replace(v2, []byte(`"operationId": "`+pending.OperationID+`"`), []byte(`"operationId": null`), 1),
		bytes.Replace(v2, []byte(pending.OperationID), []byte("invalid-id"), 1),
		bytes.Replace(legacy, []byte(`"version": 1`), []byte(`"version": 2`), 1),
	} {
		if err := os.WriteFile(path, corrupted, 0600); err != nil {
			t.Fatal(err)
		}
		if allowed, err := a.marketFallbackIntentAllowsAppend(opts, trackingBSGID); err == nil || allowed {
			t.Fatalf("malformed pending allowed append: %v %v", allowed, err)
		}
		beforeWrites := len(fake.writes)
		if err := a.processLocalFirstMarketFallback(context.Background(), a.apiKey, opts); err == nil || len(fake.writes) != beforeWrites {
			t.Fatal("invalid intent started Market write")
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(after, corrupted) {
			t.Fatal("invalid intent was overwritten")
		}
	}
}

func TestLocalFirstMarketFallbackClosedRequirementKeepsSavedStockWithoutPATCH(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	fake.snapshot.Items[0].RequiredQuantity = 0
	fake.snapshot.Items[0].RemainingQuantity = 0
	for i := range fake.snapshot.Items[0].Sources {
		fake.snapshot.Items[0].Sources[i].Completed = true
		fake.snapshot.Items[0].Sources[i].RemainingRequiredQuantity = 0
	}
	before := fallbackSyncAvailableForTest(t, a, opts)
	if err := a.processLocalFirstMarketFallback(context.Background(), a.apiKey, opts); err == nil || len(fake.writes) != 0 || fallbackSyncAvailableForTest(t, a, opts) != before {
		t.Fatalf("closed requirement uploaded or lost collected stock: %v", err)
	}
	if pending, err := a.readMarketFallbackSync(opts); pending != nil || err != nil {
		t.Fatalf("closed requirement created an intent: %+v %v", pending, err)
	}
}

type localFirstFallbackFenceContext struct {
	context.Context
	path         string
	afterJournal func()
	triggered    bool
}

func (ctx *localFirstFallbackFenceContext) Err() error {
	if !ctx.triggered {
		if _, err := os.Stat(ctx.path); err == nil {
			ctx.triggered = true
			ctx.afterJournal()
		}
	}
	return ctx.Context.Err()
}

func TestLocalFirstMarketFallbackContextAfterCaptureRemovesOnlyUnsentIntent(t *testing.T) {
	for _, change := range []string{"key", "character", "options", "cancel"} {
		t.Run(change, func(t *testing.T) {
			a, fake, opts, _ := marketFallbackSyncFixture(t)
			key := a.apiKey
			path, _ := a.marketFallbackSyncPath(opts, trackingBSGID)
			stockPath := fallbackSyncFileForTest(t, filepath.Join(a.dir, "market-fallback"))
			before, _ := os.ReadFile(stockPath)
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &localFirstFallbackFenceContext{Context: base, path: path, afterJournal: func() {
				a.mu.Lock()
				defer a.mu.Unlock()
				switch change {
				case "key":
					a.apiKey = "other-local-test-key"
				case "character":
					a.state.CharacterUID = "other-character"
				case "options":
					a.state.ItemOptions.revision++
				case "cancel":
					cancel()
				}
			}}
			if err := a.processLocalFirstMarketFallback(ctx, key, opts); !errors.Is(err, context.Canceled) || len(fake.writes) != 0 {
				t.Fatalf("stale prepared context sent Market write: %v %+v", err, fake.writes)
			}
			after, _ := os.ReadFile(stockPath)
			if !bytes.Equal(before, after) {
				t.Fatal("unsent cancellation changed stock, quality or Used")
			}
			if pending, err := a.readMarketFallbackSync(opts); pending != nil || err != nil {
				t.Fatalf("proven unsent cancellation left blocking intent: %+v %v", pending, err)
			}
			a.apiKey, a.state.ItemOptions = key, opts
			if err := a.processLocalFirstMarketFallback(context.Background(), key, opts); err != nil || len(fake.writes) != 1 {
				t.Fatalf("restored profile could not upload one fresh batch: %v %+v", err, fake.writes)
			}
		})
	}
}

func TestLocalFirstMarketFallbackReadOnlyObsoleteBatchKeepsNewTypedStockAndUsed(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	pending := &marketFallbackSyncIntent{Version: 2, OperationID: strings.Repeat("a", 32), CharUID: opts.CharacterUID, Mode: "PVP", ItemID: trackingBSGID,
		Revision: fake.snapshot.Revision, Before: 2, Target: 6, Fallback: localprogress.StockAmounts{Regular: 2, FIR: 1, Unknown: 1},
		MarketAfter: localprogress.StockAmounts{Regular: 3, FIR: 1, Unknown: 2}}
	if err := a.saveMarketFallbackSync(opts, pending); err != nil {
		t.Fatal(err)
	}
	added := pending.Fallback
	added.Regular++
	if _, err := a.marketFallbackStock().CorrectStock(opts.CharacterUID, "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &added}); err != nil {
		t.Fatal(err)
	}
	path := fallbackSyncFileForTest(t, a.dir+"/market-fallback")
	before, _ := os.ReadFile(path)
	fake.snapshot.Revision = "different-revision"
	if err := a.processLocalFirstMarketFallback(context.Background(), a.apiKey, opts); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if len(fake.writes) != 0 || !bytes.Equal(before, after) || fallbackSyncAvailableForTest(t, a, opts) != added {
		t.Fatal("read-only old CAS invalidation changed new stock, types or usage")
	}
	if pending, err := a.readMarketFallbackSync(opts); pending != nil || err != nil {
		t.Fatalf("obsolete batch remained locked: %+v %v", pending, err)
	}
}
