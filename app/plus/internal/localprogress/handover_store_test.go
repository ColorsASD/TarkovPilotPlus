package localprogress

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func handoverStoreFixture(t *testing.T) *Store {
	t.Helper()
	store := NewStore(t.TempDir())
	state := emptyState("char-1", "PVP")
	state.Stock["dev-only"] = 5
	state.RegularStock["dev-only"], state.FIRStock["dev-only"] = 2, 1
	path, _ := store.path("char-1", "PVP")
	if err := store.commit(path, state); err != nil {
		t.Fatal(err)
	}
	return store
}

func requireCollectedStock(t *testing.T, snapshot Snapshot, collected, regular, fir, used, usedRegular, usedFIR int64) Item {
	t.Helper()
	item := requireItem(t, snapshot, "dev-only")
	if item.LocalCollectedQuantity != collected || item.LocalCollectedRegularQuantity != regular || item.LocalCollectedFIRQuantity != fir || item.LocalUsedQuantity != used || item.LocalUsedRegularQuantity != usedRegular || item.LocalUsedFIRQuantity != usedFIR || item.LocalAvailableQuantity != collected-used || item.LocalRegularQuantity != regular-usedRegular || item.LocalFIRQuantity != fir-usedFIR {
		t.Fatalf("collected/used/effective projection mismatch: %+v", item)
	}
	return item
}

