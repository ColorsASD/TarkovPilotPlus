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
	"github.com/ggdiam/TarkovPilot/app/plus/internal/tarkovdev"
)

type fakeCharacterProgress struct {
	*fakeTrackingAPI
	quests                market.QuestSnapshot
	hideout               market.HideoutSnapshot
	questReads            int
	hideoutReads          int
	followTrackedRevision bool
}

func (f *fakeCharacterProgress) LoadQuests(context.Context, string, string, string) (market.QuestSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.questReads++
	snapshot := f.quests
	if f.followTrackedRevision {
		snapshot.Revision = f.snapshot.Revision
	}
	return snapshot, nil
}
func (f *fakeCharacterProgress) LoadHideout(context.Context, string, string, string) (market.HideoutSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hideoutReads++
	snapshot := f.hideout
	if f.followTrackedRevision {
		snapshot.Revision = f.snapshot.Revision
	}
	return snapshot, nil
}

type fakeRequirements struct {
	catalog  tarkovdev.Catalog
	err      error
	hook     func()
	loadHook func(context.Context) (tarkovdev.Catalog, error)
}

func (f *fakeRequirements) Load(ctx context.Context, _ string) (tarkovdev.Catalog, error) {
	if f.loadHook != nil {
		return f.loadHook(ctx)
	}
	if f.hook != nil {
		f.hook()
	}
	return f.catalog, f.err
}

func newLocalTestApp(t *testing.T) (*ItemWindowApp, *fakeCharacterProgress) {
	a, base := newTrackingTestApp(t)
	base.snapshot.Items[0].Sources = []market.TrackedSource{
		{Type: "quest", QuestID: "quest-a", Amount: 1, RemainingRequiredQuantity: 1, FIR: true},
		{Type: "quest", QuestID: "quest-a", Amount: 2, RemainingRequiredQuantity: 2},
		{Type: "quest", QuestID: "quest-b", Amount: 1, RemainingRequiredQuantity: 1, FIR: true},
		{Type: "quest", QuestID: "quest-b", Amount: 3, RemainingRequiredQuantity: 3},
	}
	fake := &fakeCharacterProgress{fakeTrackingAPI: base, followTrackedRevision: true,
		quests: market.QuestSnapshot{CharUID: "character-a", GameMode: "PVP", Revision: "revision-1", Quests: []market.Quest{
			{QuestID: "quest-a", Name: "First quest", Status: "available"}, {QuestID: "quest-b", Name: "Second quest", Status: "locked"},
		}},
		hideout: market.HideoutSnapshot{CharUID: "character-a", GameMode: "PVP", Revision: "revision-1", Modules: []market.HideoutUpgrade{}},
	}
	a.tracker = fake
	a.devRequirements = &fakeRequirements{catalog: tarkovdev.Catalog{Tasks: []tarkovdev.Task{}, Hideout: []tarkovdev.Station{}}}
	a.localStore = localprogress.NewStore(t.TempDir())
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	return a, fake
}

func TestLocalAddPersistsStockWithoutMarketWrite(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	state := addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	if len(fake.writes) != 0 || state.Tracking == nil || state.Tracking.AvailableQuantity != 1 || !state.Tracking.Local || state.Tracking.Status != "Added 1 item to Tracked Items stock." {
		t.Fatalf("local Add wrote to Market or did not save: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
	if len(state.Local.QuestItems) != 1 || state.Local.QuestItems[0].AvailableQuantity != 1 {
		t.Fatalf("local lists did not reflect stock: %+v", state.Local)
	}
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	if a.GetState().Tracking.AvailableQuantity != 1 {
		t.Fatal("refresh replaced dev-only local stock")
	}
}

func newDevOnlyTestApp(t *testing.T) (*ItemWindowApp, *fakeCharacterProgress) {
	a, fake := newLocalTestApp(t)
	fake.snapshot.Items = []market.TrackedItem{}
	a.devRequirements = &fakeRequirements{catalog: tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "quest-b", Objectives: []tarkovdev.Objective{
		{ID: "fir", Type: "giveItem", Count: 2, FoundInRaid: true, ItemIDs: []string{trackingBSGID}},
		{ID: "regular", Type: "giveItem", Count: 5, ItemIDs: []string{trackingBSGID}},
	}}}, Hideout: []tarkovdev.Station{}}}
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	return a, fake
}

