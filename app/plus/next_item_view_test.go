package plus

import (
	"reflect"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/nextitem"
)

func TestNextItemRefreshHandlesNewQuestWithoutStockWrites(t *testing.T) {
	a, fake := newLocalTestApp(t)
	item := *a.state.Item
	fake.snapshot.Items = []market.TrackedItem{{ItemID: item.BSGID, Name: item.Name, RequiredQuantity: 2, RemainingQuantity: 2,
		Sources: []market.TrackedSource{{Type: "quest", QuestID: "old-quest", Name: "Old quest", Amount: 2, RemainingRequiredQuantity: 2}}}}
	fake.quests.Quests = []market.Quest{{QuestID: "old-quest", Name: "Old quest", Status: "available"}}
	a.localAt = time.Time{}
	if err := a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, item); err != nil {
		t.Fatal(err)
	}
	before := a.GetState().Tracking
	if before.NextItem == nil || before.NextItem.Category != "Regular" {
		t.Fatalf("missing initial forecast: %+v", before.NextItem)
	}
	// A következő API-válaszban új, előre nem ismert ID és követelmény jelenik meg.
	fake.quests.Quests = append(fake.quests.Quests, market.Quest{QuestID: "new-quest-dynamic-739", Name: "Newly added quest", Status: "available"})
	fake.snapshot.Items[0].Sources = append(fake.snapshot.Items[0].Sources,
		market.TrackedSource{Type: "quest", QuestID: "new-quest-dynamic-739", Name: "Newly added quest", Amount: 1, RemainingRequiredQuantity: 1, FIR: true})
	fake.snapshot.Items[0].RequiredQuantity, fake.snapshot.Items[0].RemainingQuantity = 3, 3
	fake.snapshot.Revision = "revision-next-quest"
	a.localAt = time.Time{}
	if err := a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, item); err != nil {
		t.Fatal(err)
	}
	after := a.GetState().Tracking
	if after.NextItem == nil || after.NextItem.Category != "Found in Raid" || after.NextItem.GoalName != "Newly added quest" {
		t.Fatalf("new API quest was not included: %+v", after.NextItem)
	}
	if len(fake.writes) != 0 || before.AvailableQuantity != 0 || after.AvailableQuantity != 0 {
		t.Fatalf("forecast modified stock: writes=%+v before=%d after=%d", fake.writes, before.AvailableQuantity, after.AvailableQuantity)
	}
}

func TestNextItemRefreshHandlesNewHideoutWithoutStockWrites(t *testing.T) {
	a, fake := newLocalTestApp(t)
	item := *a.state.Item
	fake.snapshot.Items = []market.TrackedItem{{ItemID: item.BSGID, Name: item.Name, RequiredQuantity: 2, RemainingQuantity: 2,
		Sources: []market.TrackedSource{{Type: "quest", QuestID: "existing-quest", Name: "Existing quest", Amount: 2, RemainingRequiredQuantity: 2, FIR: true}}}}
	fake.quests.Quests = []market.Quest{{QuestID: "existing-quest", Name: "Existing quest", Status: "available"}}
	a.localAt = time.Time{}
	if err := a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, item); err != nil {
		t.Fatal(err)
	}
	if forecast := a.GetState().Tracking.NextItem; forecast == nil || forecast.Category != "Found in Raid" {
		t.Fatalf("initial forecast: %+v", forecast)
	}
	fake.hideout.Modules = []market.HideoutUpgrade{{UpgradeKey: "Brand new station_9", Name: "Brand new station", Level: 9, CanComplete: true}}
	fake.snapshot.Items[0].Sources = append(fake.snapshot.Items[0].Sources,
		market.TrackedSource{Type: "hideout", UpgradeKey: "Brand new station_9", Name: "Brand new station", Amount: 1, RemainingRequiredQuantity: 1})
	fake.snapshot.Items[0].RequiredQuantity, fake.snapshot.Items[0].RemainingQuantity = 3, 3
	fake.snapshot.Revision = "revision-next-hideout"
	a.localAt = time.Time{}
	if err := a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, item); err != nil {
		t.Fatal(err)
	}
	after := a.GetState().Tracking
	if after.NextItem == nil || after.NextItem.Category != "Regular" {
		t.Fatalf("new API hideout was not included: %+v", after.NextItem)
	}
	if len(fake.writes) != 0 || after.AvailableQuantity != 0 {
		t.Fatal("refresh wrote stock")
	}
}

