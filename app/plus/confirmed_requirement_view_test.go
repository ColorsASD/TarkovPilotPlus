package plus

import (
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func confirmedRequirementViewFixture(primaryID, alternativeID string, stock, alternativeStock, shared int64) market.TrackedSnapshot {
	return market.TrackedSnapshot{CharUID: "character-a", GameMode: "PVP", Revision: "confirmed-requirement-progress", Items: []market.TrackedItem{
		{ItemID: primaryID, Name: "Rye croutons", AvailableQuantity: stock, RequiredQuantity: 4, RemainingQuantity: max(0, 4-stock), Sources: []market.TrackedSource{
			{Type: "quest", QuestID: "regular-croutons", Amount: 4, RemainingRequiredQuantity: 4},
			{Type: "questGroup", QuestID: "food-shared", RequirementID: "food", Amount: 5, FIR: true},
		}},
		{ItemID: alternativeID, Name: "Instant noodles", AvailableQuantity: alternativeStock, Sources: []market.TrackedSource{
			{Type: "questGroup", QuestID: "food-shared", RequirementID: "food", Amount: 5, FIR: true},
		}},
	}, Groups: []market.TrackedGroup{{QuestID: "food-shared", QuestName: "Shared food quest", RequirementID: "food", Name: "Food", Amount: 5, FIR: true,
		RequiredQuantity: 5, AvailableQuantity: shared, RemainingQuantity: max(0, 5-shared), ItemIDs: []string{primaryID, alternativeID}}}}
}

func confirmedRequirementViewForTest(t *testing.T, raw market.TrackedSnapshot, id, name string) (*ItemTrackingState, localprogress.Snapshot) {
	t.Helper()
	snapshot := marketAnyProgressSnapshotForTest(t, raw)
	before := localprogress.Clone(snapshot)
	rawBefore := cloneTrackedSnapshot(raw)
	view := localTrackedView(market.Item{UID: "confirmed-requirement-item", BSGID: id, Name: name}, snapshot)
	if !reflect.DeepEqual(snapshot, before) || !reflect.DeepEqual(raw, rawBefore) {
		t.Fatal("a követelménysor megerősített kiosztása módosította a Market készletet vagy a gyűjtési pillanatképet")
	}
	return view, snapshot
}

func assertConfirmedRequirementRow(t *testing.T, view *ItemTrackingState, kind string, available, required int64) {
	t.Helper()
	for _, row := range view.Requirements {
		if row.Kind == kind {
			if row.AvailableQuantity != available || row.RequiredQuantity != required || row.AllocationUnknown {
				t.Fatalf("a követelménysor ugyanazt a megerősített készletet több célra számolta: kind=%s want=%d/%d row=%+v view=%+v", kind, available, required, row, view)
			}
			return
		}
	}
	t.Fatalf("hiányzik a követelménysor: kind=%s view=%+v", kind, view)
}

func TestConfirmedRequirementViewRyeReservesSharedStockBeforeRegular(t *testing.T) {
	for _, tc := range []struct {
		stock, alternativeStock, shared, regular int64
	}{
		{0, 1, 1, 0},
		{1, 1, 2, 0},
		{3, 0, 2, 1},
		{5, 0, 2, 3},
		{9, 0, 2, 4},
		{1, 1, 0, 1},
	} {
		raw := confirmedRequirementViewFixture("rye-croutons", "instant-noodles", tc.stock, tc.alternativeStock, tc.shared)
		view, _ := confirmedRequirementViewForTest(t, raw, raw.Items[0].ItemID, raw.Items[0].Name)
		assertConfirmedRequirementRow(t, view, "regular", tc.regular, 4)
		group := assertMarketAnyProgressForTest(t, view, tc.shared, 5)
		if !group.FIR || view.AvailableQuantity != tc.stock || view.MarketAvailableQuantity != tc.stock || view.RequiredQuantity != 9-min(tc.alternativeStock, tc.shared) {
			t.Fatalf("a megjelenítési kiosztás módosította a nyers Stock értékét vagy célját: case=%+v view=%+v", tc, view)
		}
		if tc.stock == 1 && tc.shared == 2 && (view.RequiredQuantity != 8 || !view.AddAllowed || !view.RemoveAllowed) {
			t.Fatalf("a Rye Regular0/4, SharedFiR2/5 eset Stock1/8 és a meglévő Add/Remove jogosultság maradjon: %+v", view)
		}
	}
}

func TestConfirmedRequirementViewAmbiguousSharedAllocationIsExplicitlyUnknown(t *testing.T) {
	for _, stock := range []int64{2, 3, 5} {
		raw := confirmedRequirementViewFixture("ambiguous-rye", "ambiguous-noodles", stock, 1, 2)
		view, _ := confirmedRequirementViewForTest(t, raw, "ambiguous-rye", "Rye croutons")
		if len(view.Requirements) != 1 || view.Requirements[0].Kind != "regular" || view.Requirements[0].RequiredQuantity != 4 || !view.Requirements[0].AllocationUnknown {
			t.Fatalf("a többféleképp kiosztható Shared készletből önkényes Regular darabszám született: %+v", view)
		}
		assertMarketAnyProgressForTest(t, view, 2, 5)
		if view.AvailableQuantity != stock || view.MarketAvailableQuantity != stock || view.RequiredQuantity != 8 {
			t.Fatalf("a bizonytalan megjelenítés módosította a készletet vagy a gyűjtési célt: %+v", view)
		}
	}
}

func TestConfirmedRequirementViewUncoveredSharedComponentDoesNotAffectUnrelatedRows(t *testing.T) {
	raw := confirmedRequirementViewFixture("uncovered-rye", "uncovered-noodles", 1, 1, 4)
	raw.Items = append(raw.Items, market.TrackedItem{ItemID: "independent-fixed-item", Name: "Independent item", AvailableQuantity: 1, RequiredQuantity: 2, RemainingQuantity: 1,
		Sources: []market.TrackedSource{{Type: "quest", QuestID: "independent-quest", Amount: 2, RemainingRequiredQuantity: 2}}})
	view, _ := confirmedRequirementViewForTest(t, raw, "uncovered-rye", "Rye croutons")
	if len(view.Requirements) != 1 || !view.Requirements[0].AllocationUnknown || view.Requirements[0].RequiredQuantity != 4 {
		t.Fatalf("a rendelkezésre álló készletnél nagyobb Shared előrehaladásból biztos Regular mennyiség született: %+v", view)
	}
	assertMarketAnyProgressForTest(t, view, 4, 5)
	independent, _ := confirmedRequirementViewForTest(t, raw, "independent-fixed-item", "Independent item")
	assertConfirmedRequirementRow(t, independent, "regular", 1, 2)
}

func TestConfirmedRequirementViewForcedReservationDoesNotDependOnNamesIDsOrOrder(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, ids := range [][2]string{{"a-primary", "z-alternative"}, {"z-primary", "a-alternative"}, {"unrelated-widget", "unrelated-part"}} {
			raw := confirmedRequirementViewFixture(ids[0], ids[1], 1, 1, 2)
			raw.Items[0].Name, raw.Items[1].Name = "Más tárgy neve", "További tárgy neve"
			if reverse {
				raw.Items[0], raw.Items[1] = raw.Items[1], raw.Items[0]
				raw.Groups[0].ItemIDs[0], raw.Groups[0].ItemIDs[1] = raw.Groups[0].ItemIDs[1], raw.Groups[0].ItemIDs[0]
			}
			view, _ := confirmedRequirementViewForTest(t, raw, ids[0], "Más tárgy neve")
			assertConfirmedRequirementRow(t, view, "regular", 0, 4)
			assertMarketAnyProgressForTest(t, view, 2, 5)
			if view.AvailableQuantity != 1 || view.RequiredQuantity != 8 {
				t.Fatalf("a tárgynév, azonosító vagy sorrend befolyásolta az általános kiosztást: %+v", view)
			}
		}
	}
}

