package plus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

const trackingItemUID = "fce1532d-cc17-4c7f-8fc8-8427e425b751"
const trackingBSGID = "544fb3364bdc2d34748b456a"

type stockWrite struct {
	Character, Item, Revision string
	Quantity                  int64
}

type fakeTrackingAPI struct {
	mu               sync.Mutex
	snapshot         market.TrackedSnapshot
	characters       []market.Character
	loads            int
	lists            int
	lookups          int
	writes           []stockWrite
	loadHook         func(int) (market.TrackedSnapshot, error)
	listHook         func() ([]market.Character, error)
	writeHook        func(stockWrite) (market.ProgressWriteResult, error)
	writeContextHook func(context.Context, stockWrite) (market.ProgressWriteResult, error)
	lookupError      error
}

func trackingFixture() market.TrackedSnapshot {
	return market.TrackedSnapshot{CharUID: "character-a", GameMode: "PVP", Revision: "revision-1", Items: []market.TrackedItem{{
		ItemID: trackingBSGID, Name: "Immobilizing splint", RequiredQuantity: 7, RemainingQuantity: 7,
		Sources: []market.TrackedSource{{Type: "quest", Amount: 7, RemainingRequiredQuantity: 7, FIR: true}},
	}}}
}

func newTrackingTestApp(t *testing.T) (*ItemWindowApp, *fakeTrackingAPI) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fake := &fakeTrackingAPI{snapshot: trackingFixture(), characters: []market.Character{{CharUID: "character-a", Name: "Minta", GameMode: "PVP"}}}
	item := market.Item{UID: trackingItemUID, BSGID: trackingBSGID, Name: "Immobilizing splint"}
	a := &ItemWindowApp{ctx: ctx, apiKey: "local-test-key", tracker: fake, dir: t.TempDir(), state: ItemState{
		ItemOptions: ItemOptions{Enabled: true, Automatic: true, Language: "en", Mode: "pvp", CharacterUID: "character-a", revision: 8},
		HasAPIKey:   true, Item: &item, Tracking: trackedView(item, fake.snapshot),
	}}
	return a, fake
}

func (f *fakeTrackingAPI) ListCharacters(context.Context, string) ([]market.Character, error) {
	f.mu.Lock()
	f.lists++
	characters := append([]market.Character(nil), f.characters...)
	f.mu.Unlock()
	if f.listHook != nil {
		return f.listHook()
	}
	return characters, nil
}

func (f *fakeTrackingAPI) LoadTracked(context.Context, string, string, string) (market.TrackedSnapshot, error) {
	f.mu.Lock()
	f.loads++
	count := f.loads
	snapshot := cloneTrackedSnapshot(f.snapshot)
	f.mu.Unlock()
	if f.loadHook != nil {
		return f.loadHook(count)
	}
	return snapshot, nil
}

func (f *fakeTrackingAPI) SetTrackedQuantity(ctx context.Context, _ string, character, item, revision string, quantity int64) (market.ProgressWriteResult, error) {
	write := stockWrite{character, item, revision, quantity}
	if f.writeContextHook != nil {
		return f.writeContextHook(ctx, write)
	}
	f.mu.Lock()
	f.writes = append(f.writes, write)
	f.mu.Unlock()
	if f.writeHook != nil {
		return f.writeHook(write)
	}
	return f.commitWrite(write), nil
}

func (f *fakeTrackingAPI) commitWrite(write stockWrite) market.ProgressWriteResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshot.Items[0].AvailableQuantity = write.Quantity
	f.snapshot.Items[0].RemainingQuantity = max(0, f.snapshot.Items[0].RequiredQuantity-write.Quantity)
	f.snapshot.Revision = "revision-saved"
	return market.ProgressWriteResult{OK: true, Changed: true, Revision: f.snapshot.Revision, NotificationDelivered: true}
}

func (f *fakeTrackingAPI) LookupItem(context.Context, string, string, string, string) (market.Item, error) {
	f.mu.Lock()
	f.lookups++
	f.mu.Unlock()
	return market.Item{UID: trackingItemUID, BSGID: trackingBSGID}, f.lookupError
}

func TestTrackedCountsAndRequirementUseBSGIDAndActiveSources(t *testing.T) {
	item := market.Item{UID: trackingItemUID, BSGID: trackingBSGID}
	snapshot := trackingFixture()
	snapshot.Items[0].AvailableQuantity = 2
	snapshot.Items[0].RemainingQuantity = 5
	for _, tc := range []struct {
		name    string
		sources []market.TrackedSource
		want    string
	}{
		{"fir", []market.TrackedSource{{FIR: true, RemainingRequiredQuantity: 7}}, "fir"},
		{"regular", []market.TrackedSource{{RemainingRequiredQuantity: 7}}, "regular"},
		{"mixed", []market.TrackedSource{{FIR: true, RemainingRequiredQuantity: 2}, {RemainingRequiredQuantity: 5}}, "mixed"},
		{"completed and canceled excluded", []market.TrackedSource{{FIR: true, RemainingRequiredQuantity: 3, Completed: true}, {FIR: true, RemainingRequiredQuantity: 3, Canceled: true}, {RemainingRequiredQuantity: 7}}, "regular"},
		{"no remaining source", []market.TrackedSource{{FIR: true}, {RemainingRequiredQuantity: 7, Completed: true}}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot.Items[0].Sources = tc.sources
			view := trackedView(item, snapshot)
			if !view.Tracked || view.Requirement != tc.want || view.AvailableQuantity != 2 || view.RequiredQuantity != 7 || view.RemainingQuantity != 5 {
				t.Fatalf("incorrect tracked requirement/counts: %+v", view)
			}
		})
	}
	snapshot.Items[0].ItemID = item.UID
	if trackedView(item, snapshot).Tracked {
		t.Fatal("market UUID must not match the BSG item ID")
	}
}

func TestAddTrackedItemReadsFreshStockAndAddsExactlyOne(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	fake.snapshot.Items[0].AvailableQuantity = 3
	fake.snapshot.Items[0].RemainingQuantity = 4
	state := a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 1 || fake.writes[0] != (stockWrite{"character-a", trackingBSGID, "revision-1", 4}) || fake.loads != 2 {
		t.Fatalf("must read, write +1 with current revision, then read back: %+v, reads=%d", fake.writes, fake.loads)
	}
	if state.Tracking.AvailableQuantity != 4 || state.Tracking.RequiredQuantity != 7 || state.Tracking.RemainingQuantity != 3 || state.Tracking.Adding || state.Tracking.Error != "" || state.Tracking.Variants != nil || !strings.Contains(state.Tracking.Status, "Added 1 item") {
		t.Fatalf("incorrect saved view: %+v", state.Tracking)
	}
}

