package plus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/scanner"
)

func primeLocalFirstForTest(t *testing.T, a *ItemWindowApp) {
	t.Helper()
	snapshot, warning, err := a.loadLocalSources(a.ctx, a.apiKey, a.state.ItemOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.rememberProgressSources(a.apiKey, a.state.ItemOptions, snapshot, warning); err != nil {
		t.Fatal(err)
	}
	a.publishLocalFirstProgress(a.apiKey, a.state.ItemOptions, "")
}

func localFirstMixedHandoverFixture(t *testing.T) (*ItemWindowApp, *fakeCharacterProgress) {
	t.Helper()
	a, fake := newLocalTestApp(t)
	fake.snapshot.Items[0].AvailableQuantity = 2
	fake.snapshot.Revision = "local-first-baseline"
	local := localprogress.StockAmounts{Regular: 1}
	remote := localprogress.StockAmounts{Regular: 2}
	if _, err := a.localStore.CorrectStock("character-a", "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &local, MarketKinds: &remote, ObservedMarketQuantity: 2}); err != nil {
		t.Fatal(err)
	}
	primeLocalFirstForTest(t, a)
	return a, fake
}

func TestLocalFirstHandoverDurableCommitPinsProfileAndDraftOnlyUntilLocalSave(t *testing.T) {
	a, fake := localFirstMixedHandoverFixture(t)
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
	beforeOpts := a.state.ItemOptions
	oldWrite := handoverJournalWrite
	t.Cleanup(func() { handoverJournalWrite = oldWrite })
	started, release := make(chan struct{}), make(chan struct{})
	first := true
	handoverJournalWrite = func(path string, data []byte) error {
		if first {
			first = false
			close(started)
			<-release
		}
		return oldWrite(path, data)
	}
	finished := make(chan ItemState, 1)
	go func() {
		finished <- a.ConfirmHandover()
		close(finished)
	}()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("blocked test commit did not finish during cleanup")
		}
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("local commit did not start")
	}
	state := a.GetState()
	if !state.Handover.Busy || len(fake.writes) != 0 {
		t.Fatal("durable local phase did not pin its draft before network")
	}
	changed := beforeOpts
	changed.CharacterUID, changed.Mode = "character-other", "pve"
	state = a.SetOptions(changed)
	if state.ItemOptions != beforeOpts || !state.Handover.Busy {
		t.Fatal("profile changed during the local commit")
	}
	a.UpdateHandoverItem(trackingBSGID+"-regular", 1, "fir")
	a.RemoveHandoverItem(trackingBSGID + "-regular")
	a.CloseHandover()
	state = a.GetState()
	if !state.Handover.Open || len(state.Handover.Items) != 1 || state.Handover.Items[0].Quantity != 2 || state.Handover.Items[0].Kind != "regular" {
		t.Fatal("immutable draft changed during the local commit")
	}
	close(release)
	select {
	case state = <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("local commit waited for Market")
	}
	if state.Handover.Busy || !state.Handover.Background || !state.Handover.Pending || len(fake.writes) != 0 {
		t.Fatal("short Busy phase leaked into background synchronization")
	}
	if err := a.processLocalFirstHandover(a.ctx, a.apiKey, a.state.ItemOptions); err != nil {
		t.Fatal(err)
	}
	requireHandoverComplete(t, a.GetState())
	if len(fake.writes) != 1 || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 1 {
		t.Fatal("guard changed normal exactly-once hand-in behavior")
	}
	state = a.SetOptions(changed)
	if state.CharacterUID != changed.CharacterUID {
		t.Fatal("short commit guard permanently blocked later profile changes")
	}
}

func TestLocalFirstHandoverContextReplacementAfterIntentSaveNeverTouchesNewProfile(t *testing.T) {
	a, fake := localFirstMixedHandoverFixture(t)
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
	opts := a.state.ItemOptions
	oldWrite := handoverJournalWrite
	t.Cleanup(func() { handoverJournalWrite = oldWrite })
	first := true
	handoverJournalWrite = func(path string, data []byte) error {
		if err := oldWrite(path, data); err != nil {
			return err
		}
		if first {
			first = false
			// Simulate lifecycle/context replacement independently of the public
			// Busy guard, exactly after the old-profile intent becomes durable.
			a.mu.Lock()
			a.state.ItemOptions.CharacterUID = "new-character"
			a.state.ItemOptions.Mode = "pve"
			a.state.ItemOptions.revision++
			a.restoreHandoverLocked()
			a.restoreStockEditorLocked()
			a.mu.Unlock()
		}
		return nil
	}
	state := a.ConfirmHandover()
	if state.CharacterUID != "new-character" || state.Handover != nil || state.Error != "" || a.handover != nil || len(fake.writes) != 0 {
		t.Fatalf("old commit touched new-profile UI or panicked: %+v", state)
	}
	tx, err := readHandover(a.handoverPath(opts.CharacterUID, opts.Mode), opts.CharacterUID, opts.Mode)
	if err != nil || tx.LocalDone || tx.Items[0].Stage != "unsent" {
		t.Fatal("old-profile intent was not safely kept before own deduction", err)
	}
	stock, err := a.localStore.Load(opts.CharacterUID, "PVP")
	row, _ := stockEditorItem(stock, trackingBSGID)
	if err != nil || row.LocalUsedQuantity != 0 || row.LocalAvailableQuantity != 1 {
		t.Fatal("canceled old-profile callback deducted own stock", err)
	}
}

func TestLocalFirstHandoverUnsafeItemIsRejectedBeforeLocalDeductionAndCoherentRefreshRestoresIt(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	amounts := localprogress.StockAmounts{Regular: 2}
	if _, err := a.localStore.CorrectStock("character-a", "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &amounts}); err != nil {
		t.Fatal(err)
	}
	primeLocalFirstForTest(t, a)
	reads := fake.loads
	message := "Requirements for this item are being refreshed."
	a.mu.Lock()
	a.progressUnsafeItems = map[string]string{trackingBSGID: message}
	a.mu.Unlock()
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 1, "regular"))
	state := a.ConfirmHandover()
	if state.Handover.Error != message || state.Handover.Busy || fake.loads != reads || len(fake.writes) != 0 {
		t.Fatal("unsafe hand-in was not rejected before any I/O")
	}
	if _, err := os.Stat(a.handoverPath("character-a", "PVP")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsafe hand-in created an intent", err)
	}
	row := handoverStockForTest(t, a, trackingBSGID)
	if row.LocalAvailableQuantity != 2 || row.LocalUsedQuantity != 0 {
		t.Fatal("unsafe hand-in consumed remembered own stock")
	}
	// A full coherent requirement refresh, rather than a partial single-row
	// update, clears the guard and restores the ordinary local-only action.
	primeLocalFirstForTest(t, a)
	reads = fake.loads
	state = a.ConfirmHandover()
	requireHandoverComplete(t, state)
	row = handoverStockForTest(t, a, trackingBSGID)
	if fake.loads != reads || len(fake.writes) != 0 || row.LocalAvailableQuantity != 1 || row.LocalUsedQuantity != 1 {
		t.Fatal("coherent refresh did not restore the normal local-first hand-in")
	}
}

