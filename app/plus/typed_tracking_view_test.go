package plus

import (
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
)

func TestTypedUnknownGroupClearsEveryCandidateCounter(t *testing.T) {
	view := &ItemTrackingState{AnyGroups: []ItemTrackingAnyGroup{{GroupID: "quest:missing:any", RequiredQuantity: 5, AvailableQuantity: 3,
		Sources: []ItemTrackingAnySource{{GroupID: "quest:missing:any", RequiredQuantity: 5, AvailableQuantity: 3,
			Items: []ItemTrackingAnyItem{{ItemID: "selected", AvailableQuantity: 1}, {ItemID: "other", AvailableQuantity: 2}}}},
	}}}
	row := localprogress.Item{ItemID: "selected", QualityKnown: true, AvailableQuantity: 1, MarketAvailableQuantity: 1}
	applyTypedTrackingView(view, row, localprogress.Snapshot{Items: []localprogress.Item{row}})
	group := view.AnyGroups[0]
	if !group.AllocationUnknown || group.AvailableQuantity != 0 || group.RequiredQuantity != 5 {
		t.Fatalf("unknown typed group lost its raw demand: %+v", group)
	}
	for _, source := range group.Sources {
		if source.AvailableQuantity != 0 {
			t.Fatal("unknown source reused API group stock")
		}
		for _, candidate := range source.Items {
			if candidate.AvailableQuantity != 0 {
				t.Fatalf("unknown candidate reused untyped API stock: %+v", candidate)
			}
		}
	}
}
