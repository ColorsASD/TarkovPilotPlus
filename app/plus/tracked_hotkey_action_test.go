package plus

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

type trackedHotkeyRoundTripFunc func(*http.Request) (*http.Response, error)

func (f trackedHotkeyRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newTrackedHotkeyTestApp(t *testing.T) (*ItemWindowApp, *fakeTrackingAPI) {
	t.Helper()
	a, fake := newTrackingTestApp(t)
	a.state.AddHotkey = "F2"
	a.state.FIRHotkey = "F1"
	a.state.AddHotkeyReady = true
	a.addHotkeyRequests = make(chan *trackedAddHotkeyTarget, 1)
	a.publishTrackedAddHotkey()
	return a, fake
}

func runTrackedHotkeyTestWorker(t *testing.T, a *ItemWindowApp) {
	t.Helper()
	ctx, cancel := context.WithCancel(a.ctx)
	a.ctx = ctx
	done := make(chan struct{})
	go func() {
		a.runTrackedAddHotkeys()
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Items Tracker shortcut worker did not stop")
		}
	})
}

func waitTrackedHotkeyIdle(t *testing.T, a *ItemWindowApp) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for a.addHotkeyPending.Load() {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("Items Tracker shortcut action did not complete")
		}
	}
}

func trackedHotkeyAPICalls(fake *fakeTrackingAPI) (int, []stockWrite) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.loads, append([]stockWrite(nil), fake.writes...)
}

func syncTrackedHotkeyStagedTest(t *testing.T, a *ItemWindowApp, fake *fakeTrackingAPI, kind string) ItemState {
	t.Helper()
	loads, writes := trackedHotkeyAPICalls(fake)
	state := a.GetState()
	stock, err := a.loadMarketFallback(state.ItemOptions)
	if err != nil {
		t.Fatal(err)
	}
	row, unique := stockEditorItem(stock, trackingBSGID)
	wantRegular, wantFIR := int64(1), int64(0)
	if kind == "fir" {
		wantRegular, wantFIR = 0, 1
	}
	if loads != 0 || len(writes) != 0 || state.Tracking.AvailableQuantity != 1 || state.Tracking.Adding ||
		!unique || row.LocalRegularQuantity != wantRegular || row.LocalFIRQuantity != wantFIR || row.LocalAvailableQuantity != 1 {
		t.Fatalf("foreground action must save exactly one typed piece without API work: reads=%d writes=%+v row=%+v tracking=%+v", loads, writes, row, state.Tracking)
	}
	return syncCachedMarketTest(t, a)
}

func TestTrackedHotkeyAddsExactlyOneFromFreshStock(t *testing.T) {
	a, fake := newTrackedHotkeyTestApp(t)
	// The displayed/cached count deliberately differs from the API stock.
	a.trackedSnapshot = trackingFixture()
	a.trackedAt = time.Now()
	a.trackedRevision = a.state.ItemOptions.revision
	fake.snapshot.Items[0].AvailableQuantity = 3
	fake.snapshot.Items[0].RemainingQuantity = 4
	runTrackedHotkeyTestWorker(t, a)
	a.requestTrackedAddHotkey()
	waitTrackedHotkeyIdle(t, a)
	state := syncTrackedHotkeyStagedTest(t, a, fake, "regular")
	loads, writes := trackedHotkeyAPICalls(fake)
	if loads != 2 || len(writes) != 1 || writes[0] != (stockWrite{"character-a", trackingBSGID, "revision-1", 4}) {
		t.Fatalf("background upload must GET fresh stock, PATCH +1 with its revision, then read back: reads=%d writes=%+v", loads, writes)
	}
	if state.Tracking.AvailableQuantity != 4 || state.Tracking.RemainingQuantity != 3 || state.Tracking.Adding || state.Tracking.Error != "" {
		t.Fatalf("shortcut did not publish the confirmed stock: %+v", state.Tracking)
	}
}