func TestConfirmedRequirementViewFixedFIRAndRegularConsumeEachStockPieceOnce(t *testing.T) {
	for _, stock := range []int64{0, 1, 2, 3, 5, 8} {
		raw := confirmedRequirementViewFixture("mixed-fixed", "unneeded-alternative", stock, 0, 0)
		raw.Groups = nil
		raw.Items = raw.Items[:1]
		raw.Items[0].RequiredQuantity, raw.Items[0].RemainingQuantity = 5, max(0, 5-stock)
		raw.Items[0].Sources = []market.TrackedSource{
			{Type: "quest", QuestID: "fir-fixed", Amount: 2, RemainingRequiredQuantity: 2, FIR: true},
			{Type: "quest", QuestID: "regular-fixed", Amount: 3, RemainingRequiredQuantity: 3},
		}
		view, _ := confirmedRequirementViewForTest(t, raw, raw.Items[0].ItemID, raw.Items[0].Name)
		assertConfirmedRequirementRow(t, view, "fir", min(stock, 2), 2)
		assertConfirmedRequirementRow(t, view, "regular", min(max(0, stock-2), 3), 3)
		if view.AvailableQuantity != stock || view.RequiredQuantity != 5 {
			t.Fatalf("a fix FiR/Regular sorok kiosztása átírta a készletet: %+v", view)
		}
	}
}