func TestLocalModeMarketItemWritesMarketAndMirrorsStock(t *testing.T) {
	a, fake := newLocalTestApp(t)
	state := addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 1 || state.Tracking.AvailableQuantity != 1 || state.Tracking.Storage != "market" || !state.Tracking.Local {
		t.Fatalf("Market membership did not route to API: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
	if state.Local.QuestItems[0].AvailableQuantity != 1 || state.Local.QuestItems[0].Storage != "market" || state.Tracking.RequiredQuantity != 7 {
		t.Fatalf("merged view lost confirmed Market stock or requirements: %+v", state)
	}
	fake.quests.Revision, fake.hideout.Revision = fake.snapshot.Revision, fake.snapshot.Revision
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	if a.GetState().Tracking.AvailableQuantity != 1 {
		t.Fatal("refresh lost remote increment")
	}
}

func TestLocalModeMarketRowWithOnlyDevDemandAddsLocalStock(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	fake.snapshot.Items = []market.TrackedItem{{ItemID: trackingBSGID, AvailableQuantity: 2}}
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	state := addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	if len(fake.writes) != 0 || state.Tracking.AvailableQuantity != 3 || state.Tracking.RequiredQuantity != 7 || state.Tracking.Storage != "local" {
		t.Fatalf("empty Market sources misrouted dev demand: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
	assertLocalStockOnly(t, a, trackingBSGID, 1)
}

func TestLocalModeMarketFailureKeepsPendingStockSeparateFromDevStock(t *testing.T) {
	a, fake := newLocalTestApp(t)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, market.ErrProgressForbidden
	}
	state := addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	saved, err := a.localStore.Load("character-a", "PVP")
	if err != nil || len(fake.writes) != 1 || state.Tracking.AvailableQuantity != 1 || state.Tracking.Error != "" || len(saved.Items) != 0 || !state.ProgressSync.Pending {
		t.Fatalf("remote failure changed local stock: writes=%+v state=%+v saved=%+v err=%v", fake.writes, state.Tracking, saved, err)
	}
}

func TestLocalCompletionKeepsThreeCompletedAndFourRequired(t *testing.T) {
	a, fake := newLocalTestApp(t)
	initial := a.GetState().Tracking
	if initial.RequiredQuantity != 7 || len(initial.Requirements) != 2 ||
		initial.Requirements[0] != (ItemTrackingRequirement{Kind: "fir", RequiredQuantity: 2}) ||
		initial.Requirements[1] != (ItemTrackingRequirement{Kind: "regular", RequiredQuantity: 5}) {
		t.Fatalf("initial requirement: %+v", a.GetState().Tracking)
	}
	fake.quests.Quests[0].Completed = true
	fake.quests.Quests[0].Status = "completed"
	for i := 0; i < 2; i++ {
		fake.snapshot.Items[0].Sources[i].Completed = true
		fake.snapshot.Items[0].Sources[i].RemainingRequiredQuantity = 0
	}
	fake.snapshot.Items[0].RequiredQuantity = 4
	fake.snapshot.Items[0].SpentQuantity = 3
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	state := a.GetState()
	if state.Local.Error != "" || state.Tracking.RequiredQuantity != 4 || len(state.Local.QuestItems) != 1 {
		t.Fatalf("completion did not remove exactly one quest: %+v", state)
	}
	row := state.Local.QuestItems[0]
	if row.RequiredQuantity != 4 || row.FIR != 1 || row.Regular != 3 || row.Completed != 3 {
		t.Fatalf("wrong remaining/completed split: %+v", row)
	}
	if len(state.Tracking.Requirements) != 2 ||
		state.Tracking.Requirements[0] != (ItemTrackingRequirement{Kind: "fir", RequiredQuantity: 1}) ||
		state.Tracking.Requirements[1] != (ItemTrackingRequirement{Kind: "regular", RequiredQuantity: 3}) {
		t.Fatalf("scan panel lost original aggregate FiR/regular requirements: %+v", state.Tracking)
	}
	state.Local.QuestItems[0].Name = "modified"
	if a.GetState().Local.QuestItems[0].Name == "modified" {
		t.Fatal("state aliases private local lists")
	}
}

func TestLocalInconsistentRevisionsKeepMarketOnlyAndBlockLocalAdd(t *testing.T) {
	for _, stock := range []int64{2, 3} {
		t.Run(fmt.Sprintf("market-stock-%d", stock), func(t *testing.T) {
			a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
			fake.followTrackedRevision = false
			fake.quests.Revision = "newer-revision"
			fake.snapshot.Items[0].AvailableQuantity = stock
			fake.snapshot.Items[0].RemainingQuantity = 3 - stock
			a.localAt = time.Time{}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			state := a.GetState()
			if state.Local.Error == "" || state.Tracking.RequiredQuantity != 3 || state.Tracking.AvailableQuantity != stock {
				t.Fatalf("mixed revisions hid the valid separate Market source: %+v", state)
			}
			if a.canAddLocalForTest() {
				t.Fatalf("unsynchronized mandatory sources allowed stock updates at Market stock %d: %+v", stock, state.Tracking)
			}
			state = addAndSyncCachedTest(t, a, trackingItemUID, "regular")
			if len(fake.writes) != 0 || state.Tracking.AvailableQuantity != stock {
				t.Fatalf("failed mandatory synchronization changed stock: writes=%+v state=%+v", fake.writes, state.Tracking)
			}
			assertLocalStockOnly(t, a, trackingBSGID, 0)
		})
	}
}

func (a *ItemWindowApp) canAddLocalForTest() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.canAddTrackedItemLocked()
}

func TestLocalDevFailureShowsMarketAndBlocksStockUpdates(t *testing.T) {
	for _, stock := range []int64{2, 3} {
		t.Run(fmt.Sprintf("market-stock-%d", stock), func(t *testing.T) {
			a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
			fake.snapshot.Items[0].AvailableQuantity = stock
			fake.snapshot.Items[0].RemainingQuantity = 3 - stock
			a.devRequirements = &fakeRequirements{err: errors.New("offline")}
			a.localAt = time.Time{}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			state := a.GetState()
			if state.Local.Error == "" || !state.Tracking.Tracked || state.Tracking.RequiredQuantity != 3 || state.Tracking.AvailableQuantity != stock {
				t.Fatalf("dev failure hid valid Market tracking: %+v", state)
			}
			if a.canAddLocalForTest() {
				t.Fatalf("failed mandatory dev load allowed a stock update: %+v", state.Tracking)
			}
			state = addAndSyncCachedTest(t, a, trackingItemUID, "regular")
			if state.Tracking.Adding || state.Local.Error == "" {
				t.Fatalf("partial Market update lost the dev error or remained pending: %+v", state)
			}
			if len(fake.writes) != 0 || state.Tracking.AvailableQuantity != stock {
				t.Fatalf("failed dev load invented a remote or local increment: writes=%+v state=%+v", fake.writes, state.Tracking)
			}
			assertLocalStockOnly(t, a, trackingBSGID, 0)
		})
	}
}

func TestLocalCacheCannotCrossCharacterWithoutOptionsRevisionChange(t *testing.T) {
	a, fake := newLocalTestApp(t)
	a.state.CharacterUID = "character-b"
	fake.snapshot.CharUID = "character-b"
	fake.quests.CharUID = "character-b"
	fake.hideout.CharUID = "character-b"
	reads := fake.loads
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	if fake.loads != reads+1 || a.GetState().Tracking.CharUID != "character-b" || a.localSnapshot.CharUID != "character-b" {
		t.Fatalf("old character cache reused: reads=%d state=%+v", fake.loads, a.GetState().Tracking)
	}
}

func TestLocalRefreshDoesNotReplaceNewlyScannedItem(t *testing.T) {
	a, _ := newLocalTestApp(t)
	oldItem := *a.state.Item
	a.devRequirements = &fakeRequirements{catalog: tarkovdev.Catalog{Tasks: []tarkovdev.Task{}, Hideout: []tarkovdev.Station{}}, hook: func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.state.Item = &market.Item{UID: "new-item", BSGID: "new-id"}
		a.state.Tracking = &ItemTrackingState{ItemUID: "new-item", ItemID: "new-id", CharUID: "character-a", Loading: true}
	}}
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, oldItem)
	if a.GetState().Tracking.ItemUID != "new-item" {
		t.Fatal("finishing sync restored old item tracking")
	}
}