func TestLocalFirstHandoverOwnPoolsCompleteImmediatelyWithoutNetwork(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	local := localprogress.StockAmounts{Regular: 2, FIR: 1}
	fallback := localprogress.StockAmounts{Regular: 2}
	if _, err := a.localStore.CorrectStock("character-a", "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &local}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.marketFallbackStock().CorrectStock("character-a", "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &fallback}); err != nil {
		t.Fatal(err)
	}
	primeLocalFirstForTest(t, a)
	reads := fake.loads
	fake.loadHook = func(int) (market.TrackedSnapshot, error) {
		t.Fatal("own hand-in performed a GET")
		return market.TrackedSnapshot{}, market.ErrProgressUnavailable
	}
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 4, "regular"), handoverRowForTest(trackingBSGID, 1, "fir"))
	state := a.ConfirmHandover()
	requireHandoverComplete(t, state)
	localItem := handoverStockForTest(t, a, trackingBSGID)
	stock, err := a.marketFallbackStock().Load("character-a", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	fallbackItem, _ := stockEditorItem(stock, trackingBSGID)
	if fake.loads != reads || len(fake.writes) != 0 || localItem.LocalUsedQuantity != 3 || localItem.LocalCollectedQuantity != 3 || fallbackItem.LocalUsedQuantity != 2 || fallbackItem.LocalCollectedQuantity != 2 || state.Tracking.AvailableQuantity != 0 {
		t.Fatalf("own pools were not immediately and independently settled: %+v %+v", localItem, fallbackItem)
	}
}

func TestLocalFirstHandoverMarketIntentProjectsBeforeOneBackgroundWrite(t *testing.T) {
	a, fake := localFirstMixedHandoverFixture(t)
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
	reads := fake.loads
	state := a.ConfirmHandover()
	if state.Handover == nil || !state.Handover.Background || !state.Handover.Pending || state.Handover.Busy || state.Tracking.AvailableQuantity != 1 || fake.loads != reads || len(fake.writes) != 0 || fake.snapshot.Items[0].AvailableQuantity != 2 {
		t.Fatalf("Market was synchronous or intent projection missing: %+v", state.Handover)
	}
	tx, err := readHandover(a.handoverPath("character-a", "PVP"), "character-a", "PVP")
	if err != nil || tx.Version != 2 || !tx.LocalDone || tx.Items[0].Stage != "unsent" || tx.Items[0].Target != 1 {
		t.Fatalf("durable unsent plan missing: %+v %v", tx, err)
	}
	if own := handoverStockForTest(t, a, trackingBSGID); own.LocalUsedQuantity != 1 || own.LocalCollectedQuantity != 1 {
		t.Fatal("own receipt was not settled before Market")
	}
	if err = a.processLocalFirstHandover(a.ctx, a.apiKey, a.state.ItemOptions); err != nil {
		t.Fatal(err)
	}
	requireHandoverComplete(t, a.GetState())
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 1 || a.GetState().Tracking.AvailableQuantity != 1 {
		t.Fatal("background worker did not verify exactly one fixed target")
	}
	if err = a.processLocalFirstHandover(a.ctx, a.apiKey, a.state.ItemOptions); err != nil || len(fake.writes) != 1 {
		t.Fatal("completed background operation replayed")
	}
}

func TestLocalFirstHandoverUnsentBaselineChangeCannotBeSettledOrReplanned(t *testing.T) {
	for _, current := range []int64{1, 3} {
		t.Run(fmt.Sprint(current), func(t *testing.T) {
			a, fake := localFirstMixedHandoverFixture(t)
			handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
			a.ConfirmHandover()
			fake.snapshot.Items[0].AvailableQuantity = current
			fake.snapshot.Revision = "external-change"
			err := a.processLocalFirstHandover(a.ctx, a.apiKey, a.state.ItemOptions)
			tx, readErr := readHandover(a.handoverPath("character-a", "PVP"), "character-a", "PVP")
			if err == nil || readErr != nil || tx.Items[0].Stage != "conflict" || len(fake.writes) != 0 || !a.GetState().Handover.Pending || a.GetState().Handover.Completed || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 1 {
				t.Fatalf("unsent external target was falsely settled/replanned: %+v %v", tx, err)
			}
			state := a.GetState()
			if !state.Handover.Scoped || state.Handover.Background || a.handoverBlocksItemLocked("another-item") || !a.handoverBlocksItemLocked(trackingBSGID) {
				t.Fatal("conflict lost immutable affected-item scope or Resume visibility")
			}
			a.restoreHandoverLocked()
			if !a.GetState().Handover.Scoped || a.GetState().Handover.Background {
				t.Fatal("restart lost conflict scope")
			}
			a.ConfirmHandover()
			_ = a.processLocalFirstHandover(a.ctx, a.apiKey, a.state.ItemOptions)
			if len(fake.writes) != 0 || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 1 {
				t.Fatal("fixed conflict plan replayed own or Market deduction")
			}
		})
	}
}

func TestLocalFirstHandoverSentNoOpOnlyReadsOnResume(t *testing.T) {
	a, fake := localFirstMixedHandoverFixture(t)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{OK: true, Changed: false, Revision: fake.snapshot.Revision}, nil
	}
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
	a.ConfirmHandover()
	if err := a.processLocalFirstHandover(a.ctx, a.apiKey, a.state.ItemOptions); err == nil {
		t.Fatal("no-op was settled")
	}
	tx, err := readHandover(a.handoverPath("character-a", "PVP"), "character-a", "PVP")
	if err != nil || tx.Items[0].Stage != "sent" || len(fake.writes) != 1 {
		t.Fatal("sent phase was not kept")
	}
	if !a.GetState().Handover.Background || !a.GetState().Handover.Scoped || a.handoverBlocksItemLocked("unrelated-item") || !a.handoverBlocksItemLocked(trackingBSGID) {
		t.Fatal("sent immutable plan blocked unrelated items or lost GET-only eligibility")
	}
	reads := fake.loads
	a.restoreHandoverLocked()
	_ = a.processLocalFirstHandover(a.ctx, a.apiKey, a.state.ItemOptions)
	if len(fake.writes) != 1 || fake.loads != reads+1 || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 1 {
		t.Fatal("uncertain/no-op write was retried or own receipt duplicated")
	}
}

