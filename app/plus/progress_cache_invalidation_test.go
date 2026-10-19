package plus

import (
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func publishObservedMarketForTest(t *testing.T, a *ItemWindowApp, remote market.TrackedSnapshot) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.trackedSnapshot = cloneTrackedSnapshot(remote)
	a.applyLocalMarketStockLocked(*a.state.Item, remote, "")
}

func newObservedMarketQuotaTestApp(t *testing.T) (*ItemWindowApp, *fakeTrackingAPI) {
	t.Helper()
	a, fake := newTrackingTestApp(t)
	fake.snapshot.Items[0].RequiredQuantity = 8
	fake.snapshot.Items[0].Sources = []market.TrackedSource{
		{Type: "quest", QuestID: "quest-a", Amount: 3, RemainingRequiredQuantity: 3},
		{Type: "quest", QuestID: "quest-b", Amount: 5, RemainingRequiredQuantity: 5},
	}
	publishObservedMarketForTest(t, a, fake.snapshot)
	return a, fake
}

func observedItemAddAllowedForTest(a *ItemWindowApp) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.canAddTrackedItemLocked()
}

func TestObservedMissingOrVariantItemReplacesOnlyCurrentMarketRows(t *testing.T) {
	for _, mode := range []string{"variants", "missing"} {
		t.Run(mode, func(t *testing.T) {
			a, fake := newTrackingTestApp(t)
			fake.snapshot.Items[0].AvailableQuantity = 3
			otherID, otherUID := "other-verified-item", "other-item-uid"
			fake.snapshot.Items = append(fake.snapshot.Items, market.TrackedItem{ItemID: otherID, Name: "Other verified item", RequiredQuantity: 2, RemainingQuantity: 2,
				Sources: []market.TrackedSource{{Type: "quest", QuestID: "quest-b", Amount: 2, RemainingRequiredQuantity: 2}}})
			original := cloneTrackedSnapshot(fake.snapshot)
			remote := cloneTrackedSnapshot(fake.snapshot)
			remote.Revision = "observed-changed-layout"
			if mode == "variants" {
				variant := remote.Items[0]
				remote.Items[0].Name, remote.Items[0].AvailableQuantity = "Item variant full", 0
				variant.Name, variant.AvailableQuantity = "Item variant partial", 1
				remote.Items = append(remote.Items, variant)
			} else {
				remote.Items = remote.Items[1:]
			}
			fake.snapshot = cloneTrackedSnapshot(remote)
			publishObservedMarketForTest(t, a, remote)
			state := a.GetState()
			if observedItemAddAllowedForTest(a) || state.Tracking == nil || state.Tracking.AddFIRAllowed || state.Tracking.AddRegularAllowed {
				t.Fatalf("fresh missing or ambiguous Market row stayed writable: %+v", state.Tracking)
			}
			if mode == "variants" {
				if len(state.Tracking.Variants) != 2 || state.Tracking.Variants[0].AvailableQuantity != 0 || state.Tracking.Variants[1].AvailableQuantity != 1 {
					t.Fatalf("old unique stock survived fresh Market variants: %+v", state.Tracking)
				}
			} else if state.Tracking.Tracked || state.Tracking.MarketAvailableQuantity != 0 || state.Tracking.AvailableQuantity != 0 {
				t.Fatalf("removed Market row retained stale stock: %+v", state.Tracking)
			}
			addMarketTrackedItemForTest(a, trackingItemUID, "fir")
			if fake.loads != 0 || len(fake.writes) != 0 {
				t.Fatal("known missing or ambiguous target touched API stock")
			}
			a.state.Item = &market.Item{UID: otherUID, BSGID: otherID, Name: "Other verified item"}
			publishObservedMarketForTest(t, a, remote)
			state = addMarketTrackedItemForTest(a, otherUID, "regular")
			if state.Tracking.AvailableQuantity != 1 || state.Tracking.Error != "" || fake.loads != 2 || len(fake.writes) != 1 {
				t.Fatalf("unrelated fresh Market requirement did not save directly: %+v", state.Tracking)
			}
			original.Revision = "observed-unique-item-restored"
			fake.snapshot = cloneTrackedSnapshot(original)
			a.state.Item = &market.Item{UID: trackingItemUID, BSGID: trackingBSGID, Name: "Immobilizing splint"}
			publishObservedMarketForTest(t, a, original)
			if state = a.GetState(); state.Tracking.Error != "" || !observedItemAddAllowedForTest(a) || state.Tracking.AvailableQuantity != 3 {
				t.Fatalf("fresh unique Market row did not immediately restore normal functions: %+v", state.Tracking)
			}
		})
	}
}

