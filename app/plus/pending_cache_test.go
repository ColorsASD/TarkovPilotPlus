package plus

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

type pendingCountedProgressAPI struct {
	*typedActionTrackingFixture
	quests, hideout atomic.Int32
	afterHideout    func(int32)
}

func (f *pendingCountedProgressAPI) LoadQuests(ctx context.Context, key, character, language string) (market.QuestSnapshot, error) {
	f.quests.Add(1)
	return f.typedActionTrackingFixture.LoadQuests(ctx, key, character, language)
}

func (f *pendingCountedProgressAPI) LoadHideout(ctx context.Context, key, character, language string) (market.HideoutSnapshot, error) {
	count := f.hideout.Add(1)
	result, err := f.typedActionTrackingFixture.LoadHideout(ctx, key, character, language)
	if f.afterHideout != nil {
		f.afterHideout(count)
	}
	return result, err
}

func TestTypedPendingVerifiedPreflightUsesFourRequestsPerFollowingIntent(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 0, 0)
	counted := &pendingCountedProgressAPI{typedActionTrackingFixture: &typedActionTrackingFixture{fake}}
	a.tracker = counted
	holdTypedPendingWorkerForTest(a)
	for i := 0; i < 4; i++ {
		if i%2 == 0 {
			a.AddFIRTrackedItem(trackingItemUID)
		} else {
			a.AddRegularTrackedItem(trackingItemUID)
		}
	}
	startFastTypedPendingWorkerForTest(a)
	state := waitAutomaticPendingDrainForTest(t, a)
	reads, writes := trackedHotkeyAPICalls(fake)
	if len(writes) != 4 || reads != 5 || counted.quests.Load() != 5 || counted.hideout.Load() != 5 {
		t.Fatalf("adjacent verified intents did not reuse one full matching preflight snapshot: tracked=%d quests=%d hideout=%d writes=%d", reads, counted.quests.Load(), counted.hideout.Load(), len(writes))
	}
	if state.PendingAdds.Error != "" || state.Tracking.FIRQuantity != 2 || state.Tracking.RegularQuantity != 2 || state.Tracking.AvailableQuantity != 4 {
		t.Fatalf("verified cache changed typed stock: %+v", state.Tracking)
	}
}

func TestFreshProgressInvalidatesOnlyOlderCachesOfItsOwnProfile(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 0, 0)
	snapshot := localprogress.Clone(a.confirmedProgress)
	snapshot.Revision = "fresh-completion-revision"
	base := pendingMarketAdd{Typed: true, Stage: "queued", Key: a.apiKey, Opts: a.state.ItemOptions}
	for index := 0; index < 6; index++ {
		entry := base
		entry.verified = &pendingVerifiedProgress{key: entry.Key, language: entry.Opts.Language,
			remote: cloneTrackedSnapshot(fake.snapshot), progress: localprogress.Clone(a.confirmedProgress), at: time.Now()}
		switch index {
		case 1:
			entry.Key += "-other-account"
		case 2:
			entry.Opts.CharacterUID += "-other-character"
		case 3:
			entry.Opts.Mode = "PVE"
		case 4:
			entry.verified.progress.Revision = snapshot.Revision
			entry.verified.remote.Revision = snapshot.Revision
		case 5:
			entry.Opts.Language, entry.verified.language = "hu", "hu"
		}
		a.pendingAddQueue = append(a.pendingAddQueue, entry)
	}
	incomplete := localprogress.Clone(snapshot)
	incomplete.MetadataComplete = false
	a.invalidatePendingProgressCachesLocked(a.apiKey, a.state.ItemOptions, incomplete)
	if a.pendingAddQueue[0].verified == nil {
		t.Fatal("incomplete metadata invalidated a verified cache")
	}
	a.invalidatePendingProgressCachesLocked(a.apiKey, a.state.ItemOptions, snapshot)
	for index, entry := range a.pendingAddQueue {
		wantRetained := index != 0 && index != 5
		if (entry.verified != nil) != wantRetained {
			t.Fatalf("profile-scoped cache invalidation is incorrect at %d: retained=%v", index, entry.verified != nil)
		}
	}
	assertQualityStoreNoWrites(t, fake)
}

func TestFreshSnapshotPublicationDropsCachedPreflightBeforeNextStockWrite(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 0, 0)
	holdTypedPendingWorkerForTest(a)
	a.AddRegularTrackedItem(trackingItemUID)
	a.mu.Lock()
	a.pendingAddQueue[0].verified = &pendingVerifiedProgress{key: a.apiKey, language: a.state.ItemOptions.Language,
		remote: cloneTrackedSnapshot(fake.snapshot), progress: localprogress.Clone(a.confirmedProgress), at: time.Now()}
	a.mu.Unlock()
	qualityStoreRemoteTotal(t, a, fake, 0, "fresh-metadata-before-worker")
	a.mu.Lock()
	retained := a.pendingAddQueue[0].verified != nil
	a.mu.Unlock()
	if retained {
		t.Fatal("new API publication retained an earlier revision's quest/hideout cache")
	}
	startFastTypedPendingWorkerForTest(a)
	state := waitAutomaticPendingDrainForTest(t, a)
	reads, writes := trackedHotkeyAPICalls(fake)
	if reads != 2 || len(writes) != 1 || writes[0].Revision != "fresh-metadata-before-worker" || state.Tracking.RegularQuantity != 1 {
		t.Fatalf("worker reused obsolete metadata instead of reading the current revision: reads=%d writes=%+v state=%+v", reads, writes, state.Tracking)
	}
}