func TestTrackedHotkeyKindsShareOnePendingWorker(t *testing.T) {
	a, _ := newTrackedHotkeyTestApp(t)
	a.publishTrackedAddHotkey()
	regular, fir := a.addHotkeyTarget.Load(), a.addHotkeyFIRTarget.Load()
	if regular == nil || fir == nil || regular.Kind != "regular" || fir.Kind != "fir" ||
		!a.trackedAddHotkeyTargetCurrent(regular) || !a.trackedAddHotkeyTargetCurrent(fir) {
		t.Fatal("the unified tracker did not publish independent typed targets")
	}
	a.requestTrackedAddFIRHotkey()
	a.requestTrackedAddHotkey()
	if len(a.addHotkeyRequests) != 1 || !a.addHotkeyPending.Load() {
		t.Fatal("a second kind bypassed the occupied worker")
	}
	if queued := <-a.addHotkeyRequests; queued != fir || queued.Kind != "fir" {
		t.Fatal("the first physical target lost its kind")
	}
	a.addHotkeyPending.Store(false)
	a.state.FIRHotkey = "off"
	a.publishTrackedAddHotkey()
	if a.addHotkeyFIRTarget.Load() != nil || a.trackedAddHotkeyTargetCurrent(fir) || a.addHotkeyTarget.Load() == nil {
		t.Fatal("disabling FiR retained its queued target or disabled Regular")
	}
}

func TestTrackedHotkeyDefaultActionsKeepPhysicalKindThroughUnifiedRouting(t *testing.T) {
	for _, kind := range []string{"regular", "fir"} {
		t.Run(kind, func(t *testing.T) {
			a, fake := newTrackedHotkeyTestApp(t)
			if a.GetState().AddHotkey != "F2" || a.GetState().FIRHotkey != "F1" {
				t.Fatal("fixture did not use Regular F2 / Found in Raid F1")
			}
			if kind == "fir" {
				a.requestTrackedAddFIRHotkey()
			} else {
				a.requestTrackedAddHotkey()
			}
			queued := <-a.addHotkeyRequests
			if queued.Kind != kind || queued.CharUID != "character-a" || queued.ItemID != trackingBSGID || queued.Revision != a.state.ItemOptions.revision {
				t.Fatalf("shortcut lost its immutable physical quality or owner: %+v", queued)
			}
			a.addHotkeyRequests <- queued
			runTrackedHotkeyTestWorker(t, a)
			waitTrackedHotkeyIdle(t, a)
			syncTrackedHotkeyStagedTest(t, a, fake, kind)
			row := handoverStockForTest(t, a, trackingBSGID)
			wantRegular, wantFIR := int64(1), int64(0)
			if kind == "fir" {
				wantRegular, wantFIR = 0, 1
			}
			_, writes := trackedHotkeyAPICalls(fake)
			if len(writes) != 1 || writes[0].Quantity != 1 || row.MarketRegularQuantity != wantRegular || row.MarketFIRQuantity != wantFIR || row.LocalCollectedQuantity != 0 {
				t.Fatalf("public hotkey action used the other quality or duplicated Market count locally: writes=%+v row=%+v", writes, row)
			}
		})
	}
}

func TestTrackedHotkeyOneDisabledActionDoesNotBlockOtherCustomChord(t *testing.T) {
	for _, kind := range []string{"regular", "fir"} {
		t.Run(kind, func(t *testing.T) {
			a, fake := newTrackedHotkeyTestApp(t)
			a.state.AddHotkey, a.state.FIRHotkey = "Ctrl+Alt+Shift+Q", "off"
			if kind == "fir" {
				a.state.AddHotkey, a.state.FIRHotkey = "off", "Ctrl+Alt+Shift+Q"
			}
			a.publishTrackedAddHotkey()
			state := a.GetState()
			if !trackedAddHotkeyEnabled(state.ItemOptions) || (a.addHotkeyTarget.Load() != nil) != (kind == "regular") ||
				(a.addHotkeyFIRTarget.Load() != nil) != (kind == "fir") || state.AddHotkeyReady != (kind == "regular") || state.FIRHotkeyReady != (kind == "fir") {
				t.Fatalf("individual off setting disabled both actions or displayed the other ready badge: %+v", state)
			}
			if kind == "fir" {
				a.requestTrackedAddHotkey()
				a.requestTrackedAddFIRHotkey()
			} else {
				a.requestTrackedAddFIRHotkey()
				a.requestTrackedAddHotkey()
			}
			runTrackedHotkeyTestWorker(t, a)
			waitTrackedHotkeyIdle(t, a)
			syncTrackedHotkeyStagedTest(t, a, fake, kind)
			_, writes := trackedHotkeyAPICalls(fake)
			row := handoverStockForTest(t, a, trackingBSGID)
			if len(writes) != 1 || (row.MarketFIRQuantity == 1) != (kind == "fir") || (row.MarketRegularQuantity == 1) != (kind == "regular") {
				t.Fatalf("disabled action queued work or custom chord lost its quality: writes=%+v row=%+v", writes, row)
			}
		})
	}
}

