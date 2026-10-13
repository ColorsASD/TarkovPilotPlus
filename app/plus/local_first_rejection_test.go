package plus

import (
	"context"
	"errors"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func definiteProgressFailuresForTest() map[string]error {
	return map[string]error{
		"rate-limit":            market.ErrRateLimited,
		"unauthorized":          market.ErrUnauthorized,
		"forbidden":             market.ErrProgressForbidden,
		"not-found":             market.ErrProgressNotFound,
		"conflict":              market.ErrProgressConflict,
		"revision-required":     market.ErrProgressRevisionRequired,
		"invalid-quantity":      market.ErrProgressInvalidQuantity,
		"pre-wire-cancellation": context.Canceled,
		"pre-wire-deadline":     context.DeadlineExceeded,
	}
}

func TestLocalFirstHandoverDefiniteRejectionRemainsUnsentAndRecoversAfterRestart(t *testing.T) {
	for name, rejected := range definiteProgressFailuresForTest() {
		t.Run(name, func(t *testing.T) {
			a, fake := localFirstMixedHandoverFixture(t)
			handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
			a.ConfirmHandover()
			fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
				if len(fake.writes) == 1 {
					return market.ProgressWriteResult{}, rejected
				}
				return fake.commitWrite(write), nil
			}
			reads := fake.loads
			if err := a.processLocalFirstHandover(a.ctx, a.apiKey, a.state.ItemOptions); !errors.Is(err, rejected) {
				t.Fatalf("rejection was lost: %v", err)
			}
			tx, err := readHandover(a.handoverPath("character-a", "PVP"), "character-a", "PVP")
			if err != nil || !tx.LocalDone || tx.Items[0].Stage != "unsent" || tx.Items[0].Target != 1 || fake.loads != reads+1 || len(fake.writes) != 1 || fake.snapshot.Items[0].AvailableQuantity != 2 {
				t.Fatalf("definite rejection became an uncertain write or made a readback: %+v, %v, GET=%d PATCH=%d", tx, err, fake.loads-reads, len(fake.writes))
			}
			if item := handoverStockForTest(t, a, trackingBSGID); item.LocalUsedQuantity != 1 || item.LocalCollectedQuantity != 1 {
				t.Fatal("rejection lost or replayed the already settled local receipt")
			}
			a.restoreHandoverLocked()
			if err := a.processLocalFirstHandover(a.ctx, a.apiKey, a.state.ItemOptions); err != nil {
				t.Fatal(err)
			}
			requireHandoverComplete(t, a.GetState())
			if len(fake.writes) != 2 || fake.snapshot.Items[0].AvailableQuantity != 1 || handoverStockForTest(t, a, trackingBSGID).LocalUsedQuantity != 1 {
				t.Fatal("restored rejected hand-in did not settle exactly once")
			}
			if err := a.processLocalFirstHandover(a.ctx, a.apiKey, a.state.ItemOptions); err != nil || len(fake.writes) != 2 {
				t.Fatal("completed hand-in was replayed")
			}
		})
	}
}

func TestLocalFirstStockEditDefiniteRejectionRemainsUnsentAndRecoversAfterRestart(t *testing.T) {
	for name, rejected := range definiteProgressFailuresForTest() {
		t.Run(name, func(t *testing.T) {
			a, fake := localFirstMixedHandoverFixture(t)
			a.OpenStockEditor()
			a.SaveStockEditorItem(editorRequestForTest(a, "market", 0, 1, 0))
			fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
				if len(fake.writes) == 1 {
					return market.ProgressWriteResult{}, rejected
				}
				return fake.commitWrite(write), nil
			}
			reads := fake.loads
			if err := a.processLocalFirstStockEdit(a.ctx, a.apiKey, a.state.ItemOptions); !errors.Is(err, rejected) {
				t.Fatalf("rejection was lost: %v", err)
			}
			pending, err := a.readStockEditorPending(a.state.ItemOptions)
			if err != nil || pending == nil || pending.Stage != "unsent" || pending.Amounts.Regular != 1 || pending.Baseline.Regular != 2 || fake.loads != reads+1 || len(fake.writes) != 1 || fake.snapshot.Items[0].AvailableQuantity != 2 {
				t.Fatalf("definite rejection became an uncertain edit or made a readback: %+v, %v, GET=%d PATCH=%d", pending, err, fake.loads-reads, len(fake.writes))
			}
			a.restoreStockEditorLocked()
			if err := a.processLocalFirstStockEdit(a.ctx, a.apiKey, a.state.ItemOptions); err != nil {
				t.Fatal(err)
			}
			pending, err = a.readStockEditorPending(a.state.ItemOptions)
			if err != nil || pending != nil || len(fake.writes) != 2 || fake.snapshot.Items[0].AvailableQuantity != 1 {
				t.Fatal("restored rejected edit did not settle exactly once")
			}
			item := handoverStockForTest(t, a, trackingBSGID)
			if item.LocalUsedQuantity != 0 || item.MarketRegularQuantity != 1 {
				t.Fatal("rejected edit changed hand-in history or lost the confirmed type")
			}
			if err := a.processLocalFirstStockEdit(a.ctx, a.apiKey, a.state.ItemOptions); err != nil || len(fake.writes) != 2 {
				t.Fatal("completed edit was replayed")
			}
		})
	}
}

func TestLocalFirstRejectionNeverResetsUncertainSentWrites(t *testing.T) {
	for name, failure := range map[string]error{
		"uncertain":               market.ErrProgressWriteUncertain,
		"after-wire-cancellation": errors.Join(market.ErrProgressWriteUncertain, context.Canceled),
		"after-wire-deadline":     errors.Join(market.ErrProgressWriteUncertain, context.DeadlineExceeded),
		"uncertain-rate-limit":    errors.Join(market.ErrProgressWriteUncertain, market.ErrRateLimited),
	} {
		t.Run(name, func(t *testing.T) {
			for _, operation := range []string{"handover", "stock-edit"} {
				t.Run(operation, func(t *testing.T) {
					a, fake := localFirstMixedHandoverFixture(t)
					var process func() error
					if operation == "handover" {
						handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 2, "regular"))
						a.ConfirmHandover()
						process = func() error { return a.processLocalFirstHandover(a.ctx, a.apiKey, a.state.ItemOptions) }
					} else {
						a.OpenStockEditor()
						a.SaveStockEditorItem(editorRequestForTest(a, "market", 0, 1, 0))
						process = func() error { return a.processLocalFirstStockEdit(a.ctx, a.apiKey, a.state.ItemOptions) }
					}
					fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) { return market.ProgressWriteResult{}, failure }
					if err := process(); err == nil {
						t.Fatal("uncertain unconfirmed write was settled")
					}
					if operation == "handover" {
						tx, err := readHandover(a.handoverPath("character-a", "PVP"), "character-a", "PVP")
						if err != nil || tx.Items[0].Stage != "sent" {
							t.Fatal("uncertain hand-in was made eligible for another PATCH")
						}
						a.restoreHandoverLocked()
					} else {
						pending, err := a.readStockEditorPending(a.state.ItemOptions)
						if err != nil || pending == nil || pending.Stage != "sent" {
							t.Fatal("uncertain correction was made eligible for another PATCH")
						}
						a.restoreStockEditorLocked()
					}
					reads := fake.loads
					if err := process(); err == nil || fake.loads != reads+1 || len(fake.writes) != 1 || fake.snapshot.Items[0].AvailableQuantity != 2 {
						t.Fatal("uncertain saved operation sent another PATCH on restart")
					}
				})
			}
		})
	}
}
