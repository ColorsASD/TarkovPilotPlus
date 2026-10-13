package plus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
)

func stockUsageFixture(t *testing.T) (*ItemWindowApp, *handoverTransaction) {
	t.Helper()
	a := &ItemWindowApp{dir: t.TempDir()}
	tx := &handoverTransaction{Version: 1, ID: "0123456789abcdef0123456789abcdef", CharUID: "char-1", Mode: "PVP", LocalDone: true,
		Items: []handoverOperation{{ItemID: "item-1", Stage: "confirmed", Target: 42, Revision: "private-revision-never-persisted",
			Market: localprogress.HandoverEntry{ItemID: "item-1", Regular: 1, FIR: 2, Unknown: 1}, RegularToMarket: 2, FIRToMarket: 2}}}
	return a, tx
}

func stockUsageBytes(t *testing.T, a *ItemWindowApp, tx *handoverTransaction) []byte {
	t.Helper()
	path, _, err := a.stockUsagePath(tx.CharUID, tx.Mode)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestStockUsagePartialHandInIsDurableAndReplayIsIdempotent(t *testing.T) {
	a, tx := stockUsageFixture(t)
	tx.Items = append(tx.Items, handoverOperation{ItemID: "item-2", Stage: "prepared",
		Market: localprogress.HandoverEntry{ItemID: "item-2", Regular: 2}, RegularToMarket: 2})
	if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); err != nil {
		t.Fatal(err)
	}
	before := stockUsageBytes(t, a, tx)
	if err := a.recordStockEditorMarketUsage(tx, &tx.Items[1]); !errors.Is(err, errInvalidStockUsage) || !bytes.Equal(before, stockUsageBytes(t, a, tx)) {
		t.Fatalf("unfinished second item changed receipt history: %v", err)
	}
	tx.Items[1].Stage = "confirmed"
	if err := a.recordStockEditorMarketUsage(tx, &tx.Items[1]); err != nil {
		t.Fatal(err)
	}
	restarted := &ItemWindowApp{dir: a.dir}
	usage, err := restarted.loadStockEditorMarketUsage(ItemOptions{CharacterUID: "char-1", Mode: "pvp"})
	want := map[string]localprogress.StockAmounts{"item-1": {Regular: 1, FIR: 2, Unknown: 1}, "item-2": {Regular: 2}}
	if err != nil || !reflect.DeepEqual(usage, want) {
		t.Fatalf("partial/completed Market receipts were not restored: %v %v", usage, err)
	}
	before = stockUsageBytes(t, a, tx)
	for i := range tx.Items {
		tx.Items[i].Stage = "done"
		if err := restarted.recordStockEditorMarketUsage(tx, &tx.Items[i]); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(before, stockUsageBytes(t, a, tx)) {
		t.Fatal("done replay rewrote immutable usage")
	}
	tx.ID = "abcdef0123456789abcdef0123456789"
	tx.Items = tx.Items[:1]
	if err := restarted.recordStockEditorMarketUsage(tx, &tx.Items[0]); err != nil {
		t.Fatal(err)
	}
	usage, err = restarted.loadStockEditorMarketUsage(ItemOptions{CharacterUID: "char-1", Mode: "PVP"})
	if err != nil || usage["item-1"] != (localprogress.StockAmounts{Regular: 2, FIR: 4, Unknown: 2}) {
		t.Fatalf("distinct operation did not aggregate once: %v %v", usage, err)
	}
}

func TestStockUsageConflictingReceiptNeverOverwritesHistory(t *testing.T) {
	a, tx := stockUsageFixture(t)
	if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); err != nil {
		t.Fatal(err)
	}
	before := stockUsageBytes(t, a, tx)
	tx.Items[0].Market.Regular++
	tx.Items[0].RegularToMarket++
	if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); !errors.Is(err, errStockUsageConflict) || !bytes.Equal(before, stockUsageBytes(t, a, tx)) {
		t.Fatalf("same operation with changed quantities overwrote history: %v", err)
	}
}