func TestConfirmedRequirementViewReadOnlyVariantsReserveOnlyTheirOwnEligibleStock(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, sameName := range []bool{false, true} {
			raw := confirmedRequirementViewFixture("variant-food", "food-alternative", 1, 1, 2)
			raw.Items = append(raw.Items, market.TrackedItem{ItemID: "variant-food", Name: "Damaged food variant", AvailableQuantity: 7, RequiredQuantity: 3,
				Sources: []market.TrackedSource{{Type: "quest", QuestID: "other-regular-quest", Amount: 3, RemainingRequiredQuantity: 3}}})
			if sameName {
				raw.Items[2].Name = raw.Items[0].Name
			}
			if reverse {
				raw.Items[0], raw.Items[2] = raw.Items[2], raw.Items[0]
			}
			view, _ := confirmedRequirementViewForTest(t, raw, "variant-food", "Rye croutons")
			if len(view.Variants) != 2 || view.Error == "" || view.AddAllowed || view.RemoveAllowed {
				t.Fatalf("a kétértelmű készletváltozat módosíthatóvá vált: %+v", view)
			}
			for _, variant := range view.Variants {
				if variant.AddAllowed || variant.RemoveAllowed || variant.Error == "" {
					t.Fatalf("a követelménysor kiosztása feloldotta a variáns írásgátlását: %+v", variant)
				}
				if variant.AvailableQuantity == 1 {
					assertConfirmedRequirementRow(t, variant, "regular", 0, 4)
					assertMarketAnyProgressForTest(t, variant, 2, 5)
				} else {
					assertConfirmedRequirementRow(t, variant, "regular", 3, 3)
					if variant.AvailableQuantity != 7 || len(variant.AnyGroups) != 0 {
						t.Fatalf("a másik forráshoz tartozó variáns készlete átszivárgott a Shared csoportba: %+v", variant)
					}
				}
			}
		}
	}
}

func TestConfirmedRequirementViewPendingStockDoesNotBecomeConfirmedFixedProgress(t *testing.T) {
	raw := confirmedRequirementViewFixture("rye-pending", "noodles-pending", 1, 1, 2)
	snapshot := marketAnyProgressSnapshotForTest(t, raw)
	for i := range snapshot.Items {
		snapshot.Items[i].AvailableQuantity += 3
	}
	before := localprogress.Clone(snapshot)
	view := localTrackedView(market.Item{UID: "pending-rye-uid", BSGID: "rye-pending"}, snapshot)
	assertConfirmedRequirementRow(t, view, "regular", 0, 4)
	assertMarketAnyProgressForTest(t, view, 2, 5)
	if view.AvailableQuantity != 4 || view.MarketAvailableQuantity != 1 || !reflect.DeepEqual(snapshot, before) {
		t.Fatalf("a függő készletnövelés megerősített követelményt töltött ki vagy átírta a pillanatképet: %+v", view)
	}
}

func TestConfirmedRequirementViewInactiveVariantSourcesDoNotReserveStock(t *testing.T) {
	for _, inactive := range []string{"completed", "canceled", "optional"} {
		t.Run(inactive, func(t *testing.T) {
			raw := confirmedRequirementViewFixture("inactive-variant", "active-alternative", 1, 1, 1)
			raw.Items = append(raw.Items, market.TrackedItem{ItemID: "inactive-variant", Name: "Other variant", AvailableQuantity: 7, RequiredQuantity: 3,
				Sources: []market.TrackedSource{{Type: "quest", QuestID: "other-fixed-quest", Amount: 3, RemainingRequiredQuantity: 3}}})
			source := &raw.Items[0].Sources[1]
			switch inactive {
			case "completed":
				source.Completed = true
			case "canceled":
				source.Canceled = true
			case "optional":
				source.Optional = true
			}
			view, _ := confirmedRequirementViewForTest(t, raw, "inactive-variant", "Rye croutons")
			if len(view.Variants) != 2 {
				t.Fatalf("hiányoznak a külön készletváltozatok: %+v", view)
			}
			variant := view.Variants[0]
			assertConfirmedRequirementRow(t, variant, "regular", 1, 4)
			if len(variant.AnyGroups) != 0 || variant.AvailableQuantity != 1 || variant.AddAllowed || variant.RemoveAllowed {
				t.Fatalf("az inaktív Shared forrás a saját Regular készletből foglalt: %+v", variant)
			}
		})
	}
}