func TestCachedNewMarketItemCanBeUsedImmediatelyFromFreshSnapshot(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	publishObservedMarketForTest(t, a, fake.snapshot)
	newID, newUID := "new-market-item", "new-market-item-uid"
	remote := cloneTrackedSnapshot(fake.snapshot)
	remote.Revision = "new-market-source"
	remote.Items = []market.TrackedItem{{ItemID: newID, Name: "New Market item", AvailableQuantity: 1, RequiredQuantity: 2, RemainingQuantity: 1,
		Sources: []market.TrackedSource{{Type: "quest", QuestID: "new-quest", Amount: 2, RemainingRequiredQuantity: 2}}}}
	a.state.Item = &market.Item{UID: newUID, BSGID: newID, Name: "New Market item"}
	fake.snapshot = cloneTrackedSnapshot(remote)
	reads := fake.loads
	publishObservedMarketForTest(t, a, remote)
	state := addMarketTrackedItemForTest(a, newUID, "regular")
	if state.Tracking.Error != "" || state.Tracking.MarketAvailableQuantity != 2 || state.Tracking.AvailableQuantity != 2 || state.Tracking.RequiredQuantity != 2 || fake.loads != reads+2 || len(fake.writes) != 1 {
		t.Fatalf("fresh new Market source used stale requirements or waited for network: %+v", state.Tracking)
	}
	if _, found := stockItem(a.localSnapshot, trackingBSGID); found {
		t.Fatal("replaced complete snapshot retained an absent previous item")
	}
}

func TestSessionStockTypesResetWhenMarketQuantityChangesExternally(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	a.recordLocalMarketAdd(a.apiKey, a.state.ItemOptions, *a.state.Item, "fir", 0, 1)
	remote := cloneTrackedSnapshot(fake.snapshot)
	remote.Revision, remote.Items[0].AvailableQuantity = "confirmed-known-add", 1
	publishObservedMarketForTest(t, a, remote)
	row, _ := stockItem(a.localSnapshot, trackingBSGID)
	if row.MarketFIRQuantity != 1 || row.AvailableQuantity != 1 {
		t.Fatalf("session label did not describe the observed Market stock: %+v", row)
	}
	remote.Revision, remote.Items[0].AvailableQuantity = "external-stock-change", 2
	publishObservedMarketForTest(t, a, remote)
	row, _ = stockItem(a.localSnapshot, trackingBSGID)
	if row.MarketFIRQuantity != 0 || row.MarketRegularQuantity != 0 || row.AvailableQuantity != 2 {
		t.Fatalf("external Market change inherited an obsolete session label: %+v", row)
	}
}

