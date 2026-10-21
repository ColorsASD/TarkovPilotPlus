package plus

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func waitAutomaticPendingDrainForTest(t *testing.T, a *ItemWindowApp) ItemState {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		state := a.GetState()
		if state.PendingAdds != nil && state.PendingAdds.Count == 0 && !state.PendingAdds.Running {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("automatic pending retries did not drain the queue: %+v", a.GetState().PendingAdds)
	return ItemState{}
}

func holdTypedPendingWorkerForTest(a *ItemWindowApp) {
	a.mu.Lock()
	a.pendingAddRunning = true
	a.mu.Unlock()
}

func startFastTypedPendingWorkerForTest(a *ItemWindowApp) {
	a.mu.Lock()
	for i := range a.pendingAddQueue {
		a.pendingAddQueue[i].retryDelay = time.Millisecond
	}
	a.pendingAddRunning = false
	a.startPendingAddWorkerLocked()
	a.mu.Unlock()
}

func TestTypedPendingAutomaticallyRecoversRateLimitAndPreSendTimeout(t *testing.T) {
	for _, stage := range []string{"before-read", "after-write-read", "write-429", "write-pre-send-deadline"} {
		t.Run(stage, func(t *testing.T) {
			a, fake := newTypedActionTestApp(t, 0, 0)
			var attempts atomic.Int32
			fake.loadHook = func(read int) (market.TrackedSnapshot, error) {
				if stage == "before-read" && read == 1 || stage == "after-write-read" && read == 2 {
					return market.TrackedSnapshot{}, market.ErrRateLimited
				}
				fake.mu.Lock()
				defer fake.mu.Unlock()
				return cloneTrackedSnapshot(fake.snapshot), nil
			}
			fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
				if attempts.Add(1) == 1 {
					if stage == "write-429" {
						return market.ProgressWriteResult{}, market.ErrRateLimited
					}
					if stage == "write-pre-send-deadline" {
						return market.ProgressWriteResult{}, context.DeadlineExceeded
					}
				}
				return fake.commitWrite(write), nil
			}
			holdTypedPendingWorkerForTest(a)
			a.AddRegularTrackedItem(trackingItemUID)
			startFastTypedPendingWorkerForTest(a)
			state := waitAutomaticPendingDrainForTest(t, a)
			if state.PendingAdds.Error != "" || state.Tracking.AvailableQuantity != 1 || state.Tracking.RegularQuantity != 1 || state.Tracking.FIRQuantity != 0 {
				t.Fatalf("automatic retry lost or duplicated the typed intent: pending=%+v tracking=%+v", state.PendingAdds, state.Tracking)
			}
			wantWrites := int32(1)
			if stage == "write-429" || stage == "write-pre-send-deadline" {
				wantWrites = 2
			}
			if attempts.Load() != wantWrites || fake.commits != 1 {
				t.Fatalf("unexpected PATCH retries: attempts=%d commits=%d", attempts.Load(), fake.commits)
			}
		})
	}
}

func TestTypedPendingUncertainRateLimitOnlyRepeatsReads(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 0, 0)
	ctx, cancel := context.WithCancel(a.ctx)
	a.ctx = ctx
	t.Cleanup(cancel)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, errors.Join(market.ErrProgressWriteUncertain, market.ErrRateLimited)
	}
	holdTypedPendingWorkerForTest(a)
	a.AddFIRTrackedItem(trackingItemUID)
	startFastTypedPendingWorkerForTest(a)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		reads, writes := trackedHotkeyAPICalls(fake)
		if len(writes) > 1 {
			t.Fatalf("uncertain PATCH was replayed: %+v", writes)
		}
		if reads >= 4 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	state := waitPendingForTest(t, a)
	reads, writes := trackedHotkeyAPICalls(fake)
	if reads < 4 || len(writes) != 1 || state.PendingAdds.Count != 1 || state.Tracking.FIRQuantity != 1 {
		t.Fatalf("uncertain change did not remain one pending intent with repeated reads only: reads=%d writes=%+v pending=%+v tracking=%+v", reads, writes, state.PendingAdds, state.Tracking)
	}
}