func TestConfirmedRequirementViewInactiveSharedGroupsDoNotReserveFixedStock(t *testing.T) {
	for _, inactive := range []string{"completed", "canceled", "optional"} {
		t.Run(inactive, func(t *testing.T) {
			raw := confirmedRequirementViewFixture("rye-inactive", "noodles-inactive", 1, 1, 2)
			switch inactive {
			case "completed":
				raw.Groups[0].Completed = true
				raw.Groups[0].RequiredQuantity, raw.Groups[0].AvailableQuantity, raw.Groups[0].RemainingQuantity = 0, 0, 0
			case "canceled":
				raw.Groups[0].Canceled = true
			case "optional":
				raw.Groups[0].Optional = true
			}
			view, _ := confirmedRequirementViewForTest(t, raw, "rye-inactive", "Rye croutons")
			assertConfirmedRequirementRow(t, view, "regular", 1, 4)
			if view.AvailableQuantity != 1 || len(view.AnyGroups) != 0 {
				t.Fatalf("lezárt vagy opcionális csoport aktív készletet foglalt: %+v", view)
			}
			if inactive == "completed" && (len(view.CompletedSources) != 1 || view.CompletedSources[0].Quantity != 5) {
				t.Fatalf("a teljesített csoport eredeti mennyisége elveszett: %+v", view.CompletedSources)
			}
		})
	}
}

func TestConfirmedRequirementViewOneAddStillSendsOneAbsoluteMarketStockWrite(t *testing.T) {
	a, fake := newTrackingTestApp(t)
	raw := confirmedRequirementViewFixture(trackingBSGID, "api-write-noodles", 1, 1, 2)
	fake.snapshot = cloneTrackedSnapshot(raw)
	fake.groupProgressByCommit = [][]int64{{3}}
	a.state.Item.Name = "Rye croutons"
	setTrackingTestSnapshot(t, a, raw)
	assertConfirmedRequirementRow(t, a.GetState().Tracking, "regular", 0, 4)
	state := addMarketTrackedItemForTest(a, trackingItemUID)
	if len(fake.writes) != 1 || fake.writes[0].Item != trackingBSGID || fake.writes[0].Quantity != 2 || state.Tracking.AvailableQuantity != 2 || state.Tracking.MarketAvailableQuantity != 2 {
		t.Fatalf("az egyetlen Add új típus szerinti vagy ismételt készletírást indított: writes=%+v view=%+v", fake.writes, state.Tracking)
	}
	assertConfirmedRequirementRow(t, state.Tracking, "regular", 0, 4)
	assertMarketAnyProgressForTest(t, state.Tracking, 3, 5)
	if fake.snapshot.Items[1].AvailableQuantity != 1 || fake.snapshot.Items[0].SpentQuantity != 0 {
		t.Fatal("a követelménynézet készletet fogyasztott vagy másik tárgy készletét módosította")
	}
}

