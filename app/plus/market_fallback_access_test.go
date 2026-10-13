package plus

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func fallbackAccessStock(t *testing.T, a *ItemWindowApp, opts ItemOptions, id string) localprogress.Item {
	t.Helper()
	stock, err := a.localStore.Load(opts.CharacterUID, strings.ToUpper(opts.Mode))
	row, unique := stockEditorItem(stock, id)
	if err != nil || !unique {
		t.Fatalf("own stock missing: %+v %v", stock, err)
	}
	return row
}

func TestMarketFallbackAccessPendingSameItemAllowsIndependentLocalHandIn(t *testing.T) {
	for _, quantity := range []int64{1, 2} {
		t.Run(string(rune('0'+quantity)), func(t *testing.T) {
			a, fake, opts, path := savedFallbackGateFixture(t)
			pendingBefore, _ := os.ReadFile(path)
			fallbackBefore := fallbackSyncAvailableForTest(t, a, opts)
			handoverDraftForTest(a, handoverRowForTest(trackingBSGID, quantity, "regular"))
			state := a.ConfirmHandover()
			requireHandoverComplete(t, state)
			row := fallbackAccessStock(t, a, opts, trackingBSGID)
			pendingAfter, _ := os.ReadFile(path)
			if row.LocalAvailableQuantity != 2-quantity || row.LocalRegularQuantity != 2-quantity ||
				row.LocalCollectedQuantity != 3 || row.LocalUsedQuantity != 1+quantity || row.LocalUsedRegularQuantity != 1+quantity ||
				!state.MarketFallbackPending || state.MarketFallbackInvalid || state.MarketFallbackItemID != trackingBSGID ||
				len(fake.writes) != 0 || fake.snapshot.Items[0].AvailableQuantity != 2 ||
				fallbackSyncAvailableForTest(t, a, opts) != fallbackBefore || !bytes.Equal(pendingBefore, pendingAfter) {
				t.Fatalf("independent hand-in touched captured upload or failed: local=%+v state=%+v", row, state.Handover)
			}
			if _, err := os.Stat(a.handoverPath(opts.CharacterUID, opts.Mode)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("completed independent hand-in left its journal")
			}
			a.ConfirmHandover()
			if again := fallbackAccessStock(t, a, opts, trackingBSGID); again.LocalUsedQuantity != row.LocalUsedQuantity || len(fake.writes) != 0 {
				t.Fatal("repeat consumed independent local stock again")
			}
		})
	}
}

func TestMarketFallbackAccessMixedDraftRejectsBeforeAnyLocalReceiptOrJournal(t *testing.T) {
	a, fake, opts, path := savedFallbackGateFixture(t)
	otherID := "independent-local-item"
	amounts := localprogress.StockAmounts{Regular: 1}
	if _, err := a.localStore.CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode),
		localprogress.ManualStockCorrection{ItemID: otherID, LocalAvailable: &amounts}); err != nil {
		t.Fatal(err)
	}
	pendingBefore, _ := os.ReadFile(path)
	fallbackBefore := fallbackSyncAvailableForTest(t, a, opts)
	handoverDraftForTest(a, handoverRowForTest(otherID, 1, "regular"), handoverRowForTest(trackingBSGID, 3, "regular"))
	state := a.ConfirmHandover()
	pendingAfter, _ := os.ReadFile(path)
	row, other := fallbackAccessStock(t, a, opts, trackingBSGID), fallbackAccessStock(t, a, opts, otherID)
	if state.Handover == nil || state.Handover.Busy || state.Handover.Pending || state.Handover.Completed ||
		!strings.Contains(state.Handover.Error, "Only independent Local Progress stock") ||
		!strings.Contains(state.Handover.Error, "No items were deducted") ||
		row.LocalAvailableQuantity != 2 || row.LocalUsedQuantity != 1 || other.LocalAvailableQuantity != 1 || other.LocalUsedQuantity != 0 ||
		a.handover != nil || len(fake.writes) != 0 || fake.snapshot.Items[0].AvailableQuantity != 2 ||
		fallbackSyncAvailableForTest(t, a, opts) != fallbackBefore || !bytes.Equal(pendingBefore, pendingAfter) {
		t.Fatalf("partial mixed draft was deducted or silently rejected: %+v local=%+v other=%+v", state.Handover, row, other)
	}
	if _, err := os.Stat(a.handoverPath(opts.CharacterUID, opts.Mode)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected whole plan created a hand-in journal")
	}
}