func TestLocalFirstHandoverReceiptCommittedButPhaseSaveFailsCanResume(t *testing.T) {
	a, fake := localFirstMixedHandoverFixture(t)
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
	snapshot, _, err := a.localFirstSnapshot(a.apiKey, a.state.ItemOptions)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := a.planHandoverSnapshot(a.state.ItemOptions, a.state.Handover.Items, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	tx.Version = 2
	tx.Items[0].Stage, tx.Items[0].Revision = "unsent", snapshot.Revision
	if err = a.saveHandover(tx); err != nil {
		t.Fatal(err)
	}
	a.handover = tx
	a.state.Handover.Pending = true
	path := a.handoverPath(tx.CharUID, tx.Mode)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = a.commitLocalFirstHandover(tx); err == nil || tx.LocalDone || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 1 || len(fake.writes) != 0 {
		t.Fatal("failed phase commit falsely became durable or lost the committed own receipt")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	state := a.ConfirmHandover()
	if state.Handover == nil || !state.Handover.Background || !a.handover.LocalDone {
		t.Fatal("same-app retry did not persist its local phase")
	}
	if err = a.processLocalFirstHandover(a.ctx, a.apiKey, a.state.ItemOptions); err != nil {
		t.Fatal(err)
	}
	requireHandoverComplete(t, a.GetState())
	if handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 1 || len(fake.writes) != 1 {
		t.Fatal("phase-save recovery repeated a debit")
	}
}

type cancelAfterSentContext struct {
	context.Context
	cancel context.CancelFunc
	sent   func() bool
}

func (c cancelAfterSentContext) Err() error {
	if c.sent() {
		c.cancel()
	}
	return c.Context.Err()
}

func TestLocalFirstHandoverCancellationBeforePatchRestoresUnsent(t *testing.T) {
	a, fake := localFirstMixedHandoverFixture(t)
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
	a.ConfirmHandover()
	base, cancel := context.WithCancel(a.ctx)
	defer cancel()
	ctx := cancelAfterSentContext{Context: base, cancel: cancel, sent: func() bool {
		tx, e := readHandover(a.handoverPath("character-a", "PVP"), "character-a", "PVP")
		return e == nil && tx.Items[0].Stage == "sent"
	}}
	if err := a.processLocalFirstHandover(ctx, a.apiKey, a.state.ItemOptions); !errors.Is(err, context.Canceled) {
		t.Fatal("post-prepare cancellation was not preserved", err)
	}
	tx, err := readHandover(a.handoverPath("character-a", "PVP"), "character-a", "PVP")
	if err != nil || tx.Items[0].Stage != "unsent" || !tx.LocalDone || len(fake.writes) != 0 || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 1 {
		t.Fatalf("known unsent cancellation was frozen as uncertain or own receipt lost: %+v %v", tx, err)
	}
	if err = a.processLocalFirstHandover(a.ctx, a.apiKey, a.state.ItemOptions); err != nil {
		t.Fatal(err)
	}
	requireHandoverComplete(t, a.GetState())
	if len(fake.writes) != 1 || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 1 {
		t.Fatal("proven unsent resume was not exactly once")
	}
}

func TestLocalFirstHandoverCanceledOldProfileCannotPublishIntoNewProfile(t *testing.T) {
	a, fake := localFirstMixedHandoverFixture(t)
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
	a.ConfirmHandover()
	key, opts := a.apiKey, a.state.ItemOptions
	fake.loadHook = func(int) (market.TrackedSnapshot, error) {
		changed := opts
		changed.Mode = "pve"
		changed.CharacterUID = "new-character"
		a.SetOptions(changed)
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	if err := a.processLocalFirstHandover(a.ctx, key, opts); !errors.Is(err, context.Canceled) {
		t.Fatal("stale profile callback was accepted", err)
	}
	state := a.GetState()
	if len(fake.writes) != 0 || state.CharacterUID != "new-character" || state.Handover != nil || state.Error != "" {
		t.Fatalf("old callback changed the new profile: %+v", state)
	}
	tx, err := readHandover(a.handoverPath(opts.CharacterUID, opts.Mode), opts.CharacterUID, opts.Mode)
	if err != nil || !tx.LocalDone || tx.Items[0].Stage != "unsent" {
		t.Fatal("old profile's durable own settlement was lost", err)
	}
}

func handoverDraftForTest(a *ItemWindowApp, rows ...ItemHandoverEntry) {
	a.OpenHandover()
	a.mu.Lock()
	a.state.Handover.Items = append([]ItemHandoverEntry{}, rows...)
	a.mu.Unlock()
}

func handoverRowForTest(id string, quantity int64, kind string) ItemHandoverEntry {
	return ItemHandoverEntry{ID: id + "-" + kind, ItemID: id, Name: "Test item", Quantity: quantity, Kind: kind}
}

func handoverStockForTest(t *testing.T, a *ItemWindowApp, id string) localprogress.Item {
	t.Helper()
	snapshot, err := a.localStore.Load(a.state.CharacterUID, strings.ToUpper(a.state.Mode))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.Items {
		if row.ItemID == id {
			return row
		}
	}
	t.Fatalf("stock item %s missing: %+v", id, snapshot)
	return localprogress.Item{}
}

func requireHandoverComplete(t *testing.T, state ItemState) {
	t.Helper()
	if state.Handover == nil || !state.Handover.Completed || state.Handover.Busy || state.Handover.Pending || state.Handover.Error != "" {
		t.Fatalf("hand-in did not complete: %+v", state.Handover)
	}
}

func requireHandoverPending(t *testing.T, a *ItemWindowApp, state ItemState) {
	t.Helper()
	if state.Handover == nil || !state.Handover.Pending || state.Handover.Completed || state.Handover.Busy || state.Handover.Error == "" {
		t.Fatalf("unverified hand-in was not kept pending: %+v", state.Handover)
	}
	if a.canAddLocalForTest() {
		t.Fatal("pending hand-in allowed a stock shortcut")
	}
	if (a.handover == nil || a.handover.Version == 1 || !a.handover.LocalDone) && state.Handover.Scoped {
		t.Fatal("legacy, corrupt or uncommitted own plan claimed an affected-item scope")
	}
}

func TestHandoverLocalTypedRemovalKeepsCollectedAndQuestProgress(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "fir")
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 1, "regular"), handoverRowForTest(trackingBSGID, 1, "fir"))
	state := confirmAndDrainHandoverForTest(a)
	requireHandoverComplete(t, state)
	row := handoverStockForTest(t, a, trackingBSGID)
	if row.LocalCollectedQuantity != 3 || row.LocalCollectedRegularQuantity != 2 || row.LocalCollectedFIRQuantity != 1 ||
		row.LocalUsedQuantity != 2 || row.LocalUsedRegularQuantity != 1 || row.LocalUsedFIRQuantity != 1 ||
		row.LocalAvailableQuantity != 1 || row.LocalRegularQuantity != 1 || row.LocalFIRQuantity != 0 {
		t.Fatalf("collected stock was deleted or physical qualities were mixed: %+v", row)
	}
	if len(fake.writes) != 0 || fake.quests.Quests[0].Completed || fake.quests.Quests[1].Completed || state.Tracking.AvailableQuantity != 1 {
		t.Fatalf("local hand-in wrote Market progress or kept consumed stock available: writes=%+v state=%+v", fake.writes, state)
	}
	if _, err := os.Stat(a.handoverPath("character-a", "PVP")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed operation journal was not cleared: %v", err)
	}
	confirmAndDrainHandoverForTest(a)
	if again := handoverStockForTest(t, a, trackingBSGID); again.LocalUsedQuantity != 2 || len(fake.writes) != 0 {
		t.Fatalf("repeated confirmation deducted stock again: %+v", again)
	}
}

