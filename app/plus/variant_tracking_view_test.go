package plus

import (
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

const variantArmorID = "variant-mf-untar"

// A Market ugyanazt az itemId értéket adja a két önálló készletsorhoz.
// A questGroup forrásnak nincs saját gyűjtési igénye, a közös előrehaladás
// kizárólag az explicit csoportból olvasható ki.
func variantArmorProgressFixture() market.TrackedSnapshot {
	return market.TrackedSnapshot{CharUID: "variant-character", GameMode: "PVP", Revision: "variant-progress", Items: []market.TrackedItem{
		{ItemID: variantArmorID, Name: "MF-UNTAR body armor (0/50)", RequiredQuantity: 1, RemainingQuantity: 1,
			Sources: []market.TrackedSource{{Type: "quest", QuestID: "fishing-gear", Name: "Fishing Gear", Amount: 1, RemainingRequiredQuantity: 1}}},
		{ItemID: variantArmorID, Name: "MF-UNTAR body armor", AvailableQuantity: 2,
			Sources: []market.TrackedSource{{Type: "questGroup", QuestID: "scavenger", RequirementID: "armor", Name: "Scavenger", GroupName: "Body armor", Amount: 15, FIR: true}}},
		{ItemID: "another-armor", Name: "Another armor"},
	}, Groups: []market.TrackedGroup{{QuestID: "scavenger", QuestName: "Scavenger", RequirementID: "armor", Name: "Body armor", Amount: 15,
		RequiredQuantity: 15, AvailableQuantity: 5, RemainingQuantity: 10, FIR: true, ItemIDs: []string{variantArmorID, "another-armor"}}}}
}

func variantProgressViewForTest(t *testing.T, raw market.TrackedSnapshot, name string, quests ...market.Quest) (*ItemTrackingState, localprogress.Snapshot) {
	t.Helper()
	snapshot := marketAnyProgressSnapshotForTest(t, raw, quests...)
	view := localTrackedView(market.Item{UID: "variant-market-uid", BSGID: variantArmorID, Name: name}, snapshot)
	if len(view.Variants) != 2 || view.Error == "" || view.AddAllowed || view.RemoveAllowed {
		t.Fatalf("a két Market készletsornak külön, csak olvasható nézetben kell maradnia: %+v", view)
	}
	for _, variant := range view.Variants {
		if variant == nil || !variant.Tracked || variant.Storage != "market" || variant.Error == "" || variant.AddAllowed || variant.RemoveAllowed || len(variant.Variants) != 0 {
			t.Fatalf("egy többértelmű variáns készletmódosítást enged vagy elvesztette az azonosságát: %+v", variant)
		}
	}
	return view, snapshot
}

func TestLocalVariantMFUNTARUsesOwnRegularStockAndExplicitSharedFIRProgress(t *testing.T) {
	raw := variantArmorProgressFixture()
	rawBefore := cloneTrackedSnapshot(raw)
	for _, scannedName := range []string{raw.Items[0].Name, raw.Items[1].Name, "ismeretlen név", ""} {
		t.Run(scannedName, func(t *testing.T) {
			view, snapshot := variantProgressViewForTest(t, raw, scannedName)
			before := localprogress.Clone(snapshot)
			empty, normal := view.Variants[0], view.Variants[1]
			if empty.VariantName != raw.Items[0].Name || empty.AvailableQuantity != 0 || empty.MarketAvailableQuantity != 0 || empty.RequiredQuantity != 1 || empty.MarketRequiredQuantity != 1 || empty.RemainingQuantity != 1 ||
				empty.Requirement != "regular" || !reflect.DeepEqual(empty.Requirements, []ItemTrackingRequirement{{Kind: "regular", AvailableQuantity: 0, RequiredQuantity: 1}}) || len(empty.AnyGroups) != 0 {
				t.Fatalf("a Fishing Gear variáns Regular 0/1 készletét felülírta a másik sor: %+v", empty)
			}
			if normal.VariantName != raw.Items[1].Name || normal.AvailableQuantity != 2 || normal.MarketAvailableQuantity != 2 || normal.RequiredQuantity != 0 || normal.MarketRequiredQuantity != 0 || len(normal.Requirements) != 0 {
				t.Fatalf("a normál variáns önálló 2 darabos készlete vagy nulla fix igénye elveszett: %+v", normal)
			}
			group := assertMarketAnyProgressForTest(t, normal, 5, 15)
			if !group.FIR || len(group.Sources) != 1 || group.Sources[0].QuestName != "Scavenger" || group.Sources[0].Category != "Body armor" || group.Sources[0].AvailableQuantity != 5 || group.Sources[0].RequiredQuantity != 15 {
				t.Fatalf("a Shared (FiR) sor nem az explicit Market csoportból készült: %+v", group)
			}
			localTrackedView(market.Item{UID: "variant-market-uid", BSGID: variantArmorID, Name: scannedName}, snapshot)
			if !reflect.DeepEqual(snapshot, before) || !reflect.DeepEqual(raw, rawBefore) {
				t.Fatal("a variánsok megjelenítése módosította a Market készletet vagy követelményeket")
			}
		})
	}
}

func TestLocalVariantSharedProgressKeepsRegularFIRAndConfirmedCountersSeparate(t *testing.T) {
	for _, fir := range []bool{false, true} {
		for _, available := range []int64{0, 5, 15, 19} {
			raw := variantArmorProgressFixture()
			raw.Items[1].Sources[0].FIR, raw.Groups[0].FIR = fir, fir
			raw.Groups[0].AvailableQuantity, raw.Groups[0].RemainingQuantity = available, max(0, 15-available)
			view, snapshot := variantProgressViewForTest(t, raw, raw.Items[1].Name)
			group := assertMarketAnyProgressForTest(t, view.Variants[1], min(available, 15), 15)
			if group.FIR != fir || view.Variants[1].AvailableQuantity != 2 || view.Variants[1].RequiredQuantity != 0 || len(view.Variants[0].AnyGroups) != 0 || len(view.Variants[1].Requirements) != 0 {
				t.Fatalf("a Shared típus vagy Market csoportszámláló összekeveredett a variáns készletével: fir=%v available=%d view=%+v", fir, available, view)
			}
			if snapshot.MarketGroups[0].AvailableQuantity != available {
				t.Fatal("a megjelenítés korlátozása átírta az eredeti Market csoportszámlálót")
			}
		}
	}
}

func TestLocalVariantFixedRequirementsUseEachRowsOwnConfirmedStock(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, sameName := range []bool{false, true} {
			raw := variantArmorProgressFixture()
			raw.Groups = nil
			raw.Items[0].AvailableQuantity = 7
			raw.Items[0].RequiredQuantity, raw.Items[0].RemainingQuantity = 3, 0
			raw.Items[0].Sources = []market.TrackedSource{{Type: "quest", QuestID: "collector", Amount: 3, RemainingRequiredQuantity: 3, FIR: true}}
			raw.Items[1].RequiredQuantity, raw.Items[1].RemainingQuantity = 6, 4
			raw.Items[1].Sources = []market.TrackedSource{{Type: "quest", QuestID: "fishing-gear", Amount: 6, RemainingRequiredQuantity: 6}}
			if sameName {
				raw.Items[1].Name = raw.Items[0].Name
			}
			if reverse {
				raw.Items[0], raw.Items[1] = raw.Items[1], raw.Items[0]
			}
			// A nem egyező tárgy a listán belül sem tolhatja el a variánsok sorait.
			raw.Items = []market.TrackedItem{raw.Items[2], raw.Items[0], raw.Items[1]}
			view, _ := variantProgressViewForTest(t, raw, raw.Items[1].Name)
			for i, variant := range view.Variants {
				row := raw.Items[i+1]
				kind := "regular"
				if row.Sources[0].FIR {
					kind = "fir"
				}
				expected := []ItemTrackingRequirement{{Kind: kind, RequiredQuantity: row.RequiredQuantity, AvailableQuantity: min(row.AvailableQuantity, row.RequiredQuantity)}}
				if variant.VariantName != row.Name || variant.AvailableQuantity != row.AvailableQuantity || variant.MarketAvailableQuantity != row.AvailableQuantity || variant.MarketRequiredQuantity != row.RequiredQuantity || variant.Requirement != kind || !reflect.DeepEqual(variant.Requirements, expected) || len(variant.AnyGroups) != 0 {
					t.Fatalf("a variáns név vagy listahely alapján másik sor készletét használta: reverse=%v sameName=%v row=%+v view=%+v", reverse, sameName, row, variant)
				}
			}
			if sameName && view.Tracked {
				t.Fatal("az azonos megjelenítési név önkényesen kiválasztott egy Market készletsort")
			}
		}
	}
}