func mixedTrackingRequirementsFixture() market.TrackedSnapshot {
	snapshot := trackingFixture()
	snapshot.Items[0].RequiredQuantity = 10
	snapshot.Items[0].AvailableQuantity = 5
	snapshot.Items[0].RemainingQuantity = 5
	snapshot.Items[0].Sources = []market.TrackedSource{
		{Type: "quest", FIR: true, Amount: 20, RemainingRequiredQuantity: 4},
		{Type: "quest", FIR: true, Amount: 3, RemainingRequiredQuantity: 3},
		{Type: "hideout", Amount: 10, RemainingRequiredQuantity: 3},
		{FIR: true, Amount: 100, RemainingRequiredQuantity: 100, Completed: true},
		{Amount: 100, RemainingRequiredQuantity: 100, Canceled: true},
		{FIR: true, Amount: 100, RemainingRequiredQuantity: 100, Optional: true},
		{Amount: 1, RemainingRequiredQuantity: 0},
	}
	return snapshot
}

func TestTrackedRequirementsKeepFIRAndRegularSeparateFromSharedStock(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	fake.snapshot = mixedTrackingRequirementsFixture()
	a.state.Tracking = trackedView(*a.state.Item, fake.snapshot)
	view := a.state.Tracking
	if view.Requirement != "mixed" || len(view.Requirements) != 2 ||
		view.Requirements[0] != (ItemTrackingRequirement{Kind: "fir", RequiredQuantity: 7}) ||
		view.Requirements[1] != (ItemTrackingRequirement{Kind: "regular", RequiredQuantity: 3}) ||
		view.AvailableQuantity != 5 || view.RequiredQuantity != 10 || view.RemainingQuantity != 5 {
		t.Fatalf("source requirements were merged, reduced by stock, or taken from original amounts: %+v", view)
	}
	state := a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 6 || state.Tracking.AvailableQuantity != 6 ||
		len(state.Tracking.Requirements) != 2 || state.Tracking.Requirements[0].RequiredQuantity != 7 || state.Tracking.Requirements[1].RequiredQuantity != 3 {
		t.Fatalf("adding shared stock changed a requirement or invented a per-type write: writes=%+v view=%+v", fake.writes, state.Tracking)
	}
}

func TestTrackedRequirementGroupsArePrivateStateCopies(t *testing.T) {
	a, _ := newTrackingTestApp(t)
	a.state.Tracking = trackedView(*a.state.Item, mixedTrackingRequirementsFixture())
	state := a.GetState()
	state.Tracking.Requirements[0].Kind = "changed"
	state.Tracking.Requirements[0].RequiredQuantity = 999
	fresh := a.GetState()
	if fresh.Tracking.Requirements[0].Kind != "fir" || fresh.Tracking.Requirements[0].RequiredQuantity != 7 {
		t.Fatal("returned requirements alias private application state")
	}
}

func completedCMSFixture() market.TrackedSnapshot {
	snapshot := trackingFixture()
	snapshot.Items[0].Name = "CMS surgical kit"
	snapshot.Items[0].RequiredQuantity = 0
	snapshot.Items[0].RemainingQuantity = 0
	snapshot.Items[0].SpentQuantity = 4
	snapshot.Items[0].Sources = []market.TrackedSource{{Type: "quest", QuestID: "completed-quest", Amount: 4, FIR: true, Completed: true}}
	return snapshot
}

func TestCompletedSourcesKeepOriginalQuantitySeparateFromStock(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	fake.snapshot = completedCMSFixture()
	a.state.Item.Name = "CMS surgical kit"
	a.state.Tracking = trackedView(*a.state.Item, fake.snapshot)
	view := a.GetState().Tracking
	if view.AvailableQuantity != 0 || view.RequiredQuantity != 0 || len(view.Requirements) != 0 ||
		len(view.CompletedSources) != 1 || view.CompletedSources[0] != (ItemTrackingCompletedSource{Type: "quest", Quantity: 4}) {
		t.Fatalf("completion was lost or became stock/active demand: %+v", view)
	}
	view.CompletedSources[0].Quantity = 999
	view.CompletedSources[0].Type = "changed"
	if fresh := a.GetState().Tracking.CompletedSources[0]; fresh.Quantity != 4 || fresh.Type != "quest" {
		t.Fatal("returned completion data aliases private application state")
	}
	state := a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 1 || fake.writes[0].Quantity != 1 || state.Tracking.AvailableQuantity != 1 ||
		state.Tracking.RequiredQuantity != 0 || len(state.Tracking.CompletedSources) != 1 || state.Tracking.CompletedSources[0].Quantity != 4 {
		t.Fatalf("adding actual stock changed historical completion: writes=%+v view=%+v", fake.writes, state.Tracking)
	}
}

func TestOnlyExplicitUncanceledCompletionBecomesACompletedSource(t *testing.T) {
	item := market.Item{UID: trackingItemUID, BSGID: trackingBSGID}
	snapshot := completedCMSFixture()
	snapshot.Items[0].RequiredQuantity = 5
	snapshot.Items[0].AvailableQuantity = 2
	snapshot.Items[0].RemainingQuantity = 3
	snapshot.Items[0].Sources = []market.TrackedSource{
		{Type: "quest", Amount: 4, Completed: true},
		{UpgradeKey: "Workbench_2", Amount: 2, Completed: true},
		{Amount: 0, Completed: true},
		{Type: "quest", Amount: 3, Completed: true, Optional: true},
		{Type: "quest", Amount: 9, Completed: true, Canceled: true},
		{Type: "quest", Amount: 9, RemainingRequiredQuantity: 0},
		{Type: "quest", Amount: 9, RemainingRequiredQuantity: 9, Optional: true},
		{Type: "quest", Amount: 2, RemainingRequiredQuantity: 2, FIR: true},
		{Type: "hideout", Amount: 3, RemainingRequiredQuantity: 3},
	}
	view := trackedView(item, snapshot)
	if len(view.CompletedSources) != 4 || view.CompletedSources[0].Quantity != 4 ||
		view.CompletedSources[1].Type != "hideout" || view.CompletedSources[2].Type != "unknown" ||
		view.CompletedSources[2].Quantity != 0 || !view.CompletedSources[3].Optional {
		t.Fatalf("missing, canceled, or optional completion was misclassified: %+v", view.CompletedSources)
	}
	if view.AvailableQuantity != 2 || view.RequiredQuantity != 5 || view.RemainingQuantity != 3 || view.Requirement != "mixed" ||
		len(view.Requirements) != 2 || view.Requirements[0].RequiredQuantity != 2 || view.Requirements[1].RequiredQuantity != 3 {
		t.Fatalf("historical completion changed active shared progress: %+v", view)
	}
	for _, sources := range [][]market.TrackedSource{
		nil,
		{{Type: "quest", Amount: 4}},
		{{Type: "quest", Amount: 4, Canceled: true}},
		{{Type: "quest", Amount: 4, Optional: true}},
	} {
		snapshot.Items[0].RequiredQuantity = 0
		snapshot.Items[0].Sources = sources
		if view := trackedView(item, snapshot); len(view.CompletedSources) != 0 {
			t.Fatal("zero demand or spent quantity invented a completed quest")
		}
	}
}