func TestHandoverKeepsCollectedStockAndProjectsOnlyRemainingTypedStock(t *testing.T) {
	store := handoverStoreFixture(t)
	entry := HandoverEntry{ItemID: "dev-only", Regular: 1, FIR: 1, Unknown: 1}
	result, err := store.RecordHandover("char-1", "PVP", "transfer-1", []HandoverEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	item := requireCollectedStock(t, result, 5, 2, 1, 3, 1, 1)
	if item.AvailableQuantity != 2 || item.Name != "" || len(item.Sources) != 0 || item.RequiredQuantity != 0 || item.MarketAvailableQuantity != 0 {
		t.Fatalf("local handover returned live goal or Market counters: %+v", item)
	}
	path, _ := store.path("char-1", "PVP")
	state, err := readState(path, "char-1", "PVP")
	if err != nil || state.Stock["dev-only"] != 5 || state.RegularStock["dev-only"] != 2 || state.FIRStock["dev-only"] != 1 || state.LocalUsed["dev-only"] != (usedLocalStock{Regular: 1, FIR: 1, Unknown: 1}) || len(state.HandoverReceipts) != 1 {
		t.Fatalf("collected stock changed or ledger did not retain typed usage: %+v %v", state, err)
	}
	assertPureStockFile(t, rawStore(t, store))
	input := marketStockFixture("dev-only", 987654321)
	input.Items[0].Sources = append(input.Items[0].Sources, storeSnapshot().Items[0].Sources...)
	input.Items[0].Name = "MARKET-PRIVATE-ITEM-NAME"
	input.Items[0].SpentQuantity = 123456789
	merged, err := store.Apply(input)
	if err != nil {
		t.Fatal(err)
	}
	item = requireCollectedStock(t, merged, 5, 2, 1, 3, 1, 1)
	if item.MarketAvailableQuantity != 987654321 || item.AvailableQuantity != 987654323 {
		t.Fatalf("raw Market plus effective local stock was not projected: %+v", item)
	}
	assertPureStockFile(t, rawStore(t, store), "987654321", "123456789", "MARKET-PRIVATE-ITEM-NAME", "market-private-quest")
	for i := range input.Items[0].Sources {
		input.Items[0].Sources[i].Completed = true
		input.Items[0].Sources[i].RemainingRequiredQuantity = 0
	}
	merged, err = store.Apply(input)
	if err != nil {
		t.Fatal(err)
	}
	requireCollectedStock(t, merged, 5, 2, 1, 3, 1, 1)
	if merged.Items[0].RequiredQuantity != 0 {
		t.Fatal("quest completion did not close demand")
	}
}

func TestHandoverIdempotencySurvivesRestartAndCanonicalPayloadOrder(t *testing.T) {
	store := handoverStoreFixture(t)
	entries := []HandoverEntry{{ItemID: "dev-only", Regular: 1}, {ItemID: "dev-only", FIR: 1, Unknown: 1}}
	first, err := store.RecordHandover("char-1", "PVP", "transfer-1", entries)
	if err != nil {
		t.Fatal(err)
	}
	before := rawStore(t, store)
	restarted := NewStore(store.dir)
	repeated, err := restarted.RecordHandover("char-1", "PVP", "transfer-1", []HandoverEntry{{ItemID: "dev-only", Regular: 1, FIR: 1, Unknown: 1}})
	if err != nil || !reflect.DeepEqual(first, repeated) || !bytes.Equal(before, rawStore(t, restarted)) {
		t.Fatalf("same operation consumed twice or rewrote immutable receipt: %+v %v", repeated, err)
	}
	if result, err := restarted.RecordHandover("char-1", "PVP", "transfer-1", []HandoverEntry{{ItemID: "dev-only", Regular: 2}}); !errors.Is(err, ErrHandoverConflict) || result.Items != nil {
		t.Fatalf("same operation replaced its payload: %+v %v", result, err)
	}
	loaded, err := restarted.Load("char-1", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	requireCollectedStock(t, loaded, 5, 2, 1, 3, 1, 1)
	if _, err := restarted.Apply(storeSnapshot()); err != nil {
		t.Fatal(err)
	}
	added, err := restarted.AddKind("char-1", "PVP", "dev-only", "regular")
	if err != nil {
		t.Fatal(err)
	}
	item := requireCollectedStock(t, added, 6, 3, 1, 3, 1, 1)
	if item.AvailableQuantity != 3 {
		t.Fatal("new collection reused a historical handover")
	}
}

func TestHandoverUsesExactTypeAndDoesNotConvertFIRToRegular(t *testing.T) {
	store := handoverStoreFixture(t)
	for _, entry := range []HandoverEntry{
		{ItemID: "dev-only", Regular: 3},
		{ItemID: "dev-only", FIR: 2},
		{ItemID: "dev-only", Unknown: 3},
	} {
		before := rawStore(t, store)
		if result, err := store.RecordHandover("char-1", "PVP", "too-many", []HandoverEntry{entry}); !errors.Is(err, ErrHandoverUnavailable) || result.Items != nil || !bytes.Equal(before, rawStore(t, store)) {
			t.Fatalf("wrong quality supplied requested kind: %+v %v", result, err)
		}
	}
	if _, err := store.RecordHandover("char-1", "PVP", "all-regular", []HandoverEntry{{ItemID: "dev-only", Regular: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordHandover("char-1", "PVP", "regular-again", []HandoverEntry{{ItemID: "dev-only", Regular: 1}}); !errors.Is(err, ErrHandoverUnavailable) {
		t.Fatalf("remaining FiR/unknown replaced consumed Regular: %v", err)
	}
	result, err := store.RecordHandover("char-1", "PVP", "all-fir", []HandoverEntry{{ItemID: "dev-only", FIR: 1}})
	if err != nil {
		t.Fatal(err)
	}
	requireCollectedStock(t, result, 5, 2, 1, 3, 2, 1)
}

func TestHandoverEmptyReceiptAndCharacterIsolation(t *testing.T) {
	store := handoverStoreFixture(t)
	baseline, err := store.RecordHandover("char-1", "PVP", "empty-transfer", nil)
	if err != nil {
		t.Fatal(err)
	}
	requireCollectedStock(t, baseline, 5, 2, 1, 0, 0, 0)
	if _, err := store.RecordHandover("char-1", "PVP", "empty-transfer", []HandoverEntry{{ItemID: "dev-only", Regular: 1}}); !errors.Is(err, ErrHandoverConflict) {
		t.Fatalf("historical empty operation became new consumption: %v", err)
	}
	if _, err := store.RecordHandover("char-2", "PVP", "isolated", []HandoverEntry{{ItemID: "dev-only", Regular: 1}}); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("other character consumed this stock: %v", err)
	}
	if _, err := store.RecordHandover("char-1", "PVE", "isolated", []HandoverEntry{{ItemID: "dev-only", Regular: 1}}); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("other game mode consumed this stock: %v", err)
	}
}

func TestHandoverBatchFailureAndDiskFailureNeverPublishUsage(t *testing.T) {
	store := handoverStoreFixture(t)
	before := rawStore(t, store)
	if result, err := store.RecordHandover("char-1", "PVP", "batch", []HandoverEntry{{ItemID: "dev-only", Regular: 1}, {ItemID: "missing", Regular: 1}}); !errors.Is(err, ErrItemNotFound) || result.Items != nil || !bytes.Equal(before, rawStore(t, store)) {
		t.Fatalf("part of failed batch became visible: %+v %v", result, err)
	}
	injected := errors.New("handover disk failure")
	store.writeFile = func(string, []byte) error { return injected }
	if result, err := store.RecordHandover("char-1", "PVP", "disk-operation", []HandoverEntry{{ItemID: "dev-only", Regular: 1}}); !errors.Is(err, injected) || result.Items != nil || !bytes.Equal(before, rawStore(t, store)) {
		t.Fatalf("failed receipt became visible: %+v %v", result, err)
	}
	loaded, err := NewStore(store.dir).Load("char-1", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	requireCollectedStock(t, loaded, 5, 2, 1, 0, 0, 0)
	store.writeFile = atomicWrite
	result, err := store.RecordHandover("char-1", "PVP", "disk-operation", []HandoverEntry{{ItemID: "dev-only", Regular: 1}})
	if err != nil {
		t.Fatal(err)
	}
	requireCollectedStock(t, result, 5, 2, 1, 1, 1, 0)
}

func TestHandoverV2AndV3MigrationKeepTotalsQualityAndInitializeEmptyLedger(t *testing.T) {
	for _, version := range []int{2, 3} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			store := NewStore(t.TempDir())
			path, _ := store.path("char-1", "PVP")
			var legacy any = legacyStockProgress{Version: 2, CharUID: "char-1", GameMode: "PVP", Stock: map[string]int64{"dev-only": 5}}
			regular, fir := int64(0), int64(0)
			if version == 3 {
				regular, fir = 2, 1
				legacy = legacyTypedProgress{Version: 3, CharUID: "char-1", GameMode: "PVP", Stock: map[string]int64{"dev-only": 5}, RegularStock: map[string]int64{"dev-only": regular}, FIRStock: map[string]int64{"dev-only": fir}, MarketKinds: map[string]marketKindAnnotation{"remote-item": {FIR: 1, StockHash: marketStockHash("remote-item", 987654321)}}}
			}
			data, _ := json.Marshal(legacy)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load("char-1", "PVP")
			if err != nil {
				t.Fatal(err)
			}
			requireCollectedStock(t, loaded, 5, regular, fir, 0, 0, 0)
			if _, err := store.RecordHandover("char-1", "PVP", "legacy-transfer", []HandoverEntry{{ItemID: "dev-only", Unknown: 2}}); err != nil {
				t.Fatal(err)
			}
			state, err := readState(path, "char-1", "PVP")
			if err != nil || state.Version != storeVersion || state.Stock["dev-only"] != 5 || state.RegularStock["dev-only"] != regular || state.FIRStock["dev-only"] != fir || len(state.HandoverReceipts) != 1 || state.TransferReceipts == nil {
				t.Fatalf("migration lost collection or typed history: %+v %v", state, err)
			}
			if version == 3 && state.MarketKinds["remote-item"].FIR != 1 {
				t.Fatal("migration lost Market quality annotation")
			}
			assertPureStockFile(t, rawStore(t, store), "987654321")
			loaded, err = NewStore(store.dir).Load("char-1", "PVP")
			if err != nil {
				t.Fatal(err)
			}
			requireCollectedStock(t, loaded, 5, regular, fir, 2, 0, 0)
		})
	}
}

func TestCorruptHandoverLedgerCannotBeSilentlyOverwritten(t *testing.T) {
	for _, mutation := range []string{"missing-used", "missing-receipts", "used-mismatch", "receipt-mismatch", "orphan-item", "negative", "over-regular", "over-unknown", "nil-entries", "duplicate-entry", "bad-operation"} {
		t.Run(mutation, func(t *testing.T) {
			store := handoverStoreFixture(t)
			if _, err := store.RecordHandover("char-1", "PVP", "valid-operation", []HandoverEntry{{ItemID: "dev-only", Regular: 1}}); err != nil {
				t.Fatal(err)
			}
			path, _ := store.path("char-1", "PVP")
			state, _ := readState(path, "char-1", "PVP")
			switch mutation {
			case "missing-used":
				state.LocalUsed = nil
			case "missing-receipts":
				state.HandoverReceipts = nil
			case "used-mismatch":
				state.LocalUsed["dev-only"] = usedLocalStock{Regular: 2}
			case "receipt-mismatch":
				state.HandoverReceipts["valid-operation"] = []HandoverEntry{{ItemID: "dev-only", FIR: 1}}
			case "orphan-item":
				state.LocalUsed["orphan"] = usedLocalStock{Regular: 1}
				state.HandoverReceipts["orphan-operation"] = []HandoverEntry{{ItemID: "orphan", Regular: 1}}
			case "negative":
				state.HandoverReceipts["valid-operation"][0].Regular = -1
			case "over-regular":
				state.LocalUsed["dev-only"] = usedLocalStock{Regular: 3}
				state.HandoverReceipts["valid-operation"][0].Regular = 3
			case "over-unknown":
				state.LocalUsed["dev-only"] = usedLocalStock{Unknown: 3}
				state.HandoverReceipts["valid-operation"] = []HandoverEntry{{ItemID: "dev-only", Unknown: 3}}
			case "nil-entries":
				state.HandoverReceipts["valid-operation"] = nil
			case "duplicate-entry":
				state.HandoverReceipts["valid-operation"] = append(state.HandoverReceipts["valid-operation"], HandoverEntry{ItemID: "dev-only", Regular: 1})
				state.LocalUsed["dev-only"] = usedLocalStock{Regular: 2}
			case "bad-operation":
				state.HandoverReceipts["\x00bad"] = []HandoverEntry{}
			}
			data, _ := json.Marshal(state)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			for _, action := range []func() error{
				func() error { _, err := store.Load("char-1", "PVP"); return err },
				func() error { _, err := store.Apply(storeSnapshot()); return err },
				func() error { _, err := store.RecordHandover("char-1", "PVP", "new-operation", nil); return err },
				func() error {
					_, err := store.RecordMarketRemoval("char-1", "PVP", HandoverEntry{ItemID: "remote-item", Unknown: 1}, 3, 2)
					return err
				},
			} {
				if !errors.Is(action(), ErrCorruptStore) {
					t.Fatal("corrupt usage ledger accepted a read or write")
				}
			}
			if !bytes.Equal(data, rawStore(t, store)) {
				t.Fatal("corrupt ledger was overwritten")
			}
		})
	}
}

func TestConfirmedMarketRemovalUpdatesOnlyMatchingQualityAndDoesNotDebitLocalStock(t *testing.T) {
	store := handoverStoreFixture(t)
	if _, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "regular", 2, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "fir", 3, 4); err != nil {
		t.Fatal(err)
	}
	removed := HandoverEntry{ItemID: "remote-item", Regular: 1, Unknown: 1}
	result, err := store.RecordMarketRemoval("char-1", "PVP", removed, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	requireCollectedStock(t, result, 5, 2, 1, 0, 0, 0)
	remote := requireItem(t, result, "remote-item")
	if remote.MarketRegularQuantity != 0 || remote.MarketFIRQuantity != 1 || remote.MarketAvailableQuantity != 0 || remote.AvailableQuantity != 0 {
		t.Fatalf("confirmed removal invented remote/local counters or consumed wrong type: %+v", remote)
	}
	before := rawStore(t, store)
	if _, err := NewStore(store.dir).RecordMarketRemoval("char-1", "PVP", removed, 4, 2); err != nil || !bytes.Equal(before, rawStore(t, store)) {
		t.Fatalf("annotation confirmation was not repeatable: %v", err)
	}
	merged, err := store.Apply(marketStockFixture("remote-item", 2))
	if err != nil || requireItem(t, merged, "remote-item").MarketFIRQuantity != 1 {
		t.Fatalf("remaining quality was lost on matching live stock: %+v %v", merged, err)
	}
	path, _ := store.path("char-1", "PVP")
	state, _ := readState(path, "char-1", "PVP")
	if len(state.HandoverReceipts) != 0 || len(state.LocalUsed) != 0 || state.Stock["dev-only"] != 5 || state.Stock["remote-item"] != 0 {
		t.Fatalf("Market confirmation became own consumption or collected remote count: %+v", state)
	}
	assertPureStockFile(t, rawStore(t, store), "market-private-quest")
}

func TestMarketRemovalTypeValidationRebaseAndDiskFailure(t *testing.T) {
	store := handoverStoreFixture(t)
	if _, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "fir", 0, 1); err != nil {
		t.Fatal(err)
	}
	before := rawStore(t, store)
	if _, err := store.RecordMarketRemoval("char-1", "PVP", HandoverEntry{ItemID: "remote-item", Regular: 1}, 1, 0); !errors.Is(err, ErrHandoverUnavailable) || !bytes.Equal(before, rawStore(t, store)) {
		t.Fatalf("known FiR was relabeled as Regular removal: %v", err)
	}
	injected := errors.New("Market removal metadata disk failure")
	store.writeFile = func(string, []byte) error { return injected }
	if result, err := store.RecordMarketRemoval("char-1", "PVP", HandoverEntry{ItemID: "remote-item", FIR: 1}, 1, 0); !errors.Is(err, injected) || result.Items != nil || !bytes.Equal(before, rawStore(t, store)) {
		t.Fatalf("failed annotation write was visible: %+v %v", result, err)
	}
	store.writeFile = atomicWrite
	result, err := store.RecordMarketRemoval("char-1", "PVP", HandoverEntry{ItemID: "remote-item", Unknown: 1}, 5, 4)
	if err != nil {
		t.Fatal(err)
	}
	remote := requireItem(t, result, "remote-item")
	if remote.MarketRegularQuantity != 0 || remote.MarketFIRQuantity != 0 {
		t.Fatalf("external stock change retained unproven quality: %+v", remote)
	}
}