func TestLocalVariantSharedGroupRequiresMatchingSourceIdentity(t *testing.T) {
	for _, mismatch := range []string{"missing-source", "quest", "requirement", "fir", "missing-explicit-group", "candidate"} {
		t.Run(mismatch, func(t *testing.T) {
			raw := variantArmorProgressFixture()
			switch mismatch {
			case "missing-source":
				raw.Items[1].Sources = nil
			case "quest":
				raw.Items[1].Sources[0].QuestID = "different-quest"
			case "requirement":
				raw.Items[1].Sources[0].RequirementID = "different-requirement"
			case "fir":
				raw.Items[1].Sources[0].FIR = false
			case "missing-explicit-group":
				raw.Groups = nil
			case "candidate":
				raw.Groups[0].ItemIDs = []string{"another-armor", "third-armor"}
			}
			view, _ := variantProgressViewForTest(t, raw, raw.Items[1].Name)
			for _, variant := range view.Variants {
				if len(variant.AnyGroups) != 0 {
					t.Fatalf("az explicit csoport másik vagy ismeretlen készletváltozathoz kapcsolódott: %+v", variant)
				}
			}
		})
	}
}

func TestLocalVariantInactiveSharedGroupsDoNotBecomeFixedRequirements(t *testing.T) {
	for _, inactive := range []string{"completed-source", "canceled-source", "optional-source", "completed-group", "canceled-group", "optional-group", "completed-quest", "canceled-quest"} {
		t.Run(inactive, func(t *testing.T) {
			raw := variantArmorProgressFixture()
			quests := []market.Quest{}
			switch inactive {
			case "completed-source":
				raw.Items[1].Sources[0].Completed = true
			case "canceled-source":
				raw.Items[1].Sources[0].Canceled = true
			case "optional-source":
				raw.Items[1].Sources[0].Optional = true
			case "completed-group":
				raw.Groups[0].Completed = true
				raw.Groups[0].RequiredQuantity, raw.Groups[0].AvailableQuantity, raw.Groups[0].RemainingQuantity = 0, 0, 0
			case "canceled-group":
				raw.Groups[0].Canceled = true
			case "optional-group":
				raw.Groups[0].Optional = true
			case "completed-quest":
				quests = append(quests, market.Quest{QuestID: "scavenger", Status: "completed", Completed: true})
			case "canceled-quest":
				quests = append(quests, market.Quest{QuestID: "scavenger", Status: "failed"})
			}
			view, _ := variantProgressViewForTest(t, raw, raw.Items[1].Name, quests...)
			variant := view.Variants[1]
			if len(variant.AnyGroups) != 0 || len(variant.Requirements) != 0 || variant.AvailableQuantity != 2 || variant.RequiredQuantity != 0 {
				t.Fatalf("a lezárt vagy opcionális csoport aktív igényt hozott létre: %+v", variant)
			}
			if len(view.Variants[0].Requirements) != 1 || view.Variants[0].Requirements[0].Kind != "regular" || view.Variants[0].Requirements[0].RequiredQuantity != 1 {
				t.Fatal("a csoport lezárása módosította a másik variáns Fishing Gear követelményét")
			}
		})
	}
}