func TestNextItemStateCopyOwnsForecastData(t *testing.T) {
	source := &ItemTrackingState{NextItem: &nextitem.Forecast{Category: "Regular", FixedStock: &localprogress.RequirementStock{Regular: 1, Known: true}, PossibleCategories: []string{"Regular"}, Changes: []nextitem.Change{{GoalName: "Original", Delta: 1}}, Targets: []nextitem.Target{{GoalName: "Original target", Kind: "quest", AvailableQuantity: 1, RequiredQuantity: 4}}}}
	copy := cloneTrackingState(source)
	copy.NextItem.FixedStock.Regular = 3
	copy.NextItem.PossibleCategories[0] = "Changed"
	copy.NextItem.Changes[0].GoalName = "Changed"
	copy.NextItem.Targets[0].GoalName = "Changed"
	copy.NextItem.Targets[0].AvailableQuantity = 3
	if source.NextItem.PossibleCategories[0] != "Regular" || source.NextItem.Changes[0].GoalName != "Original" ||
		source.NextItem.Targets[0].GoalName != "Original target" || source.NextItem.Targets[0].AvailableQuantity != 1 || source.NextItem.FixedStock.Regular != 1 {
		t.Fatal("view copy aliases forecast")
	}
}

func TestNextItemFallbackDoesNotInventCompleteMetadata(t *testing.T) {
	tracked := market.TrackedSnapshot{CharUID: "char-test", GameMode: "PVP", Revision: "rev-test", Items: []market.TrackedItem{}}
	snapshot := marketProgressOnly(tracked)
	if snapshot.MetadataComplete {
		t.Fatal("tracked-only fallback claimed full goal metadata")
	}
	before := localprogress.Clone(snapshot)
	_ = nextitem.Calculate(snapshot, "item-test")
	if !reflect.DeepEqual(before, snapshot) {
		t.Fatal("forecast changed fallback snapshot")
	}
}

func TestNextItemCompletedGoalsRemainVisibleWithoutEnablingAdd(t *testing.T) {
	a, fake := newLocalTestApp(t)
	item := *a.state.Item
	fake.snapshot.Items = []market.TrackedItem{{ItemID: item.BSGID, Name: item.Name, SpentQuantity: 5,
		Sources: []market.TrackedSource{
			{Type: "quest", QuestID: "completed-quest", Name: "Completed quest", Amount: 3, Completed: true},
			{Type: "hideout", UpgradeKey: "Completed station_2", Name: "Completed station", Amount: 2, Completed: true},
		}}}
	fake.quests.Quests = []market.Quest{{QuestID: "completed-quest", Name: "Completed quest", Completed: true, Status: "completed"}}
	fake.hideout.Modules = []market.HideoutUpgrade{{UpgradeKey: "Completed station_2", Name: "Completed station", Level: 2, Completed: true}}
	fake.snapshot.Revision = "revision-completed-goals"
	a.localAt = time.Time{}
	if err := a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, item); err != nil {
		t.Fatal(err)
	}
	view := a.GetState().Tracking
	if view.AddAllowed || view.NextItem == nil || len(view.NextItem.Targets) != 2 {
		t.Fatalf("completed details missing or Add enabled: %+v", view)
	}
	for _, target := range view.NextItem.Targets {
		if !target.Completed || target.AvailableQuantity != target.RequiredQuantity || target.RequiredQuantity == 0 {
			t.Fatalf("completed goal did not retain its full ratio: %+v", target)
		}
	}
	if len(fake.writes) != 0 || view.AvailableQuantity != 0 {
		t.Fatal("completed detail rendering modified stock")
	}
}
