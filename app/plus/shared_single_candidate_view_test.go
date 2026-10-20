package plus

import (
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestSingleAddressableSharedCandidateKeepsVariantCreditAndSharedDetails(t *testing.T) {
	raw := sharedArmorItemsFixture()
	raw.Items = raw.Items[:3]
	raw.Groups[0].ItemIDs = []string{variantArmorID, sharedArmor6B13ID}
	raw.Groups[0].AvailableQuantity, raw.Groups[0].RemainingQuantity = 5, 10
	snapshot := marketAnyProgressSnapshotForTest(t, raw)
	view := localTrackedView(market.Item{UID: "single-armor", BSGID: sharedArmor6B13ID}, snapshot)
	if view.AvailableQuantity != 3 || view.RequiredQuantity != 13 || !view.AddAllowed || len(view.Requirements) != 0 {
		t.Fatalf("az egyetlen címezhető jelölt elvesztette a variáns készletét vagy fix FiR igény lett belőle: %+v", view)
	}
	group := assertMarketAnyProgressForTest(t, view, 5, 15)
	if !group.FIR || len(group.Sources) != 1 || group.Sources[0].QuestName != "Scavenger" || len(group.Sources[0].Items) != 2 {
		t.Fatalf("az explicit csoport előrehaladása vagy készletlistája eltűnt: %+v", group)
	}
}
