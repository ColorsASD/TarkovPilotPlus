package plus

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

type pendingProfileWrite struct {
	key string
	stockWrite
}

type pendingProfileTrackingAPI struct {
	*fakeTrackingAPI
	mu       sync.Mutex
	routes   map[[2]string]*fakeTrackingAPI
	errors   map[[2]string]error
	reads    map[[2]string]int
	writes   []pendingProfileWrite
	readHook func(context.Context, string, string, int) error
}

func (f *pendingProfileTrackingAPI) LoadTracked(ctx context.Context, key, character, language string) (market.TrackedSnapshot, error) {
	identity := [2]string{key, character}
	f.mu.Lock()
	f.reads[identity]++
	count, route, err := f.reads[identity], f.routes[identity], f.errors[identity]
	f.mu.Unlock()
	if f.readHook != nil {
		if hookErr := f.readHook(ctx, key, character, count); hookErr != nil {
			return market.TrackedSnapshot{}, hookErr
		}
	}
	if err != nil {
		return market.TrackedSnapshot{}, err
	}
	if route == nil {
		return market.TrackedSnapshot{}, market.ErrProgressNotFound
	}
	return route.LoadTracked(ctx, key, character, language)
}

func (f *pendingProfileTrackingAPI) SetTrackedQuantity(ctx context.Context, key, character, item, revision string, quantity int64) (market.ProgressWriteResult, error) {
	f.mu.Lock()
	route := f.routes[[2]string{key, character}]
	f.writes = append(f.writes, pendingProfileWrite{key: key, stockWrite: stockWrite{character, item, revision, quantity}})
	f.mu.Unlock()
	if route == nil {
		return market.ProgressWriteResult{}, market.ErrProgressNotFound
	}
	return route.SetTrackedQuantity(ctx, key, character, item, revision, quantity)
}

func newPendingProfileTestApp(t *testing.T, stock int64) (*ItemWindowApp, *pendingProfileTrackingAPI, *fakeTrackingAPI) {
	t.Helper()
	a, old := newPendingRemoveTestApp(t, stock, false)
	fixture := &pendingProfileTrackingAPI{fakeTrackingAPI: old,
		routes: map[[2]string]*fakeTrackingAPI{{a.apiKey, a.state.CharacterUID}: old},
		errors: make(map[[2]string]error), reads: make(map[[2]string]int)}
	a.tracker = fixture
	return a, fixture, old
}

func switchPendingProfileForTest(a *ItemWindowApp, key string, remote market.TrackedSnapshot) {
	a.mu.Lock()
	a.apiKey = key
	a.state.CharacterUID, a.state.Mode = remote.CharUID, strings.ToLower(remote.GameMode)
	a.state.ItemOptions.revision++
	a.applyLocalMarketStockLocked(*a.state.Item, remote, "")
	a.mu.Unlock()
}

func pendingProfileSnapshot(character, mode string, stock int64) market.TrackedSnapshot {
	remote := trackingFixture()
	remote.CharUID, remote.GameMode, remote.Revision = character, mode, character+"-"+mode+"-initial"
	remote.Items[0].AvailableQuantity = stock
	remote.Items[0].RemainingQuantity = max(0, remote.Items[0].RequiredQuantity-stock)
	return remote
}

