package localprogress

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestManualStockCorrectionPreservesTypedUsageAndReceiptAcrossRestart(t *testing.T) {
	store := handoverStoreFixture(t)
	entry := HandoverEntry{ItemID: "dev-only", Regular: 1, FIR: 1, Unknown: 1}
	if _, err := store.RecordHandover("char-1", "PVP", "original-hand-in", []HandoverEntry{entry}); err != nil {
		t.Fatal(err)
	}
	path, _ := store.path("char-1", "PVP")
	before, err := readState(path, "char-1", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	correction := ManualStockCorrection{ItemID: "dev-only", LocalAvailable: &StockAmounts{Regular: 4, FIR: 2, Unknown: 1}}
	result, err := store.CorrectStock("char-1", "PVP", correction)
	if err != nil {
		t.Fatal(err)
	}
	item := requireCollectedStock(t, result, 10, 5, 3, 3, 1, 1)
	if item.LocalAvailableQuantity != 7 || item.LocalRegularQuantity != 4 || item.LocalFIRQuantity != 2 {
		t.Fatalf("manual available target was added instead of set: %+v", item)
	}
	after, err := readState(path, "char-1", "PVP")
	if err != nil || !reflect.DeepEqual(before.LocalUsed, after.LocalUsed) || !reflect.DeepEqual(before.HandoverReceipts, after.HandoverReceipts) {
		t.Fatalf("manual correction changed immutable usage: %+v %v", after, err)
	}
	data := rawStore(t, store)
	assertPureStockFile(t, data)
	restarted := NewStore(store.dir)
	loaded, err := restarted.Load("char-1", "PVP")
	if err != nil || !reflect.DeepEqual(result, loaded) {
		t.Fatalf("manual correction was not durable: %+v %v", loaded, err)
	}
	repeated, err := restarted.CorrectStock("char-1", "PVP", correction)
	if err != nil || !reflect.DeepEqual(result, repeated) || !bytes.Equal(data, rawStore(t, store)) {
		t.Fatalf("repeated absolute correction changed stock: %+v %v", repeated, err)
	}
	replayed, err := restarted.RecordHandover("char-1", "PVP", "original-hand-in", []HandoverEntry{entry})
	if err != nil || !reflect.DeepEqual(result, replayed) || !bytes.Equal(data, rawStore(t, store)) {
		t.Fatalf("original receipt replay consumed corrected stock: %+v %v", replayed, err)
	}
	if _, err := restarted.RecordHandover("char-1", "PVP", "original-hand-in", []HandoverEntry{{ItemID: "dev-only", Regular: 2}}); !errors.Is(err, ErrHandoverConflict) {
		t.Fatalf("manual correction unlocked immutable receipt replacement: %v", err)
	}
}

func TestManualStockClearSetsAvailableZeroWithoutCreatingUsage(t *testing.T) {
	store := handoverStoreFixture(t)
	if _, err := store.RecordHandover("char-1", "PVP", "original-hand-in", []HandoverEntry{{ItemID: "dev-only", Regular: 1, FIR: 1, Unknown: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CorrectStock("char-1", "PVP", ManualStockCorrection{ItemID: "dev-only", MarketKinds: &StockAmounts{Regular: 1, FIR: 2}, ObservedMarketQuantity: 3}); err != nil {
		t.Fatal(err)
	}
	path, _ := store.path("char-1", "PVP")
	before, _ := readState(path, "char-1", "PVP")
	result, err := store.CorrectStock("char-1", "PVP", ManualStockCorrection{ItemID: "dev-only", LocalAvailable: &StockAmounts{}})
	if err != nil {
		t.Fatal(err)
	}
	item := requireCollectedStock(t, result, 3, 1, 1, 3, 1, 1)
	if item.AvailableQuantity != 0 || item.LocalRegularQuantity != 0 || item.LocalFIRQuantity != 0 || item.MarketRegularQuantity != 1 || item.MarketFIRQuantity != 2 {
		t.Fatalf("clear changed another stock source or retained local available: %+v", item)
	}
	after, err := readState(path, "char-1", "PVP")
	if err != nil || !reflect.DeepEqual(before.LocalUsed, after.LocalUsed) || !reflect.DeepEqual(before.HandoverReceipts, after.HandoverReceipts) || !reflect.DeepEqual(before.MarketKinds, after.MarketKinds) {
		t.Fatalf("clear fabricated usage or changed Market annotations: %+v %v", after, err)
	}
	if _, err := store.CorrectStock("char-1", "PVP", ManualStockCorrection{ItemID: "fresh-local", LocalAvailable: &StockAmounts{}}); err != nil {
		t.Fatal(err)
	}
	after, _ = readState(path, "char-1", "PVP")
	if _, exists := after.LocalUsed["fresh-local"]; exists || len(after.HandoverReceipts) != 1 {
		t.Fatal("clearing unused stock created a hand-in record")
	}
}

func TestManualMarketKindsUsesObservedFingerprintAndNeverMirrorsMarketTotal(t *testing.T) {
	store := NewStore(t.TempDir())
	const observed int64 = 987654321
	result, err := store.CorrectStock("char-1", "PVP", ManualStockCorrection{
		ItemID: "market-only", MarketKinds: &StockAmounts{Regular: 2, FIR: 3, Unknown: observed - 5}, ObservedMarketQuantity: observed,
	})
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, result, "market-only")
	if item.MarketAvailableQuantity != 0 || item.AvailableQuantity != 0 || item.LocalCollectedQuantity != 0 || item.MarketRegularQuantity != 2 || item.MarketFIRQuantity != 3 {
		t.Fatalf("correction returned a mirrored Market count: %+v", item)
	}
	path, _ := store.path("char-1", "PVP")
	state, err := readState(path, "char-1", "PVP")
	if err != nil || len(state.Stock) != 0 || state.MarketKinds["market-only"].StockHash != marketStockHash("market-only", observed) {
		t.Fatalf("annotation was not tied to observed count: %+v %v", state, err)
	}
	assertPureStockFile(t, rawStore(t, store), "987654321", "987654316", "observedMarketQuantity", "marketKindsCorrection")
	merged, err := store.Apply(marketStockFixture("market-only", observed))
	if err != nil || merged.Items[0].MarketRegularQuantity != 2 || merged.Items[0].MarketFIRQuantity != 3 || merged.Items[0].AvailableQuantity != observed {
		t.Fatalf("matching live snapshot lost corrected quality: %+v %v", merged, err)
	}
	merged, err = store.Apply(marketStockFixture("market-only", observed-1))
	if err != nil || merged.Items[0].MarketRegularQuantity != 0 || merged.Items[0].MarketFIRQuantity != 0 {
		t.Fatalf("external count change retained unproven corrected quality: %+v %v", merged, err)
	}
}

func TestManualMarketZeroPreservesOwnTypedStockAndHistory(t *testing.T) {
	store := handoverStoreFixture(t)
	if _, err := store.RecordHandover("char-1", "PVP", "original-hand-in", []HandoverEntry{{ItemID: "dev-only", FIR: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CorrectStock("char-1", "PVP", ManualStockCorrection{ItemID: "dev-only", MarketKinds: &StockAmounts{Regular: 1, Unknown: 2}, ObservedMarketQuantity: 3}); err != nil {
		t.Fatal(err)
	}
	path, _ := store.path("char-1", "PVP")
	before, _ := readState(path, "char-1", "PVP")
	result, err := store.CorrectStock("char-1", "PVP", ManualStockCorrection{ItemID: "dev-only", MarketKinds: &StockAmounts{}, ObservedMarketQuantity: 0})
	if err != nil {
		t.Fatal(err)
	}
	item := requireCollectedStock(t, result, 5, 2, 1, 1, 0, 1)
	if item.MarketRegularQuantity != 0 || item.MarketFIRQuantity != 0 || item.LocalAvailableQuantity != 4 {
		t.Fatalf("Market zero altered own stock: %+v", item)
	}
	after, err := readState(path, "char-1", "PVP")
	if err != nil || len(after.MarketKinds) != 0 {
		t.Fatalf("Market zero retained annotation: %+v %v", after, err)
	}
	before.MarketKinds = after.MarketKinds
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("Market zero changed another stored field: before=%+v after=%+v", before, after)
	}
}

func TestManualCorrectionChangesBothSourcesAtomicallyAndPreservesOtherIdentities(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.CorrectStock("char-1", "PVP", ManualStockCorrection{ItemID: "other-item", LocalAvailable: &StockAmounts{Unknown: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CorrectStock("char-2", "PVP", ManualStockCorrection{ItemID: "dev-only", LocalAvailable: &StockAmounts{FIR: 8}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CorrectStock("char-1", "PVE", ManualStockCorrection{ItemID: "dev-only", LocalAvailable: &StockAmounts{Regular: 9}}); err != nil {
		t.Fatal(err)
	}
	result, err := store.CorrectStock("char-1", "PVP", ManualStockCorrection{
		ItemID: "dev-only", LocalAvailable: &StockAmounts{Regular: 1, FIR: 2, Unknown: 3}, MarketKinds: &StockAmounts{Regular: 4, FIR: 5, Unknown: 6}, ObservedMarketQuantity: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	item := requireCollectedStock(t, result, 6, 1, 2, 0, 0, 0)
	if item.MarketRegularQuantity != 4 || item.MarketFIRQuantity != 5 || requireItem(t, result, "other-item").LocalAvailableQuantity != 2 {
		t.Fatalf("dual correction changed another item: %+v", result)
	}
	for _, fixture := range []struct {
		char, mode string
		quantity   int64
	}{{"char-2", "PVP", 8}, {"char-1", "PVE", 9}} {
		loaded, err := NewStore(store.dir).Load(fixture.char, fixture.mode)
		if err != nil || requireItem(t, loaded, "dev-only").LocalAvailableQuantity != fixture.quantity {
			t.Fatalf("correction crossed character/mode boundary: %+v %v", loaded, err)
		}
	}
}

func TestManualCorrectionRejectsInvalidOrOverflowingTargetsWithoutWriting(t *testing.T) {
	store := handoverStoreFixture(t)
	if _, err := store.RecordHandover("char-1", "PVP", "already-used", []HandoverEntry{{ItemID: "dev-only", Regular: 1}}); err != nil {
		t.Fatal(err)
	}
	before := rawStore(t, store)
	for _, fixture := range []struct {
		name       string
		correction ManualStockCorrection
		err        error
	}{
		{"empty", ManualStockCorrection{ItemID: "dev-only"}, ErrInvalidQuantity},
		{"invalid ID", ManualStockCorrection{ItemID: "../escape", LocalAvailable: &StockAmounts{}}, ErrInvalidIdentity},
		{"negative", ManualStockCorrection{ItemID: "dev-only", LocalAvailable: &StockAmounts{Unknown: -1}}, ErrInvalidQuantity},
		{"sum overflow", ManualStockCorrection{ItemID: "dev-only", LocalAvailable: &StockAmounts{Regular: MaxQuantity, FIR: 1}}, ErrInvalidQuantity},
		{"used plus available overflow", ManualStockCorrection{ItemID: "dev-only", LocalAvailable: &StockAmounts{Unknown: MaxQuantity}}, ErrInvalidQuantity},
		{"typed larger than observed", ManualStockCorrection{ItemID: "dev-only", MarketKinds: &StockAmounts{FIR: 2}, ObservedMarketQuantity: 1}, ErrInvalidQuantity},
		{"typed smaller than observed", ManualStockCorrection{ItemID: "dev-only", MarketKinds: &StockAmounts{Regular: 1}, ObservedMarketQuantity: 2}, ErrInvalidQuantity},
		{"invalid observed", ManualStockCorrection{ItemID: "dev-only", MarketKinds: &StockAmounts{}, ObservedMarketQuantity: -1}, ErrInvalidQuantity},
		{"atomic dual invalid", ManualStockCorrection{ItemID: "dev-only", LocalAvailable: &StockAmounts{FIR: 1}, MarketKinds: &StockAmounts{Regular: 2}, ObservedMarketQuantity: 1}, ErrInvalidQuantity},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			result, err := store.CorrectStock("char-1", "PVP", fixture.correction)
			if !errors.Is(err, fixture.err) || result.Items != nil || !bytes.Equal(before, rawStore(t, store)) {
				t.Fatalf("invalid correction altered stock: %+v %v", result, err)
			}
		})
	}
	if _, err := store.CorrectStock("char-1", "PVP", ManualStockCorrection{ItemID: "dev-only", LocalAvailable: &StockAmounts{Regular: MaxQuantity - 1}}); err != nil {
		t.Fatalf("valid collected boundary rejected: %v", err)
	}
}

func TestManualCorrectionWriteFailureAndCorruptFileKeepPreviousData(t *testing.T) {
	store := handoverStoreFixture(t)
	before := rawStore(t, store)
	diskErr := errors.New("test disk failure")
	store.writeFile = func(string, []byte) error { return diskErr }
	result, err := store.CorrectStock("char-1", "PVP", ManualStockCorrection{
		ItemID: "dev-only", LocalAvailable: &StockAmounts{FIR: 4}, MarketKinds: &StockAmounts{Regular: 2}, ObservedMarketQuantity: 2,
	})
	if !errors.Is(err, diskErr) || result.Items != nil || !bytes.Equal(before, rawStore(t, store)) {
		t.Fatalf("failed atomic correction leaked a new projection: %+v %v", result, err)
	}
	loaded, err := store.Load("char-1", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	requireCollectedStock(t, loaded, 5, 2, 1, 0, 0, 0)
	store.writeFile = atomicWrite
	path, _ := store.path("char-1", "PVP")
	corrupt := []byte(`{"version":4,"stock":"broken"}`)
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = store.CorrectStock("char-1", "PVP", ManualStockCorrection{ItemID: "dev-only", LocalAvailable: &StockAmounts{Regular: 2}})
	if !errors.Is(err, ErrCorruptStore) || result.Items != nil || !bytes.Equal(corrupt, rawStore(t, store)) {
		t.Fatalf("manual correction silently replaced corrupt state: %+v %v", result, err)
	}
}

func TestManualCorrectionMigratesUnknownLegacyStockWithoutInventingUsage(t *testing.T) {
	store := NewStore(t.TempDir())
	path, _ := store.path("char-1", "PVP")
	legacy := []byte(`{"version":2,"charUid":"char-1","gameMode":"PVP","stock":{"dev-only":4,"other-item":2}}`)
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := store.CorrectStock("char-1", "PVP", ManualStockCorrection{ItemID: "dev-only", LocalAvailable: &StockAmounts{Regular: 1, FIR: 2, Unknown: 3}})
	if err != nil {
		t.Fatal(err)
	}
	requireCollectedStock(t, result, 6, 1, 2, 0, 0, 0)
	if other := requireItem(t, result, "other-item"); other.LocalAvailableQuantity != 2 || other.LocalRegularQuantity != 0 || other.LocalFIRQuantity != 0 {
		t.Fatalf("migration changed another legacy unknown stock: %+v", other)
	}
	state, err := readState(path, "char-1", "PVP")
	if err != nil || state.Version != storeVersion || len(state.LocalUsed) != 0 || len(state.HandoverReceipts) != 0 {
		t.Fatalf("manual migration fabricated usage: %+v %v", state, err)
	}
	assertPureStockFile(t, rawStore(t, store))
}
