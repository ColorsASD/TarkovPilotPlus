package plus

import (
	"bytes"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

// A valódi háttérfrissítés a forrás GET válaszára vár, és közben birtokolja
// a közös zárolást. A teszt nem stock-write callbacket vagy UI flaget blokkol.
func blockProgressRefreshForTest(t *testing.T, a *ItemWindowApp, fake *fakeCharacterProgress) func() {
	t.Helper()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	blockedRead := fake.loads + 1
	fake.loadHook = func(count int) (market.TrackedSnapshot, error) {
		if count == blockedRead {
			close(started)
			<-release
		}
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	a.mu.Lock()
	a.localAt = time.Time{}
	a.mu.Unlock()
	go func() {
		a.trackingMu.Lock()
		a.refreshTracking()
		a.trackingMu.Unlock()
		close(done)
	}()
	var once sync.Once
	unblock := func() {
		once.Do(func() {
			close(release)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("background progress refresh did not stop")
			}
		})
	}
	t.Cleanup(unblock)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("background progress refresh did not reach its source GET")
	}
	return unblock
}

func requireNoHandoverJournal(t *testing.T, a *ItemWindowApp) {
	t.Helper()
	if _, err := os.Stat(a.handoverPath("character-a", "PVP")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected action created a hand-in journal: %v", err)
	}
}

func TestProgressBusyCMSAddReportsBlockedRefreshWithoutWritingOrQueuing(t *testing.T) {
	a, fake, dir := newFixedAddGateTestApp(t, true, true)
	a.state.Item.Name = "CMS surgical kit"
	before := addGateFile(t, dir)
	unblock := blockProgressRefreshForTest(t, a, fake)
	for i := 0; i < 3; i++ {
		state := addAndSyncCachedTest(t, a, trackingItemUID, "fir")
		if state.Tracking == nil || state.Tracking.Status != progressBusyMessage || state.Tracking.Error != "" ||
			state.Error != "" || state.Tracking.Adding || len(fake.writes) != 0 || a.handover != nil {
			t.Fatalf("blocked CMS add was silent or changed progress: %+v", state)
		}
	}
	if !bytes.Equal(before, addGateFile(t, dir)) || len(a.trackingRequests) != 0 {
		t.Fatal("rejected Add changed local stock or queued a refresh/action")
	}
	requireNoHandoverJournal(t, a)
	unblock()
	if len(fake.writes) != 0 {
		t.Fatal("rejected Add ran after the background refresh unlocked")
	}
	state := addAndSyncCachedTest(t, a, trackingItemUID, "fir")
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 1 || state.Tracking.AvailableQuantity != 1 || state.Tracking.Error != "" {
		t.Fatalf("a fresh CMS click did not work after refresh: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
}

func TestProgressBusyConfirmReportsBlockedRefreshAndWorksAfterUnlock(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "market"}[remote], func(t *testing.T) {
			a, fake, dir := newFixedAddGateTestApp(t, remote, true)
			addAndSyncCachedTest(t, a, trackingItemUID, "fir")
			handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 1, "fir"))
			before, writes := addGateFile(t, dir), len(fake.writes)
			unblock := blockProgressRefreshForTest(t, a, fake)
			for i := 0; i < 2; i++ {
				state := a.ConfirmHandover()
				if state.Handover.Error != progressBusyMessage || state.Handover.Busy || state.Handover.Pending || state.Handover.Completed ||
					state.Error != "" || a.handover != nil || len(fake.writes) != writes || len(state.Handover.Items) != 1 {
					t.Fatalf("blocked confirmation was silent or changed its draft: %+v", state.Handover)
				}
			}
			if !bytes.Equal(before, addGateFile(t, dir)) || len(a.trackingRequests) != 0 {
				t.Fatal("blocked confirmation changed stock or queued a later operation")
			}
			requireNoHandoverJournal(t, a)
			unblock()
			if len(fake.writes) != writes {
				t.Fatal("confirmation was replayed automatically after refresh")
			}
			state := confirmAndSyncCachedTest(t, a)
			requireHandoverComplete(t, state)
			if remote {
				if len(fake.writes) != writes+1 || fake.writes[len(fake.writes)-1].Quantity != 0 {
					t.Fatalf("new confirmation failed to remove confirmed Market stock: %+v", fake.writes)
				}
			} else if row := handoverStockForTest(t, a, trackingBSGID); row.LocalCollectedQuantity != 1 || row.LocalUsedQuantity != 1 || row.LocalAvailableQuantity != 0 {
				t.Fatalf("local hand-in did not preserve collected count: %+v", row)
			}
			requireNoHandoverJournal(t, a)
		})
	}
}