func TestCompletedSourceTypeRequiresUnambiguousReferences(t *testing.T) {
	for _, tc := range []struct {
		source market.TrackedSource
		want   string
	}{
		{market.TrackedSource{Type: "QUEST"}, "quest"},
		{market.TrackedSource{Type: "hideout"}, "hideout"},
		{market.TrackedSource{QuestID: "quest-a"}, "quest"},
		{market.TrackedSource{UpgradeKey: "Workbench_2"}, "hideout"},
		{market.TrackedSource{QuestID: "quest-a", UpgradeKey: "Workbench_2"}, "unknown"},
		{market.TrackedSource{Type: "quest", UpgradeKey: "Workbench_2"}, "unknown"},
		{market.TrackedSource{Type: "hideout", QuestID: "quest-a"}, "unknown"},
		{market.TrackedSource{Type: "other"}, "unknown"},
		{market.TrackedSource{QuestID: " ", UpgradeKey: "\t"}, "unknown"},
	} {
		if got := completedSourceType(tc.source); got != tc.want {
			t.Fatalf("completion type got %q, want %q", got, tc.want)
		}
	}
}

func TestTrackedConflictReconcilesFromNewStockOnce(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	fake.snapshot.Items[0].AvailableQuantity = 3
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		if write.Revision == "revision-1" {
			fake.mu.Lock()
			fake.snapshot.Items[0].AvailableQuantity = 5
			fake.snapshot.Revision = "revision-2"
			fake.mu.Unlock()
			return market.ProgressWriteResult{}, market.ErrProgressConflict
		}
		return fake.commitWrite(write), nil
	}
	state := a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 2 || fake.writes[0].Quantity != 4 || fake.writes[1].Quantity != 6 || fake.writes[1].Revision != "revision-2" || state.Tracking.AvailableQuantity != 6 || state.Tracking.Error != "" {
		t.Fatalf("conflict must add one to the new stock, not replay the old count: %+v %+v", fake.writes, state.Tracking)
	}
}

func TestTrackedRepeatedConflictStopsWithoutFurtherWrites(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, market.ErrProgressConflict
	}
	state := a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 2 || state.Tracking.Error == "" || state.Tracking.Adding {
		t.Fatalf("repeated conflict should stop: %+v %+v", fake.writes, state.Tracking)
	}
}

func TestTrackedUncertainWriteOnlyReadsBack(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{false: "not_applied", true: "applied"}[applied], func(t *testing.T) {
			a, fake := newTrackingTestApp(t)
			fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
				if applied {
					fake.commitWrite(write)
				}
				return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
			}
			state := a.AddTrackedItem(trackingItemUID)
			if len(fake.writes) != 1 || fake.loads != 2 || state.Tracking.Adding {
				t.Fatalf("an uncertain PATCH must never be replayed: writes=%+v, loads=%d, view=%+v", fake.writes, fake.loads, state.Tracking)
			}
			if applied {
				if state.Tracking.AvailableQuantity != 1 || state.Tracking.Error != "" || !strings.Contains(state.Tracking.Status, "verified") {
					t.Fatalf("readback should show observed stock: %+v", state.Tracking)
				}
			} else {
				if state.Tracking.Error == "" {
					t.Fatal("unknown result must be visible")
				}
				a.AddTrackedItem(trackingItemUID)
				if len(fake.writes) != 1 {
					t.Fatal("error must require a refresh before another explicit add")
				}
			}
		})
	}
}

func TestConfirmedWriteSurvivesReadbackOrNotificationFailure(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	fake.loadHook = func(count int) (market.TrackedSnapshot, error) {
		if count > 1 {
			return market.TrackedSnapshot{}, errors.New("readback failed")
		}
		return trackingFixture(), nil
	}
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		result := fake.commitWrite(write)
		result.NotificationDelivered = false
		return result, nil
	}
	state := a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 1 || state.Tracking.AvailableQuantity != 1 || state.Tracking.Error != "" || state.Tracking.Adding || !strings.Contains(state.Tracking.Status, "Added 1 item") || !strings.Contains(state.Tracking.Status, "notification") {
		t.Fatalf("confirmed PATCH remains successful without web notification/readback: %+v", state.Tracking)
	}
	if !a.trackedAt.IsZero() {
		t.Fatal("failed readback must not mark the complete snapshot as freshly fetched")
	}
	fake.loadHook = nil
	a.refreshTracking()
	if fake.loads != 3 || a.trackedAt.IsZero() || a.GetState().Tracking.AvailableQuantity != 1 {
		t.Fatal("the next scan must read fresh API data without losing confirmed stock")
	}
}

func TestConcurrentAddIsSuppressed(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan ItemState, 1)
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		close(entered)
		<-release
		return fake.commitWrite(write), nil
	}
	go func() { done <- a.AddTrackedItem(trackingItemUID) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("write not started")
	}
	second := a.AddTrackedItem(trackingItemUID)
	close(release)
	var first ItemState
	select {
	case first = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("write not completed")
	}
	if !second.Tracking.Adding || len(fake.writes) != 1 || first.Tracking.AvailableQuantity != 1 {
		t.Fatalf("double click must produce one write: %+v", fake.writes)
	}
}

func TestAddRejectsStaleOrUntrackedTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ItemWindowApp)
	}{
		{"different displayed item", func(a *ItemWindowApp) { a.state.Item.UID = "another" }},
		{"different tracked item", func(a *ItemWindowApp) { a.state.Tracking.ItemUID = "another" }},
		{"different BSG ID", func(a *ItemWindowApp) { a.state.Tracking.ItemID = "another" }},
		{"different character", func(a *ItemWindowApp) { a.state.CharacterUID = "another" }},
		{"untracked item", func(a *ItemWindowApp) { a.state.Tracking.Tracked = false }},
		{"error", func(a *ItemWindowApp) { a.state.Tracking.Error = "error" }},
		{"loading", func(a *ItemWindowApp) { a.state.Tracking.Loading = true }},
		{"no API key", func(a *ItemWindowApp) { a.apiKey = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake := newTrackingTestApp(t)
			tc.change(a)
			a.AddTrackedItem(trackingItemUID)
			if fake.loads != 0 || len(fake.writes) != 0 {
				t.Fatal("stale/untracked target must not touch API stock")
			}
		})
	}
}

func TestSeparateFIRInventoryCannotUseGenericQuantityPatch(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	fake.snapshot.FIRInventorySeparate = true
	state := a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 0 || !state.Tracking.Tracked || !state.Tracking.InventorySeparate || state.Tracking.Error == "" || state.Tracking.Adding {
		t.Fatalf("unsupported separate stock must not be modified: %+v", state.Tracking)
	}
}

func TestItemChangeDuringFreshReadCancelsPatch(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	fake.loadHook = func(int) (market.TrackedSnapshot, error) {
		a.acceptItem(market.Item{UID: "another", BSGID: "other-item"}, true, a.GetState().ItemOptions, nil)
		return trackingFixture(), nil
	}
	state := a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 0 || state.Item.UID != "another" || state.Tracking != nil {
		t.Fatal("old item must not be written or redisplayed after selection changes")
	}
}

func TestProfileChangeAfterPatchCannotOverwriteCurrentState(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		result := fake.commitWrite(write)
		opts := a.GetState().ItemOptions
		opts.CharacterUID = "character-b"
		a.SetOptions(opts)
		return result, nil
	}
	state := a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 1 || fake.writes[0].Character != "character-a" || state.CharacterUID != "character-b" || state.Tracking != nil {
		t.Fatal("saved response must not retarget the character selected afterwards")
	}
}

func TestPendingStockWriteIsCanceledWhenItsTargetChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ItemWindowApp)
	}{
		{"item", func(a *ItemWindowApp) {
			a.acceptItem(market.Item{UID: "another", BSGID: "other-item"}, true, a.GetState().ItemOptions, nil)
		}},
		{"character", func(a *ItemWindowApp) {
			opts := a.GetState().ItemOptions
			opts.CharacterUID = "character-b"
			a.SetOptions(opts)
		}},
		{"mode", func(a *ItemWindowApp) { opts := a.GetState().ItemOptions; opts.Mode = "pve"; a.SetOptions(opts) }},
		{"API key cleared", func(a *ItemWindowApp) { a.ClearAPIKey() }},
		{"API key replaced", func(a *ItemWindowApp) {
			a.mu.Lock()
			a.apiKey = "replacement-test-key"
			a.state.ItemOptions.revision++
			a.clearTrackingLocked()
			a.state.Item = nil
			a.mu.Unlock()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake := newTrackingTestApp(t)
			entered, canceled, done := make(chan struct{}), make(chan bool, 1), make(chan ItemState, 1)
			fake.writeContextHook = func(ctx context.Context, write stockWrite) (market.ProgressWriteResult, error) {
				close(entered)
				select {
				case <-ctx.Done():
					canceled <- true
					return market.ProgressWriteResult{}, ctx.Err()
				case <-time.After(3 * time.Second):
					canceled <- false
					return fake.commitWrite(write), nil
				}
			}
			go func() { done <- a.AddTrackedItem(trackingItemUID) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("pending write not started")
			}
			tc.change(a)
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("target change did not cancel the pending write")
			}
			if !<-canceled || fake.snapshot.Items[0].AvailableQuantity != 0 {
				t.Fatal("old pending operation reached the stock after its target changed")
			}
		})
	}
}

func TestTrackingAutoSelectsOnlyOneCompatibleCharacter(t *testing.T) {
	for _, tc := range []struct {
		name       string
		characters []market.Character
		want       string
		reads      int
	}{
		{"one compatible", []market.Character{{CharUID: "character-a", GameMode: "PVP"}, {CharUID: "pve-character", GameMode: "PVE"}}, "character-a", 1},
		{"multiple compatible", []market.Character{{CharUID: "character-a", GameMode: "PVP"}, {CharUID: "character-b", GameMode: "PVP"}}, "", 0},
		{"no compatible", []market.Character{{CharUID: "pve-character", GameMode: "PVE"}}, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake := newTrackingTestApp(t)
			a.state.CharacterUID = ""
			fake.characters = tc.characters
			a.refreshTracking()
			state := a.GetState()
			if state.CharacterUID != tc.want || fake.loads != tc.reads {
				t.Fatalf("unexpected character selection: %+v reads=%d", state, fake.loads)
			}
			if tc.want == "" && state.CharactersError == "" {
				t.Fatal("selection hint missing")
			}
			if tc.want != "" && (state.Tracking == nil || !state.Tracking.Tracked) {
				t.Fatal("tracked view missing")
			}
		})
	}
}

func TestOldCatalogResolvesBSGIDOnceAndPreservesItOnSameItem(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	a.state.Item.BSGID = ""
	a.refreshTracking()
	if fake.lookups != 1 || !a.GetState().Tracking.Tracked {
		t.Fatal("old catalog item must resolve documented BSG ID")
	}
	old := *a.GetState().Item
	old.BSGID = ""
	a.acceptItem(old, true, a.GetState().ItemOptions, nil)
	a.refreshTracking()
	if fake.lookups != 1 || fake.loads != 1 || a.GetState().Item.BSGID != trackingBSGID {
		t.Fatal("same item should reuse BSG ID and short-lived tracked snapshot")
	}
}