func TestStockUsageRejectsUnconfirmedOrInvalidOperationWithoutCreatingFile(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*handoverTransaction)
	}{
		{"invalid-version", func(tx *handoverTransaction) { tx.Version = 3 }},
		{"local-not-done", func(tx *handoverTransaction) { tx.LocalDone = false }},
		{"unprepared", func(tx *handoverTransaction) { tx.Items[0].Stage = "" }},
		{"prepared", func(tx *handoverTransaction) { tx.Items[0].Stage = "prepared" }},
		{"rejected", func(tx *handoverTransaction) { tx.Items[0].Stage = "rejected" }},
		{"invalid-operation", func(tx *handoverTransaction) { tx.ID = "not-hex" }},
		{"invalid-character", func(tx *handoverTransaction) { tx.CharUID = "../char" }},
		{"invalid-mode", func(tx *handoverTransaction) { tx.Mode = "other" }},
		{"wrong-item", func(tx *handoverTransaction) { tx.Items[0].Market.ItemID = "other" }},
		{"negative", func(tx *handoverTransaction) { tx.Items[0].Market.Unknown = -1 }},
		{"request-mismatch", func(tx *handoverTransaction) { tx.Items[0].RegularToMarket++ }},
		{"zero-with-request", func(tx *handoverTransaction) { tx.Items[0].Market = localprogress.HandoverEntry{ItemID: "item-1"} }},
		{"duplicate-item", func(tx *handoverTransaction) { tx.Items = append(tx.Items, tx.Items[0]) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, tx := stockUsageFixture(t)
			test.edit(tx)
			if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); err == nil {
				t.Fatal("invalid operation was recorded")
			}
			entries, err := os.ReadDir(a.dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid operation created history: %v %v", entries, err)
			}
		})
	}
	a, tx := stockUsageFixture(t)
	if a.recordStockEditorMarketUsage(nil, nil) == nil || a.recordStockEditorMarketUsage(tx, nil) == nil {
		t.Fatal("nil transaction/operation accepted")
	}
	unlinked := tx.Items[0]
	unlinked.ItemID, unlinked.Market.ItemID = "outside-tx", "outside-tx"
	if a.recordStockEditorMarketUsage(tx, &unlinked) == nil {
		t.Fatal("operation absent from transaction accepted")
	}
}

func TestStockUsageLocalOnlyHandInDoesNotCreateMarketReceipt(t *testing.T) {
	a, tx := stockUsageFixture(t)
	tx.Items[0].Market = localprogress.HandoverEntry{ItemID: "item-1"}
	tx.Items[0].Local = localprogress.HandoverEntry{ItemID: "item-1", FIR: 3}
	tx.Items[0].Stage = ""
	tx.Items[0].RegularToMarket, tx.Items[0].FIRToMarket = 0, 0
	if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); err != nil {
		t.Fatal(err)
	}
	usage, err := a.loadStockEditorMarketUsage(ItemOptions{CharacterUID: tx.CharUID, Mode: tx.Mode})
	entries, readErr := os.ReadDir(a.dir)
	if err != nil || usage == nil || len(usage) != 0 || readErr != nil || len(entries) != 0 {
		t.Fatalf("local hand-in produced Market history: %v %v %v", usage, err, entries)
	}
}

func TestStockUsageCharacterAndModeRemainIsolated(t *testing.T) {
	a, tx := stockUsageFixture(t)
	if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); err != nil {
		t.Fatal(err)
	}
	for _, opts := range []ItemOptions{{CharacterUID: "char-2", Mode: "PVP"}, {CharacterUID: "char-1", Mode: "PVE"}, {CharacterUID: "char-1", Mode: "SEASON"}} {
		usage, err := a.loadStockEditorMarketUsage(opts)
		if err != nil || len(usage) != 0 {
			t.Fatalf("history crossed character/mode: %v %v", usage, err)
		}
	}
}

