package plus

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func syncCachedMarketTest(t *testing.T, a *ItemWindowApp) ItemState {
	t.Helper()
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	a.mu.Unlock()
	a.progressNetworkMu.Lock()
	_ = a.processLocalFirstMarketFallback(a.ctx, key, opts)
	a.progressNetworkMu.Unlock()
	return a.GetState()
}

func addAndSyncCachedTest(t *testing.T, a *ItemWindowApp, uid, kind string) ItemState {
	t.Helper()
	a.addTrackedItemKind(uid, nil, kind)
	return syncCachedMarketTest(t, a)
}

func confirmAndSyncCachedTest(t *testing.T, a *ItemWindowApp) ItemState {
	t.Helper()
	a.ConfirmHandover()
	key, opts := a.apiKey, a.GetState().ItemOptions
	a.progressNetworkMu.Lock()
	_ = a.processLocalFirstHandover(a.ctx, key, opts)
	a.progressNetworkMu.Unlock()
	return a.GetState()
}

func TestCachedAddHasZeroForegroundAPIAndPreservesSourceQuota(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	reads, quests, hideout := fake.loads, fake.questReads, fake.hideoutReads
	fake.loadHook = func(int) (market.TrackedSnapshot, error) { return market.TrackedSnapshot{}, context.DeadlineExceeded }
	for i := int64(1); i <= 8; i++ {
		state := a.AddTrackedItem(trackingItemUID)
		if state.Tracking.AvailableQuantity != i || state.Tracking.Adding || state.Local.Loading || state.Tracking.Error != "" {
			t.Fatalf("immediate local Add %d failed: %+v", i, state)
		}
	}
	if fake.loads != reads || fake.questReads != quests || fake.hideoutReads != hideout || len(fake.writes) != 0 {
		t.Fatalf("foreground Add used network: reads %d→%d writes %+v", reads, fake.loads, fake.writes)
	}
	stock, _ := a.loadMarketFallback(a.state.ItemOptions)
	row, _ := stockEditorItem(stock, trackingBSGID)
	if row.LocalRegularQuantity != 3 {
		t.Fatalf("Market quota was not staged separately: %+v", row)
	}
	assertLocalStockOnly(t, a, trackingBSGID, 5)
	state := a.GetState()
	if state.ProgressSync == nil || !state.ProgressSync.Pending || state.ProgressSync.PendingCount != 1 || state.ProgressSync.PendingItems != 3 {
		t.Fatalf("pending queue not visible: %+v", state.ProgressSync)
	}
	fake.loadHook = nil
	state = syncCachedMarketTest(t, a)
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 3 || state.Tracking.AvailableQuantity != 8 || state.ProgressSync.Pending {
		t.Fatalf("batch upload duplicated or lost stock: writes %+v state %+v", fake.writes, state)
	}
}

func TestCachedInspectReuseDoesNotRefreshSourcesOrClearCurrentStock(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	reads := fake.loads
	a.AddTrackedItemFIR(trackingItemUID)
	for i := 0; i < 12; i++ {
		a.requestTracking()
	}
	state := a.GetState()
	if fake.loads != reads || state.Tracking.AvailableQuantity != 1 || state.Tracking.Loading || state.Local.Loading {
		t.Fatalf("cached inspect requested API or erased stock: reads%d→%d state%+v", reads, fake.loads, state)
	}
}

func TestCachedCharacterRefreshDoesNotBlockLocalHandover(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	a.AddTrackedItemFIR(trackingItemUID)
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 1, "fir"))
	reads := fake.loads
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	fake.listHook = func() ([]market.Character, error) {
		close(started)
		<-release
		return []market.Character{{CharUID: "character-a", GameMode: "PVP"}}, nil
	}
	go func() { done <- a.refreshLocalFirstSources(a.ctx, a.apiKey, a.GetState().ItemOptions) }()
	t.Cleanup(func() {
		close(release)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("background character refresh failed: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("background character refresh did not finish")
		}
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background character refresh did not start")
	}
	if state := a.GetState(); state.CharactersLoading || state.Local.Loading {
		t.Fatalf("warm background refresh blocks local hand-in: %+v", state)
	}
	state := a.ConfirmHandover()
	requireHandoverComplete(t, state)
	stock := handoverStockForTest(t, a, trackingBSGID)
	if stock.LocalUsedQuantity != 1 || state.Tracking.AvailableQuantity != 0 || fake.loads != reads || len(fake.writes) != 0 {
		t.Fatalf("local hand-in waited for the background GET or lost the durable receipt: %+v", stock)
	}
}