func TestTrackingCacheKeepsOriginalFetchTimeAndExpiresDuringRepeatedScans(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	a.refreshTracking()
	if fake.loads != 1 || a.trackedAt.IsZero() {
		t.Fatal("initial tracking refresh must record a successful API fetch")
	}
	// Backdate the fetch instead of waiting for real time to pass. Repeated
	// scans within the TTL must keep this exact timestamp, not restart it.
	fetchedAt := time.Now().Add(-time.Second)
	a.trackedAt = fetchedAt
	fake.snapshot.Items[0].AvailableQuantity = 3
	fake.snapshot.Items[0].RemainingQuantity = 4
	for i := 0; i < 10; i++ {
		a.refreshTracking()
		if fake.loads != 1 || !a.trackedAt.Equal(fetchedAt) || a.GetState().Tracking.AvailableQuantity != 0 {
			t.Fatalf("cached scan %d renewed the original fetch or refetched prematurely: loads=%d fetchedAt=%v", i, fake.loads, a.trackedAt)
		}
	}
	a.trackedAt = time.Now().Add(-6 * time.Second)
	before := time.Now()
	a.refreshTracking()
	after := time.Now()
	if fake.loads != 2 || a.GetState().Tracking.AvailableQuantity != 3 || a.trackedAt.Before(before) || a.trackedAt.After(after) {
		t.Fatalf("expired snapshot must fetch changed stock and record the actual fetch time: loads=%d view=%+v fetchedAt=%v", fake.loads, a.GetState().Tracking, a.trackedAt)
	}
	fetchedAt = a.trackedAt
	a.refreshTracking()
	if fake.loads != 2 || !a.trackedAt.Equal(fetchedAt) {
		t.Fatal("newly fetched snapshot should be reusable without extending its lifetime")
	}
}