func TestCachedSameQuantitySourceChangesImmediatelyRebuildRequirements(t *testing.T) {
	changes := map[string]struct {
		change   func(*market.TrackedSource)
		required int64
	}{
		"fir":       {func(source *market.TrackedSource) { source.FIR = true }, 8},
		"quest":     {func(source *market.TrackedSource) { source.QuestID = "another-quest" }, 8},
		"remaining": {func(source *market.TrackedSource) { source.RemainingRequiredQuantity = 2 }, 7},
		"completed": {func(source *market.TrackedSource) { source.Completed = true }, 5},
		"optional":  {func(source *market.TrackedSource) { source.Optional = true }, 5},
		"amount":    {func(source *market.TrackedSource) { source.Amount, source.RemainingRequiredQuantity = 4, 4 }, 9},
		"kind": {func(source *market.TrackedSource) {
			source.Type, source.QuestID, source.UpgradeKey = "hideout", "", "medical:1"
		}, 8},
	}
	for name, tc := range changes {
		t.Run(name, func(t *testing.T) {
			a, fake := newObservedMarketQuotaTestApp(t)
			publishObservedMarketForTest(t, a, fake.snapshot)
			remote := cloneTrackedSnapshot(fake.snapshot)
			remote.Revision = "changed-source-same-item-quantity"
			tc.change(&remote.Items[0].Sources[0])
			fake.snapshot = cloneTrackedSnapshot(remote)
			reads := fake.loads
			publishObservedMarketForTest(t, a, remote)
			baseline, unique := stockItem(a.localSnapshot, trackingBSGID)
			if !unique || len(baseline.Sources) != len(remote.Items[0].Sources) || !reflect.DeepEqual(baseline.Sources[0].TrackedSource, remote.Items[0].Sources[0]) {
				t.Fatalf("observed normalized Market source was not retained: %+v", baseline)
			}
			state := addMarketTrackedItemForTest(a, trackingItemUID, "regular")
			if state.Tracking.Error != "" || state.Tracking.RequiredQuantity != tc.required || state.Tracking.AvailableQuantity != 1 || fake.loads != reads+2 || len(fake.writes) != 1 {
				t.Fatalf("complete source change retained stale requirements or blocked an eligible add: %+v", state.Tracking)
			}
		})
	}
}

func TestCachedMarketSourceOrderingAndStockRevisionKeepNormalAdd(t *testing.T) {
	a, fake := newObservedMarketQuotaTestApp(t)
	fake.snapshot.Items[0].Sources = []market.TrackedSource{
		{Type: "quest", QuestID: "quest-a", Amount: 1, RemainingRequiredQuantity: 1},
		{Type: "quest", QuestID: "third-quest", Amount: 2, RemainingRequiredQuantity: 2},
	}
	publishObservedMarketForTest(t, a, fake.snapshot)
	remote := cloneTrackedSnapshot(fake.snapshot)
	remote.Revision, remote.Items[0].AvailableQuantity = "stock-only-revision", 1
	remote.Items[0].Sources[0], remote.Items[0].Sources[1] = remote.Items[0].Sources[1], remote.Items[0].Sources[0]
	fake.snapshot = cloneTrackedSnapshot(remote)
	reads := fake.loads
	publishObservedMarketForTest(t, a, remote)
	state := addMarketTrackedItemForTest(a, trackingItemUID, "regular")
	if state.Tracking.Error != "" || state.Tracking.RequiredQuantity != 3 || state.Tracking.AvailableQuantity != 2 || fake.loads != reads+2 || len(fake.writes) != 1 {
		t.Fatalf("reordered requirements blocked normal local Add: %+v", state.Tracking)
	}
}

