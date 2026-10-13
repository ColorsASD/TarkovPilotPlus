package localprogress

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func backupStockFixture(t *testing.T) []byte {
	t.Helper()
	state := emptyState("backup-character", "SEASON")
	state.Stock["item-a"] = 7
	state.RegularStock["item-a"], state.FIRStock["item-a"] = 3, 2
	state.MarketKinds["item-a"] = marketKindAnnotation{Regular: 4, FIR: 1, StockHash: marketStockHash("item-a", 8)}
	state.LocalUsed["item-a"] = usedLocalStock{Regular: 1, FIR: 1, Unknown: 1}
	state.HandoverReceipts["backup-receipt"] = []HandoverEntry{{ItemID: "item-a", Regular: 1, FIR: 1, Unknown: 1}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func modifyBackupStockFixture(t *testing.T, data []byte, edit func(*storedProgress)) []byte {
	t.Helper()
	var state storedProgress
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	edit(&state)
	result, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBackupStockValidationPreservesIdentityTypedStockAndReceiptsWithoutMarketCount(t *testing.T) {
	data := backupStockFixture(t)
	before := bytes.Clone(data)
	snapshot, err := ValidateBackupStock(data)
	if err != nil || snapshot.CharUID != "backup-character" || snapshot.GameMode != "SEASON" || snapshot.Revision != "local" || len(snapshot.Items) != 1 {
		t.Fatalf("valid current backup rejected or identity changed: %+v %v", snapshot, err)
	}
	item := snapshot.Items[0]
	if item.ItemID != "item-a" || item.LocalCollectedQuantity != 7 || item.LocalAvailableQuantity != 4 || item.LocalRegularQuantity != 2 || item.LocalFIRQuantity != 1 ||
		item.LocalUsedQuantity != 3 || item.LocalUsedRegularQuantity != 1 || item.LocalUsedFIRQuantity != 1 || item.AvailableQuantity != 4 ||
		item.MarketAvailableQuantity != 0 || item.MarketRegularQuantity != 4 || item.MarketFIRQuantity != 1 || item.MarketTracked || len(item.Sources) != 0 {
		t.Fatalf("backup fabricated Market stock or lost local history/types: %+v", item)
	}
	if !bytes.Equal(data, before) {
		t.Fatal("validation mutated the input backup")
	}
}

func TestBackupStockValidationAcceptsEmptyCurrentProfilesAndAnnotationOnlyStock(t *testing.T) {
	for _, mode := range []string{"PVP", "PVE", "SEASON"} {
		state := emptyState("backup-character", mode)
		data, _ := json.Marshal(state)
		snapshot, err := ValidateBackupStock(data)
		if err != nil || snapshot.Items == nil || len(snapshot.Items) != 0 || snapshot.GameMode != mode {
			t.Fatalf("empty profile rejected: %+v %v", snapshot, err)
		}
		state.MarketKinds["item-a"] = marketKindAnnotation{FIR: 2, StockHash: marketStockHash("item-a", 3)}
		data, _ = json.Marshal(state)
		snapshot, err = ValidateBackupStock(data)
		if err != nil || len(snapshot.Items) != 1 || snapshot.Items[0].AvailableQuantity != 0 || snapshot.Items[0].MarketAvailableQuantity != 0 || snapshot.Items[0].MarketFIRQuantity != 2 {
			t.Fatalf("annotation-only profile invented a remote quantity: %+v %v", snapshot, err)
		}
	}
}

func TestBackupStockValidationRejectsMalformedOrAmbiguousJSON(t *testing.T) {
	base := backupStockFixture(t)
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"array-root", []byte(`[]`)},
		{"trailing", append(bytes.Clone(base), []byte(` {}`)...)},
		{"unknown-key", bytes.Replace(base, []byte(`"version":5`), []byte(`"version":5,"apiKey":"forbidden"`), 1)},
		{"duplicate-key", bytes.Replace(base, []byte(`"version":5`), []byte(`"version":5,"version":5`), 1)},
		{"duplicate-case-alias", bytes.Replace(base, []byte(`"version":5`), []byte(`"version":5,"Version":5`), 1)},
		{"case-alias", bytes.Replace(base, []byte(`"version":5`), []byte(`"Version":5`), 1)},
		{"duplicate-stock-id", bytes.Replace(base, []byte(`"stock":{"item-a":7}`), []byte(`"stock":{"item-a":7,"item-a":8}`), 1)},
		{"duplicate-annotation-kind", bytes.Replace(base, []byte(`"regular":4`), []byte(`"regular":4,"regular":5`), 1)},
		{"annotation-case-alias", bytes.Replace(base, []byte(`"regular":4`), []byte(`"Regular":4`), 1)},
		{"receipt-case-alias", bytes.Replace(base, []byte(`"itemId":"item-a"`), []byte(`"ItemId":"item-a"`), 1)},
		{"null-stock-count", bytes.Replace(base, []byte(`"stock":{"item-a":7}`), []byte(`"stock":{"item-a":null}`), 1)},
		{"float-stock-count", bytes.Replace(base, []byte(`"stock":{"item-a":7}`), []byte(`"stock":{"item-a":7.0}`), 1)},
		{"null-annotation", bytes.Replace(base, []byte(`"marketKinds":{"item-a":{`), []byte(`"marketKinds":{"item-a":null,"unexpected":{`), 1)},
		{"null-used-entry", bytes.Replace(base, []byte(`"localUsed":{"item-a":{"regular":1,"fir":1,"unknown":1}}`), []byte(`"localUsed":{"item-a":null}`), 1)},
		{"null-receipt-array", bytes.Replace(base, []byte(`"backup-receipt":[{"itemId":"item-a","regular":1,"fir":1,"unknown":1}]`), []byte(`"backup-receipt":null`), 1)},
		{"null-receipt-entry", bytes.Replace(base, []byte(`"backup-receipt":[{"itemId":"item-a","regular":1,"fir":1,"unknown":1}]`), []byte(`"backup-receipt":[null]`), 1)},
		{"invalid-utf8", append(bytes.Clone(base), byte(0xff))},
	} {
		t.Run(test.name, func(t *testing.T) {
			if snapshot, err := ValidateBackupStock(test.data); !errors.Is(err, ErrCorruptStore) || snapshot.Items != nil {
				t.Fatalf("malformed backup accepted: %+v %v", snapshot, err)
			}
		})
	}
}

func TestBackupStockValidationRequiresAllFieldsAndExplicitNumericSubfields(t *testing.T) {
	base := backupStockFixture(t)
	var original map[string]json.RawMessage
	if err := json.Unmarshal(base, &original); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"version", "charUid", "gameMode", "stock", "regularStock", "firStock", "marketKinds", "localUsed", "handoverReceipts"} {
		for _, missing := range []bool{false, true} {
			fields := make(map[string]json.RawMessage, len(original))
			for key, value := range original {
				fields[key] = value
			}
			if missing {
				delete(fields, field)
			} else {
				fields[field] = json.RawMessage(`null`)
			}
			data, _ := json.Marshal(fields)
			if _, err := ValidateBackupStock(data); !errors.Is(err, ErrCorruptStore) {
				t.Fatalf("missing/null root field accepted: %s missing=%v: %v", field, missing, err)
			}
		}
	}
	for _, path := range []string{"marketKinds", "localUsed", "handoverReceipts"} {
		keys := []string{"regular", "fir", "unknown"}
		if path == "marketKinds" {
			keys = []string{"regular", "fir", "stockHash"}
		}
		for _, key := range keys {
			for _, missing := range []bool{false, true} {
				fields := make(map[string]json.RawMessage, len(original))
				for rootKey, value := range original {
					fields[rootKey] = value
				}
				var entries map[string]json.RawMessage
				_ = json.Unmarshal(fields[path], &entries)
				entryKey := "item-a"
				var entry map[string]json.RawMessage
				if path == "handoverReceipts" {
					entryKey = "backup-receipt"
					var array []json.RawMessage
					_ = json.Unmarshal(entries[entryKey], &array)
					_ = json.Unmarshal(array[0], &entry)
				} else {
					_ = json.Unmarshal(entries[entryKey], &entry)
				}
				if missing {
					delete(entry, key)
				} else {
					entry[key] = json.RawMessage(`null`)
				}
				raw, _ := json.Marshal(entry)
				if path == "handoverReceipts" {
					raw, _ = json.Marshal([]json.RawMessage{raw})
				}
				entries[entryKey] = raw
				fields[path], _ = json.Marshal(entries)
				data, _ := json.Marshal(fields)
				if _, err := ValidateBackupStock(data); !errors.Is(err, ErrCorruptStore) {
					t.Fatalf("missing/null nested field accepted: %s.%s missing=%v: %v", path, key, missing, err)
				}
			}
		}
	}
}

