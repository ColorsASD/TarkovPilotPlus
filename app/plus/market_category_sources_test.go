package plus

import (
	"errors"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestMarketCategorySourcesKeepAPINamesAndSharedQuantities(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	otherID := "another-category-candidate"
	remote := cloneTrackedSnapshot(fake.snapshot)
	remote.Items = []market.TrackedItem{
		{ItemID: trackingBSGID, Name: "Candidate", AvailableQuantity: 1},
		{ItemID: otherID, Name: "Other candidate"},
	}
	remote.Groups = []market.TrackedGroup{
		{QuestID: "quest-a", QuestName: "Medical supplies", RequirementID: "medical", Name: "Medical items", Amount: 3, RequiredQuantity: 3, AvailableQuantity: 1, RemainingQuantity: 2, ItemIDs: []string{trackingBSGID, otherID}},
		{QuestID: "quest-b", QuestName: "Food supplies", RequirementID: "food", Name: "Food", Amount: 2, RequiredQuantity: 2, RemainingQuantity: 2, ItemIDs: []string{trackingBSGID, otherID}},
	}
	setTrackingTestSnapshot(t, a, remote)
	state := a.GetState()
	if len(state.Tracking.AnyGroups) != 1 {
		t.Fatalf("same-candidate categories did not keep their combined display: %+v", state.Tracking)
	}
	group := state.Tracking.AnyGroups[0]
	if group.RequiredQuantity != 5 || group.AvailableQuantity != 1 || len(group.Sources) != 2 {
		t.Fatalf("category metadata changed shared stock allocation: %+v", group)
	}
	medical, food := group.Sources[0], group.Sources[1]
	if medical.QuestName != "Medical supplies" || medical.Category != "Medical items" || medical.RequiredQuantity != 3 || medical.AvailableQuantity != 1 ||
		food.QuestName != "Food supplies" || food.Category != "Food" || food.RequiredQuantity != 2 || food.AvailableQuantity != 0 {
		t.Fatalf("Market category or quest information was lost: %+v", group.Sources)
	}
	group.Sources[0].Category = "modified caller copy"
	if a.GetState().Tracking.AnyGroups[0].Sources[0].Category != "Medical items" {
		t.Fatal("category detail slice aliases private state")
	}
	if remote.Items[0].RequiredQuantity != 0 || len(remote.Items[0].Sources) != 0 {
		t.Fatal("projecting categories changed the raw Market response")
	}
}

func TestMarketCategorySourceClonesAreIndependentInVariants(t *testing.T) {
	source := ItemTrackingAnySource{GroupID: "category", Category: "Food", QuestName: "Supplies", RequiredQuantity: 2}
	original := &ItemTrackingState{Variants: []*ItemTrackingState{{AnyGroups: []ItemTrackingAnyGroup{{Sources: []ItemTrackingAnySource{source}}}}}}
	copy := cloneTrackingState(original)
	copy.Variants[0].AnyGroups[0].Sources[0].Category = "modified"
	if original.Variants[0].AnyGroups[0].Sources[0].Category != "Food" {
		t.Fatal("variant category metadata aliases the application state")
	}
}

func TestMarketPayloadTooLargeHasExplicitStockFeedback(t *testing.T) {
	message := trackingError(errors.Join(market.ErrProgressPayloadTooLarge, errors.New("private response")))
	if !strings.Contains(message, "too large") || strings.Contains(message, "private response") {
		t.Fatalf("payload rejection was not safely explained: %s", message)
	}
}