func TestTrackedHotkeyCaptureIsTransientAndInvalidatesQueuedTargets(t *testing.T) {
	a, fake := newTrackedHotkeyTestApp(t)
	// A teszt nem telepít natív hookot: a bindings objektum csak a pause/resume
	// konfigurációt ellenőrzi, a többi művelet ugyanazt a public workert használja.
	a.addHotkey = &trackedAddHotkey{statuses: make(chan struct{}, 1)}
	a.addHotkey.SetKeys(a.state.AddHotkey, a.state.FIRHotkey)
	settings, err := json.Marshal(a.state.ItemOptions)
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(a.dir, "item-settings.json")
	if err := os.WriteFile(settingsPath, settings, 0600); err != nil {
		t.Fatal(err)
	}
	old := a.addHotkeyFIRTarget.Load()
	a.requestTrackedAddFIRHotkey()
	state := a.SetHotkeyCapture(true)
	binding := a.addHotkey.bindings.Load()
	if !state.HotkeyCapture || state.AddHotkeyReady || state.FIRHotkeyReady || a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() != nil ||
		binding == nil || binding.regular != 0 || binding.fir != 0 || a.canAddLocalForTest() {
		t.Fatalf("capture did not pause both native and public stock actions: %+v bindings=%+v", state, binding)
	}
	a.AddTrackedItem(trackingItemUID)
	a.AddTrackedItemFIR(trackingItemUID)
	state = a.SetHotkeyCapture(false)
	binding = a.addHotkey.bindings.Load()
	if state.HotkeyCapture || !state.AddHotkeyReady || !state.FIRHotkeyReady || binding == nil || binding.regular != trackedAddHotkeyVK("F2") || binding.fir != trackedAddHotkeyVK("F1") ||
		a.trackedAddHotkeyTargetCurrent(old) || a.addHotkeyFIRTarget.Load() == old {
		t.Fatalf("ending capture changed configured shortcuts or revived an old queued target: %+v bindings=%+v", state, binding)
	}
	runTrackedHotkeyTestWorker(t, a)
	waitTrackedHotkeyIdle(t, a)
	loads, writes := trackedHotkeyAPICalls(fake)
	after, err := os.ReadFile(settingsPath)
	if err != nil || !bytes.Equal(settings, after) || loads != 0 || len(writes) != 0 {
		t.Fatalf("capture changed persisted options or executed the old pending quality: reads=%d writes=%+v settings=%s err=%v", loads, writes, after, err)
	}
}

func TestTrackedHotkeyPreservesBothDisabledChoices(t *testing.T) {
	a, _ := newTrackedHotkeyTestApp(t)
	a.state.AddHotkey = "off"
	a.state.FIRHotkey = "off"
	a.publishTrackedAddHotkey()
	a.requestTrackedAddHotkey()
	a.requestTrackedAddFIRHotkey()
	if trackedAddHotkeyEnabled(a.state.ItemOptions) || a.addHotkeyTarget.Load() != nil ||
		a.addHotkeyFIRTarget.Load() != nil || len(a.addHotkeyRequests) != 0 {
		t.Fatal("the unified tracker overrode explicitly disabled shortcut settings")
	}
}

func TestTrackedHotkeyStockSaveFailureBlocksBothKinds(t *testing.T) {
	a, _ := newTrackedHotkeyTestApp(t)
	a.state.Local = &LocalProgressState{Error: "controlled save failure", StockError: true}
	a.state.Tracking.Storage = "market"
	a.state.Tracking.MarketRequiredQuantity = 3
	a.state.Tracking.MarketAvailableQuantity = 0
	a.publishTrackedAddHotkey()
	a.requestTrackedAddHotkey()
	a.requestTrackedAddFIRHotkey()
	if a.canAddTrackedItemLocked() || a.addHotkeyTarget.Load() != nil ||
		a.addHotkeyFIRTarget.Load() != nil || len(a.addHotkeyRequests) != 0 {
		t.Fatal("a quality-save failure still allowed an unannotated Market or local increment")
	}
	// A két forrás kötelező: adatkimaradáskor az ismert Market kvóta sem írható.
	a.state.Local.StockError = false
	a.publishTrackedAddHotkey()
	if a.canAddTrackedItemLocked() || a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() != nil {
		t.Fatal("a public catalog outage allowed stock changes through a Market-only fallback")
	}
}