func TestBackupStockValidationEnforcesCurrentIdentityQuantitiesAndReceiptInvariants(t *testing.T) {
	base := backupStockFixture(t)
	for _, test := range []struct {
		name string
		edit func(*storedProgress)
	}{
		{"legacy-v1", func(state *storedProgress) { state.Version = 1 }},
		{"legacy-v2", func(state *storedProgress) { state.Version = 2 }},
		{"legacy-v3", func(state *storedProgress) { state.Version = 3 }},
		{"future-version", func(state *storedProgress) { state.Version = 6 }},
		{"wrong-mode", func(state *storedProgress) { state.GameMode = "pvp" }},
		{"invalid-owner", func(state *storedProgress) { state.CharUID = "../other" }},
		{"negative-stock", func(state *storedProgress) { state.Stock["item-a"] = -1 }},
		{"over-limit-stock", func(state *storedProgress) { state.Stock["item-a"] = MaxQuantity + 1 }},
		{"typed-over-stock", func(state *storedProgress) { state.RegularStock["item-a"] = 6 }},
		{"orphan-typed", func(state *storedProgress) { state.RegularStock["item-b"] = 1 }},
		{"negative-typed", func(state *storedProgress) { state.FIRStock["item-a"] = -1 }},
		{"invalid-hash", func(state *storedProgress) {
			state.MarketKinds["item-a"] = marketKindAnnotation{FIR: 1, StockHash: "bad"}
		}},
		{"annotation-over-limit", func(state *storedProgress) {
			state.MarketKinds["item-a"] = marketKindAnnotation{Regular: MaxQuantity, FIR: 1, StockHash: marketStockHash("item-a", 1)}
		}},
		{"used-without-receipt", func(state *storedProgress) { state.HandoverReceipts = map[string][]HandoverEntry{} }},
		{"receipt-mismatch", func(state *storedProgress) { state.HandoverReceipts["backup-receipt"][0].Regular = 2 }},
		{"used-exceeds-collected", func(state *storedProgress) {
			state.LocalUsed["item-a"] = usedLocalStock{Regular: 4, FIR: 1, Unknown: 1}
			state.HandoverReceipts["backup-receipt"][0].Regular = 4
		}},
		{"duplicate-receipt-item", func(state *storedProgress) {
			state.HandoverReceipts["backup-receipt"] = append(state.HandoverReceipts["backup-receipt"], state.HandoverReceipts["backup-receipt"][0])
		}},
		{"orphan-used", func(state *storedProgress) { state.LocalUsed["item-b"] = usedLocalStock{Regular: 1} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := modifyBackupStockFixture(t, base, test.edit)
			if _, err := ValidateBackupStock(data); !errors.Is(err, ErrCorruptStore) {
				t.Fatalf("invalid state accepted: %v", err)
			}
		})
	}
}

func TestBackupStockValidationHonorsExactEightMiBFileLimit(t *testing.T) {
	data := backupStockFixture(t)
	data = append(data, bytes.Repeat([]byte(" "), int(maxStoreBytes)-len(data))...)
	if _, err := ValidateBackupStock(data); err != nil {
		t.Fatalf("valid backup at the file limit rejected: %v", err)
	}
	if _, err := ValidateBackupStock(append(data, ' ')); !errors.Is(err, ErrStoreTooLarge) {
		t.Fatalf("oversized backup was not rejected before parsing: %v", err)
	}
}