func TestProgressBusyNativePressesNeverQueueWritesAndIgnoreStaleTargets(t *testing.T) {
	a, fake, dir := newFixedAddGateTestApp(t, true, false)
	a.state.AddHotkeyReady = true
	a.addHotkeyRequests = make(chan *trackedAddHotkeyTarget, 1)
	a.publishTrackedAddHotkey()
	runTrackedHotkeyTestWorker(t, a)
	before := addGateFile(t, dir)
	a.trackingMu.Lock()
	for i := 0; i < 4; i++ {
		a.requestTrackedAddHotkey()
		waitTrackedHotkeyIdle(t, a)
		if state := a.GetState(); state.Tracking.Status != progressBusyMessage || state.Tracking.Error != "" || len(fake.writes) != 0 {
			a.trackingMu.Unlock()
			t.Fatalf("blocked native action was silent or wrote stock: %+v", state.Tracking)
		}
	}
	stale := *a.addHotkeyFIRTarget.Load()
	stale.Revision--
	a.mu.Lock()
	a.state.Tracking.Status = "current item status"
	a.mu.Unlock()
	a.addTrackedItemKind(trackingItemUID, &stale, "fir")
	a.AddTrackedItem("different-item-uid")
	if state := a.GetState(); state.Tracking.Status != "current item status" || !bytes.Equal(before, addGateFile(t, dir)) || len(fake.writes) != 0 || len(a.addHotkeyRequests) != 0 {
		a.trackingMu.Unlock()
		t.Fatal("stale action changed current feedback or left queued work")
	}
	a.trackingMu.Unlock()
	requireNoHandoverJournal(t, a)
	if len(fake.writes) != 0 {
		t.Fatal("blocked native action was replayed after unlocking")
	}
	a.requestTrackedAddFIRHotkey()
	waitTrackedHotkeyIdle(t, a)
	syncCachedMarketTest(t, a)
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 1 {
		t.Fatalf("new physical action did not work after unlock: %+v", fake.writes)
	}
}

func TestProgressBusyPendingHandoverKeepsJournalAndOriginalError(t *testing.T) {
	a, fake, _ := newFixedAddGateTestApp(t, true, false)
	addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 1, "regular"))
	state := a.confirmHandoverLegacy()
	requireHandoverPending(t, a, state)
	path := a.handoverPath("character-a", "PVP")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oldError, writes := state.Handover.Error, len(fake.writes)
	a.trackingMu.Lock()
	state = a.ConfirmHandover()
	a.trackingMu.Unlock()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || state.Handover.Status != progressBusyMessage || state.Handover.Error != oldError ||
		!state.Handover.Pending || state.Handover.Busy || len(fake.writes) != writes {
		t.Fatalf("busy resume replaced journal state or its original error: %+v, err=%v", state.Handover, err)
	}
	// A hibás journalhoz nem tartozik bizonyított, folytatható tranzakció.
	a.mu.Lock()
	a.handover = nil
	a.state.Handover.Status = "Resume the previous hand-in before changing stock."
	a.mu.Unlock()
	a.trackingMu.Lock()
	state = a.ConfirmHandover()
	a.trackingMu.Unlock()
	if state.Handover.Error != oldError || state.Handover.Status != "Resume the previous hand-in before changing stock." {
		t.Fatal("busy feedback hid an invalid journal error")
	}
}
