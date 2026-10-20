package plus

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

const sharedArmorGroupID = "quest:scavenger:armor"
const sharedArmor6B13ID = "armor-6b13"
const sharedArmorInterceptorID = "armor-interceptor"

func sharedArmorItemsFixture() market.TrackedSnapshot {
	raw := variantArmorProgressFixture()
	raw.Items[0].AvailableQuantity, raw.Items[0].RemainingQuantity = 7, 0
	raw.Items[2] = market.TrackedItem{ItemID: sharedArmor6B13ID, Name: "6B13 assault armor", AvailableQuantity: 3,
		Sources: []market.TrackedSource{{Type: "questGroup", QuestID: "scavenger", RequirementID: "armor", Amount: 15, FIR: true}}}
	raw.Items = append(raw.Items, market.TrackedItem{ItemID: sharedArmorInterceptorID, Name: "Interceptor OTV body armor", AvailableQuantity: 1,
		Sources: []market.TrackedSource{{Type: "questGroup", QuestID: "scavenger", RequirementID: "armor", Amount: 15, FIR: true}}})
	raw.Groups[0].ItemIDs = []string{variantArmorID, sharedArmor6B13ID, sharedArmorInterceptorID}
	raw.Groups[0].AvailableQuantity, raw.Groups[0].RemainingQuantity = 6, 9
	return raw
}

func sharedArmorExpectedItems() []ItemTrackingAnyItem {
	return []ItemTrackingAnyItem{
		{ItemID: sharedArmor6B13ID, Name: "6B13 assault armor", AvailableQuantity: 3},
		{ItemID: sharedArmorInterceptorID, Name: "Interceptor OTV body armor", AvailableQuantity: 1},
		{ItemID: variantArmorID, Name: "MF-UNTAR body armor", AvailableQuantity: 2},
	}
}

func assertSharedArmorSourceItems(t *testing.T, view *ItemTrackingState, fir bool) {
	t.Helper()
	if len(view.AnyGroups) != 1 || view.AnyGroups[0].AvailableQuantity != 6 || view.AnyGroups[0].RequiredQuantity != 15 || view.AnyGroups[0].FIR != fir || len(view.AnyGroups[0].Sources) != 1 {
		t.Fatalf("a közös Market előrehaladás megváltozott a készletsorok felsorolásától: %+v", view)
	}
	source := view.AnyGroups[0].Sources[0]
	if source.GroupID != sharedArmorGroupID || source.AvailableQuantity != 6 || source.RequiredQuantity != 15 || !reflect.DeepEqual(source.Items, sharedArmorExpectedItems()) {
		t.Fatalf("a Shared forrás nem a három saját, jogosult készletsort tartalmazza: %+v", source)
	}
}

func TestSharedTrackingItemsExposeSameConfirmedArmorRowsInNormalAndVariantViews(t *testing.T) {
	for _, fir := range []bool{false, true} {
		raw := sharedArmorItemsFixture()
		raw.Groups[0].FIR = fir
		for itemIndex := range raw.Items {
			for sourceIndex := range raw.Items[itemIndex].Sources {
				source := &raw.Items[itemIndex].Sources[sourceIndex]
				if source.QuestID == "scavenger" {
					source.FIR = fir
				}
			}
		}
		snapshot := marketAnyProgressSnapshotForTest(t, raw)
		before := localprogress.Clone(snapshot)
		if got := sharedTrackingItems(snapshot, sharedArmorGroupID); !reflect.DeepEqual(got, sharedArmorExpectedItems()) {
			t.Fatalf("a helper a sérült Regular készletet is beszámolta vagy elvesztett egy jogosult sort: %+v", got)
		}
		for _, id := range []string{sharedArmor6B13ID, sharedArmorInterceptorID} {
			view := localTrackedView(market.Item{UID: id + "-uid", BSGID: id}, snapshot)
			if len(view.Variants) != 0 {
				t.Fatalf("az egyértelmű tárgyhoz indokolatlan variánsnézet készült: %+v", view)
			}
			assertSharedArmorSourceItems(t, view, fir)
		}
		view := localTrackedView(market.Item{UID: "mf-untar-uid", BSGID: variantArmorID, Name: raw.Items[1].Name}, snapshot)
		if len(view.Variants) != 2 || view.Variants[0].AvailableQuantity != 7 || len(view.Variants[0].AnyGroups) != 0 || len(view.Variants[0].Requirements) != 1 || view.Variants[0].Requirements[0].Kind != "regular" {
			t.Fatalf("a másik same-ID Regular sor bekerült a Shared készletbe: %+v", view)
		}
		assertSharedArmorSourceItems(t, view, fir)
		assertSharedArmorSourceItems(t, view.Variants[1], fir)
		if !reflect.DeepEqual(snapshot, before) {
			t.Fatal("a Shared részletezés módosította a készletet, az előrehaladást vagy a kiosztást")
		}
	}
}

