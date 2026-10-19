package plus

import (
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

const marketAnyYMXC = "ymxc-water"
const marketAnyWater = "water-06l"

func marketAnyProgressFixtureForTest() market.TrackedSnapshot {
	return market.TrackedSnapshot{CharUID: "fixture-character", GameMode: "PVP", Revision: "confirmed-progress", Items: []market.TrackedItem{
		{ItemID: marketAnyYMXC, Name: "Bottle of YMXC water", RequiredQuantity: 1, AvailableQuantity: 1,
			Sources: []market.TrackedSource{{Type: "quest", QuestID: "collector", Name: "Collector", Amount: 1, RemainingRequiredQuantity: 1, FIR: true}}},
		{ItemID: marketAnyWater, Name: "Bottle of water (0.6L)", RequiredQuantity: 2, RemainingQuantity: 2,
			Sources: []market.TrackedSource{{Type: "quest", QuestID: "thrifty", Name: "The Survivalist Path - Thrifty", Amount: 2, RemainingRequiredQuantity: 2}}},
		{ItemID: "emergency-water-ration", Name: "Emergency Water Ration"},
		{ItemID: "water-candidate-four"},
		{ItemID: "water-candidate-five"},
	}, Groups: []market.TrackedGroup{{QuestID: "aquarius", QuestName: "Operation Aquarius", RequirementID: "drinking-water", Name: "Drinking water", Amount: 3,
		RequiredQuantity: 3, AvailableQuantity: 1, RemainingQuantity: 2, FIR: true,
		ItemIDs: []string{marketAnyYMXC, marketAnyWater, "emergency-water-ration", "water-candidate-four", "water-candidate-five"}}}}
}

func marketAnyProgressSnapshotForTest(t *testing.T, raw market.TrackedSnapshot, quests ...market.Quest) localprogress.Snapshot {
	t.Helper()
	merged, err := localprogress.Merge(raw,
		market.QuestSnapshot{CharUID: raw.CharUID, GameMode: raw.GameMode, Revision: raw.Revision, Quests: append([]market.Quest{}, quests...)},
		market.HideoutSnapshot{CharUID: raw.CharUID, GameMode: raw.GameMode, Revision: raw.Revision, Modules: []market.HideoutUpgrade{}})
	if err != nil {
		t.Fatal(err)
	}
	allocated, err := localprogress.Allocate(merged)
	if err != nil {
		t.Fatal(err)
	}
	return allocated
}

func marketAnyProgressViewForTest(id string, snapshot localprogress.Snapshot) *ItemTrackingState {
	return localTrackedView(market.Item{UID: id + "-uid", BSGID: id}, snapshot)
}

func TestRemoveAllowedUsesProjectedSharedStockWithoutAnActiveRequirement(t *testing.T) {
	raw := marketAnyProgressFixtureForTest()
	raw.Groups = nil
	raw.Items = []market.TrackedItem{{ItemID: marketAnyWater, Name: "Water", AvailableQuantity: 1,
		Sources: []market.TrackedSource{{Type: "quest", QuestID: "finished", Amount: 2, Completed: true}}}}
	snapshot := marketAnyProgressSnapshotForTest(t, raw)
	view := marketAnyProgressViewForTest(marketAnyWater, snapshot)
	if view.RequiredQuantity != 0 || view.AddAllowed || !view.RemoveAllowed {
		t.Fatalf("removing existing stock incorrectly required another collection goal: %+v", view)
	}
	for _, stock := range []int64{0, 1, 2} {
		projected := localprogress.Clone(snapshot)
		projected.Items[0].AvailableQuantity = stock
		current := marketAnyProgressViewForTest(marketAnyWater, projected)
		if current.RemoveAllowed != (stock > 0) || current.AvailableQuantity != stock || current.MarketAvailableQuantity != 1 {
			t.Fatalf("signed pending stock did not control removal at %d: %+v", stock, current)
		}
	}
	// Egy még el nem mentett Add is visszavehető; a megerősített számláló 0.
	snapshot.Items[0].MarketAvailableQuantity, snapshot.Items[0].AvailableQuantity = 0, 1
	if current := marketAnyProgressViewForTest(marketAnyWater, snapshot); !current.RemoveAllowed || current.MarketAvailableQuantity != 0 {
		t.Fatalf("a positive pending addition could not be removed: %+v", current)
	}
}

func TestRemoveAllowedRejectsSeparateAmbiguousAndUnverifiedStock(t *testing.T) {
	snapshot := marketAnyProgressSnapshotForTest(t, marketAnyProgressFixtureForTest())
	separate := localprogress.Clone(snapshot)
	separate.FIRInventorySeparate = true
	if view := marketAnyProgressViewForTest(marketAnyYMXC, separate); view.RemoveAllowed {
		t.Fatal("separate inventory enabled an ambiguous stock removal")
	}
	ambiguous := localprogress.Clone(snapshot)
	ambiguous.Items = append(ambiguous.Items, ambiguous.Items[0])
	if view := marketAnyProgressViewForTest(marketAnyYMXC, ambiguous); view.RemoveAllowed || len(view.Variants) != 2 {
		t.Fatalf("duplicate item variants enabled a stock removal: %+v", view)
	}
	for _, view := range []*ItemTrackingState{{Loading: true}, {Adding: true}, {Error: "stock unavailable"}, {InventorySeparate: true}} {
		simplifyLocalTrackingView(view, snapshot.Items[0], snapshot)
		if view.RemoveAllowed {
			t.Fatalf("an unverified view enabled removal: %+v", view)
		}
	}
}

func TestRemoveGateDoesNotDependOnAddEligibilityAndUsesProjectedQuantity(t *testing.T) {
	a, _ := newTrackingTestApp(t)
	a.localSnapshot.Items[0].RequiredQuantity = 0
	a.localSnapshot.Items[0].Sources = nil
	a.state.Tracking.AddAllowed = false
	for _, stock := range []int64{0, 1, 2} {
		a.localSnapshot.Items[0].AvailableQuantity = stock
		a.localSnapshot.Items[0].MarketAvailableQuantity = 1
		a.mu.Lock()
		allowed := a.allowTrackedItemRemovalLocked(trackingBSGID)
		a.mu.Unlock()
		if allowed != (stock > 0) || a.state.Tracking.RemoveAllowed != allowed {
			t.Fatalf("remove gate did not use projected stock %d: %+v", stock, a.state.Tracking)
		}
	}
	a.localSnapshot.Items = append(a.localSnapshot.Items, a.localSnapshot.Items[0])
	a.mu.Lock()
	allowed := a.allowTrackedItemRemovalLocked(trackingBSGID)
	a.mu.Unlock()
	if allowed || a.state.Tracking.RemoveAllowed {
		t.Fatal("remove gate guessed a duplicate BSG stock variant")
	}
}

func assertMarketAnyProgressForTest(t *testing.T, view *ItemTrackingState, available, required int64) ItemTrackingAnyGroup {
	t.Helper()
	if len(view.AnyGroups) != 1 || view.AnyGroups[0].AvailableQuantity != available || view.AnyGroups[0].RequiredQuantity != required {
		t.Fatalf("wrong Market Any progress: %+v", view)
	}
	return view.AnyGroups[0]
}

func TestAnyMarketProgressAquariusKeepsCollectorAndThriftyAllocation(t *testing.T) {
	raw := marketAnyProgressFixtureForTest()
	rawBefore := cloneTrackedSnapshot(raw)
	snapshot := marketAnyProgressSnapshotForTest(t, raw)
	before := localprogress.Clone(snapshot)
	for _, target := range []struct {
		id, kind                                   string
		stock, required, fixedStock, fixedRequired int64
	}{
		{marketAnyYMXC, "fir", 1, 4, 1, 1},
		{marketAnyWater, "regular", 0, 4, 0, 2},
	} {
		view := marketAnyProgressViewForTest(target.id, snapshot)
		if view.AvailableQuantity != target.stock || view.RequiredQuantity != target.required || len(view.Requirements) != 1 ||
			view.Requirements[0].Kind != target.kind || view.Requirements[0].AvailableQuantity != target.fixedStock || view.Requirements[0].RequiredQuantity != target.fixedRequired || !view.AddAllowed {
			t.Fatalf("Market progress changed fixed stock, target or Add eligibility for %s: %+v", target.id, view)
		}
		group := assertMarketAnyProgressForTest(t, view, 1, 3)
		if len(group.Sources) != 1 || group.Sources[0].QuestName != "Operation Aquarius" || group.Sources[0].Category != "Drinking water" ||
			group.Sources[0].AvailableQuantity != 1 || group.Sources[0].RequiredQuantity != 3 {
			t.Fatalf("tooltip used collection allocation instead of API progress: %+v", group)
		}
	}
	for _, row := range snapshot.Items {
		for _, source := range row.Sources {
			if source.GroupID != "" && (source.GroupRemainingQuantity != 3 || source.AllocatedQuantity != 0) {
				t.Fatalf("API progress changed the collection allocation: %+v", source)
			}
		}
	}
	if !reflect.DeepEqual(raw, rawBefore) || !reflect.DeepEqual(snapshot, before) {
		t.Fatal("presenting API progress mutated stock, requirements or source allocation")
	}
}

func TestAnyMarketProgressAggregatesEachExplicitGroupOnce(t *testing.T) {
	raw := marketAnyProgressFixtureForTest()
	second := raw.Groups[0]
	second.QuestID, second.QuestName, second.RequirementID, second.Name = "another-quest", "Another quest", "water", "Other water category"
	second.Amount, second.RequiredQuantity, second.AvailableQuantity, second.RemainingQuantity = 5, 5, 2, 3
	raw.Groups = append(raw.Groups, second)
	snapshot := marketAnyProgressSnapshotForTest(t, raw)
	for _, target := range []struct {
		id       string
		required int64
	}{{marketAnyYMXC, 9}, {marketAnyWater, 9}} {
		view := marketAnyProgressViewForTest(target.id, snapshot)
		if view.RequiredQuantity != target.required || !view.AddAllowed {
			t.Fatalf("summarizing API groups changed collection target: %+v", view)
		}
		group := assertMarketAnyProgressForTest(t, view, 3, 8)
		if len(group.Sources) != 2 {
			t.Fatalf("source progress was repeated per candidate or lost: %+v", group)
		}
		progress := map[string][2]int64{}
		for _, source := range group.Sources {
			progress[source.QuestName] = [2]int64{source.AvailableQuantity, source.RequiredQuantity}
		}
		if progress["Operation Aquarius"] != [2]int64{1, 3} || progress["Another quest"] != [2]int64{2, 5} {
			t.Fatalf("group tooltip lost individual API counters: %+v", progress)
		}
	}
}

func TestAnyMarketProgressPartialAndCompletedKeepOriginalQuantities(t *testing.T) {
	raw := marketAnyProgressFixtureForTest()
	raw.Groups[0].Amount, raw.Groups[0].RequiredQuantity, raw.Groups[0].AvailableQuantity, raw.Groups[0].RemainingQuantity = 5, 2, 1, 1
	partial := marketAnyProgressSnapshotForTest(t, raw)
	view := marketAnyProgressViewForTest(marketAnyYMXC, partial)
	assertMarketAnyProgressForTest(t, view, 1, 2)
	if view.RequiredQuantity != 3 || !view.AddAllowed || len(view.CompletedSources) != 0 {
		t.Fatalf("partial group restored its original five-piece requirement: %+v", view)
	}
	for _, completion := range []string{"group", "quest"} {
		t.Run(completion, func(t *testing.T) {
			completedRaw := cloneTrackedSnapshot(raw)
			quests := []market.Quest{}
			if completion == "group" {
				completedRaw.Groups[0].Completed = true
				completedRaw.Groups[0].RequiredQuantity, completedRaw.Groups[0].AvailableQuantity, completedRaw.Groups[0].RemainingQuantity = 0, 0, 0
			} else {
				quests = append(quests, market.Quest{QuestID: "aquarius", Completed: true, Status: "completed"})
			}
			before := cloneTrackedSnapshot(completedRaw)
			completed := marketAnyProgressSnapshotForTest(t, completedRaw, quests...)
			view := marketAnyProgressViewForTest(marketAnyYMXC, completed)
			if view.AvailableQuantity != 1 || view.RequiredQuantity != 1 || view.AddAllowed || len(view.AnyGroups) != 0 ||
				len(view.CompletedSources) != 1 || view.CompletedSources[0].Quantity != 5 {
				t.Fatalf("completed API group changed stock or retained an active Any requirement: %+v", view)
			}
			if !reflect.DeepEqual(completedRaw, before) {
				t.Fatal("completion presentation mutated raw API state")
			}
		})
	}
}

func TestAnyMarketProgressPendingStockRetainsConfirmedAPICounter(t *testing.T) {
	raw := marketAnyProgressFixtureForTest()
	item := market.Item{UID: "ymxc-uid", BSGID: marketAnyYMXC}
	opts := ItemOptions{CharacterUID: raw.CharUID, Mode: "pvp", revision: 1}
	a := &ItemWindowApp{apiKey: "fixture-key", state: ItemState{ItemOptions: opts, Item: &item}}
	a.applyLocalMarketStockLocked(item, raw, "")
	confirmed := localprogress.Clone(a.confirmedProgress)
	a.pendingAddQueue = []pendingMarketAdd{{ID: 1, Key: a.apiKey, Opts: opts, Item: item, Stage: "queued"}, {ID: 2, Key: a.apiKey, Opts: opts, Item: item, Stage: "queued"}}
	a.rebuildPendingViewLocked()
	view := a.state.Tracking
	assertMarketAnyProgressForTest(t, view, 1, 3)
	if view.AvailableQuantity != 3 || view.MarketAvailableQuantity != 1 || view.RequiredQuantity != 4 || !view.AddAllowed ||
		len(view.Requirements) != 1 || view.Requirements[0].AvailableQuantity != 1 || view.Requirements[0].RequiredQuantity != 1 {
		t.Fatalf("pending additions did not keep immediate stock and confirmed API progress separate: %+v", view)
	}
	if !reflect.DeepEqual(confirmed, a.confirmedProgress) {
		t.Fatal("pending projection modified confirmed API progress")
	}
}

func TestAnyMarketProgressLegacySourceFallsBackToCollectionAllocation(t *testing.T) {
	snapshot := marketAnyProgressSnapshotForTest(t, marketAnyProgressFixtureForTest())
	for itemIndex := range snapshot.Items {
		for sourceIndex := range snapshot.Items[itemIndex].Sources {
			source := &snapshot.Items[itemIndex].Sources[sourceIndex]
			source.MarketGroupProgressKnown, source.MarketGroupAvailableQuantity = false, 0
		}
	}
	view := marketAnyProgressViewForTest(marketAnyYMXC, snapshot)
	assertMarketAnyProgressForTest(t, view, 0, 3)
	if view.AvailableQuantity != 1 || view.RequiredQuantity != 4 || !view.AddAllowed {
		t.Fatalf("legacy fallback changed collection stock or eligibility: %+v", view)
	}
}

func TestAnyMarketProgressExplicitZeroDoesNotUsePlannerProgress(t *testing.T) {
	raw := marketAnyProgressFixtureForTest()
	raw.Items[0].AvailableQuantity = 2
	raw.Groups[0].AvailableQuantity, raw.Groups[0].RemainingQuantity = 0, 3
	snapshot := marketAnyProgressSnapshotForTest(t, raw)
	view := marketAnyProgressViewForTest(marketAnyYMXC, snapshot)
	group := assertMarketAnyProgressForTest(t, view, 0, 3)
	if len(group.Sources) != 1 || group.Sources[0].AvailableQuantity != 0 || view.AvailableQuantity != 2 || view.RequiredQuantity != 4 || !view.AddAllowed {
		t.Fatalf("explicit API zero was replaced by inferred planner progress: %+v", view)
	}
}

func TestAnyMarketProgressCapsSurplusOnlyInPresentation(t *testing.T) {
	raw := marketAnyProgressFixtureForTest()
	raw.Groups[0].AvailableQuantity, raw.Groups[0].RemainingQuantity = 9, 0
	snapshot := marketAnyProgressSnapshotForTest(t, raw)
	view := marketAnyProgressViewForTest(marketAnyYMXC, snapshot)
	group := assertMarketAnyProgressForTest(t, view, 3, 3)
	if len(group.Sources) != 1 || group.Sources[0].AvailableQuantity != 3 || view.RequiredQuantity != 4 || !view.AddAllowed {
		t.Fatalf("API surplus changed collection eligibility or visible progress: %+v", view)
	}
	for _, row := range snapshot.Items {
		for _, source := range row.Sources {
			if source.GroupID != "" && source.MarketGroupAvailableQuantity != 9 {
				t.Fatal("capping visible progress changed the raw API counter")
			}
		}
	}
}

func TestMarketFixedSourceRowsUseSharedConfirmedStockIndependently(t *testing.T) {
	raw := market.TrackedSnapshot{CharUID: "fixture-character", GameMode: "PVP", Revision: "confirmed-fixed-progress", Items: []market.TrackedItem{{
		ItemID: "mixed-fixed-item", Name: "Shared stock item", RequiredQuantity: 5, AvailableQuantity: 2, RemainingQuantity: 3,
		Sources: []market.TrackedSource{
			{Type: "quest", QuestID: "fir-quest", Amount: 2, RemainingRequiredQuantity: 2, FIR: true},
			{Type: "quest", QuestID: "regular-quest", Amount: 3, RemainingRequiredQuantity: 3},
		},
	}}}
	rawBefore := cloneTrackedSnapshot(raw)
	snapshot := marketAnyProgressSnapshotForTest(t, raw)
	before := localprogress.Clone(snapshot)
	view := marketAnyProgressViewForTest("mixed-fixed-item", snapshot)
	expected := []ItemTrackingRequirement{{Kind: "fir", AvailableQuantity: 2, RequiredQuantity: 2}, {Kind: "regular", AvailableQuantity: 2, RequiredQuantity: 3}}
	if view.AvailableQuantity != 2 || view.RequiredQuantity != 5 || !view.AddAllowed || !reflect.DeepEqual(view.Requirements, expected) {
		t.Fatalf("fixed source rows did not use the confirmed shared stock independently: %+v", view)
	}
	item := market.Item{UID: "mixed-fixed-item-uid", BSGID: "mixed-fixed-item"}
	opts := ItemOptions{CharacterUID: raw.CharUID, Mode: "pvp", revision: 1}
	a := &ItemWindowApp{apiKey: "fixture-key", state: ItemState{ItemOptions: opts, Item: &item}}
	a.applyLocalMarketStockLocked(item, raw, "")
	confirmed := localprogress.Clone(a.confirmedProgress)
	a.pendingAddQueue = []pendingMarketAdd{{ID: 1, Key: a.apiKey, Opts: opts, Item: item, Stage: "queued"}}
	a.rebuildPendingViewLocked()
	view = a.state.Tracking
	if view.AvailableQuantity != 3 || view.MarketAvailableQuantity != 2 || view.RequiredQuantity != 5 || !view.AddAllowed || !reflect.DeepEqual(view.Requirements, expected) {
		t.Fatalf("pending Add changed confirmed requirement-row progress: %+v", view)
	}
	if !reflect.DeepEqual(raw, rawBefore) || !reflect.DeepEqual(snapshot, before) || !reflect.DeepEqual(confirmed, a.confirmedProgress) {
		t.Fatal("fixed source presentation or pending projection mutated confirmed input")
	}
}