func TestLocalHotkeyUsesLocalStoreAndOptionsRetainCombinedMode(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	a.state.AddHotkey = "F2"
	a.state.AddHotkeyReady = true
	a.publishTrackedAddHotkey()
	target := a.addHotkeyTarget.Load()
	if target == nil {
		t.Fatal("local shortcut was not available")
	}
	state := a.addTrackedItem(trackingItemUID, target)
	if len(fake.writes) != 0 || state.Tracking.AvailableQuantity != 1 {
		t.Fatal("hotkey did not stay local")
	}
	opts := state.ItemOptions
	opts.Automatic = false
	state = a.SetOptions(opts)
	data, _ := json.Marshal(state)
	if state.Local == nil || state.Tracking == nil || state.Tracking.AvailableQuantity != 1 || strings.Contains(string(data), `"local":null`) {
		t.Fatal("scanner option change discarded the same character's valid progress cache")
	}
	if !state.LocalProgress {
		t.Fatal("scanner options disabled the mandatory combined mode")
	}
}

func TestLocalModeHotkeyMarketItemWritesMarket(t *testing.T) {
	a, fake := newLocalTestApp(t)
	a.state.AddHotkey, a.state.AddHotkeyReady = "F2", true
	a.publishTrackedAddHotkey()
	target := a.addHotkeyTarget.Load()
	if target == nil {
		t.Fatal("Market shortcut was not available in Local Progress")
	}
	state := a.addTrackedItem(trackingItemUID, target)
	if len(fake.writes) != 0 {
		t.Fatal("hotkey waited for a Market write")
	}
	state = syncCachedMarketTest(t, a)
	if len(fake.writes) != 1 || state.Tracking.AvailableQuantity != 1 || state.Tracking.Storage != "market" {
		t.Fatalf("Regular shortcut did not route to Market: writes=%+v state=%+v", fake.writes, state.Tracking)
	}
}