func TestTrackedHotkeyRejectsUnavailableAddTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ItemWindowApp)
	}{
		{"shortcut disabled", func(a *ItemWindowApp) { a.state.AddHotkey = "Off" }},
		{"shortcut unavailable", func(a *ItemWindowApp) { a.state.AddHotkeyReady = false }},
		{"API key missing", func(a *ItemWindowApp) { a.apiKey = "" }},
		{"API key unavailable", func(a *ItemWindowApp) { a.state.HasAPIKey = false }},
		{"tracker missing", func(a *ItemWindowApp) { a.tracker = nil }},
		{"item missing", func(a *ItemWindowApp) { a.state.Item = nil }},
		{"item identity missing", func(a *ItemWindowApp) { a.state.Item.UID = "" }},
		{"game item ID missing", func(a *ItemWindowApp) { a.state.Item.BSGID = "" }},
		{"tracking missing", func(a *ItemWindowApp) { a.state.Tracking = nil }},
		{"different item", func(a *ItemWindowApp) { a.state.Tracking.ItemUID = "another" }},
		{"different game item ID", func(a *ItemWindowApp) { a.state.Tracking.ItemID = "another" }},
		{"different character", func(a *ItemWindowApp) { a.state.Tracking.CharUID = "another" }},
		{"character missing", func(a *ItemWindowApp) { a.state.CharacterUID = "" }},
		{"untracked item", func(a *ItemWindowApp) { a.state.Tracking.Tracked = false }},
		{"hidden completed requirement", func(a *ItemWindowApp) { a.state.Tracking.RequiredQuantity = 0 }},
		{"busy scan", func(a *ItemWindowApp) { a.state.Busy = true }},
		{"scan error", func(a *ItemWindowApp) { a.state.Error = "scan failed" }},
		{"tracking loading", func(a *ItemWindowApp) { a.state.Tracking.Loading = true }},
		{"tracking adding", func(a *ItemWindowApp) { a.state.Tracking.Adding = true }},
		{"tracking error", func(a *ItemWindowApp) { a.state.Tracking.Error = "tracker failed" }},
		{"combined progress missing", func(a *ItemWindowApp) { a.state.Local = nil }},
		{"combined progress loading", func(a *ItemWindowApp) { a.state.Local.Loading = true }},
		{"combined progress error", func(a *ItemWindowApp) { a.state.Local.Error = "public source unavailable" }},
		{"stock annotation failure", func(a *ItemWindowApp) { a.state.Local.StockError = true }},
		{"separate inventory", func(a *ItemWindowApp) { a.state.Tracking.InventorySeparate = true }},
		{"ambiguous variants", func(a *ItemWindowApp) {
			a.state.Tracking.Variants = []*ItemTrackingState{cloneTrackingState(a.state.Tracking)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake := newTrackedHotkeyTestApp(t)
			tc.change(a)
			a.publishTrackedAddHotkey()
			a.requestTrackedAddHotkey()
			loads, writes := trackedHotkeyAPICalls(fake)
			if a.addHotkeyTarget.Load() != nil || a.addHotkeyPending.Load() || len(a.addHotkeyRequests) != 0 || loads != 0 || len(writes) != 0 {
				t.Fatalf("unavailable Add action must not queue or call the API: target=%+v pending=%v queued=%d reads=%d writes=%+v", a.addHotkeyTarget.Load(), a.addHotkeyPending.Load(), len(a.addHotkeyRequests), loads, writes)
			}
		})
	}
}