func TestConfirmedMarketRemovalRestoresJournalQualityAfterIntermediateRefresh(t *testing.T) {
	for _, downgradeAfterHash := range []bool{false, true} {
		name := "missing annotation after refresh"
		if downgradeAfterHash {
			name = "zero annotation already matches after hash"
		}
		t.Run(name, func(t *testing.T) {
			store := handoverStoreFixture(t)
			if _, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "regular", 2, 3); err != nil {
				t.Fatal(err)
			}
			if _, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "fir", 3, 4); err != nil {
				t.Fatal(err)
			}
			removed := HandoverEntry{ItemID: "remote-item", Regular: 1, Unknown: 1}
			remaining := HandoverEntry{ItemID: "remote-item", FIR: 1, Unknown: 1}
			// Az API-módosítás sikerült, de a közvetlen visszaolvasás megszakadt.
			// A friss élő mennyiségre az Apply törli a régi fingerprint jelölését.
			refreshed, err := store.Apply(marketStockFixture("remote-item", 2))
			if err != nil {
				t.Fatal(err)
			}
			remote := requireItem(t, refreshed, "remote-item")
			if remote.MarketRegularQuantity != 0 || remote.MarketFIRQuantity != 0 {
				t.Fatalf("unconfirmed old quality survived raw count change: %+v", remote)
			}
			if downgradeAfterHash {
				if _, err := store.RecordMarketRemoval("char-1", "PVP", removed, 4, 2); err != nil {
					t.Fatal(err)
				}
			}
			// Újraindítás után a journal már megerősített célértéke helyreállítja
			// az eredeti saját FiR-jelölést; nem hoz létre helyi darabot.
			restarted := NewStore(store.dir)
			result, err := restarted.RecordMarketRemoval("char-1", "PVP", removed, 4, 2, remaining)
			if err != nil {
				t.Fatal(err)
			}
			requireCollectedStock(t, result, 5, 2, 1, 0, 0, 0)
			remote = requireItem(t, result, "remote-item")
			if remote.MarketRegularQuantity != 0 || remote.MarketFIRQuantity != 1 || remote.MarketAvailableQuantity != 0 || remote.AvailableQuantity != 0 {
				t.Fatalf("journal failed to restore only confirmed quality: %+v", remote)
			}
			beforeRepeat := rawStore(t, restarted)
			restarted.writeFile = func(string, []byte) error { return errors.New("repeat must not write") }
			if _, err := restarted.RecordMarketRemoval("char-1", "PVP", removed, 4, 2, remaining); err != nil || !bytes.Equal(beforeRepeat, rawStore(t, restarted)) {
				t.Fatalf("restored annotation repetition wrote or changed state: %v", err)
			}
			restarted.writeFile = atomicWrite
			merged, err := restarted.Apply(marketStockFixture("remote-item", 2))
			if err != nil || requireItem(t, merged, "remote-item").MarketFIRQuantity != 1 {
				t.Fatalf("restored quality did not survive matching refresh: %+v %v", merged, err)
			}
			path, _ := restarted.path("char-1", "PVP")
			state, err := readState(path, "char-1", "PVP")
			if err != nil || state.MarketKinds["remote-item"] != (marketKindAnnotation{FIR: 1, StockHash: marketStockHash("remote-item", 2)}) || len(state.LocalUsed) != 0 || len(state.HandoverReceipts) != 0 || state.Stock["remote-item"] != 0 {
				t.Fatalf("journal confirmation persisted remote stock or own consumption: %+v %v", state, err)
			}
			assertPureStockFile(t, rawStore(t, restarted), "market-private-quest", "private-market-revision")
			merged, err = restarted.Apply(marketStockFixture("remote-item", 3))
			if err != nil {
				t.Fatal(err)
			}
			remote = requireItem(t, merged, "remote-item")
			if remote.MarketRegularQuantity != 0 || remote.MarketFIRQuantity != 0 {
				t.Fatalf("later external count change retained restored quality: %+v", remote)
			}
		})
	}
}