func TestHandoverFreshOpenStartsEmptyWithOrWithoutCurrentItem(t *testing.T) {
	for _, currentItem := range []bool{false, true} {
		t.Run(fmt.Sprintf("current-item-%t", currentItem), func(t *testing.T) {
			a, fake := newDevOnlyTestApp(t)
			if !currentItem {
				a.state.Item = nil
				a.state.Tracking = nil
			}
			state := a.OpenHandover()
			if state.Handover == nil || !state.Handover.Open || state.Handover.Error != "" || state.Handover.CharUID != "character-a" ||
				len(state.Handover.Items) != 0 || state.Handover.Pending || state.Handover.Busy || state.Handover.Completed || len(fake.writes) != 0 {
				t.Fatalf("fresh hand-in was seeded from the tracker or required a current item: %+v", state.Handover)
			}
			a.CloseHandover()
			if reopened := a.OpenHandover(); reopened.Handover == nil || !reopened.Handover.Open || len(reopened.Handover.Items) != 0 || reopened.Handover.Error != "" {
				t.Fatalf("closing an unused hand-in changed fresh-open behavior: %+v", reopened.Handover)
			}
		})
	}
}

func TestHandoverAutomaticOverrideNeverChangesGlobalScanning(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(fmt.Sprintf("global-automatic-%t", global), func(t *testing.T) {
			a, _ := newDevOnlyTestApp(t)
			a.state.Automatic = global
			state := a.OpenHandover()
			if state.Automatic != global || !state.Handover.Automatic || !handoverAutomaticScan(state) {
				t.Fatalf("fresh hand-in did not enable its independent automatic switch: %+v", state)
			}
			state = a.SetHandoverAutomatic(false)
			if state.Automatic != global || state.Handover.Automatic || handoverAutomaticScan(state) {
				t.Fatalf("modal automatic switch changed global scanning or stayed active: %+v", state)
			}
			state = a.SetHandoverAutomatic(true)
			if state.Automatic != global || !handoverAutomaticScan(state) {
				t.Fatal("modal automatic switch could not be independently re-enabled")
			}
			a.mu.Lock()
			a.state.Handover.Busy = true
			a.mu.Unlock()
			state = a.SetHandoverAutomatic(false)
			if !state.Handover.Automatic || handoverAutomaticScan(state) || state.Automatic != global {
				t.Fatal("busy hand-in changed its frozen toggle or permitted an automatic scan")
			}
			a.mu.Lock()
			a.state.Handover.Busy = false
			a.mu.Unlock()
			state = a.CloseHandover()
			if state.Handover != nil || state.Automatic != global || handoverAutomaticScan(state) != global {
				t.Fatalf("closing the modal did not restore global scanning behavior: %+v", state)
			}
		})
	}
}

func TestHandoverTypedLocalFirstRemovesOnlySelectedMarketQuality(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		regular, fir                     int64
		wantMarketRegular, wantMarketFIR int64
		wantLocalRegular, wantLocalFIR   int64
		wantUsedRegular, wantUsedFIR     int64
	}{
		{name: "regular-only", regular: 3, wantMarketFIR: 1, wantLocalFIR: 1, wantUsedRegular: 1},
		{name: "fir-only", fir: 2, wantMarketRegular: 2, wantLocalRegular: 1, wantUsedFIR: 1},
		{name: "both-types", regular: 2, fir: 2, wantMarketRegular: 1, wantUsedRegular: 1, wantUsedFIR: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake, dir := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
			fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
			fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
			fixtureAddAndSyncForTest(t, a, trackingItemUID, "fir")
			fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
			fixtureAddAndSyncForTest(t, a, trackingItemUID, "fir")
			before := handoverStockForTest(t, a, trackingBSGID)
			if len(fake.writes) != 3 || before.MarketRegularQuantity != 2 || before.MarketFIRQuantity != 1 || before.LocalRegularQuantity != 1 || before.LocalFIRQuantity != 1 {
				t.Fatalf("typed mixed fixture was not M R2/F1 + L R1/F1: %+v", before)
			}
			rows := []ItemHandoverEntry{}
			if tc.regular > 0 {
				rows = append(rows, handoverRowForTest(trackingBSGID, tc.regular, "regular"))
			}
			if tc.fir > 0 {
				rows = append(rows, handoverRowForTest(trackingBSGID, tc.fir, "fir"))
			}
			handoverDraftForTest(a, rows...)
			state := confirmAndDrainHandoverForTest(a)
			requireHandoverComplete(t, state)
			row := handoverStockForTest(t, a, trackingBSGID)
			wantMarket := tc.wantMarketRegular + tc.wantMarketFIR
			if len(fake.writes) != 4 || fake.writes[3].Quantity != wantMarket || fake.snapshot.Items[0].AvailableQuantity != wantMarket ||
				row.MarketRegularQuantity != tc.wantMarketRegular || row.MarketFIRQuantity != tc.wantMarketFIR ||
				row.LocalRegularQuantity != tc.wantLocalRegular || row.LocalFIRQuantity != tc.wantLocalFIR ||
				row.LocalUsedRegularQuantity != tc.wantUsedRegular || row.LocalUsedFIRQuantity != tc.wantUsedFIR ||
				row.LocalCollectedQuantity != 2 || row.LocalCollectedRegularQuantity != 1 || row.LocalCollectedFIRQuantity != 1 ||
				state.Tracking.AvailableQuantity != wantMarket+tc.wantLocalRegular+tc.wantLocalFIR {
				t.Fatalf("type selection removed another quality, duplicated Market stock or skipped local-first: row=%+v writes=%+v state=%+v", row, fake.writes, state.Tracking)
			}
			assertLocalStockFileOnly(t, dir, trackingBSGID, 2)
		})
	}
}