func TestPendingProfileRevokedKeyDoesNotBlockNewKeyAddOrRemove(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "add", true: "remove"}[remove], func(t *testing.T) {
			a, fixture, _ := newPendingProfileTestApp(t, 2)
			oldKey, character := a.apiKey, a.state.CharacterUID
			fixture.errors[[2]string{oldKey, character}] = market.ErrUnauthorized
			if remove {
				a.RemoveTrackedItem(trackingItemUID)
			} else {
				a.AddTrackedItem(trackingItemUID)
			}
			state := waitPendingForTest(t, a)
			if state.PendingAdds.Count != 1 || state.PendingAdds.Error == "" {
				t.Fatalf("revoked-key operation did not remain pending: %+v", state.PendingAdds)
			}
			const newKey = "valid-profile-fixture-key"
			newRemote := pendingProfileSnapshot(character, "PVP", 3)
			fixture.routes[[2]string{newKey, character}] = &fakeTrackingAPI{snapshot: newRemote}
			switchPendingProfileForTest(a, newKey, newRemote)
			if remove {
				a.RemoveTrackedItem(trackingItemUID)
			} else {
				a.AddTrackedItem(trackingItemUID)
			}
			state = waitPendingForTest(t, a)
			want := int64(4)
			if remove {
				want = 2
			}
			fixture.mu.Lock()
			writes := append([]pendingProfileWrite(nil), fixture.writes...)
			oldReads := fixture.reads[[2]string{oldKey, character}]
			fixture.mu.Unlock()
			a.mu.Lock()
			remaining := append([]pendingMarketAdd(nil), a.pendingAddQueue...)
			a.mu.Unlock()
			if len(writes) != 1 || writes[0].key != newKey || writes[0].Character != character || writes[0].Quantity != want || writes[0].Revision != newRemote.Revision || oldReads != 2 {
				t.Fatalf("revoked profile blocked or redirected the new profile: writes=%+v oldReads=%d", writes, oldReads)
			}
			if len(remaining) != 1 || remaining[0].Key != oldKey || remaining[0].Stage != "queued" || remaining[0].Error == "" || state.PendingAdds.Count != 1 || state.PendingAdds.Running || state.PendingAdds.Error == "" || a.GetPendingAddCount() != 1 || state.Tracking.MarketAvailableQuantity != want || state.Tracking.AvailableQuantity != want {
				t.Fatalf("old pending intent or its unsaved warning was discarded: pending=%+v state=%+v tracking=%+v", remaining, state.PendingAdds, state.Tracking)
			}
			assertNoMarketStockFilesForTest(t, a.dir)
		})
	}
}

