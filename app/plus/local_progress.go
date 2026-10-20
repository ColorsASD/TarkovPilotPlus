package plus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/nextitem"
)

type characterProgressAPI interface {
	LoadQuests(context.Context, string, string, string) (market.QuestSnapshot, error)
	LoadHideout(context.Context, string, string, string) (market.HideoutSnapshot, error)
}

type localProgressLoadError struct {
	stage string
	cause error
}

func (e *localProgressLoadError) Error() string { return e.stage + ": " + e.cause.Error() }
func (e *localProgressLoadError) Unwrap() error { return e.cause }
func localProgressFailure(stage string, err error) error {
	return &localProgressLoadError{stage: stage, cause: err}
}

func localProgressErrorText(err error) string {
	var failure *localProgressLoadError
	if !errors.As(err, &failure) {
		return "Market progress is unavailable. Refresh progress again."
	}
	switch failure.stage {
	case "tracked items", "quests", "hideout":
		return "Tarkov Market " + failure.stage + " could not be loaded. " + trackingError(err)
	case "synchronization":
		return "Market progress changed during refresh. Refresh progress again."
	case "merge":
		return "Tarkov Market requirements could not be loaded consistently. Refresh progress again."

	}
	return "Market progress is unavailable. Refresh progress again."
}

type LocalProgressState struct {
	Loading   bool      `json:"loading"`
	Error     string    `json:"error"`
	Warning   string    `json:"warning"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func cloneLocalProgressState(state *LocalProgressState) *LocalProgressState {
	if state == nil {
		return nil
	}
	copy := *state
	return &copy
}

func localMarketSnapshot(snapshot localprogress.Snapshot) market.TrackedSnapshot {
	result := market.TrackedSnapshot{CharUID: snapshot.CharUID, GameMode: snapshot.GameMode, Revision: snapshot.Revision, FIRInventorySeparate: snapshot.FIRInventorySeparate}
	for _, item := range snapshot.Items {
		row := market.TrackedItem{ItemID: item.ItemID, Name: item.Name, ShortName: item.ShortName, Slots: item.Slots, Icon: item.Icon,
			RequiredQuantity: item.RequiredQuantity, SpentQuantity: item.SpentQuantity, AvailableQuantity: item.AvailableQuantity, RemainingQuantity: item.RemainingQuantity}
		if item.CollectionCalculated {
			row.RequiredQuantity, row.RemainingQuantity = item.CollectionRequiredQuantity, item.CollectionRemainingQuantity
		}
		for _, source := range item.Sources {
			projected := source.TrackedSource
			if source.AllocationKnown && source.Alternatives > 1 {
				projected.RemainingRequiredQuantity = 0
				if source.GroupRemainingQuantity > 0 {
					projected.RemainingRequiredQuantity = source.AllocatedQuantity + source.GroupRemainingQuantity
				}
			}
			row.Sources = append(row.Sources, projected)
		}
		result.Items = append(result.Items, row)
	}
	return result
}

// A teljes számítás kizárólag a Market válaszából készül, memóriában.
func marketProgressOnly(snapshot market.TrackedSnapshot) localprogress.Snapshot {
	merged, err := localprogress.Merge(snapshot,
		market.QuestSnapshot{CharUID: snapshot.CharUID, GameMode: snapshot.GameMode, Revision: snapshot.Revision, Quests: []market.Quest{}},
		market.HideoutSnapshot{CharUID: snapshot.CharUID, GameMode: snapshot.GameMode, Revision: snapshot.Revision, Modules: []market.HideoutUpgrade{}})
	if err == nil {
		merged.MetadataComplete = false
		return merged
	}
	result := localprogress.Snapshot{CharUID: snapshot.CharUID, GameMode: snapshot.GameMode, Revision: snapshot.Revision,
		FIRInventorySeparate: snapshot.FIRInventorySeparate, Items: []localprogress.Item{}}
	result.MarketGroups = cloneTrackedSnapshot(snapshot).Groups
	for _, item := range snapshot.Items {
		row := localprogress.Item{ItemID: item.ItemID, Name: item.Name, ShortName: item.ShortName, Slots: item.Slots, Icon: item.Icon,
			RequiredQuantity: item.RequiredQuantity, SpentQuantity: item.SpentQuantity, AvailableQuantity: item.AvailableQuantity,
			RemainingQuantity: item.RemainingQuantity, MarketTracked: true, MarketRequiredQuantity: item.RequiredQuantity,
			MarketAvailableQuantity: item.AvailableQuantity, Sources: []localprogress.Source{}}
		for _, source := range item.Sources {
			row.Sources = append(row.Sources, localprogress.Source{TrackedSource: source, Origin: localprogress.OriginMarket})
		}
		result.Items = append(result.Items, row)
	}
	return result
}

func alternativeNote(source localprogress.Source) string {
	if source.Alternatives <= 1 || source.Completed || source.Canceled || source.Optional || source.RemainingRequiredQuantity <= 0 {
		return ""
	}
	return fmt.Sprintf("Shared requirement: %d of %d eligible items", source.RemainingRequiredQuantity, source.Alternatives)
}

func localTrackedView(item market.Item, snapshot localprogress.Snapshot) *ItemTrackingState {
	if allocated, err := localprogress.Allocate(snapshot); err == nil {
		snapshot = allocated
	}
	confirmedRequirements := localprogress.ConfirmedRequirementStock(snapshot)
	view := trackedView(item, localMarketSnapshot(snapshot))
	view.Storage = "market"
	if len(view.Variants) != 0 {
		// trackedView tartja a Market eredeti sorsorrendjét. Név vagy az első
		// közös ID helyett minden panelt a saját rekordjához kötünk.
		index := 0
		for rowIndex, row := range snapshot.Items {
			if row.ItemID != item.BSGID {
				continue
			}
			variant := view.Variants[index]
			populateVariantTrackingView(variant, row, snapshot, confirmedRequirements[rowIndex])
			if view.Tracked && view.VariantName == variant.VariantName {
				variants, message := view.Variants, view.Error
				*view = *cloneTrackingState(variant)
				view.Variants, view.Error = variants, message
			}
			index++
		}
		return view
	}
	for _, row := range snapshot.Items {
		if row.ItemID == item.BSGID {
			view.Storage = "market"
			view.MarketAvailableQuantity = row.MarketAvailableQuantity
			view.MarketRequiredQuantity = row.MarketRequiredQuantity
			view.MarketTracked = row.MarketTracked
			for i := range view.Requirements {
				if view.Requirements[i].Kind == "fir" {
					view.Requirements[i].AvailableQuantity = row.CollectionFIRAvailableQuantity
				} else {
					view.Requirements[i].AvailableQuantity = row.CollectionRegularAvailableQuantity
				}
			}
			break
		}
	}
	notes := make(map[string]bool)
	for rowIndex, row := range snapshot.Items {
		if row.ItemID != item.BSGID {
			continue
		}
		for _, source := range row.Sources {
			if source.Completed || source.Canceled || source.Optional || source.RemainingRequiredQuantity <= 0 {
				continue
			}
			if note := alternativeNote(source); note != "" && !notes[source.GroupID] {
				notes[source.GroupID] = true
				view.AlternativeNotes = append(view.AlternativeNotes, note)
				if source.AllocationKnown && source.GroupRemainingQuantity > 0 {
					view.AnyGroups = append(view.AnyGroups, ItemTrackingAnyGroup{
						GroupID: source.GroupID, FIR: source.FIR,
						RequiredQuantity:  source.RemainingRequiredQuantity,
						AvailableQuantity: source.RemainingRequiredQuantity - source.GroupRemainingQuantity,
					})
				}
			}
		}
		simplifyLocalTrackingView(view, row, snapshot, confirmedRequirements[rowIndex])
		if !view.InventorySeparate && view.Error == "" {
			view.AddAllowed, _ = localprogress.AddAllowed(snapshot, item.BSGID)
		}
		break
	}
	if view.Tracked && view.Error == "" && !view.InventorySeparate {
		forecast := nextitem.Calculate(snapshot, item.BSGID)
		view.NextItem = &forecast
	}
	return view
}

func (a *ItemWindowApp) loadLocalSources(ctx context.Context, key string, opts ItemOptions) (localprogress.Snapshot, string, error) {
	progress, hasProgress := a.tracker.(characterProgressAPI)
	var latest market.TrackedSnapshot
	for attempt := 0; attempt < 2; attempt++ {
		tracked, err := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
		if err != nil {
			return localprogress.Snapshot{}, "", localProgressFailure("tracked items", err)
		}
		if tracked.CharUID != opts.CharacterUID || !strings.EqualFold(tracked.GameMode, opts.Mode) || tracked.Revision == "" || tracked.Items == nil {
			return localprogress.Snapshot{}, "", localProgressFailure("tracked items", market.ErrProgressNotFound)
		}
		latest = tracked
		a.mu.Lock()
		if a.localCurrentLocked(key, opts) {
			a.reconcilePendingSnapshotLocked(tracked)
		}
		a.mu.Unlock()
		if !hasProgress {
			return marketProgressOnly(tracked), "", nil
		}
		quests, err := progress.LoadQuests(ctx, key, opts.CharacterUID, opts.Language)
		if err != nil {
			return marketProgressOnly(tracked), "", localProgressFailure("quests", err)
		}
		hideout, err := progress.LoadHideout(ctx, key, opts.CharacterUID, opts.Language)
		if err != nil {
			return marketProgressOnly(tracked), "", localProgressFailure("hideout", err)
		}
		if tracked.Revision != quests.Revision || tracked.Revision != hideout.Revision {
			continue
		}
		merged, err := localprogress.Merge(tracked, quests, hideout)
		if err != nil {
			return marketProgressOnly(tracked), "", localProgressFailure("merge", err)
		}
		return merged, "", nil
	}
	return marketProgressOnly(latest), "", localProgressFailure("synchronization", market.ErrProgressConflict)
}

func (a *ItemWindowApp) localCurrentLocked(key string, opts ItemOptions) bool {
	return a.apiKey == key && a.state.ItemOptions.revision == opts.revision && a.state.CharacterUID == opts.CharacterUID
}

func (a *ItemWindowApp) localItemCurrentLocked(item market.Item) bool {
	return item.UID != "" && a.state.Item != nil && a.state.Item.UID == item.UID && a.state.Item.BSGID == item.BSGID
}

func (a *ItemWindowApp) refreshLocalProgress(ctx context.Context, key string, opts ItemOptions, item market.Item) error {
	a.mu.Lock()
	if !a.localCurrentLocked(key, opts) {
		a.mu.Unlock()
		return context.Canceled
	}
	fresh := a.localRevision == opts.revision && a.localSnapshot.CharUID == opts.CharacterUID &&
		strings.EqualFold(a.localSnapshot.GameMode, opts.Mode) && !a.localAt.IsZero() && time.Since(a.localAt) < 5*time.Second
	if fresh {
		if a.localItemCurrentLocked(item) {
			a.state.Tracking = a.pendingTrackedViewLocked(item)
		}
		a.mu.Unlock()
		a.emit()
		return nil
	}
	a.state.Local = &LocalProgressState{Loading: true}
	a.mu.Unlock()
	a.emit()
	a.marketNetworkMu.Lock()
	defer a.marketNetworkMu.Unlock()
	snapshot, warning, err := a.loadLocalSources(ctx, key, opts)
	a.mu.Lock()
	if ctx.Err() != nil || !a.localCurrentLocked(key, opts) {
		a.mu.Unlock()
		return context.Canceled
	}
	valid := snapshot.CharUID == opts.CharacterUID && strings.EqualFold(snapshot.GameMode, opts.Mode) && snapshot.Revision != ""
	if valid {
		snapshot, _ = localprogress.Allocate(snapshot)
		a.confirmedProgress, a.localRevision = snapshot, opts.revision
		a.rebuildPendingViewLocked()
		a.state.Local = &LocalProgressState{UpdatedAt: time.Now(), Warning: warning}
		if a.localItemCurrentLocked(item) {
			a.state.Tracking = a.pendingTrackedViewLocked(item)
		}
	} else {
		a.state.Local = &LocalProgressState{}
		if a.localItemCurrentLocked(item) {
			previous := a.state.Tracking
			if previous != nil && previous.ItemUID == item.UID && previous.ItemID == item.BSGID && previous.CharUID == opts.CharacterUID {
				a.state.Tracking = cloneTrackingState(previous)
				a.state.Tracking.Loading = false
			} else {
				a.state.Tracking = &ItemTrackingState{ItemUID: item.UID, ItemID: item.BSGID, CharUID: opts.CharacterUID}
			}
		}
	}
	if err != nil {
		message := localProgressErrorText(err)
		a.state.Local.Error = message
		a.localAt = time.Time{}
		if a.localItemCurrentLocked(item) && a.state.Tracking != nil {
			a.state.Tracking.Error = message
		}
	} else {
		a.localAt = time.Now()
	}
	a.mu.Unlock()
	a.emit()
	return err
}

func (a *ItemWindowApp) RefreshLocalProgress() ItemState {
	a.mu.Lock()
	a.localAt, a.trackedAt = time.Time{}, time.Time{}
	a.state.Local = &LocalProgressState{Loading: true}
	a.mu.Unlock()
	a.emit()
	a.requestTracking()
	go a.RecheckPendingAdds()
	return a.GetState()
}

// Teljesen új Market-válaszból építjük újra a követelményeket és az Any csoportokat.
func (a *ItemWindowApp) applyLocalMarketStockLocked(item market.Item, remote market.TrackedSnapshot, status string) {
	a.applyMergedMarketSnapshotLocked(item, remote, marketProgressOnly(remote), status)
}

func (a *ItemWindowApp) applyMergedMarketSnapshotLocked(item market.Item, remote market.TrackedSnapshot, merged localprogress.Snapshot, status string) {
	a.reconcilePendingSnapshotLocked(remote)
	snapshot, _ := localprogress.Allocate(merged)
	a.confirmedProgress, a.localRevision, a.localAt = snapshot, a.state.ItemOptions.revision, time.Now()
	a.rebuildPendingViewLocked()
	a.state.Local = &LocalProgressState{UpdatedAt: a.localAt}
	if a.localItemCurrentLocked(item) {
		a.state.Tracking = a.pendingTrackedViewLocked(item)
		a.state.Tracking.Status = status
	}
}

// A kijelzett keszlet tartalmazza a memoriaban lefoglalt, meg nem nyugtazott
// darabokat is. A confirmedProgress mindig csak a Market keszlete marad.
func (a *ItemWindowApp) rebuildPendingViewLocked() {
	snapshot := localprogress.Clone(a.confirmedProgress)
	for i := range snapshot.Items {
		row := &snapshot.Items[i]
		pending := a.pendingAddAmountLocked(a.state.ItemOptions, row.ItemID)
		row.AvailableQuantity = min(localprogress.MaxQuantity, max(0, row.MarketAvailableQuantity+pending))
		row.RemainingQuantity = max(0, row.RequiredQuantity-row.AvailableQuantity)
	}
	if allocated, err := localprogress.Allocate(snapshot); err == nil {
		snapshot = allocated
	}
	a.localSnapshot = snapshot
	if a.state.Item != nil && snapshot.CharUID == a.state.CharacterUID && strings.EqualFold(snapshot.GameMode, a.state.Mode) {
		a.state.Tracking = a.pendingTrackedViewLocked(*a.state.Item)
	}
}

func (a *ItemWindowApp) pendingTrackedViewLocked(item market.Item) *ItemTrackingState {
	view := localTrackedView(item, a.localSnapshot)
	if row, found := stockItem(a.confirmedProgress, item.BSGID); found {
		view.MarketAvailableQuantity = row.MarketAvailableQuantity
	}
	return view
}