func TestTypedPendingVerifiedCacheConflictRereadsFreshStockAndMetadata(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 0, 0)
	counted := &pendingCountedProgressAPI{typedActionTrackingFixture: &typedActionTrackingFixture{fake}}
	counted.afterHideout = func(count int32) {
		if count == 2 {
			setPendingACKExternalStock(fake, 2, "external-after-verified-predecessor")
		}
	}
	a.tracker = counted
	var conflicts atomic.Int32
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		fake.mu.Lock()
		revision := fake.snapshot.Revision
		fake.mu.Unlock()
		if write.Revision != revision {
			conflicts.Add(1)
			return market.ProgressWriteResult{}, market.ErrProgressConflict
		}
		return fake.commitWrite(write), nil
	}
	holdTypedPendingWorkerForTest(a)
	a.AddRegularTrackedItem(trackingItemUID)
	a.AddFIRTrackedItem(trackingItemUID)
	startFastTypedPendingWorkerForTest(a)
	state := waitAutomaticPendingDrainForTest(t, a)
	reads, writes := trackedHotkeyAPICalls(fake)
	if conflicts.Load() != 1 || fake.commits != 2 || len(writes) != 3 || reads != 4 || counted.quests.Load() != 4 || counted.hideout.Load() != 4 {
		t.Fatalf("cached CAS conflict did not perform a fresh full preflight: conflicts=%d commits=%d reads=%d quests=%d hideout=%d writes=%+v", conflicts.Load(), fake.commits, reads, counted.quests.Load(), counted.hideout.Load(), writes)
	}
	if writes[2].Revision != "external-after-verified-predecessor" || writes[2].Quantity != 3 || state.Tracking.AvailableQuantity != 3 || state.Tracking.FIRQuantity != 1 || state.Tracking.RegularQuantity != 1 || state.Tracking.UncategorizedQuantity != 1 || state.PendingAdds.Error != "" {
		t.Fatalf("cached operation overwrote external stock or invented its quality: pending=%+v tracking=%+v writes=%+v", state.PendingAdds, state.Tracking, writes)
	}
}

func TestPendingVerifiedCacheRequiresQueuedFreshMatchingProfileAndClonesData(t *testing.T) {
	a, fake := newTypedActionTestApp(t, 0, 0)
	now := time.Now()
	base := pendingMarketAdd{Typed: true, Stage: "queued", Key: a.apiKey, Opts: a.state.ItemOptions,
		verified: &pendingVerifiedProgress{key: a.apiKey, language: a.state.Language, remote: cloneTrackedSnapshot(fake.snapshot), progress: localprogress.Clone(a.confirmedProgress), at: now}}
	remote, progress, ok := cachedPendingProgress(base, now)
	if !ok {
		t.Fatal("valid verified cache was rejected")
	}
	remote.Items[0].AvailableQuantity, progress.Items[0].AvailableQuantity = 99, 99
	if base.verified.remote.Items[0].AvailableQuantity != 0 || base.verified.progress.Items[0].AvailableQuantity != 0 {
		t.Fatal("cached snapshots shared mutable item storage")
	}
	for _, change := range []func(*pendingMarketAdd){
		func(e *pendingMarketAdd) { e.Stage = "sent" },
		func(e *pendingMarketAdd) { e.Typed = false },
		func(e *pendingMarketAdd) { e.Key = "another-key" },
		func(e *pendingMarketAdd) { e.Opts.CharacterUID = "another-character" },
		func(e *pendingMarketAdd) { e.Opts.Mode = "pve" },
		func(e *pendingMarketAdd) { e.Opts.Language = "hu" },
		func(e *pendingMarketAdd) { e.verified.at = now.Add(-pendingVerifiedProgressTTL - time.Millisecond) },
		func(e *pendingMarketAdd) { e.verified.progress.Revision = "another-revision" },
		func(e *pendingMarketAdd) { e.verified.progress.MetadataComplete = false },
	} {
		entry := base
		cache := *base.verified
		entry.verified = &cache
		change(&entry)
		if _, _, ok := cachedPendingProgress(entry, now); ok {
			t.Fatalf("cache accepted an incompatible or sent intent: %+v", entry)
		}
	}
}