func TestTrackedHotkeyConcurrentTriggersDoNotQueueRepeatAdds(t *testing.T) {
	a, fake := newTrackedHotkeyTestApp(t)
	runTrackedHotkeyTestWorker(t, a)
	// Hold only the local state lock. The foreground worker must retain its
	// pending guard while it saves; it no longer waits for a Market request.
	a.mu.Lock()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(a.mu.Unlock) }
	t.Cleanup(release)
	a.requestTrackedAddHotkey()
	deadline := time.Now().Add(3 * time.Second)
	for len(a.addHotkeyRequests) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("shortcut worker did not consume its local action")
		}
		time.Sleep(time.Millisecond)
	}
	var triggers sync.WaitGroup
	for range 32 {
		triggers.Add(1)
		go func() {
			defer triggers.Done()
			a.requestTrackedAddHotkey()
		}()
	}
	triggers.Wait()
	if !a.addHotkeyPending.Load() || len(a.addHotkeyRequests) != 0 {
		t.Fatal("presses while a local Add is pending must not queue repeat stock changes")
	}
	release()
	waitTrackedHotkeyIdle(t, a)
	syncTrackedHotkeyStagedTest(t, a, fake, "regular")
	loads, writes := trackedHotkeyAPICalls(fake)
	if loads != 2 || len(writes) != 1 || writes[0].Quantity != 1 {
		t.Fatalf("concurrent shortcut callbacks produced repeated stock changes: reads=%d writes=%+v", loads, writes)
	}
}

func TestTrackedHotkeyConcurrentEligibleCallbacksQueueOneAction(t *testing.T) {
	a, fake := newTrackedHotkeyTestApp(t)
	var triggers sync.WaitGroup
	start := make(chan struct{})
	for range 32 {
		triggers.Add(1)
		go func() {
			defer triggers.Done()
			<-start
			a.requestTrackedAddHotkey()
		}()
	}
	close(start)
	triggers.Wait()
	loads, writes := trackedHotkeyAPICalls(fake)
	if !a.addHotkeyPending.Load() || len(a.addHotkeyRequests) != 1 || loads != 0 || len(writes) != 0 {
		t.Fatalf("eligible native callbacks must queue one action without blocking on API work: pending=%v queued=%d reads=%d writes=%+v", a.addHotkeyPending.Load(), len(a.addHotkeyRequests), loads, writes)
	}
	runTrackedHotkeyTestWorker(t, a)
	waitTrackedHotkeyIdle(t, a)
	syncTrackedHotkeyStagedTest(t, a, fake, "regular")
	loads, writes = trackedHotkeyAPICalls(fake)
	if loads != 2 || len(writes) != 1 || writes[0].Quantity != 1 {
		t.Fatalf("concurrent eligible callbacks produced repeated stock changes: reads=%d writes=%+v", loads, writes)
	}
}

func TestTrackedHotkeyQueuedTargetCannotFollowStateChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ItemWindowApp)
	}{
		{"displayed item replaced", func(a *ItemWindowApp) {
			a.state.Item.UID = "another-item"
			a.state.Tracking.ItemUID = "another-item"
		}},
		{"character replaced", func(a *ItemWindowApp) {
			a.state.CharacterUID = "character-b"
			a.state.Tracking.CharUID = "character-b"
		}},
		{"options revision changed", func(a *ItemWindowApp) { a.state.ItemOptions.revision++ }},
		{"shortcut changed", func(a *ItemWindowApp) { a.state.AddHotkey = "Ctrl+F2" }},
		{"shortcut disabled", func(a *ItemWindowApp) { a.state.AddHotkey = "Off" }},
		{"other state emitted", func(a *ItemWindowApp) { a.state.Status = "New state" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake := newTrackedHotkeyTestApp(t)
			original := a.addHotkeyTarget.Load()
			if original == nil {
				t.Fatal("test item must initially allow Add")
			}
			a.requestTrackedAddHotkey()
			if !a.addHotkeyPending.Load() || len(a.addHotkeyRequests) != 1 {
				t.Fatal("eligible callback did not queue its immutable target")
			}
			tc.change(a)
			a.emit()
			if a.addHotkeyTarget.Load() == original {
				t.Fatal("emitted state reused an old queued shortcut target")
			}
			runTrackedHotkeyTestWorker(t, a)
			waitTrackedHotkeyIdle(t, a)
			loads, writes := trackedHotkeyAPICalls(fake)
			if loads != 0 || len(writes) != 0 {
				t.Fatalf("old shortcut was retargeted after state changed: reads=%d writes=%+v", loads, writes)
			}
			assertNoTrackingTestStockAdded(t, a)
		})
	}
}