func TestHandoverMixedStockConsumesLocalBeforeOneAbsoluteMarketWrite(t *testing.T) {
	a, fake, dir := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	for i := 0; i < 5; i++ {
		fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	}
	if len(fake.writes) != 3 || handoverStockForTest(t, a, trackingBSGID).LocalAvailableQuantity != 2 {
		t.Fatal("mixed fixture did not establish Market 3 and Local 2")
	}
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 4, "regular"))
	state := confirmAndDrainHandoverForTest(a)
	requireHandoverComplete(t, state)
	if len(fake.writes) != 4 || fake.writes[3] != (stockWrite{"character-a", trackingBSGID, "revision-saved-3", 1}) ||
		fake.snapshot.Items[0].AvailableQuantity != 1 || state.Tracking.AvailableQuantity != 1 {
		t.Fatalf("hand-in did not use local-first then one absolute Market target: writes=%+v state=%+v", fake.writes, state)
	}
	row := handoverStockForTest(t, a, trackingBSGID)
	if row.LocalCollectedQuantity != 2 || row.LocalUsedQuantity != 2 || row.LocalAvailableQuantity != 0 || row.MarketRegularQuantity != 1 || row.MarketAvailableQuantity != 0 {
		t.Fatalf("Market quantity was duplicated locally or quality annotations were not reduced: %+v", row)
	}
	assertLocalStockFileOnly(t, dir, trackingBSGID, 2)
	confirmAndDrainHandoverForTest(a)
	if len(fake.writes) != 4 || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 2 {
		t.Fatal("completed mixed operation was repeated")
	}
}

func TestLegacyV1HandoverUncertainMarketAppliedSettlesByFreshReadback(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	for i := 0; i < 4; i++ {
		fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	}
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		fake.commitWrite(write)
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
	state := a.confirmHandoverLegacy()
	requireHandoverComplete(t, state)
	row := handoverStockForTest(t, a, trackingBSGID)
	if len(fake.writes) != 4 || fake.snapshot.Items[0].AvailableQuantity != 2 || row.LocalUsedQuantity != 1 || row.MarketRegularQuantity != 2 {
		t.Fatalf("confirmed readback was retried or missed a single debit: writes=%+v row=%+v", fake.writes, row)
	}
}

func TestLegacyV1HandoverUncertainAppliedFailedVerificationKeepsRemainingQuality(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "fir")
	initialReads := fake.loads
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		fake.commitWrite(write)
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	fake.loadHook = func(read int) (market.TrackedSnapshot, error) {
		if read == initialReads+3 {
			return market.TrackedSnapshot{}, market.ErrProgressUnavailable
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 1, "regular"))
	requireHandoverPending(t, a, a.confirmHandoverLegacy())
	fake.writeHook = nil
	fake.loadHook = nil
	requireHandoverComplete(t, a.confirmHandoverLegacy())
	row := handoverStockForTest(t, a, trackingBSGID)
	if len(fake.writes) != 4 || fake.snapshot.Items[0].AvailableQuantity != 2 || row.MarketRegularQuantity != 1 || row.MarketFIRQuantity != 1 {
		t.Fatalf("own confirmed removal was reconciled as external stock and lost known qualities: writes=%+v row=%+v", fake.writes, row)
	}
}

func TestHandoverRestartPreparedAppliedKeepsRemainingMarketQuality(t *testing.T) {
	a, fake, dir := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "fir")
	draft := []ItemHandoverEntry{handoverRowForTest(trackingBSGID, 1, "regular")}
	tx, err := a.planHandover(a.ctx, a.apiKey, a.state.ItemOptions, draft)
	if err != nil {
		t.Fatal(err)
	}
	tx.LocalDone = true
	tx.Items[0].Stage = "prepared"
	tx.Items[0].Target = 2
	tx.Items[0].MarketRemaining = handoverRemainingKinds(a.localSnapshot.Items[0], tx.Items[0].Market, 2)
	tx.Items[0].Revision = fake.snapshot.Revision
	if err := a.saveHandover(tx); err != nil {
		t.Fatal(err)
	}
	// A feltételes PATCH lefut, de a megerősítés és a típusnyugta előtt leáll a folyamat.
	fake.commitWrite(stockWrite{"character-a", trackingBSGID, tx.Items[0].Revision, 2})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	restarted := &ItemWindowApp{ctx: ctx, dir: a.dir, apiKey: a.apiKey, tracker: fake,
		devRequirements: a.devRequirements, localStore: localprogress.NewStore(dir), state: a.GetState()}
	restarted.mu.Lock()
	restarted.restoreHandoverLocked()
	restarted.mu.Unlock()
	// Az induláskori GET nem minősítheti külső változásnak a saját mentett targetet.
	restarted.refreshLocalProgress(restarted.ctx, restarted.apiKey, restarted.state.ItemOptions, *restarted.state.Item)
	restarted.OpenHandover()
	requireHandoverComplete(t, confirmAndDrainHandoverForTest(restarted))
	row := handoverStockForTest(t, restarted, trackingBSGID)
	if len(fake.writes) != 3 || row.MarketRegularQuantity != 1 || row.MarketFIRQuantity != 1 {
		t.Fatalf("restart lost remaining known types before settling the saved operation: writes=%+v row=%+v", fake.writes, row)
	}
}

