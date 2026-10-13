package plus

import (
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestCachedFreshAmbiguousOrMissingItemInvalidatesOnlyAffectedStock(t *testing.T) {
	for _, mode := range []string{"ambiguous", "missing"} {
		t.Run(mode, func(t *testing.T) {
			a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
			fake.snapshot.Items[0].AvailableQuantity = 3
			own := localprogress.StockAmounts{FIR: 1}
			if _, err := a.localStore.CorrectStock("character-a", "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &own}); err != nil {
				t.Fatal(err)
			}
			primeLocalFirstForTest(t, a)
			base := localprogress.Clone(a.progressBaseSnapshot)
			otherID := "other-verified-item"
			base.Items = append(base.Items, localprogress.Item{ItemID: otherID, Name: "Other verified item", RequiredQuantity: 1, RemainingQuantity: 1, Sources: []localprogress.Source{{Origin: localprogress.OriginDev, TrackedSource: market.TrackedSource{Type: "quest", QuestID: "quest-b", Amount: 1, RemainingRequiredQuantity: 1}}}})
			if err := a.rememberProgressSources(a.apiKey, a.state.ItemOptions, base, ""); err != nil {
				t.Fatal(err)
			}
			a.progressWorkRequests = make(chan struct{}, 1)
			remote := cloneTrackedSnapshot(fake.snapshot)
			remote.Revision = "observed-changed-layout"
			if mode == "ambiguous" {
				variant := remote.Items[0]
				remote.Items[0].Name, remote.Items[0].AvailableQuantity = "Item variant full", 0
				variant.Name, variant.AvailableQuantity = "Item variant partial", 1
				remote.Items = append(remote.Items, variant)
			} else {
				remote.Items = []market.TrackedItem{}
			}
			a.trackingMu.Lock()
			err := a.updateLocalFirstMarketSnapshot(a.apiKey, a.state.ItemOptions, remote)
			if err == nil {
				a.publishLocalFirstProgress(a.apiKey, a.state.ItemOptions, "")
			}
			a.trackingMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			state := a.GetState()
			if state.Tracking == nil || state.Tracking.Error == "" || state.Tracking.AddFIRAllowed || state.Tracking.AddRegularAllowed || a.canAddLocalForTest() {
				t.Fatalf("fresh unsafe item stayed writable: %+v", state.Tracking)
			}
			if mode == "ambiguous" {
				if len(state.Tracking.Variants) != 2 || state.Tracking.Variants[0].AvailableQuantity != 0 || state.Tracking.Variants[1].AvailableQuantity != 1 {
					t.Fatalf("old unique stock survived the fresh variants: %+v", state.Tracking)
				}
			} else if state.Tracking.MarketAvailableQuantity != 0 || state.Tracking.AvailableQuantity != 1 {
				t.Fatalf("removed Market stock retained old quantity: %+v", state.Tracking)
			}
			assertCachedSavedItemQuantity(t, a, trackingBSGID, 1)
			reads := fake.loads
			a.AddTrackedItemFIR(trackingItemUID)
			assertCachedSavedItemQuantity(t, a, trackingBSGID, 1)
			a.mu.Lock()
			a.state.Item = &market.Item{UID: "other-item-uid", BSGID: otherID, Name: "Other verified item"}
			a.mu.Unlock()
			a.trackingMu.Lock()
			a.publishLocalFirstProgress(a.apiKey, a.state.ItemOptions, "")
			a.trackingMu.Unlock()
			state = a.AddTrackedItem("other-item-uid")
			if state.Tracking.AvailableQuantity != 1 || state.Tracking.Error != "" || fake.loads != reads || len(fake.writes) != 0 {
				t.Fatalf("unrelated local Add was blocked or performed network IO: %+v", state.Tracking)
			}
			// A koherens követelménylista után a saját készlet és a megszokott
			// hozzáadási lehetőség is visszatér; a guard nem maradhat ragadós.
			fake.snapshot.Revision = "fresh-full-graph"
			snapshot, warning, err := a.loadLocalSources(a.ctx, a.apiKey, a.state.ItemOptions)
			if err != nil {
				t.Fatal(err)
			}
			if err = a.rememberProgressSources(a.apiKey, a.state.ItemOptions, snapshot, warning); err != nil {
				t.Fatal(err)
			}
			a.mu.Lock()
			a.state.Item = &market.Item{UID: trackingItemUID, BSGID: trackingBSGID, Name: "Immobilizing splint"}
			a.mu.Unlock()
			a.trackingMu.Lock()
			a.publishLocalFirstProgress(a.apiKey, a.state.ItemOptions, "")
			a.trackingMu.Unlock()
			if state = a.GetState(); state.Tracking.Error != "" || !a.canAddLocalForTest() || state.Tracking.AvailableQuantity != 4 {
				t.Fatalf("confirmed full graph did not restore normal item functions: %+v", state.Tracking)
			}
		})
	}
}

func TestPendingUploadDetailsUseLocalMetadataAndAreCloned(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	expectedName := a.GetState().Item.Name
	state := a.AddTrackedItemFIR(trackingItemUID)
	if state.ProgressSync == nil || len(state.ProgressSync.Operations) != 1 {
		t.Fatalf("missing detailed pending operation: %+v", state.ProgressSync)
	}
	op := state.ProgressSync.Operations[0]
	if op.ItemID != trackingBSGID || op.ItemName != expectedName || op.Kind != "add" || op.Quantity != 1 || op.Status == "" || len(fake.writes) != 0 {
		t.Fatalf("pending metadata does not describe the locally saved item: %+v", op)
	}
	state.ProgressSync.Operations[0].ItemName = "modified caller copy"
	if a.GetState().ProgressSync.Operations[0].ItemName == "modified caller copy" {
		t.Fatal("pending operation aliases application state")
	}
	a.mu.Lock()
	a.trackedSnapshot, a.trackedRevision = cloneTrackedSnapshot(fake.snapshot), a.state.ItemOptions.revision
	a.trackedSnapshot.Items = []market.TrackedItem{}
	a.mu.Unlock()
	a.refreshProgressSyncState(a.state.ItemOptions, "", "", nil)
	state = a.GetState()
	if !state.ProgressSync.Pending || !strings.Contains(state.ProgressSync.Operations[0].Error, "no unique") {
		t.Fatalf("unuploadable saved item is silently waiting: %+v", state.ProgressSync)
	}
	assertLocalStockOnly(t, a, trackingBSGID, 0)
	stock, err := a.loadMarketFallback(a.state.ItemOptions)
	if err != nil {
		t.Fatal(err)
	}
	row, _ := stockEditorItem(stock, trackingBSGID)
	if row.LocalFIRQuantity != 1 {
		t.Fatalf("diagnostics modified pending stock: %+v", row)
	}
}

func TestCachedDevOnlyItemNewMarketSourceUsesFreshStockUntilFullRefresh(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	own := localprogress.StockAmounts{FIR: 1}
	if _, err := a.localStore.CorrectStock("character-a", "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &own}); err != nil {
		t.Fatal(err)
	}
	primeLocalFirstForTest(t, a)
	base := localprogress.Clone(a.progressBaseSnapshot)
	base.Items[0].MarketTracked = false
	base.Items[0].MarketRequiredQuantity, base.Items[0].MarketAvailableQuantity = 0, 0
	base.Items[0].Sources = []localprogress.Source{{Origin: localprogress.OriginDev, TrackedSource: market.TrackedSource{Type: "quest", QuestID: "dev-quest", Amount: 4, RemainingRequiredQuantity: 4}}}
	base.Items[0].RequiredQuantity = 4
	if err := a.rememberProgressSources(a.apiKey, a.state.ItemOptions, base, ""); err != nil {
		t.Fatal(err)
	}
	remote := cloneTrackedSnapshot(fake.snapshot)
	remote.Revision = "new-market-source"
	remote.Items[0].AvailableQuantity = 2
	a.trackingMu.Lock()
	err := a.updateLocalFirstMarketSnapshot(a.apiKey, a.state.ItemOptions, remote)
	if err == nil {
		a.publishLocalFirstProgress(a.apiKey, a.state.ItemOptions, "")
	}
	a.trackingMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	state := a.GetState()
	if state.Tracking == nil || state.Tracking.MarketAvailableQuantity != 2 || state.Tracking.AvailableQuantity != 3 || state.Tracking.Error == "" || state.Tracking.AddFIRAllowed || state.Tracking.AddRegularAllowed {
		t.Fatalf("new Market source kept the old dev-only cache: %+v", state.Tracking)
	}
	assertCachedSavedItemQuantity(t, a, trackingBSGID, 1)
	if len(fake.writes) != 0 {
		t.Fatal("fresh Market observation changed stock on the network")
	}
}

func TestCachedSameQuantityMarketRequirementChangesInvalidateAffectedItem(t *testing.T) {
	changes := map[string]func(*market.TrackedSource){
		"fir":       func(source *market.TrackedSource) { source.FIR = true },
		"quest":     func(source *market.TrackedSource) { source.QuestID = "another-quest" },
		"remaining": func(source *market.TrackedSource) { source.RemainingRequiredQuantity = 2 },
		"completed": func(source *market.TrackedSource) { source.Completed = true },
		"optional":  func(source *market.TrackedSource) { source.Optional = true },
		"amount":    func(source *market.TrackedSource) { source.Amount = 4 },
		"kind": func(source *market.TrackedSource) {
			source.Type, source.QuestID, source.UpgradeKey = "hideout", "", "medical:1"
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
			primeLocalFirstForTest(t, a)
			remote := cloneTrackedSnapshot(fake.snapshot)
			remote.Revision = "changed-source-same-quantity"
			change(&remote.Items[0].Sources[0])
			a.trackingMu.Lock()
			err := a.updateLocalFirstMarketSnapshot(a.apiKey, a.state.ItemOptions, remote)
			if err == nil {
				a.publishLocalFirstProgress(a.apiKey, a.state.ItemOptions, "")
			}
			a.trackingMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			state := a.AddTrackedItem(trackingItemUID)
			if state.Tracking.Error == "" || state.Tracking.AddFIRAllowed || state.Tracking.AddRegularAllowed || state.Tracking.AvailableQuantity != 0 || len(fake.writes) != 0 {
				t.Fatalf("same-quantity requirement change retained writable stale requirements: %+v", state.Tracking)
			}
		})
	}
}