func TestCachedMarketGroupUpdatesReplaceMembershipCountsAndCompletion(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	publishObservedMarketForTest(t, a, fake.snapshot)
	otherID := "new-group-candidate"
	group := market.TrackedGroup{QuestID: "group-quest", RequirementID: "medical-items", Name: "Any medical item", Amount: 3, RequiredQuantity: 3, RemainingQuantity: 3, FIR: true, ItemIDs: []string{trackingBSGID, otherID}}
	remote := cloneTrackedSnapshot(fake.snapshot)
	remote.Revision = "group-first-observed"
	remote.Groups = []market.TrackedGroup{group}
	remote.Items = []market.TrackedItem{{ItemID: trackingBSGID, Name: "Immobilizing splint", RequiredQuantity: 3, RemainingQuantity: 2, AvailableQuantity: 1,
		Sources: []market.TrackedSource{{Type: "questGroup", QuestID: group.QuestID, RequirementID: group.RequirementID, Amount: 3, RemainingRequiredQuantity: 3, FIR: true}}}}
	a.recordLocalMarketAdd(a.apiKey, a.state.ItemOptions, *a.state.Item, "fir", 0, 1)
	publishObservedMarketForTest(t, a, remote)
	state := a.GetState()
	if len(state.Tracking.AnyGroups) != 1 || !state.Tracking.AnyGroups[0].FIR || state.Tracking.AnyGroups[0].RequiredQuantity != 3 || state.Tracking.AnyGroups[0].AvailableQuantity != 1 || !state.Tracking.AddFIRAllowed || state.Tracking.AddRegularAllowed {
		t.Fatalf("fresh Market Any group was not applied to the cached item: %+v", state.Tracking)
	}
	if candidate, unique := stockItem(a.localSnapshot, otherID); !unique || !candidate.MarketTracked || len(candidate.Sources) != 1 {
		t.Fatalf("new group candidate was missing from the complete Market baseline: %+v", candidate)
	}
	remote.Groups[0].ItemIDs[0] = "caller-mutated-candidate"
	remote.Groups[0].Name = "caller-mutated-group"
	remote.Items[0].Sources[0].Amount = 99
	if a.trackedSnapshot.Groups[0].ItemIDs[0] != trackingBSGID || a.trackedSnapshot.Groups[0].Name != group.Name || a.trackedSnapshot.Items[0].Sources[0].Amount != 3 {
		t.Fatal("cached Market response aliases the caller's groups, membership or sources")
	}
	expanded := cloneTrackedSnapshot(a.trackedSnapshot)
	expanded.Revision = "group-requirement-changed"
	expanded.Groups[0].Amount, expanded.Groups[0].RequiredQuantity, expanded.Groups[0].RemainingQuantity = 5, 5, 5
	publishObservedMarketForTest(t, a, expanded)
	state = a.GetState()
	if len(state.Tracking.AnyGroups) != 1 || state.Tracking.AnyGroups[0].RequiredQuantity != 5 || state.Tracking.AnyGroups[0].AvailableQuantity != 1 {
		t.Fatalf("new group quantity retained stale cached demand: %+v", state.Tracking)
	}
	finished := cloneTrackedSnapshot(a.trackedSnapshot)
	finished.Revision = "group-completed"
	finished.Groups[0].Completed = true
	finished.Groups[0].RequiredQuantity, finished.Groups[0].RemainingQuantity = 0, 0
	publishObservedMarketForTest(t, a, finished)
	state = a.GetState()
	if len(state.Tracking.AnyGroups) != 0 || len(state.Tracking.CompletedSources) != 1 || state.Tracking.CompletedSources[0].Quantity != 5 || state.Tracking.AddRegularAllowed || state.Tracking.AddFIRAllowed {
		t.Fatalf("group completion retained an active cached requirement: %+v", state.Tracking)
	}
	removed := cloneTrackedSnapshot(a.trackedSnapshot)
	removed.Revision = "group-removed"
	removed.Groups = []market.TrackedGroup{}
	removed.Items = []market.TrackedItem{{ItemID: trackingBSGID, Name: "Immobilizing splint", AvailableQuantity: 1, RequiredQuantity: 1,
		Sources: []market.TrackedSource{{Type: "quest", QuestID: "fixed-after-group", Amount: 1, RemainingRequiredQuantity: 1}}}}
	publishObservedMarketForTest(t, a, removed)
	state = a.GetState()
	if len(state.Tracking.AnyGroups) != 0 || len(state.Tracking.CompletedSources) != 0 || state.Tracking.RequiredQuantity != 1 {
		t.Fatalf("removed group leaked active or completed demand into its replacement: %+v", state.Tracking)
	}
	if _, exists := stockItem(a.localSnapshot, otherID); exists {
		t.Fatal("removed Market group retained a synthesized candidate")
	}
}