func TestTypedPendingRapidMixedQueueSurvivesIntermittentThrottling(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 0, 0)
	for i := range fake.snapshot.Items[0].Sources {
		fake.snapshot.Items[0].Sources[i].Amount, fake.snapshot.Items[0].Sources[i].RemainingRequiredQuantity = 60, 60
	}
	fake.snapshot.Items[0].RequiredQuantity, fake.snapshot.Items[0].RemainingQuantity = 120, 120
	remote := cloneTrackedSnapshot(fake.snapshot)
	merged := marketAnyProgressSnapshotForTest(t, remote, typedActionQuests(remote).Quests...)
	a.mu.Lock()
	a.applyMergedMarketSnapshotLocked(*a.state.Item, remote, merged, "")
	a.mu.Unlock()
	var attempts atomic.Int32
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		attempt := attempts.Add(1)
		if attempt%7 == 0 {
			return market.ProgressWriteResult{}, market.ErrRateLimited
		}
		if attempt%11 == 0 {
			return market.ProgressWriteResult{}, context.DeadlineExceeded
		}
		result := fake.commitWrite(write)
		if attempt%17 == 0 {
			return market.ProgressWriteResult{}, errors.Join(market.ErrProgressWriteUncertain, market.ErrRateLimited)
		}
		return result, nil
	}
	fake.loadHook = func(read int) (market.TrackedSnapshot, error) {
		if read%13 == 0 {
			return market.TrackedSnapshot{}, market.ErrRateLimited
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return cloneTrackedSnapshot(fake.snapshot), nil
	}
	holdTypedPendingWorkerForTest(a)
	const total = 96
	for i := 0; i < total; i++ {
		if i%2 == 0 {
			a.AddFIRTrackedItem(trackingItemUID)
		} else {
			a.AddRegularTrackedItem(trackingItemUID)
		}
	}
	if count := a.GetPendingAddCount(); count != total {
		t.Fatalf("rapid typed actions accepted %d of %d", count, total)
	}
	startFastTypedPendingWorkerForTest(a)
	state := waitAutomaticPendingDrainForTest(t, a)
	if state.PendingAdds.Error != "" || state.Tracking.AvailableQuantity != total || state.Tracking.FIRQuantity != total/2 || state.Tracking.RegularQuantity != total/2 || fake.commits != total {
		t.Fatalf("throttled rapid queue lost or duplicated stock/quality: commits=%d pending=%+v tracking=%+v", fake.commits, state.PendingAdds, state.Tracking)
	}
	if attempts.Load() <= total {
		t.Fatal("stress fixture did not exercise rejected PATCH retries")
	}
}

func TestPendingSchedulerBackoffPreservesProfileFIFOAndAllowsAnotherProfile(t *testing.T) {
	now := time.Now()
	queue := []pendingMarketAdd{
		{ID: 1, Key: "a", Opts: ItemOptions{CharacterUID: "char-a", Mode: "pvp"}, RetryAt: now.Add(time.Hour)},
		{ID: 2, Key: "a", Opts: ItemOptions{CharacterUID: "char-a", Mode: "pvp"}},
		{ID: 3, Key: "b", Opts: ItemOptions{CharacterUID: "char-b", Mode: "pve"}},
	}
	entry, ready := nextPendingMarketAdd(queue, map[uint64]struct{}{})
	if !ready || entry.ID != 3 {
		t.Fatalf("backoff blocked another profile or let its own tail overtake: %+v %v", entry, ready)
	}
	queue[0].RetryAt = now.Add(-time.Second)
	entry, ready = nextPendingMarketAdd(queue, map[uint64]struct{}{1: {}})
	if !ready || entry.ID != 1 {
		t.Fatalf("expired retry was lost when its timer fired during an active worker: %+v %v", entry, ready)
	}
}

type pendingRetryProfileTrackingAPI struct{ *pendingProfileTrackingAPI }

