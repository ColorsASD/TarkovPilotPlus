package plus

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func newFixedAddGateTestApp(t *testing.T, _ bool, fir bool) (*ItemWindowApp, *fakeCharacterProgress, string) {
	t.Helper()
	a, fake := newLocalTestApp(t)
	fake.snapshot.Items = []market.TrackedItem{{ItemID: trackingBSGID, RequiredQuantity: 1, RemainingQuantity: 1,
		Sources: []market.TrackedSource{{Type: "quest", QuestID: "quest-b", Amount: 1, RemainingRequiredQuantity: 1, FIR: fir}},
	}}
	fake.snapshot.Groups = nil
	dir := a.dir
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	return a, fake, dir
}

func addGateFile(t *testing.T, dir string) []byte {
	t.Helper()
	assertNoMarketStockFilesForTest(t, dir)
	data, err := os.ReadFile(filepath.Join(dir, "item-settings.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAddGateRejectsRegularForFIRWithoutAnyWriteAndKeepsFIRHotkey(t *testing.T) {
	a, fake, dir := newFixedAddGateTestApp(t, true, true)
	a.state.AddHotkeyReady = true
	a.addHotkeyRequests = make(chan *trackedAddHotkeyTarget, 1)
	a.publishTrackedAddHotkey()
	if a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() == nil {
		t.Fatal("native Regular target was published for a FiR-only deficit")
	}
	before := addGateFile(t, dir)
	state := addMarketKindForTest(t, a, trackingItemUID, "regular")
	if len(fake.writes) != 0 || !bytes.Equal(before, addGateFile(t, dir)) ||
		state.Tracking.Error != "" || state.Tracking.Adding || state.Tracking.AddRegularAllowed || !state.Tracking.AddFIRAllowed ||
		!strings.Contains(state.Tracking.Status, "Regular") || a.addHotkeyFIRTarget.Load() == nil {
		t.Fatalf("Regular rejection changed stock, wrote a file, or blocked FiR: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
	runTrackedHotkeyTestWorker(t, a)
	a.requestTrackedAddFIRHotkey()
	waitTrackedHotkeyIdle(t, a)
	waitPendingForTest(t, a)
	state = a.GetState()
	if len(fake.writes) != 1 || state.Tracking.AvailableQuantity != 1 || state.Tracking.RemainingQuantity != 0 ||
		state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed || state.Tracking.Error != "" || state.Tracking.Adding ||
		a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() != nil {
		t.Fatalf("FiR hotkey did not fill and close the actual Market requirement: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
}

func TestAddGateEitherQualityFillsRegularThenBothActionsStop(t *testing.T) {
	for _, kind := range []string{"regular", "fir"} {
		t.Run(kind, func(t *testing.T) {
			a, fake, dir := newFixedAddGateTestApp(t, true, false)
			if view := a.GetState().Tracking; !view.AddRegularAllowed || !view.AddFIRAllowed {
				t.Fatalf("Regular requirement rejected an eligible quality: %+v", view)
			}
			state := addMarketKindForTest(t, a, trackingItemUID, kind)
			if state.Tracking.AvailableQuantity != 1 || state.Tracking.RemainingQuantity != 0 || state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed {
				t.Fatalf("one eligible piece did not reach the cap: %+v", state.Tracking)
			}
			before, writes := addGateFile(t, dir), len(fake.writes)
			addMarketKindForTest(t, a, trackingItemUID, "regular")
			state = addMarketKindForTest(t, a, trackingItemUID, "fir")
			if state.Tracking.AvailableQuantity != 1 || state.Tracking.Adding || state.Tracking.Error != "" ||
				len(fake.writes) != writes || !bytes.Equal(before, addGateFile(t, dir)) {
				t.Fatalf("a filled Market requirement accepted another save: writes=%+v state=%+v", fake.writes, state.Tracking)
			}
		})
	}
}

func TestAddGateLegacyWrongRegularAtRawMarketQuotaStillAllowsOneFIR(t *testing.T) {
	a, fake, _ := newFixedAddGateTestApp(t, true, true)
	a.recordLocalMarketAdd(a.apiKey, a.state.ItemOptions, *a.state.Item, "regular", 0, 1)
	fake.snapshot.Items[0].AvailableQuantity, fake.snapshot.Items[0].RemainingQuantity = 1, 0
	fake.snapshot.Revision = "legacy-regular"
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	if view := a.GetState().Tracking; view.AvailableQuantity != 1 || view.RequiredQuantity != 1 || view.RemainingQuantity != 1 || view.AddRegularAllowed || !view.AddFIRAllowed {
		t.Fatalf("raw stock hid the remaining FiR deficit: %+v", view)
	}
	addMarketKindForTest(t, a, trackingItemUID, "regular")
	state := addMarketKindForTest(t, a, trackingItemUID, "fir")
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 2 || state.Tracking.RemainingQuantity != 0 ||
		state.Tracking.AvailableQuantity != 2 || state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed || state.Tracking.Storage != "market" {
		t.Fatalf("typed cap blocked an eligible Market add: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
	addMarketKindForTest(t, a, trackingItemUID, "fir")
	if len(fake.writes) != 1 {
		t.Fatal("an already fulfilled FiR requirement accepted another Market write")
	}
}

func TestAddGateMixedMarketRequirementsAllowOnlyUnmetQuality(t *testing.T) {
	a, fake, _ := newMarketQuotaTestApp(t, trackingItemUID, trackingBSGID)
	dir := a.dir
	for i := range fake.snapshot.Items[0].Sources {
		if fake.snapshot.Items[0].Sources[i].QuestID == "quest-b" {
			fake.snapshot.Items[0].Sources[i].FIR = true
		}
	}
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	for i := 0; i < 3; i++ {
		addMarketKindForTest(t, a, trackingItemUID, "regular")
	}
	before := addGateFile(t, dir)
	state := addMarketKindForTest(t, a, trackingItemUID, "regular")
	if len(fake.writes) != 3 || state.Tracking.AvailableQuantity != 3 || state.Tracking.AddRegularAllowed || !state.Tracking.AddFIRAllowed ||
		state.Tracking.Storage != "market" || !bytes.Equal(before, addGateFile(t, dir)) {
		t.Fatalf("Regular overflow was accepted for a FiR-only remainder: %+v", state.Tracking)
	}
	for i := 0; i < 5; i++ {
		state = addMarketKindForTest(t, a, trackingItemUID, "fir")
	}
	if len(fake.writes) != 8 || state.Tracking.AvailableQuantity != 8 || state.Tracking.RemainingQuantity != 0 || state.Tracking.AddFIRAllowed || state.Tracking.AddRegularAllowed {
		t.Fatalf("Market typed cap changed routing: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
	row := memoryMarketStockForTest(t, a, trackingBSGID)
	if row.MarketAvailableQuantity != 8 || row.MarketRegularQuantity != 3 || row.MarketFIRQuantity != 5 {
		t.Fatalf("confirmed Market stock or in-memory types were lost: %+v", row)
	}
	assertNoMarketStockFilesForTest(t, a.dir)
}

func TestAddGateCMSAnyFIRUsesGlobalUnknownCandidateStock(t *testing.T) {
	a, fake, _ := newFixedAddGateTestApp(t, true, true)
	const augmentin = "augmentin-item"
	setMarketGroupRowsForTest(fake, nil, marketQuestGroupForTest("quest-b", "any-cms-augmentin", 2, true, trackingBSGID, augmentin))
	fake.snapshot.Items[0].Name = "CMS surgical kit"
	fake.snapshot.Items[1].AvailableQuantity = 1
	fake.snapshot.Items[1].RemainingQuantity = 1
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	if view := a.GetState().Tracking; view.AddRegularAllowed || !view.AddFIRAllowed || len(view.AnyGroups) != 1 || view.AnyGroups[0].AvailableQuantity != 1 {
		t.Fatalf("CMS candidate lost its open FiR Any eligibility: %+v", view)
	}
	addMarketKindForTest(t, a, trackingItemUID, "regular")
	state := addMarketKindForTest(t, a, trackingItemUID, "fir")
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 1 || state.Tracking.AvailableQuantity != 1 || state.Tracking.RequiredQuantity != 0 ||
		state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed || len(state.Tracking.AnyGroups) != 1 || state.Tracking.AnyGroups[0].AvailableQuantity != 2 || state.Tracking.AnyGroups[0].RequiredQuantity != 2 {
		t.Fatalf("CMS did not cover exactly the global missing piece: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
}

func TestAddGateFilledVisibleRequirementsBlockButtonsAndHotkeys(t *testing.T) {
	for _, any := range []bool{false, true} {
		for _, fir := range []bool{false, true} {
			name := map[bool]string{false: "fixed", true: "any"}[any] + map[bool]string{false: "-regular", true: "-fir"}[fir]
			t.Run(name, func(t *testing.T) {
				a, fake, dir := newFixedAddGateTestApp(t, true, fir)
				if any {
					setMarketGroupRowsForTest(fake, nil, marketQuestGroupForTest("quest-b", "ten", 10, fir, trackingBSGID, "alternative-item"))
				} else {
					fake.snapshot.Items[0].RequiredQuantity, fake.snapshot.Items[0].RemainingQuantity = 10, 10
					fake.snapshot.Items[0].Sources[0].Amount, fake.snapshot.Items[0].Sources[0].RemainingRequiredQuantity = 10, 10
				}
				a.localAt = time.Time{}
				a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
				a.state.AddHotkeyReady = true
				a.addHotkeyRequests = make(chan *trackedAddHotkeyTarget, 1)
				a.publishTrackedAddHotkey()
				kind := "regular"
				if fir {
					kind = "fir"
				}
				for i := 0; i < 9; i++ {
					addMarketKindForTest(t, a, trackingItemUID, kind)
				}
				if view := a.GetState().Tracking; !view.AddFIRAllowed || view.AddRegularAllowed == fir {
					t.Fatalf("last required piece disabled too early: %+v", view)
				}
				runTrackedHotkeyTestWorker(t, a)
				if fir {
					a.requestTrackedAddFIRHotkey()
				} else {
					a.requestTrackedAddHotkey()
				}
				waitTrackedHotkeyIdle(t, a)
				waitPendingForTest(t, a)
				state := a.GetState()
				if state.Tracking.AvailableQuantity != 10 || state.Tracking.RemainingQuantity != 0 || state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed || a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() != nil {
					t.Fatalf("filled requirement retained a stock action: %+v", state.Tracking)
				}
				if any {
					if len(state.Tracking.AnyGroups) != 1 || state.Tracking.AnyGroups[0].AvailableQuantity != 10 || state.Tracking.AnyGroups[0].RequiredQuantity != 10 {
						t.Fatalf("full Any ratio disappeared: %+v", state.Tracking)
					}
				} else if len(state.Tracking.Requirements) != 1 || state.Tracking.Requirements[0].AvailableQuantity != 10 || state.Tracking.Requirements[0].RequiredQuantity != 10 {
					t.Fatalf("full fixed ratio disappeared: %+v", state.Tracking)
				}
				before, writes := addGateFile(t, dir), len(fake.writes)
				addMarketKindForTest(t, a, trackingItemUID, "regular")
				state = addMarketKindForTest(t, a, trackingItemUID, "fir")
				a.requestTrackedAddHotkey()
				a.requestTrackedAddFIRHotkey()
				if len(fake.writes) != writes || !bytes.Equal(before, addGateFile(t, dir)) || state.Tracking.AvailableQuantity != 10 || a.addHotkeyPending.Load() || len(a.addHotkeyRequests) != 0 {
					t.Fatalf("visible 10/10 accepted another button or shortcut: writes=%+v state=%+v", fake.writes, state.Tracking)
				}
			})
		}
	}
}

func TestAddGateQueuedShortcutRechecksStockFilledByAnotherAction(t *testing.T) {
	for _, any := range []bool{false, true} {
		for _, fir := range []bool{false, true} {
			name := map[bool]string{false: "fixed", true: "any"}[any] + map[bool]string{false: "-regular", true: "-fir"}[fir]
			t.Run(name, func(t *testing.T) {
				a, fake, dir := newFixedAddGateTestApp(t, true, fir)
				stockID := trackingBSGID
				if any {
					stockID = "alternative-item"
					setMarketGroupRowsForTest(fake, nil, marketQuestGroupForTest("quest-b", "one", 1, fir, trackingBSGID, stockID))
					a.localAt = time.Time{}
					a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
				}
				a.state.AddHotkeyReady = true
				a.addHotkeyRequests = make(chan *trackedAddHotkeyTarget, 1)
				a.publishTrackedAddHotkey()
				if fir {
					a.requestTrackedAddFIRHotkey()
				} else {
					a.requestTrackedAddHotkey()
				}
				if !a.addHotkeyPending.Load() {
					t.Fatal("open requirement did not queue shortcut")
				}
				for i := range fake.snapshot.Items {
					if fake.snapshot.Items[i].ItemID == stockID {
						fake.snapshot.Items[i].AvailableQuantity = 1
						fake.snapshot.Items[i].RemainingQuantity = max(0, fake.snapshot.Items[i].RequiredQuantity-1)
					}
				}
				fake.snapshot.Revision = "filled-by-another-action"
				before, loads := addGateFile(t, dir), fake.loads
				runTrackedHotkeyTestWorker(t, a)
				waitTrackedHotkeyIdle(t, a)
				waitPendingForTest(t, a)
				state := a.GetState()
				if fake.loads <= loads || len(fake.writes) != 0 || !bytes.Equal(before, addGateFile(t, dir)) || state.Tracking.RemainingQuantity != 0 || state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed || a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() != nil {
					t.Fatalf("queued shortcut used stale eligibility after fresh Market stock filled demand: %+v", state.Tracking)
				}
			})
		}
	}
}

func TestAddGateFilledRegularRequirementBlocksOnlyRegularShortcut(t *testing.T) {
	a, fake := newLocalTestApp(t)
	a.state.AddHotkeyReady = true
	a.addHotkeyRequests = make(chan *trackedAddHotkeyTarget, 1)
	a.publishTrackedAddHotkey()
	a.requestTrackedAddHotkey()
	fake.snapshot.Items[0].AvailableQuantity = 5
	fake.snapshot.Items[0].RemainingQuantity = 2
	fake.snapshot.Revision = "filled-regular"
	for before := int64(0); before < 5; before++ {
		a.recordLocalMarketAdd(a.apiKey, a.state.ItemOptions, *a.state.Item, "regular", before, before+1)
	}
	runTrackedHotkeyTestWorker(t, a)
	waitTrackedHotkeyIdle(t, a)
	waitPendingForTest(t, a)
	state := a.GetState()
	if state.Tracking.AvailableQuantity != 5 || state.Tracking.RemainingQuantity != 2 || state.Tracking.AddRegularAllowed || !state.Tracking.AddFIRAllowed || a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() == nil {
		t.Fatalf("filled Regular demand disabled FiR or accepted stale Regular shortcut: %+v", state.Tracking)
	}
	loads := fake.loads
	addMarketKindForTest(t, a, trackingItemUID, "regular")
	a.requestTrackedAddHotkey()
	if a.addHotkeyPending.Load() || len(a.addHotkeyRequests) != 0 || a.GetState().Tracking.AvailableQuantity != 5 {
		t.Fatal("filled Regular demand accepted another piece")
	}
	for i := 0; i < 2; i++ {
		a.requestTrackedAddFIRHotkey()
		waitTrackedHotkeyIdle(t, a)
		waitPendingForTest(t, a)
	}
	state = a.GetState()
	if fake.loads <= loads || len(fake.writes) != 2 || state.Tracking.AvailableQuantity != 7 || state.Tracking.RemainingQuantity != 0 || state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed || a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() != nil {
		t.Fatalf("eligible FiR shortcut did not fill exactly the FiR deficit: %+v", state.Tracking)
	}
}

func TestAddGateRechecksFreshMarketStockBeforePATCHAndConflictRetry(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh-read", true: "conflict-retry"}[conflict], func(t *testing.T) {
			a, fake, _ := newFixedAddGateTestApp(t, true, false)
			loads := fake.loads
			if conflict {
				fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
					fake.snapshot.Items[0].AvailableQuantity, fake.snapshot.Items[0].RemainingQuantity = 1, 0
					fake.snapshot.Revision = "external-full"
					return market.ProgressWriteResult{}, market.ErrProgressConflict
				}
			} else {
				fake.loadHook = func(count int) (market.TrackedSnapshot, error) {
					if count >= loads+1 {
						// The final fresh GET supersedes the earlier open UI state.
						fake.snapshot.Items[0].AvailableQuantity, fake.snapshot.Items[0].RemainingQuantity = 1, 0
					}
					return cloneTrackedSnapshot(fake.snapshot), nil
				}
			}
			state := addMarketTrackedItemForTest(a, trackingItemUID, "regular")
			wantWrites := 0
			if conflict {
				wantWrites = 1
			}
			if len(fake.writes) != wantWrites || state.Tracking.AvailableQuantity != 1 || state.Tracking.RemainingQuantity != 0 ||
				state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed || state.Tracking.Adding || state.Tracking.Error != "" {
				t.Fatalf("stale eligibility reached an extra PATCH: writes=%+v state=%+v", fake.writes, state.Tracking)
			}
		})
	}
}
