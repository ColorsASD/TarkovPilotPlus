package plus

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/tarkovdev"
)

func newFixedAddGateTestApp(t *testing.T, remote, fir bool) (*ItemWindowApp, *fakeCharacterProgress, string) {
	t.Helper()
	a, fake := newDevOnlyTestApp(t)
	if remote {
		fake.snapshot.Items = []market.TrackedItem{{ItemID: trackingBSGID, RequiredQuantity: 1, RemainingQuantity: 1,
			Sources: []market.TrackedSource{{Type: "quest", QuestID: "quest-b", Amount: 1, RemainingRequiredQuantity: 1, FIR: fir}},
		}}
	}
	a.devRequirements = &fakeRequirements{catalog: tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "quest-b", Objectives: []tarkovdev.Objective{
		{ID: "fixed-one", Type: "giveItem", ItemIDs: []string{trackingBSGID}, Count: 1, FoundInRaid: fir},
	}}}, Hideout: []tarkovdev.Station{}}}
	dir := t.TempDir()
	a.localStore = localprogress.NewStore(dir)
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	return a, fake, dir
}

func addGateFile(t *testing.T, dir string) []byte {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one local stock file, found %v: %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAddGateRejectsRegularForFIRWithoutAnyWriteAndKeepsFIRHotkey(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "market"}[remote], func(t *testing.T) {
			a, fake, dir := newFixedAddGateTestApp(t, remote, true)
			a.state.AddHotkeyReady = true
			a.addHotkeyRequests = make(chan *trackedAddHotkeyTarget, 1)
			a.publishTrackedAddHotkey()
			if a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() == nil {
				t.Fatal("native Regular target was published for a FiR-only deficit")
			}
			before, loads := addGateFile(t, dir), fake.loads
			state := addAndSyncCachedTest(t, a, trackingItemUID, "regular")
			if len(fake.writes) != 0 || fake.loads != loads || !bytes.Equal(before, addGateFile(t, dir)) ||
				state.Tracking.Error != "" || state.Tracking.Adding || state.Tracking.AddRegularAllowed || !state.Tracking.AddFIRAllowed ||
				!strings.Contains(state.Tracking.Status, "Regular") || a.addHotkeyFIRTarget.Load() == nil {
				t.Fatalf("Regular rejection changed stock, wrote a file, or blocked FiR: writes=%+v state=%+v", fake.writes, state.Tracking)
			}
			runTrackedHotkeyTestWorker(t, a)
			a.requestTrackedAddFIRHotkey()
			waitTrackedHotkeyIdle(t, a)
			state = syncCachedMarketTest(t, a)
			wantWrites := 0
			if remote {
				wantWrites = 1
			}
			if len(fake.writes) != wantWrites || state.Tracking.AvailableQuantity != 1 || state.Tracking.RemainingQuantity != 0 ||
				state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed || state.Tracking.Error != "" || state.Tracking.Adding ||
				a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() != nil {
				t.Fatalf("FiR hotkey did not fill and close the actual requirement: writes=%+v state=%+v", fake.writes, state.Tracking)
			}
		})
	}
}

func TestAddGateEitherQualityFillsRegularThenBothActionsStop(t *testing.T) {
	for _, remote := range []bool{false, true} {
		for _, kind := range []string{"regular", "fir"} {
			t.Run(map[bool]string{false: "local-", true: "market-"}[remote]+kind, func(t *testing.T) {
				a, fake, dir := newFixedAddGateTestApp(t, remote, false)
				if view := a.GetState().Tracking; !view.AddRegularAllowed || !view.AddFIRAllowed {
					t.Fatalf("Regular requirement rejected an eligible quality: %+v", view)
				}
				state := a.addTrackedItemKind(trackingItemUID, nil, kind)
				state = syncCachedMarketTest(t, a)
				if state.Tracking.AvailableQuantity != 1 || state.Tracking.RemainingQuantity != 0 || state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed {
					t.Fatalf("one eligible piece did not reach the cap: %+v", state.Tracking)
				}
				before, writes, loads := addGateFile(t, dir), len(fake.writes), fake.loads
				addAndSyncCachedTest(t, a, trackingItemUID, "regular")
				state = addAndSyncCachedTest(t, a, trackingItemUID, "fir")
				if state.Tracking.AvailableQuantity != 1 || state.Tracking.Adding || state.Tracking.Error != "" ||
					len(fake.writes) != writes || fake.loads != loads || !bytes.Equal(before, addGateFile(t, dir)) {
					t.Fatalf("a filled requirement accepted another save: writes=%+v state=%+v", fake.writes, state.Tracking)
				}
			})
		}
	}
}