func TestTrackingFailedRefreshDoesNotRenewExpiredSnapshot(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	a.refreshTracking()
	fetchedAt := time.Now().Add(-6 * time.Second)
	a.trackedAt = fetchedAt
	fake.snapshot.Items[0].AvailableQuantity = 2
	fake.snapshot.Items[0].RemainingQuantity = 5
	fake.loadHook = func(count int) (market.TrackedSnapshot, error) {
		if count == 2 {
			return market.TrackedSnapshot{}, errors.New("temporary API failure")
		}
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	a.refreshTracking()
	if fake.loads != 2 || !a.trackedAt.Equal(fetchedAt) || a.trackedSnapshot.Items[0].AvailableQuantity != 0 || a.GetState().Tracking.Error == "" {
		t.Fatal("failed fetch must preserve the expired snapshot and report the failure")
	}
	a.refreshTracking()
	if fake.loads != 3 || !a.trackedAt.After(fetchedAt) || a.GetState().Tracking.AvailableQuantity != 2 || a.GetState().Tracking.Error != "" {
		t.Fatal("next scan must retry the expired snapshot and display fresh API stock")
	}
}

func TestTrackingWrongProfileResponseDoesNotRenewSnapshot(t *testing.T) {
	for _, profile := range []string{"character", "mode"} {
		t.Run(profile, func(t *testing.T) {
			a, fake := newTrackingTestApp(t)
			a.refreshTracking()
			fetchedAt := time.Now().Add(-6 * time.Second)
			a.trackedAt = fetchedAt
			if profile == "character" {
				fake.snapshot.CharUID = "another-character"
			} else {
				fake.snapshot.GameMode = "PVE"
			}
			a.refreshTracking()
			if fake.loads != 2 || !a.trackedAt.Equal(fetchedAt) || a.trackedSnapshot.CharUID != "character-a" || a.trackedSnapshot.GameMode != "PVP" || a.GetState().Tracking.Error == "" {
				t.Fatal("an incompatible API snapshot must not renew cached data")
			}
		})
	}
}

func TestAddedStockIsReusedUntilItsReadbackExpires(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	fake.snapshot.Items[0].AvailableQuantity = 3
	fake.snapshot.Items[0].RemainingQuantity = 4
	state := a.AddTrackedItem(trackingItemUID)
	if fake.loads != 2 || len(fake.writes) != 1 || fake.writes[0].Quantity != 4 || state.Tracking.AvailableQuantity != 4 || a.trackedAt.IsZero() {
		t.Fatal("Add must use fresh stock and record the successful readback")
	}
	fetchedAt := a.trackedAt
	a.refreshTracking()
	if fake.loads != 2 || !a.trackedAt.Equal(fetchedAt) || a.GetState().Tracking.AvailableQuantity != 4 {
		t.Fatal("rescanning after Add must retain the confirmed stock without renewing the fetch time")
	}
	a.trackedAt = time.Now().Add(-6 * time.Second)
	fake.snapshot.Items[0].AvailableQuantity = 5
	fake.snapshot.Items[0].RemainingQuantity = 2
	a.refreshTracking()
	if fake.loads != 3 || a.GetState().Tracking.AvailableQuantity != 5 {
		t.Fatal("rescanning after expiry must fetch externally changed stock")
	}
}

func TestTrackingResponseAndStateCopiesDoNotAlias(t *testing.T) {
	a, _ := newTrackingTestApp(t)
	snapshot := trackingFixture()
	a.applyTracking(a.apiKey, a.state.ItemOptions, *a.state.Item, snapshot, time.Now(), "")
	snapshot.Items[0].AvailableQuantity = 99
	snapshot.Items[0].Sources[0].FIR = false
	state := a.GetState()
	state.Tracking.AvailableQuantity = 88
	if a.GetState().Tracking.AvailableQuantity != 0 || a.trackedSnapshot.Items[0].AvailableQuantity != 0 || !a.trackedSnapshot.Items[0].Sources[0].FIR {
		t.Fatal("private cached stock/state aliased external response")
	}
}

func TestCharacterOrKeyChangeDuringTrackingReadDiscardsResponse(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	fake.loadHook = func(int) (market.TrackedSnapshot, error) {
		opts := a.GetState().ItemOptions
		opts.CharacterUID = "character-b"
		a.SetOptions(opts)
		return trackingFixture(), nil
	}
	a.refreshTracking()
	if state := a.GetState(); state.CharacterUID != "character-b" || state.Tracking != nil {
		t.Fatalf("old tracked response leaked into new profile: %+v", state)
	}
}

func TestTrackedErrorDoesNotErasePricesOrRecognition(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	a.state.Item.Price = 10000
	a.state.Status = "Item recognized."
	fake.listHook = func() ([]market.Character, error) { return nil, market.ErrProgressForbidden }
	a.refreshTracking()
	state := a.GetState()
	if state.Item.Price != 10000 || state.Error != "" || state.Status != "Item recognized." || state.Tracking.Error == "" || state.CharactersError == "" {
		t.Fatal("private progress failure must preserve recognized item and prices")
	}
}

func TestCharacterSelectionKeepsDuplicateNamesAndWritesChosenUID(t *testing.T) {
	for _, selected := range []string{"character-b", "character-c"} {
		t.Run(selected, func(t *testing.T) {
			a, fake := newTrackingTestApp(t)
			level := 25
			characters := []market.Character{
				{CharUID: "character-a", Name: "ColorsASD", GameMode: "PVP"},
				{CharUID: "character-b", Name: "ColorsASD", GameMode: "SEASON", Level: &level},
				{CharUID: "character-c", Name: "ColorsASD", GameMode: "SEASON", Level: &level},
			}
			a.state.Characters = cloneCharacters(characters)
			fake.characters = cloneCharacters(characters)
			opts := a.GetState().ItemOptions
			opts.CharacterUID = selected
			state := a.SetOptions(opts)
			if state.Mode != "season" || state.CharacterUID != selected || len(state.Characters) != 3 {
				t.Fatalf("exact UID must choose its mode without merging equal names: %+v", state)
			}
			var saved ItemOptions
			data, err := os.ReadFile(filepath.Join(a.dir, "item-settings.json"))
			if err != nil || json.Unmarshal(data, &saved) != nil || saved.Mode != "season" || saved.CharacterUID != selected {
				t.Fatal("chosen UID and derived mode were not persisted together")
			}
			fake.snapshot.CharUID, fake.snapshot.GameMode = selected, "SEASON"
			a.acceptItem(market.Item{UID: trackingItemUID, BSGID: trackingBSGID}, true, state.ItemOptions, nil)
			a.refreshTracking()
			state = a.AddTrackedItem(trackingItemUID)
			if len(fake.writes) != 1 || fake.writes[0].Character != selected || state.Tracking.CharUID != selected || state.Tracking.AvailableQuantity != 1 {
				t.Fatalf("write must reach the selected duplicate's UID: %+v %+v", fake.writes, state.Tracking)
			}
		})
	}
}

func TestManualModeChangeReleasesIncompatibleSelectedCharacter(t *testing.T) {
	a, _ := newTrackingTestApp(t)
	a.state.Characters = []market.Character{{CharUID: "character-a", Name: "ColorsASD", GameMode: "PVP"}}
	opts := a.GetState().ItemOptions
	opts.Mode = "pve"
	state := a.SetOptions(opts)
	if state.Mode != "pve" || state.CharacterUID != "" || state.Tracking != nil || state.Item != nil {
		t.Fatal("explicit game-mode change must not retain a character from the old mode")
	}
}

func TestCharacterLevelsAndUIDsAreCopiedIndependently(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	level := 42
	fake.characters[0].Level = &level
	a.refreshTracking()
	level = 99
	state := a.GetState()
	if *state.Characters[0].Level != 42 {
		t.Fatal("state aliases API character level")
	}
	*state.Characters[0].Level = 88
	state.Characters[0].CharUID = "another"
	if fresh := a.GetState(); *fresh.Characters[0].Level != 42 || fresh.Characters[0].CharUID != "character-a" {
		t.Fatal("returned character metadata aliases private state")
	}
}

func variantTrackingFixture() market.TrackedSnapshot {
	snapshot := trackingFixture()
	snapshot.Items[0].Name = "Metal fuel tank"
	snapshot.Items[0].RequiredQuantity = 4
	snapshot.Items[0].AvailableQuantity = 1
	snapshot.Items[0].RemainingQuantity = 3
	snapshot.Items = append(snapshot.Items,
		market.TrackedItem{ItemID: trackingBSGID, Name: "Metal fuel tank (0/100)", AvailableQuantity: 2, RequiredQuantity: 6, RemainingQuantity: 4,
			Sources: []market.TrackedSource{{Type: "hideout", RemainingRequiredQuantity: 6}}},
		market.TrackedItem{ItemID: "normal-item", Name: "Secure Flash drive", AvailableQuantity: 3, RequiredQuantity: 7, RemainingQuantity: 4,
			Sources: []market.TrackedSource{{Type: "quest", FIR: true, RemainingRequiredQuantity: 7}}},
	)
	return snapshot
}

func detailedVariantTrackingFixture() market.TrackedSnapshot {
	snapshot := variantTrackingFixture()
	snapshot.Items[0].Sources = []market.TrackedSource{
		{Type: "quest", FIR: true, RemainingRequiredQuantity: 3},
		{Type: "hideout", RemainingRequiredQuantity: 1},
		{Type: "quest", QuestID: "completed-fuel-quest", Amount: 5, Completed: true},
	}
	snapshot.Items[1].Sources = append(snapshot.Items[1].Sources,
		market.TrackedSource{Type: "hideout", UpgradeKey: "completed-fuel-upgrade", Amount: 11, Completed: true},
	)
	return snapshot
}

func TestDuplicateVariantsExposeIndependentStockRequirementsAndCompletion(t *testing.T) {
	snapshot := detailedVariantTrackingFixture()
	view := trackedView(market.Item{UID: trackingItemUID, BSGID: trackingBSGID, Name: "Metal fuel tank"}, snapshot)
	if len(view.Variants) != 2 {
		t.Fatalf("both matching API rows must be exposed, excluding unrelated items: %+v", view)
	}
	full, empty := view.Variants[0], view.Variants[1]
	if !full.Tracked || full.VariantName != "Metal fuel tank" || full.AvailableQuantity != 1 || full.RequiredQuantity != 4 || full.RemainingQuantity != 3 ||
		full.Requirement != "mixed" || len(full.Requirements) != 2 ||
		full.Requirements[0] != (ItemTrackingRequirement{Kind: "fir", RequiredQuantity: 3}) ||
		full.Requirements[1] != (ItemTrackingRequirement{Kind: "regular", RequiredQuantity: 1}) ||
		len(full.CompletedSources) != 1 || full.CompletedSources[0] != (ItemTrackingCompletedSource{Type: "quest", Quantity: 5}) {
		t.Fatalf("full variant inherited, summed, or lost another row's data: %+v", full)
	}
	if !empty.Tracked || empty.VariantName != "Metal fuel tank (0/100)" || empty.AvailableQuantity != 2 || empty.RequiredQuantity != 6 || empty.RemainingQuantity != 4 ||
		empty.Requirement != "regular" || len(empty.Requirements) != 1 ||
		empty.Requirements[0] != (ItemTrackingRequirement{Kind: "regular", RequiredQuantity: 6}) ||
		len(empty.CompletedSources) != 1 || empty.CompletedSources[0] != (ItemTrackingCompletedSource{Type: "hideout", Quantity: 11}) {
		t.Fatalf("empty variant inherited, summed, or lost another row's data: %+v", empty)
	}
	for _, variant := range view.Variants {
		if variant.ItemID != trackingBSGID || variant.ItemUID != trackingItemUID || variant.CharUID != "character-a" || variant.Error == "" || len(variant.Variants) != 0 {
			t.Fatalf("each row must retain its identity and block unsupported separate stock writes: %+v", variant)
		}
	}
}

func TestDuplicateVariantsKeepAllRowsWithoutAnExactScannedName(t *testing.T) {
	snapshot := detailedVariantTrackingFixture()
	snapshot.Items = append(snapshot.Items, market.TrackedItem{
		ItemID: trackingBSGID, Name: "Metal fuel tank (50/100)", AvailableQuantity: 3, RequiredQuantity: 9, RemainingQuantity: 6,
		Sources: []market.TrackedSource{{Type: "quest", FIR: true, RemainingRequiredQuantity: 9}},
	})
	for _, name := range []string{"Unknown alias", "", "Metal fuel tank (50/100)"} {
		t.Run(name, func(t *testing.T) {
			view := trackedView(market.Item{UID: trackingItemUID, BSGID: trackingBSGID, Name: name}, snapshot)
			if len(view.Variants) != 3 || view.Error == "" {
				t.Fatalf("scanned alias must not hide or collapse same-ID rows: %+v", view)
			}
			wantNames := []string{"Metal fuel tank", "Metal fuel tank (0/100)", "Metal fuel tank (50/100)"}
			for i, stock := range []int64{1, 2, 3} {
				if variant := view.Variants[i]; variant.AvailableQuantity != stock || variant.VariantName != wantNames[i] {
					t.Fatalf("variant %d lost its original API row: %+v", i, variant)
				}
			}
			third := view.Variants[2]
			if third.RequiredQuantity != 9 || third.RemainingQuantity != 6 || len(third.Requirements) != 1 ||
				third.Requirements[0] != (ItemTrackingRequirement{Kind: "fir", RequiredQuantity: 9}) {
				t.Fatalf("third variant must preserve its independent requirements: %+v", third)
			}
		})
	}
	// Even identical names cannot identify a stock write, but both rows must be
	// available to display separately instead of silently choosing the first.
	snapshot.Items[1].Name = snapshot.Items[0].Name
	view := trackedView(market.Item{UID: trackingItemUID, BSGID: trackingBSGID, Name: "Metal fuel tank"}, snapshot)
	if view.Tracked || len(view.Variants) != 3 || view.Variants[0].AvailableQuantity != 1 || view.Variants[1].AvailableQuantity != 2 {
		t.Fatalf("duplicate display names must preserve their separate rows: %+v", view)
	}
}

func TestDuplicateVariantStateCopiesIsolateNestedRowsAndSlices(t *testing.T) {
	a, _ := newTrackingTestApp(t)
	a.state.Item.Name = "Metal fuel tank"
	snapshot := detailedVariantTrackingFixture()
	if !a.applyTracking(a.apiKey, a.state.ItemOptions, *a.state.Item, snapshot, time.Now(), "") {
		t.Fatal("valid variant snapshot was not applied")
	}
	state := a.GetState()
	state.Tracking.Variants[0].VariantName = "changed"
	state.Tracking.Variants[0].AvailableQuantity = 99
	state.Tracking.Variants[0].Requirements[0].RequiredQuantity = 88
	state.Tracking.Variants[0].CompletedSources[0].Quantity = 77
	state.Tracking.Variants[1].Requirements[0].Kind = "changed"
	state.Tracking.Variants[1].CompletedSources[0].Type = "changed"
	state.Tracking.Variants[1] = nil
	state.Tracking.Requirements[0].RequiredQuantity = 66
	snapshot.Items[0].AvailableQuantity = 55
	snapshot.Items[0].Sources[0].RemainingRequiredQuantity = 44
	fresh := a.GetState()
	if len(fresh.Tracking.Variants) != 2 || fresh.Tracking.Variants[0].VariantName != "Metal fuel tank" ||
		fresh.Tracking.Variants[0].AvailableQuantity != 1 || fresh.Tracking.Variants[0].Requirements[0].RequiredQuantity != 3 ||
		fresh.Tracking.Variants[0].CompletedSources[0].Quantity != 5 || fresh.Tracking.Variants[1] == nil ||
		fresh.Tracking.Variants[1].Requirements[0].Kind != "regular" || fresh.Tracking.Variants[1].CompletedSources[0].Type != "hideout" ||
		fresh.Tracking.Requirements[0].RequiredQuantity != 3 || a.trackedSnapshot.Items[0].AvailableQuantity != 1 ||
		a.trackedSnapshot.Items[0].Sources[0].RemainingRequiredQuantity != 3 {
		t.Fatalf("returned variants or incoming API slices aliased private state: %+v", fresh.Tracking)
	}
}

func TestDuplicateVariantsRefreshBothStocksAfterOriginalCacheExpires(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	a.state.Item.Name = "Metal fuel tank"
	fake.snapshot = detailedVariantTrackingFixture()
	a.refreshTracking()
	if fake.loads != 1 || len(a.GetState().Tracking.Variants) != 2 {
		t.Fatal("initial API response must populate both variant rows")
	}
	fetchedAt := time.Now().Add(-time.Second)
	a.trackedAt = fetchedAt
	fake.snapshot.Items[0].AvailableQuantity = 5
	fake.snapshot.Items[0].RemainingQuantity = 0
	fake.snapshot.Items[1].AvailableQuantity = 7
	fake.snapshot.Items[1].RemainingQuantity = 0
	a.refreshTracking()
	cached := a.GetState().Tracking.Variants
	if fake.loads != 1 || !a.trackedAt.Equal(fetchedAt) || cached[0].AvailableQuantity != 1 || cached[1].AvailableQuantity != 2 {
		t.Fatal("cached variants must preserve both original stocks and fetch time")
	}
	a.trackedAt = time.Now().Add(-6 * time.Second)
	a.refreshTracking()
	fresh := a.GetState().Tracking.Variants
	if fake.loads != 2 || len(fresh) != 2 || fresh[0].AvailableQuantity != 5 || fresh[1].AvailableQuantity != 7 ||
		fresh[0].RemainingQuantity != 0 || fresh[1].RemainingQuantity != 0 || fresh[0].RequiredQuantity != 4 || fresh[1].RequiredQuantity != 6 {
		t.Fatalf("next API refresh must independently update every variant: loads=%d variants=%+v", fake.loads, fresh)
	}
}

func TestDisplayedDuplicateVariantsNeverSendUnsupportedStockWrites(t *testing.T) {
	for _, name := range []string{"Metal fuel tank", "Metal fuel tank (0/100)", "Unknown alias", ""} {
		t.Run(name, func(t *testing.T) {
			a, fake := newTrackingTestApp(t)
			a.state.Item.Name = name
			fake.snapshot = detailedVariantTrackingFixture()
			a.state.Tracking = trackedView(*a.state.Item, fake.snapshot)
			state := a.AddTrackedItem(trackingItemUID)
			if len(fake.writes) != 0 || fake.loads != 0 || state.Tracking.Adding || len(state.Tracking.Variants) != 2 || state.Tracking.Error == "" {
				t.Fatalf("read-only variants must never invent a selector or issue a stock write: writes=%+v loads=%d view=%+v", fake.writes, fake.loads, state.Tracking)
			}
		})
	}
}

func TestDuplicateBSGVariantsDoNotBlockUnrelatedTrackedItems(t *testing.T) {
	snapshot := variantTrackingFixture()
	view := trackedView(market.Item{UID: "normal-market-uid", BSGID: "normal-item", Name: "Secure Flash drive"}, snapshot)
	if !view.Tracked || view.Error != "" || view.AvailableQuantity != 3 || view.RequiredQuantity != 7 || view.RemainingQuantity != 4 || view.Requirement != "fir" {
		t.Fatalf("duplicate variant elsewhere must not invalidate unrelated stock: %+v", view)
	}
	a, fake := newTrackingTestApp(t)
	fake.snapshot = snapshot
	a.state.Item = &market.Item{UID: trackingItemUID, BSGID: "normal-item", Name: "Secure Flash drive"}
	a.state.Tracking = trackedView(*a.state.Item, snapshot)
	// The fake commit helper targets the first record; return a confirmed write
	// directly here and let readback fail so the production fallback updates by ID.
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{OK: true, Changed: true, Revision: "new-revision", NotificationDelivered: true}, nil
	}
	fake.loadHook = func(count int) (market.TrackedSnapshot, error) {
		if count > 1 {
			return market.TrackedSnapshot{}, errors.New("readback failed")
		}
		return cloneTrackedSnapshot(snapshot), nil
	}
	state := a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 1 || fake.writes[0].Item != "normal-item" || fake.writes[0].Quantity != 4 || state.Tracking.AvailableQuantity != 4 || state.Tracking.Error != "" {
		t.Fatalf("unambiguous item must still add one: %+v %+v", fake.writes, state.Tracking)
	}
}

