package localprogress

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

const transferTestID = "0123456789abcdef0123456789abcdef"

func transferFixture(t *testing.T) *Store {
	t.Helper()
	s := NewStore(t.TempDir())
	amounts := StockAmounts{Regular: 4, FIR: 3, Unknown: 2}
	if _, err := s.CorrectStock("char-1", "PVP", ManualStockCorrection{ItemID: "item-a", LocalAvailable: &amounts}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordHandover("char-1", "PVP", "previous-hand-in", []HandoverEntry{{ItemID: "item-a", Regular: 1, FIR: 1}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTransferAvailableKeepsNewTypedAddsUsageAndReplaysOnceAfterRestart(t *testing.T) {
	s := transferFixture(t)
	captured := StockAmounts{Regular: 3, FIR: 2, Unknown: 2}
	// A befoglalás után új darab kerülhet ugyanabba a készletbe.
	newAvailable := StockAmounts{Regular: 4, FIR: 3, Unknown: 2}
	if _, err := s.CorrectStock("char-1", "PVP", ManualStockCorrection{ItemID: "item-a", LocalAvailable: &newAvailable}); err != nil {
		t.Fatal(err)
	}
	beforePreflight := rawStore(t, s)
	if err := s.ValidateTransferAvailable("char-1", "PVP", transferTestID, "item-a", captured); err != nil || !bytes.Equal(beforePreflight, rawStore(t, s)) {
		t.Fatalf("preflight changed stock or receipt: %v", err)
	}
	result, err := s.TransferAvailable("char-1", "PVP", transferTestID, "item-a", captured)
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("transfer failed: %+v %v", result, err)
	}
	item := result.Items[0]
	if item.LocalAvailableQuantity != 2 || item.LocalRegularQuantity != 1 || item.LocalFIRQuantity != 1 || item.LocalUsedQuantity != 2 || item.LocalCollectedQuantity != 4 {
		t.Fatalf("new additions or usage lost: %+v", item)
	}
	before := rawStore(t, s)
	restarted := NewStore(s.dir)
	if recorded, err := restarted.TransferRecorded("char-1", "PVP", transferTestID, "item-a", captured); err != nil || !recorded {
		t.Fatalf("receipt lost on restart: %v %v", recorded, err)
	}
	if _, err := restarted.TransferAvailable("char-1", "PVP", transferTestID, "item-a", captured); err != nil || !bytes.Equal(before, rawStore(t, s)) {
		t.Fatalf("replay debited twice or rewrote immutable receipt: %v", err)
	}
	if _, err := restarted.TransferAvailable("char-1", "PVP", transferTestID, "item-a", StockAmounts{Regular: 1}); !errors.Is(err, ErrHandoverConflict) || !bytes.Equal(before, rawStore(t, s)) {
		t.Fatalf("different receipt overwrote state: %v", err)
	}
	if _, err := ValidateBackupStock(before); err != nil {
		t.Fatalf("v5 transfer backup rejected: %v", err)
	}
}

func TestTransferAvailableInvalidCapacityAndDiskFailurePreserveBytes(t *testing.T) {
	for _, test := range []struct {
		name         string
		operation    string
		amounts      StockAmounts
		writeFailure bool
	}{
		{"invalid-operation", "bad", StockAmounts{Regular: 1}, false},
		{"empty", transferTestID, StockAmounts{}, false},
		{"negative", transferTestID, StockAmounts{FIR: -1}, false},
		{"overflow", transferTestID, StockAmounts{Regular: MaxQuantity, FIR: 1}, false},
		{"regular-exceeds", transferTestID, StockAmounts{Regular: 4}, false},
		{"fir-exceeds", transferTestID, StockAmounts{FIR: 3}, false},
		{"unknown-exceeds", transferTestID, StockAmounts{Unknown: 3}, false},
		{"disk-failure", transferTestID, StockAmounts{Regular: 1}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := transferFixture(t)
			before := rawStore(t, s)
			if test.writeFailure {
				s.writeFile = func(string, []byte) error { return errors.New("disk unavailable") }
			}
			if !test.writeFailure {
				if err := s.ValidateTransferAvailable("char-1", "PVP", test.operation, "item-a", test.amounts); err == nil || !bytes.Equal(before, rawStore(t, s)) {
					t.Fatalf("invalid preflight accepted or changed stock: %v", err)
				}
			}
			if _, err := s.TransferAvailable("char-1", "PVP", test.operation, "item-a", test.amounts); err == nil || !bytes.Equal(before, rawStore(t, s)) {
				t.Fatalf("invalid/failed transfer changed stock: %v", err)
			}
		})
	}
}

func TestTransferStoreV4MigrationAndStrictV5BackupReceipts(t *testing.T) {
	s := transferFixture(t)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawStore(t, s), &fields); err != nil {
		t.Fatal(err)
	}
	fields["version"] = json.RawMessage(`4`)
	delete(fields, "transferReceipts")
	v4, _ := json.Marshal(fields)
	if _, err := ValidateBackupStock(v4); err != nil {
		t.Fatalf("valid v4 backup rejected: %v", err)
	}
	path, _ := s.path("char-1", "PVP")
	if err := os.WriteFile(path, v4, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(s.dir).TransferAvailable("char-1", "PVP", transferTestID, "item-a", StockAmounts{FIR: 1}); err != nil {
		t.Fatal(err)
	}
	var state storedProgress
	_ = json.Unmarshal(rawStore(t, s), &state)
	if state.Version != 5 || len(state.TransferReceipts) != 1 || len(state.HandoverReceipts) != 1 || state.LocalUsed["item-a"].FIR != 1 {
		t.Fatalf("v4 migration lost history: %+v", state)
	}
	valid := rawStore(t, s)
	for _, corrupt := range [][]byte{
		bytes.Replace(valid, []byte(`"transferReceipts": {`), []byte(`"transferReceipts": null,"unexpected":{`), 1),
		bytes.Replace(valid, []byte(transferTestID), []byte("bad-operation"), 1),
		bytes.Replace(valid, []byte(`"transferReceipts"`), []byte(`"TransferReceipts"`), 1),
	} {
		if _, err := ValidateBackupStock(corrupt); err == nil {
			t.Fatal("invalid transfer receipt backup accepted")
		}
	}
}

func TestRepeatedStoreApplySkipsUnchangedSaveButPersistsQualityReset(t *testing.T) {
	s := NewStore(t.TempDir())
	writes := 0
	s.writeFile = func(path string, data []byte) error {
		writes++
		return atomicWrite(path, data)
	}
	snapshot := storeSnapshot()
	snapshot.Items = append(snapshot.Items, Item{ItemID: "remote", MarketTracked: true, MarketAvailableQuantity: 2, AvailableQuantity: 2, Sources: []Source{}})
	for i := 0; i < 6; i++ {
		if _, err := s.Apply(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if writes != 1 {
		t.Fatalf("unchanged Apply wrote %d times", writes)
	}
	// A korábban megerősített minőség mentése nem része az Apply számlálónak.
	s.writeFile = atomicWrite
	kinds := StockAmounts{FIR: 2}
	if _, err := s.CorrectStock("char-1", "PVP", ManualStockCorrection{ItemID: "remote", MarketKinds: &kinds, ObservedMarketQuantity: 2}); err != nil {
		t.Fatal(err)
	}
	s.writeFile = func(path string, data []byte) error {
		writes++
		return atomicWrite(path, data)
	}
	snapshot.Items[1].MarketAvailableQuantity, snapshot.Items[1].AvailableQuantity = 0, 0
	result, err := s.Apply(snapshot)
	if err != nil || writes != 2 || result.Items[1].MarketFIRQuantity != 0 {
		t.Fatalf("real quality reset was skipped: writes=%d snapshot=%+v err=%v", writes, result, err)
	}
	s.writeFile = func(string, []byte) error { return errors.New("read-only unchanged file") }
	if _, err := s.Apply(snapshot); err != nil {
		t.Fatalf("unchanged render required a redundant write: %v", err)
	}
}