func TestMarketRemovalJournalValidationAndFailedRestorationAreAtomic(t *testing.T) {
	store := handoverStoreFixture(t)
	if _, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "regular", 2, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "fir", 3, 4); err != nil {
		t.Fatal(err)
	}
	removed := HandoverEntry{ItemID: "remote-item", Regular: 1, Unknown: 1}
	remaining := HandoverEntry{ItemID: "remote-item", FIR: 1, Unknown: 1}
	before := rawStore(t, store)
	for _, tt := range []struct {
		name      string
		remaining []HandoverEntry
		want      error
	}{
		{name: "wrong item", remaining: []HandoverEntry{{ItemID: "other-item", FIR: 1, Unknown: 1}}, want: ErrInvalidIdentity},
		{name: "wrong total", remaining: []HandoverEntry{{ItemID: "remote-item", FIR: 1}}, want: ErrInvalidQuantity},
		{name: "negative type", remaining: []HandoverEntry{{ItemID: "remote-item", Regular: -1, FIR: 1, Unknown: 2}}, want: ErrInvalidQuantity},
		{name: "typed overflow", remaining: []HandoverEntry{{ItemID: "remote-item", Regular: MaxQuantity, FIR: 1}}, want: ErrInvalidQuantity},
		{name: "multiple proof entries", remaining: []HandoverEntry{remaining, remaining}, want: ErrInvalidQuantity},
		{name: "contradicts still matching prewrite quality", remaining: []HandoverEntry{{ItemID: "remote-item", Regular: 1, Unknown: 1}}, want: ErrHandoverUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := store.RecordMarketRemoval("char-1", "PVP", removed, 4, 2, tt.remaining...); !errors.Is(err, tt.want) || !bytes.Equal(before, rawStore(t, store)) {
				t.Fatalf("invalid remaining proof changed annotations: %v", err)
			}
		})
	}
	if _, err := store.RecordMarketRemoval("char-1", "PVP", removed, 4, 2, remaining); err != nil {
		t.Fatalf("matching prewrite proof was rejected: %v", err)
	}
	if _, err := store.Apply(marketStockFixture("remote-item", 3)); err != nil {
		t.Fatal(err)
	}
	before = rawStore(t, store)
	injected := errors.New("restored quality disk failure")
	store.writeFile = func(string, []byte) error { return injected }
	if result, err := store.RecordMarketRemoval("char-1", "PVP", removed, 4, 2, remaining); !errors.Is(err, injected) || result.Items != nil || !bytes.Equal(before, rawStore(t, store)) {
		t.Fatalf("failed restoration changed visible or persisted quality: %+v %v", result, err)
	}
}

