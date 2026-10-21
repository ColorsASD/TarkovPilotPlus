package plus

import (
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

const pendingVerifiedProgressTTL = 2 * time.Second

// A közvetlen előző művelet már teljesen visszaolvasott API-pillanatképe.
// A következő PATCH ugyanazt az If-Match védelmet használja; ütközéskor új
// teljes olvasás készül. Elküldött vagy újrapróbált művelet ezt nem használja.
type pendingVerifiedProgress struct {
	key, language string
	remote        market.TrackedSnapshot
	progress      localprogress.Snapshot
	at            time.Time
}

func cachedPendingProgress(entry pendingMarketAdd, now time.Time) (market.TrackedSnapshot, localprogress.Snapshot, bool) {
	cache := entry.verified
	if !entry.Typed || entry.Stage != "queued" || cache == nil || cache.key != entry.Key || cache.language != entry.Opts.Language ||
		cache.at.After(now) || now.Sub(cache.at) > pendingVerifiedProgressTTL ||
		cache.remote.CharUID != entry.Opts.CharacterUID || !strings.EqualFold(cache.remote.GameMode, entry.Opts.Mode) ||
		cache.remote.Items == nil || cache.remote.Revision == "" || cache.remote.Revision != cache.progress.Revision ||
		cache.progress.CharUID != cache.remote.CharUID || cache.progress.GameMode != cache.remote.GameMode || !cache.progress.MetadataComplete {
		return market.TrackedSnapshot{}, localprogress.Snapshot{}, false
	}
	return cloneTrackedSnapshot(cache.remote), localprogress.Clone(cache.progress), true
}

// Egy új teljes API-pillanatkép után a régebbi revízió cache-e nem lehet
// a következő művelet alapja: a cél teljesítése a két másodpercen belül is
// megváltozhatott. Más karakter vagy játékmód gyorsítótárát nem érinti.
func (a *ItemWindowApp) invalidatePendingProgressCachesLocked(key string, opts ItemOptions, snapshot localprogress.Snapshot) {
	if !snapshot.MetadataComplete || snapshot.Revision == "" || snapshot.CharUID != opts.CharacterUID || !strings.EqualFold(snapshot.GameMode, opts.Mode) {
		return
	}
	for i := range a.pendingAddQueue {
		entry := &a.pendingAddQueue[i]
		if entry.Key == key && entry.Opts.CharacterUID == opts.CharacterUID && strings.EqualFold(entry.Opts.Mode, opts.Mode) &&
			entry.verified != nil && entry.verified.progress.Revision != snapshot.Revision {
			entry.verified = nil
		}
	}
}

func (a *ItemWindowApp) cachePendingProgressLocked(entry pendingMarketAdd, remote market.TrackedSnapshot) {
	if !entry.Typed || !entry.Progress.MetadataComplete || entry.Progress.Revision != remote.Revision ||
		!pendingMarketChangeConfirmed(entry, remote) {
		return
	}
	for i := range a.pendingAddQueue {
		next := &a.pendingAddQueue[i]
		if next.Key != entry.Key || next.Opts.CharacterUID != entry.Opts.CharacterUID || !strings.EqualFold(next.Opts.Mode, entry.Opts.Mode) {
			continue
		}
		if next.Typed && next.Stage == "queued" && next.Opts.Language == entry.Opts.Language && next.RetryAt.IsZero() {
			next.verified = &pendingVerifiedProgress{key: entry.Key, language: entry.Opts.Language,
				remote: cloneTrackedSnapshot(remote), progress: localprogress.Clone(entry.Progress), at: time.Now()}
		}
		return
	}
}
