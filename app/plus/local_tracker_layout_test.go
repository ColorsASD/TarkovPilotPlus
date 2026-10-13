package plus

import (
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/tarkovdev"
)

const layoutAugmentin = "layout-augmentin"
const layoutSalewa = "layout-salewa"
const layoutIFAK = "layout-ifak"

func refreshLayoutItem(t *testing.T, a *ItemWindowApp, id, name string) ItemState {
	t.Helper()
	a.state.Item = &market.Item{UID: id + "-uid", BSGID: id, Name: name}
	a.localAt = time.Time{}
	a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
	state := a.GetState()
	if state.Local == nil || state.Local.Error != "" || state.Tracking == nil {
		t.Fatalf("layout fixture failed: %+v", state)
	}
	return state
}

// A böngészős ellenőrzés ugyanazokat a backend állapotokat használja, mint
// a számítási teszt; a frontendben nincs külön utánzott allokáció.
func medicalLayoutStates(t *testing.T) map[string]ItemState {
	t.Helper()
	a, fake := newLocalTestApp(t)
	fake.quests.Quests = append(fake.quests.Quests, market.Quest{QuestID: "quest-c", Status: "available"})
	fake.snapshot.Items = []market.TrackedItem{
		{ItemID: layoutAugmentin, Name: "Augmentin antibiotic pills", RequiredQuantity: 1, RemainingQuantity: 1,
			Sources: []market.TrackedSource{{Type: "quest", QuestID: "quest-a", Amount: 1, RemainingRequiredQuantity: 1, FIR: true}}},
		{ItemID: layoutSalewa, Name: "Salewa first aid kit", RequiredQuantity: 1, AvailableQuantity: 1,
			Sources: []market.TrackedSource{{Type: "quest", QuestID: "quest-a", Amount: 1, RemainingRequiredQuantity: 1}}},
	}
	a.devRequirements = &fakeRequirements{catalog: tarkovdev.Catalog{Tasks: []tarkovdev.Task{
		{ID: "quest-a", Objectives: []tarkovdev.Objective{
			{ID: "fixed-augmentin", Type: "giveItem", Count: 1, FoundInRaid: true, ItemIDs: []string{layoutAugmentin}},
			{ID: "fixed-salewa", Type: "giveItem", Count: 1, ItemIDs: []string{layoutSalewa}},
		}},
		{ID: "quest-b", Objectives: []tarkovdev.Objective{{ID: "regular-any", Type: "giveItem", Count: 1, ItemIDs: []string{layoutAugmentin, layoutSalewa}}}},
		{ID: "quest-c", Objectives: []tarkovdev.Objective{{ID: "fir-any", Type: "giveItem", Count: 1, FoundInRaid: true, ItemIDs: []string{layoutAugmentin, layoutSalewa}}}},
	}, Hideout: []tarkovdev.Station{}}}
	if _, err := a.localStore.RecordMarketAdd("character-a", "PVP", layoutSalewa, "fir", 0, 1); err != nil {
		t.Fatal(err)
	}
	states := map[string]ItemState{
		"augmentin-open": refreshLayoutItem(t, a, layoutAugmentin, "Augmentin antibiotic pills"),
		"salewa-open":    refreshLayoutItem(t, a, layoutSalewa, "Salewa first aid kit"),
	}
	fake.quests.Quests[1].Completed = true
	fake.quests.Quests[1].Status = "completed"
	states["augmentin-completed"] = refreshLayoutItem(t, a, layoutAugmentin, "Augmentin antibiotic pills")
	states["salewa-completed"] = refreshLayoutItem(t, a, layoutSalewa, "Salewa first aid kit")
	if len(fake.writes) != 0 {
		t.Fatal("layout fixture unexpectedly wrote Market stock")
	}
	return states
}