func TestLocalAddKeepsMarketRowsOnlyInLiveDisplay(t *testing.T) {
	a, fake := newLocalTestApp(t)
	fake.snapshot.Items[0].AvailableQuantity = 2
	a.devRequirements = &fakeRequirements{catalog: tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "quest-b", Objectives: []tarkovdev.Objective{
		{ID: "extra", Type: "giveItem", Count: 3, ItemIDs: []string{"dev-item"}},
	}}}, Hideout: []tarkovdev.Station{}}}
	original := *a.state.Item
	a.state.Item = &market.Item{UID: "dev-uid", BSGID: "dev-item", Name: "Dev-only item"}
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	state := addAndSyncCachedTest(t, a, "dev-uid", "regular")
	if len(fake.writes) != 0 || state.Tracking.AvailableQuantity != 1 || len(state.Local.QuestItems) != 2 {
		t.Fatalf("local increment lost live Market rows: %+v", state)
	}
	saved, err := a.localStore.Load("character-a", "PVP")
	if err != nil || len(saved.Items) != 1 || saved.Items[0].ItemID != "dev-item" || saved.Items[0].AvailableQuantity != 1 {
		t.Fatalf("persisted sources were combined: %+v err=%v", saved, err)
	}
	a.state.Item = &original
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, original)
	if a.GetState().Tracking.AvailableQuantity != 2 || a.GetState().Tracking.Storage != "market" {
		t.Fatal("local stock replaced the separate Market stock in the display")
	}
}

func TestLocalInitialAddRefreshCancelsOnOptionsChange(t *testing.T) {
	a, fake := newLocalTestApp(t)
	started, finished := make(chan struct{}), make(chan struct{})
	a.devRequirements = &fakeRequirements{loadHook: func(ctx context.Context) (tarkovdev.Catalog, error) {
		close(started)
		<-ctx.Done()
		return tarkovdev.Catalog{}, ctx.Err()
	}}
	go func() { a.addLocalTrackedItemKind(trackingItemUID, nil, "regular"); close(finished) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	opts := a.GetState().ItemOptions
	opts.Automatic = !opts.Automatic
	a.SetOptions(opts)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("options change did not cancel the initial sync")
	}
	if len(fake.writes) != 0 {
		t.Fatal("canceled local sync wrote Market stock")
	}
}

func TestMarketSeparateInventoryDoesNotDisableIndependentLocalStock(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	fake.snapshot.FIRInventorySeparate = true
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	state := addAndSyncCachedTest(t, a, trackingItemUID, "regular")
	if len(fake.writes) != 0 || state.Tracking.InventorySeparate || state.Tracking.Error != "" || state.Tracking.AvailableQuantity != 1 {
		t.Fatalf("Market inventory settings leaked into the local source: %+v", state.Tracking)
	}
}

