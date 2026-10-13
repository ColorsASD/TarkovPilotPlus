package plus

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
)

func savedFallbackGateFixture(t *testing.T) (*ItemWindowApp, *fakeTrackingAPI, ItemOptions, string) {
	t.Helper()
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	pending := &marketFallbackSyncIntent{Version: 1, CharUID: opts.CharacterUID, Mode: strings.ToUpper(opts.Mode), ItemID: trackingBSGID,
		Revision: fake.snapshot.Revision, Before: 2, Target: 6,
		Fallback: localprogress.StockAmounts{Regular: 2, FIR: 1, Unknown: 1}, MarketAfter: localprogress.StockAmounts{Regular: 3, FIR: 1, Unknown: 2}}
	if err := a.saveMarketFallbackSync(opts, pending); err != nil {
		t.Fatal(err)
	}
	path, err := a.marketFallbackSyncPath(opts)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	err = a.refreshMarketFallbackPendingLocked(opts)
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return a, fake, opts, path
}

func TestMarketFallbackGateOnlyLocksCanonicalPendingItemAndProfile(t *testing.T) {
	a, fake, opts, path := savedFallbackGateFixture(t)
	before, _ := os.ReadFile(path)
	for _, id := range []string{trackingBSGID, "other-item"} {
		want := id == trackingBSGID
		blocked, err := a.marketFallbackBlocksItem(opts, id)
		a.mu.Lock()
		fromState := a.marketFallbackBlocksItemLocked(id)
		a.mu.Unlock()
		if err != nil || blocked != want || fromState != want {
			t.Fatalf("wrong lane scope for %s: disk=%v state=%v err=%v", id, blocked, fromState, err)
		}
	}
	other := opts
	other.Mode = "pve"
	if blocked, err := a.marketFallbackBlocksItem(other, trackingBSGID); err != nil || blocked {
		t.Fatal("another mode inherited pending lock")
	}
	other = opts
	other.CharacterUID = "another-character"
	if blocked, err := a.marketFallbackBlocksItem(other, trackingBSGID); err != nil || blocked {
		t.Fatal("another character inherited pending lock")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) || fake.loads != 0 || len(fake.writes) != 0 || !a.state.MarketFallbackPending ||
		a.state.MarketFallbackInvalid || a.state.MarketFallbackItemID != trackingBSGID {
		t.Fatal("readonly gate changed disk/API/state identity")
	}
}

func TestMarketFallbackGateCorruptOrUnknownScopeFailsClosedWithoutDiscard(t *testing.T) {
	a, fake, opts, path := savedFallbackGateFixture(t)
	data := []byte(`{"version":1,"itemId":"cannot-trust-this"}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{trackingBSGID, "other-item"} {
		if blocked, err := a.marketFallbackBlocksItem(opts, id); !blocked || err == nil {
			t.Fatal("corrupt journal acquired a narrow trusted scope")
		}
	}
	a.mu.Lock()
	err := a.refreshMarketFallbackPendingLocked(opts)
	blocked := a.marketFallbackBlocksItemLocked("other-item")
	a.mu.Unlock()
	after, _ := os.ReadFile(path)
	if err == nil || !blocked || !a.state.MarketFallbackPending || !a.state.MarketFallbackInvalid || a.state.MarketFallbackItemID != "" ||
		!bytes.Equal(data, after) || fake.loads != 0 || len(fake.writes) != 0 {
		t.Fatal("invalid pending scope was lost, relabeled, or modified")
	}
	a.state.MarketFallbackInvalid = false
	if !a.marketFallbackBlocksItemLocked("other-item") {
		t.Fatal("legacy pending bool with no validated ID was treated as safe")
	}
}

func TestMarketFallbackGateLateRefreshCannotPublishPreviousProfile(t *testing.T) {
	a, _, opts, _ := savedFallbackGateFixture(t)
	a.state.ItemOptions.revision++
	a.state.MarketFallbackPending, a.state.MarketFallbackInvalid, a.state.MarketFallbackItemID = false, false, ""
	a.mu.Lock()
	err := a.refreshMarketFallbackPendingLocked(opts)
	a.mu.Unlock()
	if err != nil || a.state.MarketFallbackPending || a.state.MarketFallbackInvalid || a.state.MarketFallbackItemID != "" {
		t.Fatal("late result restored an obsolete profile's lock")
	}
}