func TestLocalTrackerMedicalFixedAndAnyLayout(t *testing.T) {
	states := medicalLayoutStates(t)
	for _, name := range []string{"augmentin", "salewa"} {
		for _, completed := range []bool{false, true} {
			suffix, wantTarget, wantGroups := "open", int64(3), 2
			if completed {
				suffix, wantTarget, wantGroups = "completed", 2, 1
			}
			state := states[name+"-"+suffix]
			tracking := state.Tracking
			wantStock, kind := int64(0), "fir"
			if name == "salewa" {
				wantStock, kind = 1, "regular"
			}
			if tracking.AvailableQuantity != wantStock || tracking.RequiredQuantity != wantTarget || len(tracking.Requirements) != 1 {
				t.Fatalf("%s-%s wrong stock or fixed requirement: %+v", name, suffix, tracking)
			}
			if available, required := requirementStock(t, state, kind); available != wantStock || required != 1 {
				t.Fatalf("%s-%s Any was duplicated in the fixed %s row: %+v", name, suffix, kind, tracking)
			}
			if len(tracking.AnyGroups) != wantGroups {
				t.Fatalf("%s-%s wrong Any group count: %+v", name, suffix, tracking)
			}
			for _, group := range tracking.AnyGroups {
				if group.AvailableQuantity != 0 || group.RequiredQuantity != 1 || completed && !group.FIR {
					t.Fatalf("%s-%s fixed Salewa was reused for Any or completed Any remained: %+v", name, suffix, tracking)
				}
			}
			if completed && (len(tracking.CompletedSources) != 1 || tracking.CompletedSources[0].Type != "quest" || tracking.CompletedSources[0].Quantity != 1) {
				t.Fatalf("%s missing completed Any group item count: %+v", name, tracking)
			}
			if !completed && len(tracking.CompletedSources) != 0 {
				t.Fatalf("open Any was marked completed: %+v", tracking)
			}
		}
	}
}

func groupedLayoutStates(t *testing.T, differentCandidates bool) map[string]ItemState {
	t.Helper()
	a, fake := newDevOnlyTestApp(t)
	fake.quests.Quests = append(fake.quests.Quests, market.Quest{QuestID: "quest-c", Status: "available"})
	secondCandidates := []string{layoutSalewa, layoutAugmentin}
	if differentCandidates {
		secondCandidates = []string{layoutIFAK, layoutAugmentin}
	}
	a.devRequirements = &fakeRequirements{catalog: tarkovdev.Catalog{Tasks: []tarkovdev.Task{
		{ID: "quest-b", Objectives: []tarkovdev.Objective{{ID: "any-b", Type: "giveItem", Count: 3, ItemIDs: []string{layoutAugmentin, layoutSalewa}}}},
		{ID: "quest-c", Objectives: []tarkovdev.Objective{{ID: "any-c", Type: "giveItem", Count: 3, ItemIDs: secondCandidates}}},
	}, Hideout: []tarkovdev.Station{}}}
	states := map[string]ItemState{"zero": refreshLayoutItem(t, a, layoutAugmentin, "Augmentin antibiotic pills")}
	if !differentCandidates {
		for i := 0; i < 4; i++ {
			states["partial"] = a.AddTrackedItem(layoutAugmentin + "-uid")
		}
	}
	return states
}

func TestLocalTrackerAggregatesOnlyIdenticalAnyEligibility(t *testing.T) {
	states := groupedLayoutStates(t, false)
	for key, wantAvailable := range map[string]int64{"zero": 0, "partial": 4} {
		tracking := states[key].Tracking
		if len(tracking.Requirements) != 0 || len(tracking.AnyGroups) != 1 || tracking.AnyGroups[0].AvailableQuantity != wantAvailable || tracking.AnyGroups[0].RequiredQuantity != 6 {
			t.Fatalf("%s equivalent Any groups were not displayed together: %+v", key, tracking)
		}
		if len(tracking.CompletedSources) != 0 {
			t.Fatal("covering an Any group completed a quest")
		}
	}
	different := groupedLayoutStates(t, true)["zero"].Tracking
	if len(different.AnyGroups) != 2 {
		t.Fatalf("different candidate sets were merged: %+v", different)
	}
	for _, group := range different.AnyGroups {
		if group.AvailableQuantity != 0 || group.RequiredQuantity != 3 {
			t.Fatalf("different candidate set ratio changed: %+v", different)
		}
	}
}