func TestSharedTrackingItemsRequireActiveMatchingMarketSource(t *testing.T) {
	for _, mismatch := range []string{"quest", "requirement", "fir", "upgrade", "missing-source", "canceled", "completed", "optional", "not-market", "zero-stock", "not-candidate"} {
		t.Run(mismatch, func(t *testing.T) {
			snapshot := marketAnyProgressSnapshotForTest(t, sharedArmorItemsFixture())
			row := &snapshot.Items[2]
			switch mismatch {
			case "quest":
				row.Sources[0].QuestID = "different-quest"
			case "requirement":
				row.Sources[0].RequirementID = "different-requirement"
			case "fir":
				row.Sources[0].FIR = false
			case "upgrade":
				row.Sources[0].UpgradeKey = "Workbench-1"
			case "missing-source":
				row.Sources = nil
			case "canceled":
				row.Sources[0].Canceled = true
			case "completed":
				row.Sources[0].Completed = true
			case "optional":
				row.Sources[0].Optional = true
			case "not-market":
				row.MarketTracked = false
			case "zero-stock":
				row.MarketAvailableQuantity = 0
			case "not-candidate":
				snapshot.MarketGroups[0].ItemIDs = []string{variantArmorID, sharedArmorInterceptorID}
			}
			expected := sharedArmorExpectedItems()[1:]
			if got := sharedTrackingItems(snapshot, sharedArmorGroupID); !reflect.DeepEqual(got, expected) {
				t.Fatalf("a nem jogosult készletsor bekerült a csoport részleteibe: %+v", got)
			}
		})
	}
	for _, inactive := range []string{"completed", "canceled", "optional", "unknown-group"} {
		t.Run(inactive+"-group", func(t *testing.T) {
			snapshot := marketAnyProgressSnapshotForTest(t, sharedArmorItemsFixture())
			groupID := sharedArmorGroupID
			switch inactive {
			case "completed":
				snapshot.MarketGroups[0].Completed = true
			case "canceled":
				snapshot.MarketGroups[0].Canceled = true
			case "optional":
				snapshot.MarketGroups[0].Optional = true
			case "unknown-group":
				groupID = "quest:unknown:armor"
			}
			if got := sharedTrackingItems(snapshot, groupID); len(got) != 0 {
				t.Fatalf("lezárt vagy ismeretlen csoport készletét jelenítette meg: %+v", got)
			}
		})
	}
}

func TestSharedTrackingItemsKeepPendingProjectionOutOfConfirmedDetails(t *testing.T) {
	raw := sharedArmorItemsFixture()
	snapshot := marketAnyProgressSnapshotForTest(t, raw)
	for i := range snapshot.Items {
		snapshot.Items[i].AvailableQuantity += 4
	}
	if got := sharedTrackingItems(snapshot, sharedArmorGroupID); !reflect.DeepEqual(got, sharedArmorExpectedItems()) {
		t.Fatalf("a függő készletnövelés már megerősített mennyiségként jelent meg: %+v", got)
	}
	for _, id := range []string{sharedArmor6B13ID, variantArmorID} {
		view := localTrackedView(market.Item{UID: id + "-uid", BSGID: id, Name: raw.Items[1].Name}, snapshot)
		assertSharedArmorSourceItems(t, view, true)
		if id == variantArmorID {
			assertSharedArmorSourceItems(t, view.Variants[1], true)
		}
	}
	// Egy teljesen függő tárgy még nem kerülhet a megerősített listába.
	snapshot.Items[2].MarketAvailableQuantity = 0
	expected := sharedArmorExpectedItems()[1:]
	if got := sharedTrackingItems(snapshot, sharedArmorGroupID); !reflect.DeepEqual(got, expected) {
		t.Fatalf("a még vissza nem igazolt készletsor bekerült a részletekbe: %+v", got)
	}
}

func TestSharedTrackingItemsDeduplicateSourcesWithoutCombiningVariantRows(t *testing.T) {
	snapshot := marketAnyProgressSnapshotForTest(t, sharedArmorItemsFixture())
	for i := range snapshot.Items {
		snapshot.Items[i].Sources = append(snapshot.Items[i].Sources, snapshot.Items[i].Sources...)
	}
	if got := sharedTrackingItems(snapshot, sharedArmorGroupID); !reflect.DeepEqual(got, sharedArmorExpectedItems()) {
		t.Fatalf("a megismételt forrás ugyanazt a Market készletsort többször sorolta fel: %+v", got)
	}
	// A forrás nélküli vagy eltérő Regular sor ugyanazon itemId mellett is külön marad.
	snapshot.Items[0].Name = snapshot.Items[1].Name
	if got := sharedTrackingItems(snapshot, sharedArmorGroupID); !reflect.DeepEqual(got, sharedArmorExpectedItems()) {
		t.Fatalf("az azonos megjelenítési név összevonta a különböző készletváltozatokat: %+v", got)
	}
}

func TestSharedTrackingItemsNestedStateClonesAreIndependent(t *testing.T) {
	raw := sharedArmorItemsFixture()
	snapshot := marketAnyProgressSnapshotForTest(t, raw)
	for _, id := range []string{sharedArmor6B13ID, variantArmorID} {
		t.Run(id, func(t *testing.T) {
			original := localTrackedView(market.Item{UID: id + "-uid", BSGID: id, Name: raw.Items[1].Name}, snapshot)
			before, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			copy := cloneTrackingState(original)
			copy.AnyGroups[0].Sources[0].Items[0].ItemID = "changed-item-id"
			copy.AnyGroups[0].Sources[0].Items[0].Name = "Módosított név"
			copy.AnyGroups[0].Sources[0].Items[0].AvailableQuantity = 99
			if len(copy.Variants) != 0 {
				copy.Variants[1].AnyGroups[0].Sources[0].Items[1].AvailableQuantity = 88
				copy.Variants[1].AnyGroups[0].Sources[0].Items[1].Name = "Másik módosított név"
			}
			after, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("a klónozott állapot belső Shared Items listája módosította az eredeti normál vagy variánsnézetet")
			}
			assertSharedArmorSourceItems(t, original, true)
			if len(original.Variants) != 0 {
				assertSharedArmorSourceItems(t, original.Variants[1], true)
			}
		})
	}
}