func TestLocalVariantCompletedSharedGroupKeepsOriginalAmountOnce(t *testing.T) {
	for _, sourceCompleted := range []bool{false, true} {
		for _, optional := range []bool{false, true} {
			raw := variantArmorProgressFixture()
			raw.Groups[0].Completed, raw.Groups[0].Optional = true, optional
			raw.Groups[0].RequiredQuantity, raw.Groups[0].AvailableQuantity, raw.Groups[0].RemainingQuantity = 0, 0, 0
			source := &raw.Items[1].Sources[0]
			source.Completed, source.Optional = sourceCompleted, optional
			duplicate := *source
			duplicate.Amount = 7
			raw.Items[1].Sources = append(raw.Items[1].Sources, duplicate)
			view, _ := variantProgressViewForTest(t, raw, raw.Items[1].Name)
			variant := view.Variants[1]
			expected := []ItemTrackingCompletedSource{{Type: "quest", Quantity: 15, Optional: optional}}
			if !reflect.DeepEqual(variant.CompletedSources, expected) || len(variant.Requirements) != 0 || len(variant.AnyGroups) != 0 || variant.AvailableQuantity != 2 || variant.RequiredQuantity != 0 {
				t.Fatalf("a teljesített csoport eredeti mennyisége elveszett vagy forrásonként ismétlődik: sourceCompleted=%v optional=%v view=%+v", sourceCompleted, optional, variant)
			}
			if len(view.Variants[0].CompletedSources) != 0 {
				t.Fatal("a teljesített csoport darabszáma átkerült a másik készletváltozathoz")
			}
		}
	}
}

func TestLocalVariantMarketGroupsArePreservedAndDeeplyCloned(t *testing.T) {
	raw := variantArmorProgressFixture()
	expected := cloneTrackedSnapshot(raw).Groups
	snapshot := marketAnyProgressSnapshotForTest(t, raw)
	if !reflect.DeepEqual(snapshot.MarketGroups, expected) {
		t.Fatalf("az összevonás vagy készletkiosztás elvesztette az explicit Market csoportokat: %+v", snapshot.MarketGroups)
	}
	raw.Groups[0].ItemIDs[0] = "changed-input-member"
	raw.Groups[0].AvailableQuantity = 12
	if !reflect.DeepEqual(snapshot.MarketGroups, expected) {
		t.Fatal("a bemeneti Market csoportok módosítása átírta az összevont pillanatképet")
	}
	copy := localprogress.Clone(snapshot)
	copy.MarketGroups[0].ItemIDs[0] = "changed-cloned-member"
	copy.MarketGroups[0].AvailableQuantity = 9
	copy.MarketGroups = append(copy.MarketGroups, market.TrackedGroup{QuestID: "another-quest"})
	if !reflect.DeepEqual(snapshot.MarketGroups, expected) {
		t.Fatal("a klónozott csoport vagy belső jelöltlista módosítása átírta az eredeti pillanatképet")
	}
}