func TestLegacyV1HandoverUncertainUnappliedResumeDoesNotRepeatLocalDebit(t *testing.T) {
	a, fake, dir := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	for i := 0; i < 5; i++ {
		fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	}
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 4, "regular"))
	state := a.confirmHandoverLegacy()
	requireHandoverPending(t, a, state)
	if len(fake.writes) != 4 || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 2 {
		t.Fatal("uncertain operation was blindly retried or did not record the local part")
	}
	data, err := os.ReadFile(a.handoverPath("character-a", "PVP"))
	if err != nil || strings.Contains(string(data), a.apiKey) || strings.Contains(string(data), "apiKey") || strings.Contains(string(data), "requiredQuantity") {
		t.Fatalf("operation intent was missing or contains a key/Market snapshot: %s %v", data, err)
	}
	a.CloseHandover()
	if a.canAddLocalForTest() {
		t.Fatal("closing a pending modal re-enabled stock changes")
	}
	// A készlet ugyanazon revíziója igazolja, hogy a korábbi feltételes írás nem futott le.
	fake.writeHook = nil
	a.OpenHandover()
	state = a.confirmHandoverLegacy()
	requireHandoverComplete(t, state)
	if len(fake.writes) != 5 || fake.writes[4].Quantity != 1 || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 2 {
		t.Fatalf("resuming repeated local consumption or changed the absolute target: %+v", fake.writes)
	}
	assertLocalStockFileOnly(t, dir, trackingBSGID, 2)
}

func TestLegacyV1HandoverUncertainChangedRevisionAtOldCountNeverRetries(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	for i := 0; i < 4; i++ {
		fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	}
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
	requireHandoverPending(t, a, a.confirmHandoverLegacy())
	// A régi számláló új revízióval lehet már levont, majd külső hozzáadással pótlott készlet.
	fake.snapshot.Revision = "external-write-restored-old-count"
	fake.writeHook = nil
	state := a.confirmHandoverLegacy()
	requireHandoverPending(t, a, state)
	if len(fake.writes) != 4 || fake.snapshot.Items[0].AvailableQuantity != 3 || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 1 {
		t.Fatalf("new revision at the old count caused a duplicate remote deduction: %+v", fake.writes)
	}
}

func TestLegacyV1HandoverDefiniteRejectionResumesAgainstFreshMarketWithoutDoubleLocal(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		externalAdd bool
	}{
		{name: "revision-conflict", err: market.ErrProgressConflict, externalAdd: true},
		{name: "rate-limited", err: market.ErrRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
			for i := 0; i < 5; i++ {
				fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
			}
			fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
				if tc.externalAdd {
					fake.mu.Lock()
					fake.snapshot.Items[0].AvailableQuantity++
					fake.snapshot.Revision = "external-add-revision"
					fake.mu.Unlock()
				}
				return market.ProgressWriteResult{}, tc.err
			}
			handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 4, "regular"))
			requireHandoverPending(t, a, a.confirmHandoverLegacy())
			if len(fake.writes) != 4 || a.handover.Items[0].Stage != "rejected" || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 2 {
				t.Fatalf("definite rejection was retried or treated as an applied operation: %+v", fake.writes)
			}
			fake.writeHook = nil
			requireHandoverComplete(t, a.confirmHandoverLegacy())
			want, revision := int64(1), "revision-saved-3"
			if tc.externalAdd {
				want, revision = 2, "external-add-revision"
			}
			if len(fake.writes) != 5 || fake.writes[4].Quantity != want || fake.writes[4].Revision != revision || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 2 {
				t.Fatalf("rejected resume reused a stale target/revision or deducted local stock twice: %+v", fake.writes)
			}
		})
	}
}

func TestHandoverJournalSaveFailureDeductsNeitherSource(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "fir")
	if err := os.WriteFile(filepath.Join(a.dir, "handovers"), []byte("blocks journal directory"), 0600); err != nil {
		t.Fatal(err)
	}
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 1, "regular"), handoverRowForTest(trackingBSGID, 1, "fir"))
	state := confirmAndDrainHandoverForTest(a)
	row := handoverStockForTest(t, a, trackingBSGID)
	if state.Handover.Error == "" || state.Handover.Pending || state.Handover.Completed || row.LocalUsedQuantity != 0 || row.LocalAvailableQuantity != 2 || len(fake.writes) != 0 {
		t.Fatalf("a non-durable intent changed stock: state=%+v row=%+v writes=%+v", state.Handover, row, fake.writes)
	}
}

func TestHandoverLegacyUnknownLocalPoolIsAssignedJointlyAcrossQualities(t *testing.T) {
	for _, marketKind := range []string{"regular", "fir"} {
		t.Run("market-"+marketKind, func(t *testing.T) {
			a, fake, dir := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
			if marketKind == "fir" {
				fixtureAddAndSyncForTest(t, a, trackingItemUID, "fir")
			} else {
				fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
			}
			// A migrált helyi darab ismeretlen minősége mindkét kéréshez használható,
			// de ugyanaz a darab csak egyszer adható le.
			files, err := filepath.Glob(filepath.Join(dir, "*.json"))
			if err != nil || len(files) != 1 {
				t.Fatalf("fixture local file missing: %v %v", files, err)
			}
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			fields["stock"], err = json.Marshal(map[string]int64{trackingBSGID: 1})
			if err != nil {
				t.Fatal(err)
			}
			data, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(files[0], data, 0600); err != nil {
				t.Fatal(err)
			}
			handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 1, "regular"), handoverRowForTest(trackingBSGID, 1, "fir"))
			requireHandoverComplete(t, confirmAndDrainHandoverForTest(a))
			row := handoverStockForTest(t, a, trackingBSGID)
			if len(fake.writes) != 2 || fake.writes[1].Quantity != 0 || row.LocalCollectedQuantity != 1 || row.LocalUsedQuantity != 1 ||
				row.LocalUsedRegularQuantity != 0 || row.LocalUsedFIRQuantity != 0 || row.LocalAvailableQuantity != 0 || row.MarketRegularQuantity != 0 || row.MarketFIRQuantity != 0 {
				t.Fatalf("joint unknown plan rejected a feasible quality split or consumed unknown twice: writes=%+v row=%+v", fake.writes, row)
			}
		})
	}
}

func TestLegacyV1HandoverRestartRestoresJournalAndIdempotentLocalReceipt(t *testing.T) {
	a, fake, dir := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	for i := 0; i < 5; i++ {
		fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	}
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 4, "regular"))
	requireHandoverPending(t, a, a.confirmHandoverLegacy())
	// A lemeznyugta már kész, de a journal localDone írása előtt is leállhat a folyamat.
	a.handover.LocalDone = false
	if err := a.saveHandover(a.handover); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	restarted := &ItemWindowApp{ctx: ctx, dir: a.dir, apiKey: a.apiKey, tracker: fake,
		devRequirements: a.devRequirements, localStore: localprogress.NewStore(dir), state: a.GetState()}
	restarted.mu.Lock()
	restarted.restoreHandoverLocked()
	restarted.mu.Unlock()
	state := restarted.GetState()
	if state.Handover == nil || !state.Handover.Pending || state.Handover.Open || len(state.Handover.Items) != 1 || restarted.handover == nil {
		t.Fatalf("restart did not restore the pending transaction: %+v", state.Handover)
	}
	fake.writeHook = nil
	restarted.OpenHandover()
	state = restarted.confirmHandoverLegacy()
	requireHandoverComplete(t, state)
	row := handoverStockForTest(t, restarted, trackingBSGID)
	if len(fake.writes) != 5 || row.LocalUsedQuantity != 2 || row.LocalCollectedQuantity != 2 || row.MarketRegularQuantity != 1 {
		t.Fatalf("restart deducted the recorded local receipt twice: writes=%+v stock=%+v", fake.writes, row)
	}
}

