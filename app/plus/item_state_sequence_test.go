package plus

import (
	"encoding/json"
	"sync"
	"testing"
)

func TestItemStateSequenceOrdersConcurrentSnapshots(t *testing.T) {
	app := &ItemWindowApp{}
	const count = 64
	states := make(chan ItemState, count)
	var workers sync.WaitGroup
	for range count {
		workers.Add(1)
		go func() {
			defer workers.Done()
			states <- app.GetState()
		}()
	}
	workers.Wait()
	close(states)
	seen := make(map[uint64]bool, count)
	for state := range states {
		if state.StateSequence == 0 || state.StateSequence > count || seen[state.StateSequence] {
			t.Fatalf("concurrent snapshots must have unique increasing sequence numbers: %d", state.StateSequence)
		}
		seen[state.StateSequence] = true
	}
	if next := app.GetState(); next.StateSequence != count+1 {
		t.Fatalf("later snapshots must follow every previous snapshot: %d", next.StateSequence)
	}
}

func TestItemStateSequenceKeepsDecreasedStockAsNewerState(t *testing.T) {
	app := &ItemWindowApp{state: ItemState{Tracking: &ItemTrackingState{AvailableQuantity: 2}}}
	before := app.GetState()
	app.mu.Lock()
	app.state.Tracking.AvailableQuantity = 0
	app.mu.Unlock()
	after := app.GetState()
	if before.Tracking.AvailableQuantity != 2 || after.Tracking.AvailableQuantity != 0 || after.StateSequence <= before.StateSequence {
		t.Fatalf("a stock change must remain a newer snapshot without altering the old one: before=%+v after=%+v", before, after)
	}
	data, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		StateSequence uint64 `json:"stateSequence"`
	}
	if err := json.Unmarshal(data, &wire); err != nil || wire.StateSequence != after.StateSequence {
		t.Fatalf("state sequence missing from the frontend contract: %s (%v)", data, err)
	}
}
