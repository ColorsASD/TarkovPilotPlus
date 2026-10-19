package plus

import (
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func setPendingACKExternalStock(fake *fakeTrackingAPI, quantity int64, revision string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.snapshot.Items[0].AvailableQuantity = quantity
	fake.snapshot.Items[0].RemainingQuantity = max(0, fake.snapshot.Items[0].RequiredQuantity-quantity)
	fake.snapshot.Revision = revision
}

func assertPendingACKConfirmedStock(t *testing.T, state ItemState, quantity int64) {
	t.Helper()
	if state.PendingAdds == nil || state.PendingAdds.Count != 0 || state.PendingAdds.Running || state.PendingAdds.Error != "" || state.Tracking == nil || state.Tracking.AvailableQuantity != quantity || state.Tracking.MarketAvailableQuantity != quantity {
		t.Fatalf("ACK-confirmed change did not use the latest stock exactly once: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
	}
}

func TestPendingSuccessfulACKAcceptsConcurrentStockAndNextAddUsesLatest(t *testing.T) {
	for _, tc := range []struct {
		name           string
		initial, delta int64
		latest         int64
	}{
		{"add-then-external-increase", 0, 1, 2},
		{"remove-then-external-increase", 3, -1, 4},
		{"add-then-external-decrease", 0, 1, 0},
		{"remove-then-external-decrease", 3, -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake := newPendingRemoveTestApp(t, tc.initial, false)
			fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
				ack := fake.commitWrite(write)
				fake.mu.Lock()
				first := fake.commits == 1
				fake.mu.Unlock()
				if first {
					// Revisions are opaque: this distinct revision need not sort after the ACK.
					setPendingACKExternalStock(fake, tc.latest, "0-opaque-external-change")
				}
				return ack, nil
			}
			if tc.delta > 0 {
				a.AddTrackedItem(trackingItemUID)
			} else {
				a.RemoveTrackedItem(trackingItemUID)
			}
			state := waitPendingForTest(t, a)
			assertPendingACKConfirmedStock(t, state, tc.latest)
			if len(fake.writes) != 1 || fake.writes[0].Quantity != tc.initial+tc.delta {
				t.Fatalf("concurrent stock changed or replayed the acknowledged request: %+v", fake.writes)
			}
			a.AddTrackedItem(trackingItemUID)
			state = waitPendingForTest(t, a)
			assertPendingACKConfirmedStock(t, state, tc.latest+1)
			if len(fake.writes) != 2 || fake.writes[1].Quantity != tc.latest+1 || fake.writes[1].Revision != "0-opaque-external-change" {
				t.Fatalf("next addition did not start from the external stock: %+v", fake.writes)
			}
		})
	}
}

func TestPendingSuccessfulACKReadOutageRefreshDoesNotOverlayConfirmedChange(t *testing.T) {
	for _, delta := range []int64{1, -1} {
		t.Run(map[int64]string{1: "add", -1: "remove"}[delta], func(t *testing.T) {
			initial := int64(0)
			if delta < 0 {
				initial = 4
			}
			a, fake := newPendingRemoveTestApp(t, initial, false)
			fake.loadHook = func(read int) (market.TrackedSnapshot, error) {
				if read == 2 {
					return market.TrackedSnapshot{}, market.ErrProgressUnavailable
				}
				fake.mu.Lock()
				defer fake.mu.Unlock()
				return cloneTrackedSnapshot(fake.snapshot), nil
			}
			if delta > 0 {
				a.AddTrackedItem(trackingItemUID)
			} else {
				a.RemoveTrackedItem(trackingItemUID)
			}
			state := waitPendingForTest(t, a)
			if state.PendingAdds.Count != 1 || state.PendingAdds.Error == "" || state.Tracking.AvailableQuantity != initial+delta || state.Tracking.MarketAvailableQuantity != initial || len(fake.writes) != 1 {
				t.Fatalf("read outage discarded an unverified ACK or replayed the request: pending=%+v tracking=%+v writes=%+v", state.PendingAdds, state.Tracking, fake.writes)
			}
			setPendingACKExternalStock(fake, 2, "external-after-read-outage")
			a.mu.Lock()
			a.localAt = time.Time{}
			key, opts, item := a.apiKey, a.state.ItemOptions, *a.state.Item
			a.mu.Unlock()
			if err := a.refreshLocalProgress(a.ctx, key, opts, item); err != nil {
				t.Fatal(err)
			}
			assertPendingACKConfirmedStock(t, a.GetState(), 2)
			a.AddTrackedItem(trackingItemUID)
			state = waitPendingForTest(t, a)
			assertPendingACKConfirmedStock(t, state, 3)
			if len(fake.writes) != 2 || fake.writes[1].Quantity != 3 || fake.writes[1].Revision != "external-after-read-outage" {
				t.Fatalf("refresh did not unblock the next addition at the latest stock: %+v", fake.writes)
			}
		})
	}
}