func TestHandoverBatchAggregatesOnePatchPerIDWithFreshRevision(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	const second = "591aef7986f774139d495f03"
	fake.snapshot.Items[0].AvailableQuantity = 5
	fake.snapshot.Items[0].RequiredQuantity = 5
	fake.snapshot.Items[0].Sources[0].Amount = 5
	fake.snapshot.Items[0].Sources[0].RemainingRequiredQuantity = 5
	fake.snapshot.Items = append(fake.snapshot.Items, market.TrackedItem{ItemID: second, Name: "Other item", RequiredQuantity: 4, AvailableQuantity: 4,
		Sources: []market.TrackedSource{{Type: "quest", QuestID: "quest-a", Amount: 4, RemainingRequiredQuantity: 4}}})
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if write.Revision != fake.snapshot.Revision {
			return market.ProgressWriteResult{}, market.ErrProgressConflict
		}
		for i := range fake.snapshot.Items {
			if fake.snapshot.Items[i].ItemID == write.Item {
				fake.snapshot.Items[i].AvailableQuantity = write.Quantity
				fake.snapshot.Items[i].RemainingQuantity = max(0, fake.snapshot.Items[i].RequiredQuantity-write.Quantity)
				fake.snapshot.Revision = fmt.Sprintf("batch-revision-%d", len(fake.writes))
				return market.ProgressWriteResult{OK: true, Changed: true, Revision: fake.snapshot.Revision}, nil
			}
		}
		return market.ProgressWriteResult{}, market.ErrProgressConflict
	}
	primeLocalFirstForTest(t, a)
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 1, "regular"), handoverRowForTest(trackingBSGID, 2, "regular"), handoverRowForTest(second, 2, "regular"))
	requireHandoverComplete(t, confirmAndDrainHandoverForTest(a))
	if len(fake.writes) != 2 || fake.writes[0] != (stockWrite{"character-a", trackingBSGID, "revision-1", 2}) ||
		fake.writes[1] != (stockWrite{"character-a", second, "batch-revision-1", 2}) {
		t.Fatalf("batch reused a stale revision or sent one request per duplicate row: %+v", fake.writes)
	}
}

func TestHandoverInvalidQuantityQualityAndAmbiguousMarketIDWriteNothing(t *testing.T) {
	for _, tc := range []struct {
		name      string
		quantity  int64
		kind      string
		duplicate bool
		firOnly   bool
	}{
		{name: "zero", kind: "regular"},
		{name: "negative", quantity: -1, kind: "regular"},
		{name: "over-limit", quantity: localprogress.MaxQuantity + 1, kind: "regular"},
		{name: "invalid-kind", quantity: 1, kind: "FiR"},
		{name: "not-enough", quantity: 3, kind: "regular"},
		{name: "known-fir-is-not-physical-regular", quantity: 1, kind: "regular", firOnly: true},
		{name: "duplicate-market-variant", quantity: 1, kind: "regular", duplicate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
			if tc.firOnly {
				fixtureAddAndSyncForTest(t, a, trackingItemUID, "fir")
			} else {
				fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
			}
			if tc.duplicate {
				fake.snapshot.Items = append(fake.snapshot.Items, fake.snapshot.Items[0])
			}
			primeLocalFirstForTest(t, a)
			beforeWrites := len(fake.writes)
			handoverDraftForTest(a, handoverRowForTest(trackingBSGID, tc.quantity, tc.kind))
			state := confirmAndDrainHandoverForTest(a)
			if state.Handover.Error == "" || state.Handover.Pending || state.Handover.Completed || len(fake.writes) != beforeWrites ||
				fake.snapshot.Items[0].AvailableQuantity != 1 || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 0 {
				t.Fatalf("invalid selection changed stock or created an executable operation: %+v writes=%+v", state.Handover, fake.writes)
			}
			if _, err := os.Stat(a.handoverPath("character-a", "PVP")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid hand-in created a journal: %v", err)
			}
		})
	}
}

func TestHandoverInspectCapturesEveryWindowWithoutOCRDuplicateStock(t *testing.T) {
	a, _ := newDevOnlyTestApp(t)
	items := []market.Item{{UID: trackingItemUID, BSGID: trackingBSGID, Name: "Immobilizing splint"},
		{UID: "second-uid", BSGID: "591aef7986f774139d495f03", Name: "Augmentin antibiotic pills"}}
	a.OpenHandover()
	result := scanner.Result{InspectOpen: true, TitleVariants: [][]string{{"Immobilizing splint", "IMMOBILIZING SPLINT"}, {"Augmentin antibiotic pills"}}}
	a.captureHandoverScan(result, items, a.state.ItemOptions)
	state := a.GetState()
	if len(state.Handover.Items) != 2 || state.Handover.Items[0].ItemID != trackingBSGID || state.Handover.Items[1].ItemID != items[1].BSGID {
		t.Fatalf("capture retained only the final Inspect or counted OCR variants as copies: %+v", state.Handover.Items)
	}
	a.UpdateHandoverItem(trackingBSGID, 3, "fir")
	a.captureHandoverScan(result, items, a.state.ItemOptions)
	state = a.GetState()
	if len(state.Handover.Items) != 2 || state.Handover.Items[0].Quantity != 3 || state.Handover.Items[0].Kind != "fir" {
		t.Fatalf("repeated OCR reset user quantities or duplicated items: %+v", state.Handover.Items)
	}
	a.SplitHandoverItem(trackingBSGID)
	a.SplitHandoverItem(trackingBSGID)
	state = a.GetState()
	if len(state.Handover.Items) != 3 || state.Handover.Items[2].Kind != "regular" {
		t.Fatalf("explicit quality split was not limited to two rows: %+v", state.Handover.Items)
	}
	stale := a.state.ItemOptions
	stale.revision--
	a.captureHandoverScan(scanner.Result{InspectOpen: true, TitleVariants: [][]string{{"Third item"}}}, append(items, market.Item{UID: "third", BSGID: "third-item", Name: "Third item"}), stale)
	if len(a.GetState().Handover.Items) != 3 {
		t.Fatal("stale character/options scan entered the hand-in")
	}
}

