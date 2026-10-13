package plus

import (
	"context"
	"errors"
	"reflect"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

// A biztosan elutasított vagy a HTTP-kérés előtt megszakított írás nem
// válhat GET-only szándékká. A HTTP elküldése utáni bizonytalanságot a kliens
// ErrProgressWriteUncertain jelzi; ez minden visszaállítást felülbírál.
func progressWriteDefinitelyNotApplied(err error) bool {
	if err == nil || errors.Is(err, market.ErrProgressWriteUncertain) {
		return false
	}
	return marketFallbackSyncDefiniteRejection(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// A tartós visszaállítás a befoglaláskor elmentett, változatlan szándékra
// vonatkozik. Profilváltás vagy import után másik naplót nem írhat felül.
func (a *ItemWindowApp) restoreRejectedLocalFirstHandover(tx *handoverTransaction, op *handoverOperation) error {
	a.trackingMu.Lock()
	defer a.trackingMu.Unlock()
	stored, err := readHandover(a.handoverPath(tx.CharUID, tx.Mode), tx.CharUID, tx.Mode)
	if err != nil {
		return err
	}
	if stored.ID != tx.ID || stored.Version != 2 {
		return context.Canceled
	}
	for i := range stored.Items {
		if reflect.DeepEqual(stored.Items[i], *op) && stored.Items[i].Stage == "sent" {
			stored.Items[i].Stage = "unsent"
			if err := a.saveHandover(stored); err != nil {
				return err
			}
			op.Stage = "unsent"
			return nil
		}
	}
	return context.Canceled
}

func (a *ItemWindowApp) restoreRejectedLocalFirstStockEdit(pending *stockEditorPending) error {
	a.trackingMu.Lock()
	defer a.trackingMu.Unlock()
	stored, err := a.readStockEditorPending(ItemOptions{CharacterUID: pending.CharUID, Mode: pending.Mode})
	if err != nil {
		return err
	}
	if !sameStockEditorPending(stored, pending) || stored.Stage != "sent" {
		return context.Canceled
	}
	stored.Stage = "unsent"
	if err := a.saveStockEditorPending(stored); err != nil {
		return err
	}
	pending.Stage = "unsent"
	return nil
}