func TestCachedMarketSourceOrderingAndStockRevisionKeepNormalAdd(t *testing.T) {
	a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
	fake.snapshot.Items[0].Sources = []market.TrackedSource{
		{Type: "quest", QuestID: "quest-a", Amount: 1, RemainingRequiredQuantity: 1},
		{Type: "quest", QuestID: "third-quest", Amount: 2, RemainingRequiredQuantity: 2},
	}
	snapshot, warning, err := a.loadLocalSources(a.ctx, a.apiKey, a.state.ItemOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.rememberProgressSources(a.apiKey, a.state.ItemOptions, snapshot, warning); err != nil {
		t.Fatal(err)
	}
	remote := cloneTrackedSnapshot(fake.snapshot)
	remote.Revision, remote.Items[0].AvailableQuantity = "stock-only-revision", 1
	remote.Items[0].Sources[0], remote.Items[0].Sources[1] = remote.Items[0].Sources[1], remote.Items[0].Sources[0]
	a.trackingMu.Lock()
	err = a.updateLocalFirstMarketSnapshot(a.apiKey, a.state.ItemOptions, remote)
	if err == nil {
		a.publishLocalFirstProgress(a.apiKey, a.state.ItemOptions, "")
	}
	a.trackingMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	reads := fake.loads
	state := a.AddTrackedItem(trackingItemUID)
	if state.Tracking.Error != "" || state.Tracking.AvailableQuantity != 2 || fake.loads != reads || len(fake.writes) != 0 {
		t.Fatalf("unchanged reordered requirements blocked normal local Add: %+v", state.Tracking)
	}
}

func assertCachedSavedItemQuantity(t *testing.T, a *ItemWindowApp, itemID string, expected int64) {
	t.Helper()
	stock, err := a.localStore.Load(a.state.CharacterUID, strings.ToUpper(a.state.Mode))
	if err != nil {
		t.Fatal(err)
	}
	row, unique := stockEditorItem(stock, itemID)
	if !unique || row.LocalAvailableQuantity != expected {
		t.Fatalf("saved local item quantity changed: %+v", row)
	}
}