func TestHandoverScanAndSearchCannotSeedAnotherDraftGeneration(t *testing.T) {
	a, _ := newDevOnlyTestApp(t)
	item := market.Item{UID: trackingItemUID, BSGID: trackingBSGID, Name: "Immobilizing splint"}
	result := scanner.Result{InspectOpen: true, TitleVariants: [][]string{{item.Name}}}
	opts := a.state.ItemOptions
	outside := handoverScanGeneration(a.GetState())
	if outside != 0 {
		t.Fatal("scan outside a hand-in was bound to an active generation")
	}
	a.OpenHandover()
	first := handoverScanGeneration(a.GetState())
	if first == 0 {
		t.Fatal("fresh hand-in has no capture generation")
	}
	// A dialog előtt indult OCR/keresés a később megnyitott üres listát nem töltheti.
	a.captureHandoverScan(result, []market.Item{item}, opts, outside)
	a.acceptItem(item, true, opts, nil, outside)
	if len(a.GetState().Handover.Items) != 0 {
		t.Fatal("recognition started outside the dialog seeded the new hand-in")
	}
	a.CloseHandover()
	a.OpenHandover()
	second := handoverScanGeneration(a.GetState())
	if second == 0 || second == first {
		t.Fatal("reopened hand-in reused the previous capture generation")
	}
	// A lezárt korábbi dialogban indult találat az új sessionhöz sem tartozik.
	a.captureHandoverScan(result, []market.Item{item}, opts, first)
	a.acceptItem(item, true, opts, nil, first)
	if len(a.GetState().Handover.Items) != 0 {
		t.Fatal("previous hand-in recognition seeded a reopened hand-in")
	}
	a.captureHandoverScan(result, []market.Item{item}, opts, second)
	a.acceptItem(item, true, opts, nil, second)
	state := a.GetState()
	if len(state.Handover.Items) != 1 || state.Handover.Items[0].ItemID != trackingBSGID || state.Handover.Items[0].Quantity != 1 {
		t.Fatalf("current-generation OCR/search was lost or duplicated: %+v", state.Handover.Items)
	}
	if state.Handover.generation != second || cloneHandoverState(state.Handover).generation != second {
		t.Fatal("state cloning lost the private capture generation")
	}
}

func TestLegacyV1HandoverBusyFreezesOptionsAndCapturedDraft(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	before := a.state.ItemOptions
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		changed := before
		changed.CharacterUID = "character-other"
		changed.Mode = "pve"
		state := a.SetOptions(changed)
		if state.ItemOptions != before || !state.Handover.Busy {
			t.Fatalf("settings changed during an immutable hand-in: %+v", state)
		}
		a.UpdateHandoverItem(trackingBSGID+"-regular", 3, "fir")
		a.RemoveHandoverItem(trackingBSGID + "-regular")
		a.CloseHandover()
		if state = a.GetState(); !state.Handover.Open || len(state.Handover.Items) != 1 || state.Handover.Items[0].Quantity != 1 || state.Handover.Items[0].Kind != "regular" {
			t.Fatalf("busy hand-in allowed draft edits or cancellation: %+v", state.Handover)
		}
		return fake.commitWrite(write), nil
	}
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 1, "regular"))
	requireHandoverComplete(t, a.confirmHandoverLegacy())
}

func TestHandoverCorruptJournalBlocksStockWithoutDeletingEvidence(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	path := a.handoverPath("character-a", "PVP")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"version":1,"apiKey":"must-not-be-accepted"}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	state := a.OpenHandover()
	requireHandoverPending(t, a, state)
	confirmAndDrainHandoverForTest(a)
	remaining, err := os.ReadFile(path)
	if err != nil || string(remaining) != string(data) || len(fake.writes) != 0 || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 0 {
		t.Fatal("corrupt journal was overwritten/deleted or allowed stock mutation")
	}
}

func TestHandoverJournalRejectsDraftOperationMismatch(t *testing.T) {
	a, _ := newDevOnlyTestApp(t)
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	plan, err := a.planHandover(a.ctx, a.apiKey, a.state.ItemOptions, []ItemHandoverEntry{handoverRowForTest(trackingBSGID, 1, "regular")})
	if err != nil {
		t.Fatal(err)
	}
	plan.Draft[0].Quantity = 2
	if err := a.saveHandover(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := readHandover(a.handoverPath("character-a", "PVP"), "character-a", "PVP"); err == nil {
		t.Fatal("journal accepted a draft quantity different from its debit operation")
	}
	plan.Draft[0].Quantity = 1
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), a.apiKey) || strings.Contains(string(encoded), "availableQuantity") || strings.Contains(string(encoded), "sources") {
		t.Fatal("journal contains a secret or Market snapshot")
	}
}

func TestHandoverCompletionDoesNotConsumeSavedStockAgain(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	fixtureAddAndSyncForTest(t, a, trackingItemUID, "regular")
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 1, "regular"))
	requireHandoverComplete(t, confirmAndDrainHandoverForTest(a))
	a.CloseHandover()
	fake.quests.Quests[1].Completed = true
	fake.quests.Quests[1].Status = "completed"
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	row := handoverStockForTest(t, a, trackingBSGID)
	if row.LocalCollectedQuantity != 2 || row.LocalUsedQuantity != 1 || row.LocalAvailableQuantity != 1 || len(fake.writes) != 0 {
		t.Fatalf("external quest completion automatically deducted saved stock again: %+v", row)
	}
}

// A régi protokollfixture minden hozzáadást külön visszaigazolt Market
// készletként készít elõ. Ez a teszt-adatelõkészítés, nem a public Add útja.
func fixtureAddAndSyncForTest(t *testing.T, a *ItemWindowApp, uid, kind string) ItemState {
	t.Helper()
	if kind == "fir" {
		a.AddTrackedItemFIR(uid)
	} else {
		a.AddTrackedItem(uid)
	}
	if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, a.state.ItemOptions); err != nil {
		t.Fatal(err)
	}
	return a.GetState()
}
func confirmAndDrainHandoverForTest(a *ItemWindowApp) ItemState {
	state := a.ConfirmHandover()
	if state.Handover != nil && state.Handover.Background {
		_ = a.processLocalFirstHandover(a.ctx, a.apiKey, a.state.ItemOptions)
	}
	return a.GetState()
}
