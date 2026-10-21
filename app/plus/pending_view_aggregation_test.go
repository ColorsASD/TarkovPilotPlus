package plus

import (
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestPendingViewKeepsMixedQualityProfilesAndConfirmedWritesSeparate(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 1, 1)
	a.mu.Lock()
	defer a.mu.Unlock()
	appendChange := func(fir bool, delta int64) {
		a.pendingAddQueue = append(a.pendingAddQueue, pendingMarketAdd{ID: uint64(len(a.pendingAddQueue) + 1),
			Key: a.apiKey, Opts: a.state.ItemOptions, Item: *a.state.Item, Typed: true, FIR: fir, Delta: delta, Stage: "queued"})
	}
	for i := 0; i < 3; i++ {
		appendChange(true, 1)
	}
	for i := 0; i < 4; i++ {
		appendChange(false, 1)
	}
	appendChange(true, -1)
	appendChange(false, -1)
	appendChange(false, -1)
	for _, profile := range []string{"key", "character", "mode"} {
		appendChange(true, 1)
		entry := &a.pendingAddQueue[len(a.pendingAddQueue)-1]
		switch profile {
		case "key":
			entry.Key = "another-api-key"
		case "character":
			entry.Opts.CharacterUID = "another-character"
		case "mode":
			entry.Opts.Mode = "pve"
		}
	}
	appendChange(true, 1)
	confirmed := &a.pendingAddQueue[len(a.pendingAddQueue)-1]
	confirmed.Stage, confirmed.BeforeRevision, confirmed.Before, confirmed.Target = "sent", "earlier-revision", 1, 2
	confirmed.ACK = market.ProgressWriteResult{OK: true, Changed: true, Revision: a.confirmedProgress.Revision}
	for i := 0; i < 2; i++ {
		a.rebuildPendingViewLocked()
		view := a.state.Tracking
		if view.AvailableQuantity != 6 || view.FIRQuantity != 3 || view.RegularQuantity != 3 || view.UncategorizedQuantity != 0 {
			t.Fatalf("mixed pending stock was duplicated or crossed profiles on rebuild %d: %+v", i, view)
		}
		row, unique := stockItem(a.confirmedProgress, trackingBSGID)
		if !unique || row.AvailableQuantity != 2 || row.FIRQuantity != 1 || row.RegularQuantity != 1 {
			t.Fatalf("pending projection mutated confirmed stock: %+v", row)
		}
	}
	if len(fake.writes) != 0 {
		t.Fatal("rendering a pending view sent a stock write")
	}
}

func TestAllocatedTypedViewRetainsTargetsAfterPendingRebuild(t *testing.T) {
	a, _ := newTypedActionTestApp(t, 1, 1)
	a.mu.Lock()
	defer a.mu.Unlock()
	before := localTrackedView(*a.state.Item, a.localSnapshot)
	a.rebuildPendingViewLocked()
	if !reflect.DeepEqual(before, a.state.Tracking) {
		t.Fatalf("reusing the allocated snapshot changed the displayed goals or eligibility: before=%+v after=%+v", before, a.state.Tracking)
	}
}
