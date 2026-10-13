package localprogress

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

const resetCMS = "reset-cms"
const resetOther = "reset-other"

func marketResetSnapshot(stock int64) Snapshot {
	return Snapshot{CharUID: "char-1", GameMode: "PVP", Revision: "market-reset-live-revision", Items: []Item{
		{ItemID: resetCMS, MarketTracked: true, MarketRequiredQuantity: 3, MarketAvailableQuantity: stock,
			AvailableQuantity: stock, RequiredQuantity: 10, RemainingQuantity: max(0, 10-stock), Sources: []Source{
				{TrackedSource: source("market-reset-quest", 3, true), Origin: OriginMarket},
				{TrackedSource: source("dev-reset-extra", 7, false), Origin: OriginDev},
			}},
		{ItemID: resetOther, MarketTracked: true, MarketAvailableQuantity: 3, AvailableQuantity: 3,
			RequiredQuantity: 5, RemainingQuantity: 2,
			Sources: []Source{{TrackedSource: source("dev-other-quest", 5, false), Origin: OriginDev}}},
	}}
}

func marketResetState(t *testing.T, store *Store) storedProgress {
	t.Helper()
	path, err := store.path("char-1", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	state, err := readState(path, "char-1", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func addResetStock(t *testing.T, store *Store, id string, regular, fir int) {
	t.Helper()
	for kind, amount := range map[string]int{"regular": regular, "fir": fir} {
		for i := 0; i < amount; i++ {
			if _, err := store.AddKind("char-1", "PVP", id, kind); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestMarketResetZeroClearsOnlyMarketQualityAndPreservesLocalLedger(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Apply(marketResetSnapshot(2)); err != nil {
		t.Fatal(err)
	}
	addResetStock(t, store, resetCMS, 1, 2)
	addResetStock(t, store, resetOther, 2, 0)
	entries := []HandoverEntry{{ItemID: resetCMS, Regular: 1, FIR: 1}, {ItemID: resetOther, Regular: 1}}
	if _, err := store.RecordHandover("char-1", "PVP", "reset-shared-receipt", entries); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordMarketAdd("char-1", "PVP", resetCMS, "regular", 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordMarketAdd("char-1", "PVP", resetOther, "fir", 2, 3); err != nil {
		t.Fatal(err)
	}
	before := marketResetState(t, store)
	merged, err := NewStore(store.dir).Apply(marketResetSnapshot(0))
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, merged, resetCMS)
	if item.MarketAvailableQuantity != 0 || item.MarketRegularQuantity != 0 || item.MarketFIRQuantity != 0 ||
		item.LocalCollectedQuantity != 3 || item.LocalCollectedRegularQuantity != 1 || item.LocalCollectedFIRQuantity != 2 ||
		item.LocalUsedQuantity != 2 || item.LocalUsedRegularQuantity != 1 || item.LocalUsedFIRQuantity != 1 ||
		item.LocalAvailableQuantity != 1 || item.LocalRegularQuantity != 0 || item.LocalFIRQuantity != 1 || item.AvailableQuantity != 1 {
		t.Fatalf("Market0 reset affected dev stock, types or used ledger; expected 0+1=1: %+v", item)
	}
	other := requireItem(t, merged, resetOther)
	if other.MarketFIRQuantity != 1 || other.LocalCollectedQuantity != 2 || other.LocalUsedQuantity != 1 || other.AvailableQuantity != 4 {
		t.Fatalf("CMS Market reset changed another item: %+v", other)
	}
	after := marketResetState(t, store)
	if _, exists := after.MarketKinds[resetCMS]; exists {
		t.Fatal("zero Market stock retained its quality annotation")
	}
	delete(before.MarketKinds, resetCMS)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("reset changed more than this item's Market quality metadata: before=%+v after=%+v", before, after)
	}
	bytesAfter := rawStore(t, store)
	replayed, err := NewStore(store.dir).RecordHandover("char-1", "PVP", "reset-shared-receipt", entries)
	if err != nil || !bytes.Equal(bytesAfter, rawStore(t, store)) || requireItem(t, replayed, resetCMS).LocalUsedQuantity != 2 {
		t.Fatalf("unchanged receipt was lost or applied twice after Market0: %+v %v", replayed, err)
	}
	assertPureStockFile(t, rawStore(t, store), "market-reset-live-revision", "market-reset-quest", "dev-reset-extra")
}

func TestMarketResetMatchingZeroAnnotationIsDeletedWithoutClearingFullyUsedLocalStock(t *testing.T) {
	store := NewStore(t.TempDir())
	state := emptyState("char-1", "PVP")
	state.Stock[resetCMS], state.RegularStock[resetCMS] = 1, 1
	state.LocalUsed[resetCMS] = usedLocalStock{Regular: 1}
	state.HandoverReceipts["fully-used-receipt"] = []HandoverEntry{{ItemID: resetCMS, Regular: 1}}
	state.MarketKinds[resetCMS] = marketKindAnnotation{StockHash: marketStockHash(resetCMS, 0)}
	path, _ := store.path("char-1", "PVP")
	if err := store.commit(path, state); err != nil {
		t.Fatal(err)
	}
	input := marketResetSnapshot(0)
	input.Items = input.Items[:1]
	result, err := store.Apply(input)
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, result, resetCMS)
	if item.LocalCollectedQuantity != 1 || item.LocalUsedQuantity != 1 || item.LocalAvailableQuantity != 0 {
		t.Fatalf("matching Market0 annotation erased fully used local history: %+v", item)
	}
	after := marketResetState(t, store)
	if _, exists := after.MarketKinds[resetCMS]; exists {
		t.Fatal("matching zero hash retained an empty Market annotation")
	}
	delete(state.MarketKinds, resetCMS)
	if !reflect.DeepEqual(state, after) {
		t.Fatalf("Market0 changed local-only collection or receipts: before=%+v after=%+v", state, after)
	}
}

func TestMarketResetInitialStableAndDevOnlyZeroPreserveNewLocalCollection(t *testing.T) {
	for _, marketTracked := range []bool{false, true} {
		t.Run(map[bool]string{false: "dev-only", true: "market-zero"}[marketTracked], func(t *testing.T) {
			store := NewStore(t.TempDir())
			input := marketResetSnapshot(0)
			input.Items[0].MarketTracked = marketTracked
			if _, err := store.Apply(input); err != nil {
				t.Fatal(err)
			}
			addResetStock(t, store, resetCMS, 1, 1)
			before := rawStore(t, store)
			for range 3 {
				result, err := NewStore(store.dir).Apply(input)
				if err != nil {
					t.Fatal(err)
				}
				item := requireItem(t, result, resetCMS)
				if item.AvailableQuantity != 2 || item.LocalCollectedQuantity != 2 || item.LocalRegularQuantity != 1 || item.LocalFIRQuantity != 1 || !bytes.Equal(before, rawStore(t, store)) {
					t.Fatalf("stable zero erased or changed dev-only stock: %+v", item)
				}
			}
		})
	}
}

func TestMarketResetConfirmedHandInZeroDoesNotPersistAnEmptyAnnotation(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Apply(marketResetSnapshot(1)); err != nil {
		t.Fatal(err)
	}
	addResetStock(t, store, resetCMS, 2, 0)
	if _, err := store.RecordMarketAdd("char-1", "PVP", resetCMS, "fir", 0, 1); err != nil {
		t.Fatal(err)
	}
	removed, remaining := HandoverEntry{ItemID: resetCMS, FIR: 1}, HandoverEntry{ItemID: resetCMS}
	for range 2 {
		if _, err := NewStore(store.dir).RecordMarketRemoval("char-1", "PVP", removed, 1, 0, remaining); err != nil {
			t.Fatal(err)
		}
		state := marketResetState(t, store)
		if _, exists := state.MarketKinds[resetCMS]; exists {
			t.Fatal("confirmed/repeated Market0 removal persisted an empty annotation")
		}
		if state.Stock[resetCMS] != 2 || state.RegularStock[resetCMS] != 2 || len(state.LocalUsed) != 0 {
			t.Fatalf("confirmed Market removal modified local quantity: %+v", state)
		}
	}
	result, err := NewStore(store.dir).Apply(marketResetSnapshot(0))
	if err != nil || requireItem(t, result, resetCMS).AvailableQuantity != 2 {
		t.Fatalf("Market0 plus preserved Local2 was not Stock2: %+v %v", result, err)
	}
}

func TestMarketResetQualityWriteFailurePreservesFileAndLocalCounts(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Apply(marketResetSnapshot(3)); err != nil {
		t.Fatal(err)
	}
	addResetStock(t, store, resetCMS, 1, 0)
	if _, err := store.RecordMarketAdd("char-1", "PVP", resetCMS, "regular", 2, 3); err != nil {
		t.Fatal(err)
	}
	before := rawStore(t, store)
	injected := errors.New("market-quality-reset-disk-full")
	store.writeFile = func(string, []byte) error { return injected }
	if result, err := store.Apply(marketResetSnapshot(0)); !errors.Is(err, injected) || result.Items != nil || !bytes.Equal(before, rawStore(t, store)) {
		t.Fatalf("failed quality reset changed local file: %+v %v", result, err)
	}
	store.writeFile = atomicWrite
	result, err := store.Apply(marketResetSnapshot(0))
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, result, resetCMS)
	if item.AvailableQuantity != 1 || item.LocalRegularQuantity != 1 || item.MarketRegularQuantity != 0 {
		t.Fatalf("quality reset retry affected local stock instead of Market metadata: %+v", item)
	}
}