func TestAddGateLegacyWrongRegularAtRawMarketQuotaStillAllowsOneFIR(t *testing.T) {
	a, fake, _ := newFixedAddGateTestApp(t, true, true)
	if _, err := a.localStore.RecordMarketAdd("character-a", "PVP", trackingBSGID, "regular", 0, 1); err != nil {
		t.Fatal(err)
	}
	fake.snapshot.Items[0].AvailableQuantity, fake.snapshot.Items[0].RemainingQuantity = 1, 0
	fake.snapshot.Revision = "legacy-regular"
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	if view := a.GetState().Tracking; view.AvailableQuantity != 1 || view.RequiredQuantity != 1 || view.RemainingQuantity != 1 || view.AddRegularAllowed || !view.AddFIRAllowed {
		t.Fatalf("raw stock hid the remaining FiR deficit: %+v", view)
	}
	addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	state := addAndSyncCachedTest(t, a, trackingItemUID, "fir")
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 2 || state.Tracking.RemainingQuantity != 0 ||
		state.Tracking.AvailableQuantity != 2 || state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed || state.Tracking.Storage != "market" {
		t.Fatalf("typed cap invented local stock or blocked an eligible Market add: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
	a.AddTrackedItemFIR(trackingItemUID)
	if len(fake.writes) != 1 {
		t.Fatal("an already fulfilled FiR requirement accepted another Market write")
	}
}

func TestAddGateMixedQuotaAllowsOnlyUnmetQualityInLocalRemainder(t *testing.T) {
	a, fake, dir := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	a.devRequirements.(*fakeRequirements).catalog.Tasks[1].Objectives[0].FoundInRaid = true
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	for i := 0; i < 3; i++ {
		addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	}
	before := addGateFile(t, dir)
	state := addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	if len(fake.writes) != 3 || state.Tracking.AvailableQuantity != 3 || state.Tracking.AddRegularAllowed || !state.Tracking.AddFIRAllowed ||
		state.Tracking.Storage != "local" || !bytes.Equal(before, addGateFile(t, dir)) {
		t.Fatalf("Regular overflow was saved for a FiR-only local remainder: %+v", state.Tracking)
	}
	for i := 0; i < 5; i++ {
		state = addAndSyncCachedTest(t, a, trackingItemUID, "fir")
	}
	if len(fake.writes) != 3 || state.Tracking.AvailableQuantity != 8 || state.Tracking.RemainingQuantity != 0 || state.Tracking.AddFIRAllowed || state.Tracking.AddRegularAllowed {
		t.Fatalf("Market-first/local-next typed cap changed routing: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
	assertLocalStockOnly(t, a, trackingBSGID, 5)
}

func TestAddGateCMSAnyFIRUsesGlobalUnknownCandidateStock(t *testing.T) {
	a, fake, _ := newFixedAddGateTestApp(t, true, true)
	const augmentin = "augmentin-item"
	fake.snapshot.Items = []market.TrackedItem{
		{ItemID: trackingBSGID, Name: "CMS surgical kit", RequiredQuantity: 2, RemainingQuantity: 2,
			Sources: []market.TrackedSource{{Type: "quest", QuestID: "quest-b", Amount: 2, RemainingRequiredQuantity: 2, FIR: true}}},
		{ItemID: augmentin, AvailableQuantity: 1, RequiredQuantity: 2, RemainingQuantity: 1,
			Sources: []market.TrackedSource{{Type: "quest", QuestID: "quest-b", Amount: 2, RemainingRequiredQuantity: 2, FIR: true}}},
	}
	a.devRequirements = &fakeRequirements{catalog: tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "quest-b", Objectives: []tarkovdev.Objective{
		{ID: "any-cms-augmentin", Type: "giveItem", ItemIDs: []string{trackingBSGID, augmentin}, Count: 2, FoundInRaid: true},
	}}}, Hideout: []tarkovdev.Station{}}}
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	if view := a.GetState().Tracking; view.AddRegularAllowed || !view.AddFIRAllowed || len(view.AnyGroups) != 1 || view.AnyGroups[0].AvailableQuantity != 1 {
		t.Fatalf("CMS candidate lost its open FiR Any eligibility: %+v", view)
	}
	addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	state := addAndSyncCachedTest(t, a, trackingItemUID, "fir")
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 1 || state.Tracking.AvailableQuantity != 1 ||
		state.Tracking.RequiredQuantity != 0 || state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed || len(state.Tracking.AnyGroups) != 1 ||
		state.Tracking.AnyGroups[0].AvailableQuantity != 2 || state.Tracking.AnyGroups[0].RequiredQuantity != 2 {
		t.Fatalf("CMS failed to cover the global missing piece or kept collecting: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
}

func TestAddGateFilledVisibleRequirementsBlockButtonsAndHotkeys(t *testing.T) {
	for _, remote := range []bool{false, true} {
		for _, any := range []bool{false, true} {
			for _, fir := range []bool{false, true} {
				name := map[bool]string{false: "local", true: "market"}[remote] +
					map[bool]string{false: "-fixed", true: "-any"}[any] + map[bool]string{false: "-regular", true: "-fir"}[fir]
				t.Run(name, func(t *testing.T) {
					a, fake, dir := newFixedAddGateTestApp(t, remote, fir)
					objective := &a.devRequirements.(*fakeRequirements).catalog.Tasks[0].Objectives[0]
					objective.Count = 10
					if any {
						objective.ItemIDs = append(objective.ItemIDs, "alternative-item")
					}
					if remote {
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
						addAndSyncCachedTest(t, a, trackingItemUID, kind)
					}
					if view := a.GetState().Tracking; !view.AddFIRAllowed || view.AddRegularAllowed == fir {
						t.Fatalf("the last required piece was disabled prematurely: %+v", view)
					}
					runTrackedHotkeyTestWorker(t, a)
					if fir {
						a.requestTrackedAddFIRHotkey()
					} else {
						a.requestTrackedAddHotkey()
					}
					waitTrackedHotkeyIdle(t, a)
					state := syncCachedMarketTest(t, a)
					if state.Tracking.AvailableQuantity != 10 || state.Tracking.RemainingQuantity != 0 ||
						state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed || a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() != nil {
						t.Fatalf("a filled requirement kept a stock action available: %+v", state.Tracking)
					}
					if any {
						if len(state.Tracking.AnyGroups) != 1 || state.Tracking.AnyGroups[0].AvailableQuantity != 10 || state.Tracking.AnyGroups[0].RequiredQuantity != 10 {
							t.Fatalf("filled Any demand must remain visible while both actions are disabled: %+v", state.Tracking)
						}
					} else if len(state.Tracking.Requirements) != 1 || state.Tracking.Requirements[0].AvailableQuantity != 10 || state.Tracking.Requirements[0].RequiredQuantity != 10 {
						t.Fatalf("filled typed demand must remain 10/10 while both actions are disabled: %+v", state.Tracking)
					}
					before, loads, writes := addGateFile(t, dir), fake.loads, len(fake.writes)
					a.AddTrackedItem(trackingItemUID)
					state = a.AddTrackedItemFIR(trackingItemUID)
					a.requestTrackedAddHotkey()
					a.requestTrackedAddFIRHotkey()
					if fake.loads != loads || len(fake.writes) != writes || !bytes.Equal(before, addGateFile(t, dir)) ||
						state.Tracking.AvailableQuantity != 10 || a.addHotkeyPending.Load() || len(a.addHotkeyRequests) != 0 {
						t.Fatalf("visible 10/10 demand accepted another button or shortcut: writes=%+v state=%+v", fake.writes, state.Tracking)
					}
				})
			}
		}
	}
}

func TestAddGateQueuedShortcutRechecksStockFilledByAnotherAction(t *testing.T) {
	for _, any := range []bool{false, true} {
		for _, fir := range []bool{false, true} {
			name := map[bool]string{false: "fixed", true: "any"}[any] + map[bool]string{false: "-regular", true: "-fir"}[fir]
			t.Run(name, func(t *testing.T) {
				a, fake, dir := newFixedAddGateTestApp(t, false, fir)
				stockID := trackingBSGID
				if any {
					stockID = "alternative-item"
					a.devRequirements.(*fakeRequirements).catalog.Tasks[0].Objectives[0].ItemIDs = []string{trackingBSGID, stockID}
					a.localAt = time.Time{}
					a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
				}
				a.state.AddHotkeyReady = true
				a.addHotkeyRequests = make(chan *trackedAddHotkeyTarget, 1)
				a.publishTrackedAddHotkey()
				kind := "regular"
				if fir {
					kind = "fir"
					a.requestTrackedAddFIRHotkey()
				} else {
					a.requestTrackedAddHotkey()
				}
				if !a.addHotkeyPending.Load() {
					t.Fatal("the initially open requirement did not queue its shortcut")
				}
				if _, err := a.localStore.AddKind("character-a", "PVP", stockID, kind); err != nil {
					t.Fatal(err)
				}
				before, loads := addGateFile(t, dir), fake.loads
				runTrackedHotkeyTestWorker(t, a)
				waitTrackedHotkeyIdle(t, a)
				state := a.GetState()
				if fake.loads != loads || len(fake.writes) != 0 || !bytes.Equal(before, addGateFile(t, dir)) ||
					state.Tracking.RemainingQuantity != 0 || state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed ||
					a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() != nil {
					t.Fatalf("queued shortcut used stale eligibility after the fresh stock filled demand: %+v", state.Tracking)
				}
			})
		}
	}
}

func TestAddGateFilledRegularRequirementBlocksOnlyRegularShortcut(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	a.state.AddHotkeyReady = true
	a.addHotkeyRequests = make(chan *trackedAddHotkeyTarget, 1)
	a.publishTrackedAddHotkey()
	// A korábban lenyomott Regular gyorsbillentyű még a régi készletre mutat.
	a.requestTrackedAddHotkey()
	for i := 0; i < 5; i++ {
		if _, err := a.localStore.AddKind("character-a", "PVP", trackingBSGID, "regular"); err != nil {
			t.Fatal(err)
		}
	}
	runTrackedHotkeyTestWorker(t, a)
	waitTrackedHotkeyIdle(t, a)
	state := a.GetState()
	if state.Tracking.AvailableQuantity != 5 || state.Tracking.RemainingQuantity != 2 ||
		state.Tracking.AddRegularAllowed || !state.Tracking.AddFIRAllowed || a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() == nil {
		t.Fatalf("filled Regular demand disabled FiR or accepted a stale Regular shortcut: %+v", state.Tracking)
	}
	loads := fake.loads
	a.AddTrackedItem(trackingItemUID)
	a.requestTrackedAddHotkey()
	if a.addHotkeyPending.Load() || len(a.addHotkeyRequests) != 0 || a.GetState().Tracking.AvailableQuantity != 5 {
		t.Fatal("a filled Regular requirement accepted its button or shortcut while FiR remained open")
	}
	for i := 0; i < 2; i++ {
		a.requestTrackedAddFIRHotkey()
		waitTrackedHotkeyIdle(t, a)
	}
	state = a.GetState()
	if fake.loads != loads || len(fake.writes) != 0 || state.Tracking.AvailableQuantity != 7 || state.Tracking.RemainingQuantity != 0 ||
		state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed || a.addHotkeyTarget.Load() != nil || a.addHotkeyFIRTarget.Load() != nil {
		t.Fatalf("the still eligible FiR shortcut did not fill exactly the missing FiR demand: %+v", state.Tracking)
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
					if count >= loads+2 {
						// The final fresh GET supersedes the earlier open UI state.
						fake.snapshot.Items[0].AvailableQuantity, fake.snapshot.Items[0].RemainingQuantity = 1, 0
					}
					return cloneTrackedSnapshot(fake.snapshot), nil
				}
			}
			state := a.addLocalTrackedItemKind(trackingItemUID, nil, "regular")
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