func TestDuplicateVariantsUseOwnExactNameForReadOnlyCounts(t *testing.T) {
	snapshot := variantTrackingFixture()
	for _, tc := range []struct {
		name                string
		available, required int64
		requirement         string
	}{
		{"Metal fuel tank", 1, 4, "fir"},
		{"Metal fuel tank (0/100)", 2, 6, "regular"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := trackedView(market.Item{UID: trackingItemUID, BSGID: trackingBSGID, Name: tc.name}, snapshot)
			if !view.Tracked || view.Error == "" || view.AvailableQuantity != tc.available || view.RequiredQuantity != tc.required || view.Requirement != tc.requirement {
				t.Fatalf("variant must not inherit or sum another variant's counts: %+v", view)
			}
		})
	}
	for _, name := range []string{"Unknown alias", ""} {
		view := trackedView(market.Item{UID: trackingItemUID, BSGID: trackingBSGID, Name: name}, snapshot)
		if view.Tracked || view.Error == "" || view.AvailableQuantity != 0 || view.RequiredQuantity != 0 {
			t.Fatal("ambiguous name must not choose the first stock row")
		}
	}
	snapshot.Items[1].Name = snapshot.Items[0].Name
	if view := trackedView(market.Item{UID: trackingItemUID, BSGID: trackingBSGID, Name: snapshot.Items[0].Name}, snapshot); view.Tracked || view.Error == "" {
		t.Fatal("duplicate names with duplicate IDs must not choose a stock row")
	}
}