func TestTrackedHotkeyFreshCompletionKeepsStagedStockWithoutUploading(t *testing.T) {
	a, fake := newTrackedHotkeyTestApp(t)
	fake.snapshot = completedCMSFixture()
	// The cached requirement still allows the physical press. Once its one
	// piece is durably saved, a newer completion must not erase or upload it.
	if a.addHotkeyTarget.Load() == nil {
		t.Fatal("earlier scan must initially allow Add")
	}
	runTrackedHotkeyTestWorker(t, a)
	a.requestTrackedAddHotkey()
	waitTrackedHotkeyIdle(t, a)
	loads, writes := trackedHotkeyAPICalls(fake)
	if loads != 0 || len(writes) != 0 || a.GetState().Tracking.AvailableQuantity != 1 {
		t.Fatal("shortcut must stage stock from the confirmed cache without foreground API calls")
	}
	a.RefreshLocalProgress()
	if err := a.processProgressWork(a.ctx); err == nil || !strings.Contains(err.Error(), "No active Market requirement remains") {
		t.Fatalf("closed demand must explain why the saved piece was not uploaded: %v", err)
	}
	loads, writes = trackedHotkeyAPICalls(fake)
	state := a.GetState()
	stock, err := a.loadMarketFallback(state.ItemOptions)
	row, unique := stockEditorItem(stock, trackingBSGID)
	if err != nil || !unique || row.LocalAvailableQuantity != 1 || row.LocalRegularQuantity != 1 ||
		loads < 1 || len(writes) != 0 || state.Tracking.RequiredQuantity != 0 || state.Tracking.AvailableQuantity != 1 || state.Tracking.Adding {
		t.Fatalf("fresh completion must keep the durable piece and skip PATCH: reads=%d writes=%+v row=%+v tracking=%+v err=%v", loads, writes, row, state.Tracking, err)
	}
	if a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() != nil {
		t.Fatal("completed requirement remained available for another shortcut")
	}
}

func TestTrackedHotkeyAutomaticScanningOffStillAllowsLoadedItem(t *testing.T) {
	a, fake := newTrackedHotkeyTestApp(t)
	a.state.Automatic = false
	a.publishTrackedAddHotkey()
	runTrackedHotkeyTestWorker(t, a)
	a.requestTrackedAddHotkey()
	waitTrackedHotkeyIdle(t, a)
	syncTrackedHotkeyStagedTest(t, a, fake, "regular")
	_, writes := trackedHotkeyAPICalls(fake)
	if len(writes) != 1 || writes[0].Quantity != 1 {
		t.Fatalf("manual search results must allow the shortcut with automatic scanning off: %+v", writes)
	}
}

func TestTrackedAddButtonWorksWhenShortcutIsOff(t *testing.T) {
	a, fake := newTrackedHotkeyTestApp(t)
	a.state.AddHotkey = "Off"
	a.publishTrackedAddHotkey()
	a.AddTrackedItem(trackingItemUID)
	state := syncTrackedHotkeyStagedTest(t, a, fake, "regular")
	_, writes := trackedHotkeyAPICalls(fake)
	if len(writes) != 1 || writes[0].Quantity != 1 || state.Tracking.AvailableQuantity != 1 || state.Tracking.Error != "" {
		t.Fatalf("disabling the shortcut disabled the existing Add button: writes=%+v tracking=%+v", writes, state.Tracking)
	}
}