func TestStockUsageCorruptionIsKeptAndBlocksNewReceipts(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"wrong-character", func(data []byte) []byte { return bytes.ReplaceAll(data, []byte("char-1"), []byte("char-2")) }},
		{"wrong-mode", func(data []byte) []byte { return bytes.ReplaceAll(data, []byte("PVP"), []byte("PVE")) }},
		{"unknown-field", func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"version": 1`), []byte(`"version": 1, "stock": 123`), 1)
		}},
		{"duplicate-receipt", func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"regular": 1`), []byte(`"regular": 1, "regular": 2`), 1)
		}},
		{"null-quantity", func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"regular": 1`), []byte(`"regular": null`), 1)
		}},
		{"missing-quantity", func(data []byte) []byte { return bytes.Replace(data, []byte(`"regular": 1,`), nil, 1) }},
		{"trailing-json", func(data []byte) []byte { return append(data, []byte(` {}`)...) }},
		{"missing-fields", func([]byte) []byte { return []byte(`{}`) }},
		{"null-receipts", func([]byte) []byte {
			return []byte(`{"version":1,"charUid":"char-1","gameMode":"PVP","receipts":null}`)
		}},
		{"empty-operation", func([]byte) []byte {
			return []byte(`{"version":1,"charUid":"char-1","gameMode":"PVP","receipts":{"0123456789abcdef0123456789abcdef":{}}}`)
		}},
		{"oversized", func([]byte) []byte { return bytes.Repeat([]byte(" "), maxStockUsageBytes+1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, tx := stockUsageFixture(t)
			if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); err != nil {
				t.Fatal(err)
			}
			corrupt := test.edit(stockUsageBytes(t, a, tx))
			path, _, _ := a.stockUsagePath(tx.CharUID, tx.Mode)
			if err := os.WriteFile(path, corrupt, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := a.loadStockEditorMarketUsage(ItemOptions{CharacterUID: tx.CharUID, Mode: tx.Mode}); !errors.Is(err, errInvalidStockUsage) {
				t.Fatalf("corrupt history was accepted: %v", err)
			}
			tx.ID = "abcdef0123456789abcdef0123456789"
			if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); !errors.Is(err, errInvalidStockUsage) || !bytes.Equal(corrupt, stockUsageBytes(t, a, tx)) {
				t.Fatalf("corrupt history was overwritten: %v", err)
			}
		})
	}
}

func TestStockUsageOverflowAndFailedWritePreservePreviousReceipts(t *testing.T) {
	a, tx := stockUsageFixture(t)
	tx.Items[0].Market = localprogress.HandoverEntry{ItemID: "item-1", Regular: localprogress.MaxQuantity}
	tx.Items[0].RegularToMarket, tx.Items[0].FIRToMarket = localprogress.MaxQuantity, 0
	if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); err != nil {
		t.Fatal(err)
	}
	before := stockUsageBytes(t, a, tx)
	tx.ID = "abcdef0123456789abcdef0123456789"
	tx.Items[0].Market.Regular, tx.Items[0].RegularToMarket = 1, 1
	if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); !errors.Is(err, errInvalidStockUsage) || !bytes.Equal(before, stockUsageBytes(t, a, tx)) {
		t.Fatalf("usage overflow overwrote previous receipts: %v", err)
	}
	tx.Items[0].ItemID, tx.Items[0].Market.ItemID = "item-2", "item-2"
	path, _, _ := a.stockUsagePath(tx.CharUID, tx.Mode)
	if err := os.Mkdir(path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); err == nil || !bytes.Equal(before, stockUsageBytes(t, a, tx)) {
		t.Fatalf("failed atomic write changed previous receipt history: %v", err)
	}
	if err := os.Remove(path + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); err != nil {
		t.Fatalf("explicit retry after a failed receipt save failed: %v", err)
	}
}

func TestStockUsageFileContainsOnlyMinimalMarketConsumption(t *testing.T) {
	a, tx := stockUsageFixture(t)
	a.apiKey = "private-api-key-never-persisted"
	tx.Items[0].Local = localprogress.HandoverEntry{ItemID: "item-1", Regular: 31}
	if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); err != nil {
		t.Fatal(err)
	}
	data := stockUsageBytes(t, a, tx)
	for _, private := range []string{a.apiKey, tx.Items[0].Revision, `"target"`, `"stock"`, `"local"`, `"availableQuantity"`, `"spentQuantity"`} {
		if strings.Contains(string(data), private) {
			t.Fatalf("usage file contains non-receipt data %q", private)
		}
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || len(root) != 4 || root["version"] == nil || root["charUid"] == nil || root["gameMode"] == nil || root["receipts"] == nil {
		t.Fatalf("unexpected usage file schema: %s", data)
	}
	var receipts map[string]map[string]map[string]json.RawMessage
	if json.Unmarshal(root["receipts"], &receipts) != nil || len(receipts[tx.ID]["item-1"]) != 4 {
		t.Fatal("receipt contains fields beyond item/regular/fir/unknown")
	}
	usage, err := a.loadStockEditorMarketUsage(ItemOptions{CharacterUID: tx.CharUID, Mode: tx.Mode})
	if err != nil || usage["item-1"] != (localprogress.StockAmounts{Regular: 1, FIR: 2, Unknown: 1}) {
		t.Fatalf("local consumption leaked into Market usage: %v %v", usage, err)
	}
	files, err := os.ReadDir(filepath.Join(a.dir, "stock-usage"))
	if err != nil || len(files) != 1 || strings.Contains(files[0].Name(), tx.CharUID) {
		t.Fatalf("history filename is not isolated by owner hash: %v %v", files, err)
	}
}

func TestStockUsagePreflightHasNoWriteAndDoesNotRequireCompletedStages(t *testing.T) {
	a, tx := stockUsageFixture(t)
	tx.LocalDone, tx.Items[0].Stage = false, ""
	if err := a.validateStockEditorMarketUsage(tx); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(a.dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("dry-run created receipt files: %v %v", files, err)
	}
	tx.LocalDone, tx.Items[0].Stage = true, "confirmed"
	if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); err != nil {
		t.Fatal(err)
	}
	before := stockUsageBytes(t, a, tx)
	tx.LocalDone, tx.Items[0].Stage = false, "prepared"
	if err := a.validateStockEditorMarketUsage(tx); err != nil || !bytes.Equal(before, stockUsageBytes(t, a, tx)) {
		t.Fatalf("preflight did not treat existing receipt idempotently: %v", err)
	}
	tx.Items[0].Market.Regular++
	tx.Items[0].RegularToMarket++
	if err := a.validateStockEditorMarketUsage(tx); !errors.Is(err, errStockUsageConflict) || !bytes.Equal(before, stockUsageBytes(t, a, tx)) {
		t.Fatalf("dry-run ignored a conflicting immutable receipt: %v", err)
	}
}

func TestStockUsagePreflightRejectsNextQuantityBeyondHistoryLimit(t *testing.T) {
	a, tx := stockUsageFixture(t)
	tx.Items[0].Market = localprogress.HandoverEntry{ItemID: "item-1", Regular: localprogress.MaxQuantity}
	tx.Items[0].RegularToMarket, tx.Items[0].FIRToMarket = localprogress.MaxQuantity, 0
	if err := a.recordStockEditorMarketUsage(tx, &tx.Items[0]); err != nil {
		t.Fatal(err)
	}
	before := stockUsageBytes(t, a, tx)
	if err := a.validateStockEditorMarketUsage(tx); err != nil {
		t.Fatalf("existing maximum receipt was counted twice: %v", err)
	}
	tx.ID = "abcdef0123456789abcdef0123456789"
	tx.LocalDone, tx.Items[0].Stage = false, ""
	tx.Items[0].Market.Regular, tx.Items[0].RegularToMarket = 1, 1
	if err := a.validateStockEditorMarketUsage(tx); !errors.Is(err, errInvalidStockUsage) || !bytes.Equal(before, stockUsageBytes(t, a, tx)) {
		t.Fatalf("planned overflow was not rejected before writing: %v", err)
	}
}

func TestStockUsagePreflightRejectsNextReceiptBeyondFileLimit(t *testing.T) {
	a, tx := stockUsageFixture(t)
	id := strings.Repeat("a", 128)
	entry := localprogress.HandoverEntry{ItemID: id, Regular: 1}
	history := stockUsageHistory{Version: 1, CharUID: tx.CharUID, GameMode: tx.Mode,
		Receipts: map[string]map[string]localprogress.HandoverEntry{fmt.Sprintf("%032x", 1): {id: entry}}}
	one, _ := json.MarshalIndent(history, "", "  ")
	history.Receipts[fmt.Sprintf("%032x", 2)] = map[string]localprogress.HandoverEntry{id: entry}
	two, _ := json.MarshalIndent(history, "", "  ")
	increment := len(two) - len(one)
	count := 1 + (maxStockUsageBytes-len(one))/increment
	for i := 3; i <= count; i++ {
		history.Receipts[fmt.Sprintf("%032x", i)] = map[string]localprogress.HandoverEntry{id: entry}
	}
	before, err := encodeStockUsage(history)
	if err != nil || len(before) > maxStockUsageBytes || len(before)+increment <= maxStockUsageBytes {
		t.Fatalf("fixture is not at the valid file boundary: %d %d %v", len(before), increment, err)
	}
	path, _, _ := a.stockUsagePath(tx.CharUID, tx.Mode)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	tx.ID = fmt.Sprintf("%032x", count+1)
	tx.LocalDone, tx.Items[0].Stage = false, ""
	tx.Items[0].ItemID, tx.Items[0].Market = id, entry
	tx.Items[0].RegularToMarket, tx.Items[0].FIRToMarket = 1, 0
	if err := a.validateStockEditorMarketUsage(tx); !errors.Is(err, errInvalidStockUsage) || !bytes.Equal(before, stockUsageBytes(t, a, tx)) {
		t.Fatalf("planned oversize was not rejected before writing: %v", err)
	}
}

func TestStockUsagePreflightRejectsMalformedTransactionWithoutWriting(t *testing.T) {
	for _, edit := range []func(*handoverTransaction){
		func(tx *handoverTransaction) { tx.Version = 0 },
		func(tx *handoverTransaction) { tx.ID = "not-hex" },
		func(tx *handoverTransaction) { tx.CharUID = "../char" },
		func(tx *handoverTransaction) { tx.Mode = "invalid" },
		func(tx *handoverTransaction) { tx.Items = nil },
		func(tx *handoverTransaction) { tx.Items = append(tx.Items, tx.Items[0]) },
		func(tx *handoverTransaction) { tx.Items[0].Market.ItemID = "other" },
		func(tx *handoverTransaction) { tx.Items[0].Market.FIR = -1 },
		func(tx *handoverTransaction) { tx.Items[0].RegularToMarket++ },
	} {
		a, tx := stockUsageFixture(t)
		edit(tx)
		if err := a.validateStockEditorMarketUsage(tx); err == nil {
			t.Fatal("malformed dry-run accepted")
		}
		entries, err := os.ReadDir(a.dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("malformed dry-run created files: %v %v", entries, err)
		}
	}
	a, _ := stockUsageFixture(t)
	if a.validateStockEditorMarketUsage(nil) == nil {
		t.Fatal("nil preflight accepted")
	}
}