func TestDuplicateVariantDiscoveredDuringFreshReadCannotBeWritten(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	a.state.Item.Name = "Metal fuel tank"
	fake.snapshot = variantTrackingFixture()
	state := a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 0 || !state.Tracking.Tracked || state.Tracking.Error == "" || state.Tracking.AvailableQuantity != 1 || state.Tracking.Adding || len(state.Tracking.Variants) != 2 || state.Tracking.Variants[1].AvailableQuantity != 2 {
		t.Fatalf("fresh read must catch ID ambiguity before PATCH: %+v %+v", fake.writes, state.Tracking)
	}
	a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 0 {
		t.Fatal("known ambiguous variant must remain read-only")
	}
}

func TestUncertainWriteIsNotConfirmedByAmbiguousReadback(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	a.state.Item.Name = "Metal fuel tank"
	fake.snapshot.Items[0].Name = a.state.Item.Name
	a.state.Tracking = trackedView(*a.state.Item, fake.snapshot)
	fake.loadHook = func(count int) (market.TrackedSnapshot, error) {
		if count == 1 {
			return cloneTrackedSnapshot(fake.snapshot), nil
		}
		// The exact-name record has the requested target of one, but its ID is
		// now shared by another variant. It cannot confirm which stock was saved.
		return variantTrackingFixture(), nil
	}
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	state := a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 1 || fake.loads != 2 || state.Tracking.Adding || state.Tracking.AvailableQuantity != 1 ||
		!strings.Contains(state.Tracking.Error, "add result could not be verified") || strings.Contains(state.Tracking.Status, "verified") {
		t.Fatalf("ambiguous stock must not confirm or replay an uncertain PATCH: writes=%+v loads=%d view=%+v", fake.writes, fake.loads, state.Tracking)
	}
	a.AddTrackedItem(trackingItemUID)
	if len(fake.writes) != 1 {
		t.Fatal("unconfirmed ambiguous stock must block another write")
	}
}

func TestTrackingErrorsIdentifyCauseWithoutExposingUnderlyingText(t *testing.T) {
	for _, tc := range []struct {
		err      error
		contains string
	}{
		{market.ErrInvalidProgress, "response is incomplete"},
		{market.ErrProgressUnavailable, "service is not responding"},
		{market.ErrProgressRevisionRequired, "stock revision is missing"},
		{market.ErrItemNotFound, "was not found"},
		{market.ErrInvalidCatalog, "item ID response"},
		{context.DeadlineExceeded, "timed out"},
		{errors.Join(market.ErrProgressWriteUncertain, context.DeadlineExceeded), "No automatic retry was sent"},
	} {
		message := trackingError(errors.Join(tc.err, errors.New("private-test-key-and-payload")))
		if !strings.Contains(message, tc.contains) || strings.Contains(message, "private-test") {
			t.Fatalf("incorrect or unsafe mapped error: %q", message)
		}
	}
}