func TestTrackedHotkeyAndAddAreDisabledDuringSearch(t *testing.T) {
	a, fake := newTrackedHotkeyTestApp(t)
	newItem := market.Item{UID: "another-market-item", BSGID: "another-game-item", Name: "Metal fuel tank"}
	body, err := json.Marshal([]market.Item{newItem})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	transport := http.DefaultTransport
	http.DefaultTransport = trackedHotkeyRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(entered)
		select {
		case <-release:
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	})
	t.Cleanup(func() { http.DefaultTransport = transport })
	a.client = market.NewClient(t.TempDir())
	searchContext, cancelSearch := context.WithCancel(a.ctx)
	a.ctx = searchContext
	runTrackedHotkeyTestWorker(t, a)
	done := make(chan struct{})
	var returned ItemState
	go func() {
		returned = a.SearchItem(newItem.Name)
		close(done)
	}()
	t.Cleanup(func() {
		cancelSearch()
		releaseOnce.Do(func() { close(release) })
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("controlled item search did not stop")
		}
	})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("search did not start the controlled catalog request")
	}
	if !a.GetState().Busy || a.addHotkeyTarget.Load() != nil {
		t.Fatal("search left the previous item eligible for a global shortcut")
	}
	a.requestTrackedAddHotkey()
	a.AddTrackedItem(trackingItemUID)
	loads, writes := trackedHotkeyAPICalls(fake)
	if loads != 0 || len(writes) != 0 || a.addHotkeyPending.Load() || len(a.addHotkeyRequests) != 0 {
		t.Fatalf("searching for another item allowed Add on the previous item: reads=%d writes=%+v pending=%v queued=%d", loads, writes, a.addHotkeyPending.Load(), len(a.addHotkeyRequests))
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("search did not return its matched item")
	}
	if returned.Busy || returned.Item == nil || returned.Item.UID != newItem.UID || returned.Error != "" {
		t.Fatalf("search returned a stale item or stale Busy flag: %+v", returned)
	}
	if a.GetState().Busy || a.addHotkeyTarget.Load() != nil {
		t.Fatal("search did not clear Busy or enabled Add before the new item's tracker was loaded")
	}
}

func TestTrackedHotkeyIsDisabledImmediatelyByScanNow(t *testing.T) {
	a, fake := newTrackedHotkeyTestApp(t)
	ocr := &inspectScannerFixture{}
	a.ocr = ocr
	ctx, cancel := context.WithCancel(a.ctx)
	a.ctx = ctx
	t.Cleanup(func() {
		cancel()
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			if a.scanMu.TryLock() {
				a.scanMu.Unlock()
				return
			}
			select {
			case <-tick.C:
			case <-deadline.C:
				t.Error("cancelled manual scan did not release its worker")
				return
			}
		}
	})
	returned := a.ScanNow()
	if !returned.Busy || a.addHotkeyTarget.Load() != nil {
		t.Fatal("ScanNow returned before disabling the previous item's shortcut")
	}
	a.requestTrackedAddHotkey()
	a.AddTrackedItem(trackingItemUID)
	loads, writes := trackedHotkeyAPICalls(fake)
	if loads != 0 || len(writes) != 0 || a.addHotkeyPending.Load() || len(a.addHotkeyRequests) != 0 {
		t.Fatalf("the manual scan countdown allowed an old Add target: reads=%d writes=%+v pending=%v queued=%d", loads, writes, a.addHotkeyPending.Load(), len(a.addHotkeyRequests))
	}
	cancel()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if a.scanMu.TryLock() {
			a.scanMu.Unlock()
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("cancelled manual scan did not complete")
		}
	}
	if a.GetState().Busy || a.addHotkeyTarget.Load() != nil || ocr.probes != 0 || ocr.reads != 0 {
		t.Fatalf("cancelled countdown retained Busy or performed OCR: busy=%v target=%+v probes=%d reads=%d", a.GetState().Busy, a.addHotkeyTarget.Load(), ocr.probes, ocr.reads)
	}
}

func TestTrackedManualScanEarlyExitClearsReservedBusy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ItemWindowApp)
	}{
		{"API key cleared before the worker starts", func(a *ItemWindowApp) { a.state.HasAPIKey = false }},
		{"scan disabled before the worker starts", func(a *ItemWindowApp) { a.state.Enabled = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake := newTrackedHotkeyTestApp(t)
			ocr := &inspectScannerFixture{}
			a.ocr = ocr
			// ScanNow reserves Busy synchronously. Simulate a settings/key change
			// before its asynchronous worker gets to the initial eligibility gate.
			a.state.Busy = true
			tc.change(a)
			a.scanLocked(false, 0)
			loads, writes := trackedHotkeyAPICalls(fake)
			if a.GetState().Busy || ocr.probes != 0 || ocr.reads != 0 || loads != 0 || len(writes) != 0 {
				t.Fatalf("early scan exit retained its reserved Busy state or performed work: busy=%v target=%+v probes=%d reads=%d loads=%d writes=%+v", a.GetState().Busy, a.addHotkeyTarget.Load(), ocr.probes, ocr.reads, loads, writes)
			}
		})
	}
}