// Ugyanaz a tárgy két küldetéshez kell: a Market első céljának három darabját
// a devben lévő átfedés nem duplázza meg, a hiányzó második cél további öt darab.
func newSupplementedLocalTestApp(t *testing.T, uid, itemID string) (*ItemWindowApp, *fakeCharacterProgress, string) {
	t.Helper()
	a, fake := newLocalTestApp(t)
	a.state.Item = &market.Item{UID: uid, BSGID: itemID, Name: "Market item metadata"}
	fake.snapshot.Items = []market.TrackedItem{{
		ItemID: itemID, Name: "Market item metadata", RequiredQuantity: 3, RemainingQuantity: 3,
		Sources: []market.TrackedSource{{Type: "quest", QuestID: "quest-a", Amount: 3, RemainingRequiredQuantity: 3}},
	}}
	a.devRequirements = &fakeRequirements{catalog: tarkovdev.Catalog{
		Tasks: []tarkovdev.Task{
			{ID: "quest-a", Objectives: []tarkovdev.Objective{{ID: "same-goal", Type: "giveItem", Count: 3, ItemIDs: []string{itemID}}}},
			{ID: "quest-b", Objectives: []tarkovdev.Objective{{ID: "missing-goal", Type: "giveItem", Count: 5, ItemIDs: []string{itemID}}}},
		}, Hideout: []tarkovdev.Station{},
	}}
	storeDir := t.TempDir()
	a.localStore = localprogress.NewStore(storeDir)
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	state := a.GetState()
	if state.Local == nil || state.Local.Error != "" || state.Tracking == nil || state.Tracking.RequiredQuantity != 8 || state.Tracking.AvailableQuantity != 0 {
		t.Fatalf("invalid supplemented fixture: %+v", state)
	}
	return a, fake, storeDir
}

func assertLocalStockOnly(t *testing.T, a *ItemWindowApp, itemID string, want int64) {
	t.Helper()
	saved, err := a.localStore.Load("character-a", "PVP")
	if err != nil || len(saved.Items) != 1 {
		t.Fatalf("local stock could not be read separately: snapshot=%+v err=%v", saved, err)
	}
	row := saved.Items[0]
	if row.ItemID != itemID || row.AvailableQuantity != want || row.LocalAvailableQuantity != want || row.MarketTracked ||
		row.MarketAvailableQuantity != 0 || row.MarketRequiredQuantity != 0 || row.RequiredQuantity != 0 || row.SpentQuantity != 0 || row.RemainingQuantity != 0 || len(row.Sources) != 0 {
		t.Fatalf("local snapshot contains Market progress or wrong own stock: want=%d row=%+v", want, row)
	}
}

func assertLocalStockFileOnly(t *testing.T, dir, itemID string, want int64) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one character-local stock file: files=%v err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"version": true, "charUid": true, "gameMode": true, "stock": true,
		"regularStock": true, "firStock": true, "marketKinds": true, "localUsed": true, "handoverReceipts": true, "transferReceipts": true}
	for key := range fields {
		if !allowed[key] {
			t.Fatalf("Market-derived field persisted to local stock file: %s", key)
		}
	}
	var stock map[string]int64
	if err := json.Unmarshal(fields["stock"], &stock); err != nil || len(stock) != 1 || stock[itemID] != want {
		t.Fatalf("file contains remote quantity or wrong own stock: stock=%v want=%d err=%v", stock, want, err)
	}
}