func (f *pendingRetryProfileTrackingAPI) progressRemote(key, character string) market.TrackedSnapshot {
	f.mu.Lock()
	route := f.routes[[2]string{key, character}]
	f.mu.Unlock()
	route.mu.Lock()
	defer route.mu.Unlock()
	return cloneTrackedSnapshot(route.snapshot)
}

func (f *pendingRetryProfileTrackingAPI) LoadQuests(_ context.Context, key, character, _ string) (market.QuestSnapshot, error) {
	return typedActionQuests(f.progressRemote(key, character)), nil
}

func (f *pendingRetryProfileTrackingAPI) LoadHideout(_ context.Context, key, character, _ string) (market.HideoutSnapshot, error) {
	remote := f.progressRemote(key, character)
	return market.HideoutSnapshot{CharUID: remote.CharUID, GameMode: remote.GameMode, Revision: remote.Revision, Modules: []market.HideoutUpgrade{}}, nil
}

func TestPendingRecheckDuringAnotherProfileWriteDoesNotLoseBackoffWakeup(t *testing.T) {
	a, first := newTypedActionTestApp(t, 0, 0)
	const secondKey, secondCharacter = "second-profile-key", "second-character"
	secondRemote := cloneTrackedSnapshot(first.snapshot)
	secondRemote.CharUID, secondRemote.Revision = secondCharacter, "second-initial"
	second := &fakeTrackingAPI{snapshot: secondRemote}
	profiles := &pendingProfileTrackingAPI{fakeTrackingAPI: first,
		routes: map[[2]string]*fakeTrackingAPI{{a.apiKey, a.state.CharacterUID}: first, {secondKey, secondCharacter}: second},
		errors: map[[2]string]error{}, reads: map[[2]string]int{}}
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	profiles.readHook = func(ctx context.Context, key, _ string, count int) error {
		if key == a.apiKey && count == 1 {
			return market.ErrRateLimited
		}
		if key == secondKey && count == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	a.tracker = &pendingRetryProfileTrackingAPI{profiles}
	holdTypedPendingWorkerForTest(a)
	a.AddFIRTrackedItem(trackingItemUID)
	a.mu.Lock()
	a.pendingAddQueue[0].retryDelay = time.Hour
	opts := a.state.ItemOptions
	opts.CharacterUID = secondCharacter
	a.pendingAddNextID++
	a.pendingAddQueue = append(a.pendingAddQueue, pendingMarketAdd{ID: a.pendingAddNextID, Key: secondKey, Opts: opts,
		Item: *a.state.Item, Delta: 1, Typed: true, FIR: false, Stage: "queued"})
	a.pendingAddRunning = false
	a.startPendingAddWorkerLocked()
	a.mu.Unlock()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("second profile did not run while the first was backing off")
	}
	a.RecheckPendingAdds()
	a.mu.Lock()
	retryAt, running := a.pendingAddQueue[0].RetryAt, a.pendingAddRunning
	a.mu.Unlock()
	if !running || retryAt.IsZero() || retryAt.After(time.Now()) {
		t.Fatalf("recheck did not retain an expired wakeup while another profile was active: running=%v retryAt=%v", running, retryAt)
	}
	unblock()
	state := waitAutomaticPendingDrainForTest(t, a)
	if state.PendingAdds.Error != "" || first.commits != 1 || second.commits != 1 || state.Tracking.FIRQuantity != 1 {
		t.Fatalf("manual recheck lost the first profile's wakeup or duplicated writes: first=%d second=%d pending=%+v tracking=%+v", first.commits, second.commits, state.PendingAdds, state.Tracking)
	}
	profiles.mu.Lock()
	writes := append([]pendingProfileWrite(nil), profiles.writes...)
	profiles.mu.Unlock()
	if len(writes) != 2 || writes[0].key != secondKey || writes[1].key != a.apiKey {
		t.Fatalf("backoff/recheck broke profile FIFO progress: %+v", writes)
	}
}