func TestPendingProfileBlockedHeadKeepsSameProfileFIFOAndDrainsNewProfile(t *testing.T) {
	a, fixture, _ := newPendingProfileTestApp(t, 1)
	oldKey, oldCharacter := a.apiKey, a.state.CharacterUID
	fixture.errors[[2]string{oldKey, oldCharacter}] = market.ErrUnauthorized
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	fixture.readHook = func(ctx context.Context, key, character string, count int) error {
		if key == oldKey && character == oldCharacter && count == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	a.AddTrackedItem(trackingItemUID)
	<-started
	a.RemoveTrackedItem(trackingItemUID)
	newRemote := pendingProfileSnapshot("another-character", "PVE", 2)
	fixture.mu.Lock()
	fixture.routes[[2]string{oldKey, newRemote.CharUID}] = &fakeTrackingAPI{snapshot: newRemote}
	fixture.mu.Unlock()
	switchPendingProfileForTest(a, oldKey, newRemote)
	state := a.RemoveTrackedItem(trackingItemUID)
	if state.PendingAdds.Count != 3 || state.Tracking.AvailableQuantity != 1 {
		t.Fatalf("new profile did not reserve its own removal during the old request: %+v", state)
	}
	unblock()
	state = waitPendingForTest(t, a)
	fixture.mu.Lock()
	writes := append([]pendingProfileWrite(nil), fixture.writes...)
	oldReads := fixture.reads[[2]string{oldKey, oldCharacter}]
	fixture.mu.Unlock()
	a.mu.Lock()
	remaining := append([]pendingMarketAdd(nil), a.pendingAddQueue...)
	a.mu.Unlock()
	if len(writes) != 1 || writes[0].Character != newRemote.CharUID || writes[0].Quantity != 1 || oldReads != 1 || len(remaining) != 2 || remaining[0].ID >= remaining[1].ID || remaining[0].Delta != 1 || remaining[1].Delta != -1 || remaining[0].Error == "" || remaining[1].Error != "" || state.PendingAdds.Count != 2 || state.PendingAdds.Error == "" {
		t.Fatalf("a blocked profile was bypassed, retried, or blocked the other profile: writes=%+v reads=%d queue=%+v state=%+v", writes, oldReads, remaining, state.PendingAdds)
	}
	a.RecheckPendingAdds()
	state = waitPendingForTest(t, a)
	fixture.mu.Lock()
	readsAfterRecheck, writesAfterRecheck := fixture.reads[[2]string{oldKey, oldCharacter}], len(fixture.writes)
	fixture.mu.Unlock()
	if readsAfterRecheck != 2 || writesAfterRecheck != 1 || state.PendingAdds.Count != 2 || state.PendingAdds.Running {
		t.Fatalf("an explicit recheck retried a blocked profile more than once: reads=%d writes=%d state=%+v", readsAfterRecheck, writesAfterRecheck, state.PendingAdds)
	}
}

func TestPendingProfileInterleavedChangesUseOwnFreshStockAndFIFO(t *testing.T) {
	a, fixture, old := newPendingProfileTestApp(t, 2)
	keyA, characterA := a.apiKey, a.state.CharacterUID
	initialRevisionA := old.snapshot.Revision
	const keyB = "second-key"
	remoteB := pendingProfileSnapshot("second-character", "PVE", 2)
	fixture.routes[[2]string{keyB, remoteB.CharUID}] = &fakeTrackingAPI{snapshot: remoteB}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	fixture.readHook = func(ctx context.Context, key, _ string, count int) error {
		if key == keyA && count == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	a.AddTrackedItem(trackingItemUID)
	<-started
	switchPendingProfileForTest(a, keyB, remoteB)
	a.RemoveTrackedItem(trackingItemUID)
	switchPendingProfileForTest(a, keyA, cloneTrackedSnapshot(old.snapshot))
	a.RemoveTrackedItem(trackingItemUID)
	switchPendingProfileForTest(a, keyB, remoteB)
	a.AddTrackedItem(trackingItemUID)
	unblock()
	state := waitPendingForTest(t, a)
	fixture.mu.Lock()
	writes := append([]pendingProfileWrite(nil), fixture.writes...)
	fixture.mu.Unlock()
	want := []pendingProfileWrite{
		{keyA, stockWrite{characterA, trackingBSGID, initialRevisionA, 3}},
		{keyB, stockWrite{remoteB.CharUID, trackingBSGID, remoteB.Revision, 1}},
		{keyA, stockWrite{characterA, trackingBSGID, "revision-saved", 2}},
		{keyB, stockWrite{remoteB.CharUID, trackingBSGID, "revision-saved", 2}},
	}
	if !reflect.DeepEqual(writes, want) || state.PendingAdds.Count != 0 || state.PendingAdds.Error != "" || state.Tracking.AvailableQuantity != 2 || state.Tracking.MarketAvailableQuantity != 2 {
		t.Fatalf("interleaved profiles shared an absolute target or revision: got=%+v want=%+v pending=%+v tracking=%+v", writes, want, state.PendingAdds, state.Tracking)
	}
}

func TestPendingProfileSchedulerIdentityAndSingleAttempt(t *testing.T) {
	entry := func(id uint64, key, character, mode string) pendingMarketAdd {
		return pendingMarketAdd{ID: id, Key: key, Opts: ItemOptions{CharacterUID: character, Mode: mode}}
	}
	queue := []pendingMarketAdd{
		entry(1, "a", "char", "pvp"), entry(2, "a", "char", "PVP"),
		entry(3, "b", "char", "pvp"), entry(4, "a", "other", "pvp"),
		entry(5, "a", "char", "pve"),
	}
	attempted := map[uint64]struct{}{1: {}}
	for _, want := range []uint64{3, 4, 5} {
		next, ok := nextPendingMarketAdd(queue, attempted)
		if !ok || next.ID != want {
			t.Fatalf("profile tuple or FIFO mismatch: next=%+v ready=%v want=%d", next, ok, want)
		}
		attempted[next.ID] = struct{}{}
	}
	if next, ready := nextPendingMarketAdd(queue, attempted); ready {
		t.Fatalf("already attempted profile head was retried or bypassed: %+v", next)
	}
	queue = queue[1:]
	if next, ready := nextPendingMarketAdd(queue, attempted); !ready || next.ID != 2 {
		t.Fatalf("resolved head did not release its next FIFO entry: %+v ready=%v", next, ready)
	}
}