func TestPendingSuccessfulACKAcceptsExternalZeroForExplicitAnyCandidate(t *testing.T) {
	for _, delta := range []int64{1, -1} {
		t.Run(map[int64]string{1: "add", -1: "remove"}[delta], func(t *testing.T) {
			for _, outcome := range []string{"known-ack-newer-snapshot", "unknown-ack", "same-ack-revision-contradiction"} {
				t.Run(outcome, func(t *testing.T) {
					initial := int64(0)
					if delta < 0 {
						initial = 3
					}
					a, fake := newPendingRemoveTestApp(t, initial, true)
					fake.snapshot.Groups = []market.TrackedGroup{{QuestID: "any-quest", RequirementID: "any-items", Amount: 5, RequiredQuantity: 5,
						AvailableQuantity: initial, RemainingQuantity: 5 - initial, ItemIDs: []string{trackingBSGID, "other-candidate"}}}
					setTrackingTestSnapshot(t, a, fake.snapshot)
					fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
						ack := fake.commitWrite(write)
						revision := "external-consumed-last-any-item"
						if outcome == "same-ack-revision-contradiction" {
							revision = ack.Revision
						}
						fake.mu.Lock()
						fake.snapshot.Items = []market.TrackedItem{}
						fake.snapshot.Revision = revision
						setFakeMarketGroupProgress(&fake.snapshot, 0)
						fake.mu.Unlock()
						if outcome == "unknown-ack" {
							return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
						}
						return ack, nil
					}
					if delta > 0 {
						a.AddTrackedItem(trackingItemUID)
					} else {
						a.RemoveTrackedItem(trackingItemUID)
					}
					state := waitPendingForTest(t, a)
					if outcome == "known-ack-newer-snapshot" {
						assertPendingACKConfirmedStock(t, state, 0)
						if state.Tracking.RemoveAllowed {
							t.Fatal("confirmed zero stock still allowed removal")
						}
					} else if state.PendingAdds.Count != 1 || state.PendingAdds.Error == "" {
						t.Fatalf("unproven or contradictory raw-missing zero was trusted: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
					}
					if len(fake.writes) != 1 || fake.writes[0].Quantity != initial+delta {
						t.Fatalf("explicit Any zero changed or replayed its immutable target: %+v", fake.writes)
					}
				})
			}
		})
	}
}

func TestPendingMismatchedStockNeedsValidNoncontradictoryACK(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ack      market.ProgressWriteResult
		writeErr error
		revision string
	}{
		{"unknown-outcome", market.ProgressWriteResult{}, market.ErrProgressWriteUncertain, "external-unknown"},
		{"ack-with-uncertain-transport", market.ProgressWriteResult{OK: true, Changed: true, Revision: "ack-saved"}, market.ErrProgressWriteUncertain, "external-after-uncertain-ack"},
		{"unchanged-ack", market.ProgressWriteResult{OK: true, Revision: "ack-unchanged"}, nil, "external-unchanged"},
		{"ack-missing-revision", market.ProgressWriteResult{OK: true, Changed: true}, nil, "external-missing-ack-revision"},
		{"ack-unchanged-revision", market.ProgressWriteResult{OK: true, Changed: true, Revision: "revision-1"}, nil, "external-invalid-ack-revision"},
		{"get-unchanged-revision", market.ProgressWriteResult{OK: true, Changed: true, Revision: "ack-saved"}, nil, "revision-1"},
		{"same-ack-revision-contradictory-stock", market.ProgressWriteResult{OK: true, Changed: true, Revision: "ack-saved"}, nil, "ack-saved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake := newTrackingTestApp(t)
			fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
				setPendingACKExternalStock(fake, 2, tc.revision)
				return tc.ack, tc.writeErr
			}
			a.AddTrackedItem(trackingItemUID)
			state := waitPendingForTest(t, a)
			if state.PendingAdds.Count != 1 || state.PendingAdds.Running || state.PendingAdds.Error == "" || len(fake.writes) != 1 {
				t.Fatalf("mismatched quantity was trusted without a usable ACK proof: pending=%+v tracking=%+v writes=%+v", state.PendingAdds, state.Tracking, fake.writes)
			}
			a.RecheckPendingAdds()
			state = waitPendingForTest(t, a)
			if state.PendingAdds.Count != 1 || len(fake.writes) != 1 {
				t.Fatalf("unproven write was discarded or sent again: pending=%+v writes=%+v", state.PendingAdds, fake.writes)
			}
		})
	}
}
