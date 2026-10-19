package plus

import (
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

const layoutAugmentin = "layout-augmentin"
const layoutSalewa = "layout-salewa"
const layoutIFAK = "layout-ifak"

func marketQuestGroupForTest(quest, requirement string, quantity int64, fir bool, ids ...string) market.TrackedGroup {
	return market.TrackedGroup{QuestID: quest, QuestName: quest, RequirementID: requirement, Name: "Any accepted item",
		Amount: quantity, RequiredQuantity: quantity, RemainingQuantity: quantity, FIR: fir, ItemIDs: ids}
}

func setMarketGroupRowsForTest(fake *fakeCharacterProgress, fixed []market.TrackedItem, groups ...market.TrackedGroup) {
	fake.snapshot.Items = append([]market.TrackedItem{}, fixed...)
	fake.snapshot.Groups = groups
	for _, group := range groups {
		for _, id := range group.ItemIDs {
			index := -1
			for i := range fake.snapshot.Items {
				if fake.snapshot.Items[i].ItemID == id {
					index = i
					break
				}
			}
			if index < 0 {
				fake.snapshot.Items = append(fake.snapshot.Items, market.TrackedItem{ItemID: id, Name: id})
				index = len(fake.snapshot.Items) - 1
			}
			row := &fake.snapshot.Items[index]
			row.RequiredQuantity += group.RequiredQuantity
			row.RemainingQuantity = max(0, row.RequiredQuantity-row.AvailableQuantity)
			row.Sources = append(row.Sources, market.TrackedSource{Type: "questGroup", QuestID: group.QuestID,
				RequirementID: group.RequirementID, Amount: group.Amount, RemainingRequiredQuantity: group.RequiredQuantity, FIR: group.FIR})
		}
	}
}

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
	setMarketGroupRowsForTest(fake, fake.snapshot.Items,
		marketQuestGroupForTest("quest-b", "regular-any", 1, false, layoutAugmentin, layoutSalewa),
		marketQuestGroupForTest("quest-c", "fir-any", 1, true, layoutAugmentin, layoutSalewa))

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
	a, fake := newLocalTestApp(t)
	fake.quests.Quests = append(fake.quests.Quests, market.Quest{QuestID: "quest-c", Status: "available"})
	secondCandidates := []string{layoutSalewa, layoutAugmentin}
	if differentCandidates {
		secondCandidates = []string{layoutIFAK, layoutAugmentin}
	}
	setMarketGroupRowsForTest(fake, nil,
		marketQuestGroupForTest("quest-b", "any-b", 3, false, layoutAugmentin, layoutSalewa),
		marketQuestGroupForTest("quest-c", "any-c", 3, false, secondCandidates...))
	fake.groupProgressByCommit = [][]int64{{1, 0}, {2, 0}, {3, 0}, {3, 1}, {3, 2}, {3, 3}}
	states := map[string]ItemState{"zero": refreshLayoutItem(t, a, layoutAugmentin, "Augmentin antibiotic pills")}
	if !differentCandidates {
		for i := 0; i < 4; i++ {
			states["partial"] = addMarketItemForTest(t, a, layoutAugmentin+"-uid")
		}
		for i := 0; i < 2; i++ {
			states["full"] = addMarketItemForTest(t, a, layoutAugmentin+"-uid")
		}
	}
	return states
}

func TestLocalTrackerAggregatesOnlyIdenticalAnyEligibility(t *testing.T) {
	states := groupedLayoutStates(t, false)
	for key, wantAvailable := range map[string]int64{"zero": 0, "partial": 4, "full": 6} {
		tracking := states[key].Tracking
		if len(tracking.Requirements) != 0 || len(tracking.AnyGroups) != 1 || tracking.AnyGroups[0].AvailableQuantity != wantAvailable || tracking.AnyGroups[0].RequiredQuantity != 6 {
			t.Fatalf("%s equivalent Any groups were not displayed together: %+v", key, tracking)
		}
		if len(tracking.CompletedSources) != 0 {
			t.Fatal("covering an Any group completed a quest")
		}
		if key == "full" && (tracking.RequiredQuantity != 6 || tracking.RemainingQuantity != 0 || tracking.AddAllowed) {
			t.Fatalf("full Any display reopened collection: %+v", tracking)
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

func TestLocalTrackerFullFixedRequirementsKeepRatiosUntilQuestCompletion(t *testing.T) {
	for _, fir := range []bool{false, true} {
		kind := map[bool]string{false: "regular", true: "fir"}[fir]
		t.Run(kind, func(t *testing.T) {
			a, fake := newLocalTestApp(t)
			fake.snapshot.Items = []market.TrackedItem{{ItemID: trackingBSGID, RequiredQuantity: 10, RemainingQuantity: 10,
				Sources: []market.TrackedSource{{Type: "quest", QuestID: "quest-b", Amount: 10, RemainingRequiredQuantity: 10, FIR: fir}},
			}}
			fake.snapshot.Groups = nil
			refreshLayoutItem(t, a, trackingBSGID, "Tracked item")
			var state ItemState
			for i := 0; i < 10; i++ {
				state = addMarketItemForTest(t, a, trackingBSGID+"-uid")
			}
			if available, required := requirementStock(t, state, kind); available != 10 || required != 10 {
				t.Fatalf("full fixed requirement lost its 10/10 ratio: %+v", state.Tracking)
			}
			if state.Tracking.RequiredQuantity != 10 || state.Tracking.RemainingQuantity != 0 || state.Tracking.AddAllowed || len(state.Tracking.CompletedSources) != 0 {
				t.Fatalf("full stock changed the active requirement or allowed more collection: %+v", state.Tracking)
			}
			fake.quests.Quests[1].Completed, fake.quests.Quests[1].Status = true, "completed"
			state = refreshLayoutItem(t, a, trackingBSGID, "Tracked item")
			if state.Tracking.RequiredQuantity != 0 || len(state.Tracking.Requirements) != 0 || len(state.Tracking.AnyGroups) != 0 || len(state.Tracking.CompletedSources) != 1 || state.Tracking.CompletedSources[0].Quantity != 10 {
				t.Fatalf("completed quest kept an active full fixed requirement: %+v", state.Tracking)
			}
			if len(fake.writes) != 10 {
				t.Fatal("fixed Market requirement did not persist exactly ten additions")
			}
		})
	}
}