func TestMarketFallbackAccessExistingProtectedHandInCannotResumeOrRededuct(t *testing.T) {
	for _, source := range []string{"market", "market-fallback"} {
		for _, partial := range []bool{false, true} {
			t.Run(source+map[bool]string{false: "-unsettled", true: "-local-receipt-committed"}[partial], func(t *testing.T) {
				a, fake, opts, path := savedFallbackGateFixture(t)
				txID := strings.Repeat("e", 32)
				local := localprogress.HandoverEntry{ItemID: trackingBSGID, Regular: 1}
				remote, fallback := localprogress.HandoverEntry{ItemID: trackingBSGID}, localprogress.HandoverEntry{ItemID: trackingBSGID}
				target, toMarket := int64(2), int64(0)
				if source == "market" {
					remote.Regular, target, toMarket = 1, 1, 1
				} else {
					fallback.Regular = 1
				}
				if partial {
					if _, err := a.localStore.RecordHandover(opts.CharacterUID, strings.ToUpper(opts.Mode), txID, []localprogress.HandoverEntry{local}); err != nil {
						t.Fatal(err)
					}
				}
				tx := &handoverTransaction{Version: 1, ID: txID, CharUID: opts.CharacterUID, Mode: strings.ToUpper(opts.Mode),
					Draft: []ItemHandoverEntry{handoverRowForTest(trackingBSGID, 2, "regular")},
					Items: []handoverOperation{{ItemID: trackingBSGID, Name: "Protected item", Local: local, Market: remote, Fallback: fallback, Target: target, RegularToMarket: toMarket}}}
				if err := a.saveHandover(tx); err != nil {
					t.Fatal(err)
				}
				journal := a.handoverPath(opts.CharacterUID, opts.Mode)
				handInBefore, _ := os.ReadFile(journal)
				pendingBefore, _ := os.ReadFile(path)
				fallbackBefore := fallbackSyncAvailableForTest(t, a, opts)
				localBefore := fallbackAccessStock(t, a, opts, trackingBSGID)
				a.restoreHandoverLocked()
				a.OpenHandover()
				reads := fake.loads
				state := a.ConfirmHandover()
				handInAfter, _ := os.ReadFile(journal)
				pendingAfter, _ := os.ReadFile(path)
				localAfter := fallbackAccessStock(t, a, opts, trackingBSGID)
				if state.Handover == nil || !state.Handover.Pending || state.Handover.Busy || state.Handover.Completed ||
					!strings.Contains(state.Handover.Error, "No further items were deducted") ||
					localAfter.LocalAvailableQuantity != localBefore.LocalAvailableQuantity || localAfter.LocalUsedQuantity != localBefore.LocalUsedQuantity ||
					fallbackSyncAvailableForTest(t, a, opts) != fallbackBefore || fake.loads != reads || len(fake.writes) != 0 ||
					!bytes.Equal(handInBefore, handInAfter) || !bytes.Equal(pendingBefore, pendingAfter) {
					t.Fatalf("protected saved transaction replayed or mutated: %+v", state.Handover)
				}
			})
		}
	}
}

func TestMarketFallbackAccessUnrelatedLocalAddKeepsOriginalUploadByteExact(t *testing.T) {
	a, fake := newDevOnlyTestApp(t)
	opts := a.state.ItemOptions
	otherID := "pending-other-market-item"
	fake.snapshot.Items = []market.TrackedItem{{ItemID: otherID, Name: "Pending other item", RequiredQuantity: 2, RemainingQuantity: 2,
		Sources: []market.TrackedSource{{Type: "quest", Amount: 2, RemainingRequiredQuantity: 2}}}}
	fallback := localprogress.StockAmounts{Regular: 1}
	if _, err := a.marketFallbackStock().CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode),
		localprogress.ManualStockCorrection{ItemID: otherID, LocalAvailable: &fallback}); err != nil {
		t.Fatal(err)
	}
	pending := &marketFallbackSyncIntent{Version: 1, CharUID: opts.CharacterUID, Mode: strings.ToUpper(opts.Mode), ItemID: otherID,
		Revision: fake.snapshot.Revision, Before: 0, Target: 1, Fallback: fallback, MarketAfter: fallback}
	if err := a.saveMarketFallbackSync(opts, pending); err != nil {
		t.Fatal(err)
	}
	path, _ := a.marketFallbackSyncPath(opts)
	pendingBefore, _ := os.ReadFile(path)
	a.setMarketFallbackPending(a.apiKey, opts, true)
	state := a.AddTrackedItem(trackingItemUID)
	row := fallbackAccessStock(t, a, opts, trackingBSGID)
	pendingAfter, _ := os.ReadFile(path)
	got, err := marketFallbackSyncAmounts(a.marketFallbackStock(), opts, otherID)
	if err != nil || got != fallback || row.LocalAvailableQuantity != 1 || row.LocalRegularQuantity != 1 ||
		state.Tracking == nil || state.Tracking.Error != "" || state.Tracking.Storage != "local" || state.Tracking.AvailableQuantity != 1 ||
		!state.MarketFallbackPending || state.MarketFallbackItemID != otherID || len(fake.writes) != 0 || !bytes.Equal(pendingBefore, pendingAfter) {
		t.Fatalf("unrelated local Add inherited global freeze or touched upload: %+v %v", state.Tracking, err)
	}
}