func TestLocalSupplementedItemsFillMarketQuotaThenPersistOnlyLocalRemainder(t *testing.T) {
	for _, item := range []struct{ name, uid, itemID string }{
		{"existing-item", trackingItemUID, trackingBSGID},
		{"independent-item", "second-item-uid", "591aef7986f774139d495f03"},
	} {
		t.Run(item.name, func(t *testing.T) {
			a, fake, dir := newSupplementedLocalTestApp(t, item.uid, item.itemID)
			for added := int64(1); added <= 8; added++ {
				state := addAndSyncCachedTest(t, a, item.uid, "regular")
				if state.Tracking == nil || state.Tracking.Error != "" || state.Local.Error != "" || state.Tracking.Adding ||
					state.Tracking.RequiredQuantity != 8 || state.Tracking.AvailableQuantity != added || state.Tracking.RemainingQuantity != 8-added {
					t.Fatalf("wrong merged stock after Add %d: %+v", added, state)
				}
				if len(fake.writes) != int(min(added, 3)) {
					t.Fatalf("Market received an Add beyond its own quota after %d: %+v", added, fake.writes)
				}
				for i, write := range fake.writes {
					if write.Item != item.itemID || write.Character != "character-a" || write.Quantity != int64(i+1) {
						t.Fatalf("Market quota was not filled exactly once: %+v", fake.writes)
					}
				}
				if len(state.Local.QuestItems) != 1 || state.Local.QuestItems[0].AvailableQuantity != added {
					t.Fatalf("quest list did not combine both stock sources: %+v", state.Local)
				}
				assertLocalStockOnly(t, a, item.itemID, max(0, added-3))
				assertLocalStockFileOnly(t, dir, item.itemID, max(0, added-3))
			}
			a.localAt = time.Time{}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			if state := a.GetState(); state.Tracking.AvailableQuantity != 8 || state.Tracking.Storage != "local" {
				t.Fatalf("refresh lost the local component of a Market-tracked item: %+v", state.Tracking)
			}
			// A Market készletének külső csökkenése a saját helyi részt nem írja át.
			fake.snapshot.Items[0].AvailableQuantity, fake.snapshot.Items[0].RemainingQuantity = 2, 1
			fake.snapshot.Revision = "revision-external"
			a.localAt = time.Time{}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			state := addAndSyncCachedTest(t, a, item.uid, "regular")
			if len(fake.writes) != 4 || fake.writes[3].Quantity != 3 || state.Tracking.AvailableQuantity != 8 {
				t.Fatalf("remote readback overwrote own stock or used merged stock as PATCH target: writes=%+v tracking=%+v", fake.writes, state.Tracking)
			}
			assertLocalStockOnly(t, a, item.itemID, 5)
			assertLocalStockFileOnly(t, dir, item.itemID, 5)
		})
	}
}

func TestLocalSupplementedMarketQuotaTransitionKeepsHotkeyAdd(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	fake.snapshot.Items[0].AvailableQuantity, fake.snapshot.Items[0].RemainingQuantity = 2, 1
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	a.state.AddHotkey, a.state.AddHotkeyReady = "F2", true
	for i := int64(3); i <= 4; i++ {
		a.publishTrackedAddHotkey()
		target := a.addHotkeyTarget.Load()
		if target == nil {
			t.Fatalf("hotkey disappeared at quota transition %d", i)
		}
		state := a.addTrackedItem(trackingItemUID, target)
		state = syncCachedMarketTest(t, a)
		if state.Tracking.AvailableQuantity != i || state.Tracking.Error != "" || state.Tracking.Adding {
			t.Fatalf("hotkey Add was lost at quota transition: %+v", state.Tracking)
		}
	}
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 3 {
		t.Fatalf("Regular shortcut overflowed the Market quota: %+v", fake.writes)
	}
	assertLocalStockOnly(t, a, trackingBSGID, 1)
}

func TestLocalSupplementedQuestCompletionAndMarketZeroKeepIndependentLocalStock(t *testing.T) {
	for _, reset := range []bool{false, true} {
		name := "completion-only"
		if reset {
			name = "completion-and-market-reset"
		}
		t.Run(name, func(t *testing.T) {
			a, fake, dir := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
			for i := 0; i < 5; i++ {
				addAndSyncCachedTest(t, a, trackingItemUID, "regular")
			}
			if len(fake.writes) != 3 || a.GetState().Tracking.AvailableQuantity != 5 {
				t.Fatalf("could not prepare remote three plus local two: writes=%+v state=%+v", fake.writes, a.GetState().Tracking)
			}
			fake.quests.Quests[0].Completed, fake.quests.Quests[0].Status = true, "completed"
			remote := &fake.snapshot.Items[0]
			remote.RequiredQuantity, remote.RemainingQuantity = 0, 0
			remote.Sources[0].Completed, remote.Sources[0].RemainingRequiredQuantity = true, 0
			wantAvailable, wantLocal, wantMissing := int64(5), int64(2), int64(0)
			if reset {
				remote.AvailableQuantity, remote.SpentQuantity = 0, 3
				wantAvailable, wantLocal, wantMissing = 2, 2, 3
			}
			fake.snapshot.Revision = "revision-quest-completed"
			a.localAt = time.Time{}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			state := a.GetState()
			if state.Local.Error != "" || state.Tracking.RequiredQuantity != 5 || state.Tracking.AvailableQuantity != wantAvailable || state.Tracking.RemainingQuantity != wantMissing || state.Tracking.Storage != "local" ||
				len(state.Local.QuestItems) != 1 || state.Local.QuestItems[0].Completed != 3 {
				t.Fatalf("quest completion or Market reset consumed independent local stock: %+v", state)
			}
			state = addAndSyncCachedTest(t, a, trackingItemUID, "regular")
			added := int64(0)
			if wantMissing > 0 {
				added = 1
			}
			if len(fake.writes) != 3 || state.Tracking.Error != "" || state.Tracking.AvailableQuantity != wantAvailable+added || state.Tracking.RequiredQuantity != 5 {
				t.Fatalf("zero Market quota did not continue the independent dev requirement: writes=%+v tracking=%+v", fake.writes, state.Tracking)
			}
			assertLocalStockOnly(t, a, trackingBSGID, wantLocal+added)
			assertLocalStockFileOnly(t, dir, trackingBSGID, wantLocal+added)
		})
	}
}