func TestRefreshQueuesAfterInFlightUploadWithoutCancelOrReplay(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	a.progressWorkRequests = make(chan struct{}, 1)
	a.AddTrackedItemFIR(trackingItemUID)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	fake.writeContextHook = func(ctx context.Context, w stockWrite) (market.ProgressWriteResult, error) {
		fake.mu.Lock()
		fake.writes = append(fake.writes, w)
		fake.mu.Unlock()
		close(started)
		select {
		case <-ctx.Done():
			return market.ProgressWriteResult{}, ctx.Err()
		case <-release:
		}
		return fake.commitWrite(w), nil
	}
	go func() { done <- a.processProgressWork(a.ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background upload did not start")
	}
	for i := 0; i < 4; i++ {
		state := a.RefreshLocalProgress()
		if state.Local.Loading || !state.ProgressSync.Pending {
			t.Fatalf("refresh blocked cached stock or forgot upload: %+v", state)
		}
	}
	state := a.AddTrackedItemFIR(trackingItemUID)
	if state.Tracking.AvailableQuantity != 2 {
		t.Fatalf("in-flight API blocked a new local Add: %+v", state.Tracking)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh canceled or blocked the upload")
	}
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 1 || a.GetState().Tracking.AvailableQuantity != 2 {
		t.Fatalf("refresh replayed or dropped stock: writes%+v state%+v", fake.writes, a.GetState())
	}
	fake.writeContextHook = nil
	if err := a.processProgressWork(a.ctx); err != nil {
		t.Fatal(err)
	}
	if len(fake.writes) != 2 || fake.writes[1].Quantity != 2 || a.GetState().ProgressSync.Pending {
		t.Fatalf("new Add was not uploaded exactly once: %+v", fake.writes)
	}
}

func TestCachedAddAfterVerifiedRawBeforeTransferUsesAuthoritativeFallback(t *testing.T) {
	a, fake := newLocalTestApp(t)
	key, opts := a.apiKey, a.state.ItemOptions
	a.AddTrackedItemFIR(trackingItemUID)
	before := cloneTrackedSnapshot(fake.snapshot)
	pending, err := a.prepareLocalFirstMarketFallback(a.ctx, key, opts, before)
	if err != nil {
		t.Fatal(err)
	}
	fake.commitWrite(stockWrite{opts.CharacterUID, trackingBSGID, pending.Revision, pending.Target})
	if err = a.updateLocalFirstMarketSnapshot(key, opts, fake.snapshot); err != nil {
		t.Fatal(err)
	}
	a.trackingMu.Lock()
	a.publishLocalFirstProgress(key, opts, "")
	a.trackingMu.Unlock()
	state := a.AddTrackedItemFIR(trackingItemUID)
	stock, _ := a.loadMarketFallback(opts)
	row, _ := stockEditorItem(stock, trackingBSGID)
	if row.LocalFIRQuantity != 2 || state.Tracking.AvailableQuantity != 2 {
		t.Fatalf("projected quantity overwrote the durable captured packet: saved%+v display%+v", row, state.Tracking)
	}
	if _, err = a.settleLocalFirstMarketFallback(a.ctx, key, opts, fake.snapshot, pending); err != nil {
		t.Fatal(err)
	}
	stock, _ = a.loadMarketFallback(opts)
	row, _ = stockEditorItem(stock, trackingBSGID)
	if row.LocalFIRQuantity != 1 {
		t.Fatalf("transfer erased the appended item: %+v", row)
	}
}

func TestCachedRawDecreaseClearsStaleQualityBeforeApply(t *testing.T) {
	a, fake := newLocalTestApp(t)
	addAndSyncCachedTest(t, a, trackingItemUID, "fir")
	addAndSyncCachedTest(t, a, trackingItemUID, "fir")
	remote := cloneTrackedSnapshot(fake.snapshot)
	remote.Revision = "external-decrease"
	remote.Items[0].AvailableQuantity = 1
	if err := a.updateLocalFirstMarketSnapshot(a.apiKey, a.state.ItemOptions, remote); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := a.localFirstSnapshot(a.apiKey, a.state.ItemOptions)
	if err != nil || snapshot.Items[0].MarketFIRQuantity != 0 || snapshot.Items[0].MarketAvailableQuantity != 1 {
		t.Fatalf("decreased raw stock retained invalid quality: %+v %v", snapshot, err)
	}
}

func TestCachedSyncOutageKeepsDurablePendingAndAllowsIndependentAdd(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	a.AddTrackedItem(trackingItemUID)
	fake.loadHook = func(int) (market.TrackedSnapshot, error) {
		return market.TrackedSnapshot{}, market.ErrProgressUnavailable
	}
	if err := a.processProgressWork(a.ctx); err == nil {
		t.Fatal("outage was hidden")
	}
	state := a.AddTrackedItemFIR(trackingItemUID)
	if state.Tracking.AvailableQuantity != 2 || state.Local.StockError || state.Local.Error != "" || !state.ProgressSync.Pending {
		t.Fatalf("network error disabled local stock: %+v", state)
	}
	stock, _ := a.loadMarketFallback(a.state.ItemOptions)
	row, _ := stockEditorItem(stock, trackingBSGID)
	if row.LocalRegularQuantity != 1 || row.LocalFIRQuantity != 1 {
		t.Fatalf("typed queue lost: %+v", row)
	}
	if len(fake.writes) != 0 {
		t.Fatal("failed read sent a PATCH")
	}
}

func TestCachedItemRecognitionDoesNotCancelCharacterUpload(t *testing.T) {
	a, fake := newLocalTestApp(t)
	a.AddTrackedItemFIR(trackingItemUID)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	fake.writeContextHook = func(ctx context.Context, w stockWrite) (market.ProgressWriteResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			return market.ProgressWriteResult{}, ctx.Err()
		case <-release:
		}
		return fake.commitWrite(w), nil
	}
	go func() { done <- a.processProgressWork(a.ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upload did not start")
	}
	opts := a.GetState().ItemOptions
	a.acceptItem(market.Item{UID: "other-inspect", BSGID: "other-item", Name: "Other item"}, true, opts, nil)
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("changing Inspect canceled or blocked a character upload")
	}
	if a.GetState().Item.UID != "other-inspect" || fake.snapshot.Items[0].AvailableQuantity != 1 {
		t.Fatalf("upload changed the current item or was canceled: %+v", a.GetState())
	}
}

func TestCachedCatalogBackgroundFailureDoesNotDisableStock(t *testing.T) {
	a, _ := newLocalTestApp(t)
	dir := t.TempDir()
	data, err := json.Marshal(map[string]any{"version": 1, "mode": "pvp", "lang": "en", "updatedAt": time.Now().Add(-time.Hour), "items": []market.Item{*a.state.Item}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "catalog-pvp-en.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	a.client = market.NewClient(dir)
	previous := http.DefaultTransport
	http.DefaultTransport = diagnosticTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline test transport") })
	t.Cleanup(func() { http.DefaultTransport = previous })
	a.refreshCatalog()
	if state := a.GetState(); state.Error != "" || !state.Stale || !a.canAddLocalForTest() {
		t.Fatalf("cached price refresh disabled confirmed stock: %+v", state)
	}
	state := a.AddTrackedItemFIR(trackingItemUID)
	if state.Tracking.AvailableQuantity != 1 || state.Tracking.Error != "" {
		t.Fatalf("cached stock did not accept an eligible item: %+v", state)
	}
}