func TestMarketFallbackAccessUnrelatedMarketAddOnlyQueuesSelectedSupplement(t *testing.T) {
	a, fake, opts, path := savedFallbackGateFixture(t)
	id, uid := "unrelated-market-item", "unrelated-inspected-uid"
	fake.snapshot.Items = append(fake.snapshot.Items, market.TrackedItem{ItemID: id, Name: "Unrelated item", RequiredQuantity: 2, RemainingQuantity: 2,
		Sources: []market.TrackedSource{{Type: "quest", Amount: 2, RemainingRequiredQuantity: 2}}})
	a.state.Item = &market.Item{UID: uid, BSGID: id, Name: "Unrelated item"}
	a.localAt = time.Time{}
	if err := a.refreshLocalProgress(a.ctx, a.apiKey, opts, *a.state.Item); err != nil {
		t.Fatal(err)
	}
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		for i := range fake.snapshot.Items {
			if fake.snapshot.Items[i].ItemID == write.Item {
				fake.snapshot.Items[i].AvailableQuantity = write.Quantity
				fake.snapshot.Items[i].RemainingQuantity = max(0, fake.snapshot.Items[i].RequiredQuantity-write.Quantity)
			}
		}
		fake.snapshot.Revision = "unrelated-add-verified"
		return market.ProgressWriteResult{OK: true, Changed: true, Revision: fake.snapshot.Revision, NotificationDelivered: true}, nil
	}
	pendingBefore, _ := os.ReadFile(path)
	fallbackBefore := fallbackSyncAvailableForTest(t, a, opts)
	localBefore := fallbackAccessStock(t, a, opts, trackingBSGID)
	state := a.AddTrackedItem(uid)
	pendingAfter, _ := os.ReadFile(path)
	localAfter := fallbackAccessStock(t, a, opts, trackingBSGID)
	queued, queueErr := marketFallbackSyncAmounts(a.marketFallbackStock(), opts, id)
	if state.Tracking == nil || state.Tracking.Error != "" || state.Tracking.AvailableQuantity != 1 || state.Tracking.Storage != "market" ||
		len(fake.writes) != 0 || queueErr != nil || queued != (localprogress.StockAmounts{Regular: 1}) || fake.snapshot.Items[1].AvailableQuantity != 0 ||
		fake.snapshot.Items[0].AvailableQuantity != 2 || fallbackSyncAvailableForTest(t, a, opts) != fallbackBefore ||
		localAfter.LocalAvailableQuantity != localBefore.LocalAvailableQuantity || localAfter.LocalUsedQuantity != localBefore.LocalUsedQuantity ||
		!bytes.Equal(pendingBefore, pendingAfter) {
		t.Fatalf("unrelated Market Add inherited global freeze or mutated captured lanes: %+v writes=%+v", state.Tracking, fake.writes)
	}
}

func TestMarketFallbackAccessStartupRefreshOnlyChecksPendingUpload(t *testing.T) {
	for _, obsolete := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-revision-quarantine", true: "obsolete-revision-readonly-clear"}[obsolete], func(t *testing.T) {
			a, fake, opts, path := savedFallbackGateFixture(t)
			pendingBefore, _ := os.ReadFile(path)
			fallbackBefore := fallbackSyncAvailableForTest(t, a, opts)
			localBefore := fallbackAccessStock(t, a, opts, trackingBSGID)
			if obsolete {
				fake.snapshot.Revision = "unrelated-progress-new-revision"
			}
			a.restoreStockEditorLocked()
			a.refreshTracking()
			state := a.GetState()
			localAfter := fallbackAccessStock(t, a, opts, trackingBSGID)
			pendingAfter, err := os.ReadFile(path)
			if len(fake.writes) != 0 || fake.loads == 0 || fallbackSyncAvailableForTest(t, a, opts) != fallbackBefore ||
				localAfter.LocalAvailableQuantity != localBefore.LocalAvailableQuantity || localAfter.LocalUsedQuantity != localBefore.LocalUsedQuantity ||
				state.MarketFallbackInvalid || state.MarketFallbackPending == obsolete {
				t.Fatalf("startup refresh replayed or mutated physical stock: state=%+v", state)
			}
			if obsolete {
				if !errors.Is(err, os.ErrNotExist) || state.MarketFallbackItemID != "" {
					t.Fatal("readonly obsolete proof did not clear pending intent")
				}
			} else if err != nil || !bytes.Equal(pendingBefore, pendingAfter) || state.MarketFallbackItemID != trackingBSGID {
				t.Fatal("same-revision uncertainty was silently discarded")
			}
			if matches, _ := filepath.Glob(filepath.Join(a.dir, "handovers", "*.json")); len(matches) != 0 {
				t.Fatal("refresh created a hand-in")
			}
		})
	}
}