func TestLocalSupplementedFreshMarketReadRedirectsSameAddBeforePatch(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	fake.snapshot.Items[0].AvailableQuantity, fake.snapshot.Items[0].RemainingQuantity = 2, 1
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	initialReads := fake.loads
	fake.loadHook = func(count int) (market.TrackedSnapshot, error) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		// Az Add teljes merge-je még kettőt lát; a PATCH előtti GET már hármat.
		if count == initialReads+2 {
			fake.snapshot.Items[0].AvailableQuantity, fake.snapshot.Items[0].RemainingQuantity = 3, 0
			fake.snapshot.Revision = "revision-external"
		}
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	state := a.addLocalTrackedItemKind(trackingItemUID, nil, "regular")
	if len(fake.writes) != 0 || state.Tracking.AvailableQuantity != 4 || state.Tracking.Storage != "local" || state.Tracking.Error != "" || state.Tracking.Adding {
		t.Fatalf("fresh quota transition dropped Add or sent an unnecessary PATCH: writes=%+v tracking=%+v", fake.writes, state.Tracking)
	}
	assertLocalStockOnly(t, a, trackingBSGID, 1)
}

func TestLocalSupplementedRejectedRevisionRedirectsSameAddToLocal(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	fake.snapshot.Items[0].AvailableQuantity, fake.snapshot.Items[0].RemainingQuantity = 2, 1
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		fake.mu.Lock()
		fake.snapshot.Items[0].AvailableQuantity, fake.snapshot.Items[0].RemainingQuantity = 3, 0
		fake.snapshot.Revision = "revision-external"
		fake.mu.Unlock()
		return market.ProgressWriteResult{}, market.ErrProgressConflict
	}
	state := a.addLocalTrackedItemKind(trackingItemUID, nil, "regular")
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 3 || state.Tracking.AvailableQuantity != 4 || state.Tracking.Storage != "local" || state.Tracking.Error != "" || state.Tracking.Adding {
		t.Fatalf("rejected PATCH was repeated or Add was lost after quota changed: writes=%+v tracking=%+v", fake.writes, state.Tracking)
	}
	assertLocalStockOnly(t, a, trackingBSGID, 1)
}

func TestLocalSupplementedUncertainMarketWriteNeverFallsBackToLocal(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(fmt.Sprintf("remote-accepted-%t", accepted), func(t *testing.T) {
			a, fake, dir := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
			fake.snapshot.Items[0].AvailableQuantity, fake.snapshot.Items[0].RemainingQuantity = 2, 1
			a.localAt = time.Time{}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
				if accepted {
					fake.commitWrite(write)
				}
				return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
			}
			state := addAndSyncCachedTest(t, a, trackingItemUID, "regular")
			if len(fake.writes) != 1 || state.Tracking.AvailableQuantity != 3 || state.Tracking.Adding || state.MarketFallbackPending == accepted {
				t.Fatalf("uncertain Market operation was replayed or locally duplicated: writes=%+v tracking=%+v", fake.writes, state.Tracking)
			}
			assertLocalStockOnly(t, a, trackingBSGID, 0)
			assertLocalStockFileOnly(t, dir, trackingBSGID, 0)
		})
	}
}
