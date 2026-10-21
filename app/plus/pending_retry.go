package plus

import (
	"context"
	"errors"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func retryablePendingMarketError(err error) bool {
	return errors.Is(err, market.ErrRateLimited) || errors.Is(err, market.ErrProgressUnavailable) ||
		errors.Is(err, market.ErrProgressConflict) || errors.Is(err, localprogress.ErrSnapshotMismatch) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

func definitelyUnappliedRetryablePendingWrite(err error) bool {
	return !errors.Is(err, market.ErrProgressWriteUncertain) && (errors.Is(err, market.ErrRateLimited) ||
		errors.Is(err, market.ErrProgressConflict) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
}

// A Market-kapu saját Retry-After várakozását megtartjuk. Ez a ritkított,
// megszakítható újraindítás csak a sor felélesztését biztosítja; nem küld PATCH-et.
func (a *ItemWindowApp) retryPendingMarketChange(entry pendingMarketAdd, failure error, message string) bool {
	if !entry.Typed || !retryablePendingMarketError(failure) {
		return false
	}
	a.mu.Lock()
	i := a.pendingAddIndexLocked(entry.ID)
	if i < 0 || a.ctx == nil || a.ctx.Err() != nil {
		a.mu.Unlock()
		return false
	}
	current := &a.pendingAddQueue[i]
	current.verified = nil
	current.RetryCount++
	delay := time.Second
	if current.retryDelay > 0 {
		delay = current.retryDelay
	}
	for count := 1; count < current.RetryCount && delay < 30*time.Second; count++ {
		delay *= 2
	}
	delay = min(delay, 30*time.Second)
	current.RetryAt, current.Error = time.Now().Add(delay), message
	retryAt, parent := current.RetryAt, a.ctx
	a.pendingAddError = message
	a.updatePendingAddStateLocked()
	if a.pendingAddProfileLocked(*current) && a.state.Item != nil && a.state.Item.UID == current.Item.UID && a.state.Tracking != nil {
		a.state.Tracking.Status = message
	}
	a.mu.Unlock()
	a.emit()
	go func() {
		timer := time.NewTimer(time.Until(retryAt))
		defer timer.Stop()
		select {
		case <-parent.Done():
			return
		case <-timer.C:
		}
		a.mu.Lock()
		if i := a.pendingAddIndexLocked(entry.ID); i >= 0 && a.pendingAddQueue[i].RetryAt.Equal(retryAt) {
			a.startPendingAddWorkerLocked()
		}
		a.mu.Unlock()
	}()
	return true
}