func TestConfirmedRequirementViewRepeatedFixedSourcesRetainTheirReportedTotal(t *testing.T) {
	for _, variants := range []bool{false, true} {
		raw := confirmedRequirementViewFixture("repeated-fixed-item", "unused-alternative", 8, 0, 0)
		raw.Groups = nil
		raw.Items = raw.Items[:1]
		raw.Items[0].RequiredQuantity, raw.Items[0].RemainingQuantity = 8, 0
		source := market.TrackedSource{Type: "quest", QuestID: "repeated-fixed-quest", Amount: 4, RemainingRequiredQuantity: 4}
		raw.Items[0].Sources = []market.TrackedSource{source, source}
		if variants {
			raw.Items = append(raw.Items, market.TrackedItem{ItemID: "repeated-fixed-item", Name: "Another stock variant", AvailableQuantity: 2, RequiredQuantity: 3, RemainingQuantity: 1,
				Sources: []market.TrackedSource{{Type: "quest", QuestID: "different-fixed-quest", Amount: 3, RemainingRequiredQuantity: 3}}})
		}
		view, _ := confirmedRequirementViewForTest(t, raw, "repeated-fixed-item", "Rye croutons")
		assertConfirmedRequirementRow(t, view, "regular", 8, 8)
		if view.AvailableQuantity != 8 || view.RequiredQuantity != 8 || view.MarketRequiredQuantity != 8 || len(view.AnyGroups) != 0 {
			t.Fatalf("a két azonos fix forrás nyolcdarabos Market követelménye összevonáskor elveszett: %+v", view)
		}
		if variants {
			if len(view.Variants) != 2 || view.AddAllowed || view.RemoveAllowed || view.Error == "" {
				t.Fatalf("az ismételt fix forrás feloldotta a variánsok írásgátlását: %+v", view)
			}
			assertConfirmedRequirementRow(t, view.Variants[0], "regular", 8, 8)
			assertConfirmedRequirementRow(t, view.Variants[1], "regular", 2, 3)
			if view.Variants[0].AvailableQuantity != 8 || view.Variants[1].AvailableQuantity != 2 || view.Variants[0].AddAllowed || view.Variants[1].AddAllowed || view.Variants[0].RemoveAllowed || view.Variants[1].RemoveAllowed {
				t.Fatalf("a variánsok saját készlete összekeveredett vagy módosíthatóvá vált: %+v", view.Variants)
			}
		}
	}
}

func TestConfirmedRequirementViewHybridQuestUpgradeSourceStaysFixedAndOutOfSharedDetails(t *testing.T) {
	raw := confirmedRequirementViewFixture("hybrid-source-item", "valid-shared-alternative", 4, 1, 1)
	raw.Groups[0].FIR = false
	raw.Items[0].RemainingQuantity = 0
	raw.Items[0].Sources = []market.TrackedSource{{Type: "questGroup", QuestID: "food-shared", RequirementID: "food", UpgradeKey: "Stash_2", Amount: 4, RemainingRequiredQuantity: 4}}
	raw.Items[1].Sources[0].FIR = false
	// Az azonos ID-s sorok miatt a Market csoport nem írja felül az eredeti,
	// egyaránt quest- és upgrade-azonosítót tartalmazó bizonytalan forrást.
	raw.Items = append(raw.Items, market.TrackedItem{ItemID: "hybrid-source-item", Name: "Unrelated stock variant", AvailableQuantity: 2, RequiredQuantity: 3, RemainingQuantity: 1,
		Sources: []market.TrackedSource{{Type: "quest", QuestID: "unrelated-fixed-quest", Amount: 3, RemainingRequiredQuantity: 3}}})
	view, snapshot := confirmedRequirementViewForTest(t, raw, "hybrid-source-item", "Rye croutons")
	if len(view.Variants) != 2 {
		t.Fatalf("hiányoznak az egymástól független készletváltozatok: %+v", view)
	}
	assertConfirmedRequirementRow(t, view.Variants[0], "regular", 4, 4)
	assertConfirmedRequirementRow(t, view.Variants[1], "regular", 2, 3)
	for _, variant := range view.Variants {
		if len(variant.AnyGroups) != 0 || variant.AddAllowed || variant.RemoveAllowed {
			t.Fatalf("a quest- és upgrade-azonosítót keverő forrás Shared csoporthoz kapcsolódott: %+v", variant)
		}
	}
	alternative := localTrackedView(market.Item{UID: "valid-shared-uid", BSGID: "valid-shared-alternative"}, snapshot)
	group := assertMarketAnyProgressForTest(t, alternative, 1, 5)
	if len(group.Sources) != 1 || !reflect.DeepEqual(group.Sources[0].Items, []ItemTrackingAnyItem{{ItemID: "valid-shared-alternative", Name: "Instant noodles", AvailableQuantity: 1}}) {
		t.Fatalf("a Shared részletezése a kizárt hibrid forrás készletét is felsorolta: %+v", group.Sources)
	}
	if view.Variants[0].AvailableQuantity != 4 || view.Variants[0].MarketAvailableQuantity != 4 || view.Variants[0].RequiredQuantity != 4 || alternative.AvailableQuantity != 1 {
		t.Fatal("a hibrid forrás kizárása elvesztette a fix követelményt vagy megváltoztatta valamelyik készletet")
	}
}