func TestHandoverQuantityIdentityLimitsAndPayloadIsolation(t *testing.T) {
	store := handoverStoreFixture(t)
	before := rawStore(t, store)
	for _, operation := range []string{"", " padded", "line\nfeed", strings.Repeat("x", 513), string([]byte{255})} {
		if _, err := store.RecordHandover("char-1", "PVP", operation, nil); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("invalid operation ID accepted: %q %v", operation, err)
		}
	}
	for _, entry := range []HandoverEntry{{ItemID: "dev-only", Regular: -1}, {ItemID: "dev-only", FIR: MaxQuantity + 1}, {ItemID: "dev-only", Regular: MaxQuantity, Unknown: 1}} {
		if _, err := store.RecordHandover("char-1", "PVP", "invalid-quantity", []HandoverEntry{entry}); !errors.Is(err, ErrInvalidQuantity) {
			t.Fatalf("invalid handover quantity accepted: %+v %v", entry, err)
		}
	}
	if _, err := store.RecordHandover("char-1", "PVP", "invalid-item", []HandoverEntry{{ItemID: "../escape", Regular: 1}}); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("invalid item accepted: %v", err)
	}
	if !bytes.Equal(before, rawStore(t, store)) {
		t.Fatal("invalid input rewrote stock")
	}
	entries := []HandoverEntry{{ItemID: "dev-only", Regular: 1}}
	result, err := store.RecordHandover("char-1", "PVP", "isolated-payload", entries)
	if err != nil {
		t.Fatal(err)
	}
	entries[0].Regular = 999
	result.Items[0].LocalUsedQuantity = 999
	loaded, err := store.Load("char-1", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	requireCollectedStock(t, loaded, 5, 2, 1, 1, 1, 0)
}
