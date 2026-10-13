package plus

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/tarkovdev"
)

type requirementsAPI interface {
	Load(context.Context, string) (tarkovdev.Catalog, error)
}

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
		return "Local Progress is unavailable. Refresh progress again."
	}
	switch failure.stage {
	case "tracked items", "quests", "hideout":
		return "Local Progress: Tarkov Market " + failure.stage + " could not be loaded. " + trackingError(err)
	case "synchronization":
		return "Market progress changed during refresh. Refresh Local Progress again."
	case "tarkov.dev":
		if errors.Is(err, context.DeadlineExceeded) {
			return "Local Progress: tarkov.dev requirements timed out. Refresh progress again."
		}
		return "Local Progress: tarkov.dev requirements could not be loaded. Check the connection and refresh progress again."
	case "merge":
		return "Local Progress: the Market and tarkov.dev requirements could not be matched. Market progress is shown separately."
	case "save":
		if errors.Is(err, localprogress.ErrCorruptStore) || errors.Is(err, localprogress.ErrStoreTooLarge) {
			return "Local Progress: the local stock file is invalid. The existing file was kept; local additions are disabled."
		}
		return "Local Progress: local stock could not be saved. The existing file was kept; local additions are disabled."
	}
	return "Local Progress is unavailable. Refresh progress again."
}

type LocalProgressState struct {
	Loading      bool                `json:"loading"`
	Error        string              `json:"error"`
	StockError   bool                `json:"stockError"`
	Warning      string              `json:"warning"`
	UpdatedAt    time.Time           `json:"updatedAt"`
	QuestItems   []LocalProgressItem `json:"questItems"`
	HideoutItems []LocalProgressItem `json:"hideoutItems"`
}

type LocalProgressItem struct {
	ItemID                  string   `json:"itemId"`
	Name                    string   `json:"name"`
	Type                    string   `json:"type"`
	RequiredQuantity        int64    `json:"requiredQuantity"`
	FIR                     int64    `json:"fir"`
	Regular                 int64    `json:"regular"`
	Completed               int64    `json:"completed"`
	AvailableQuantity       int64    `json:"availableQuantity"`
	AlternativeNotes        []string `json:"alternativeNotes,omitempty"`
	Storage                 string   `json:"storage"`
	MarketAvailableQuantity int64    `json:"marketAvailableQuantity"`
	LocalAvailableQuantity  int64    `json:"localAvailableQuantity"`
}

func cloneLocalProgressState(state *LocalProgressState) *LocalProgressState {
	if state == nil {
		return nil
	}
	copy := *state
	copy.QuestItems = append([]LocalProgressItem(nil), state.QuestItems...)
	copy.HideoutItems = append([]LocalProgressItem(nil), state.HideoutItems...)
	for i := range copy.QuestItems {
		copy.QuestItems[i].AlternativeNotes = append([]string(nil), copy.QuestItems[i].AlternativeNotes...)
	}
	for i := range copy.HideoutItems {
		copy.HideoutItems[i].AlternativeNotes = append([]string(nil), copy.HideoutItems[i].AlternativeNotes...)
	}
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

// Sikertelen kiegészítés mellett a külön Market forrás továbbra is látható.
// Ez a pillanatkép nem kerül a helyi készletfájlba.
func marketProgressOnly(snapshot market.TrackedSnapshot) localprogress.Snapshot {
	result := localprogress.Snapshot{CharUID: snapshot.CharUID, GameMode: snapshot.GameMode, Revision: snapshot.Revision,
		FIRInventorySeparate: snapshot.FIRInventorySeparate, Items: []localprogress.Item{}}
	for _, item := range snapshot.Items {
		row := localprogress.Item{ItemID: item.ItemID, Name: item.Name, ShortName: item.ShortName, Slots: item.Slots, Icon: item.Icon,
			RequiredQuantity: item.RequiredQuantity, SpentQuantity: item.SpentQuantity, AvailableQuantity: item.AvailableQuantity,
			RemainingQuantity: item.RemainingQuantity, MarketTracked: true, MarketRequiredQuantity: item.RequiredQuantity,
			MarketAvailableQuantity: item.AvailableQuantity, Sources: []localprogress.Source{}}
		row.MarketRawSources = append([]market.TrackedSource{}, item.Sources...)
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
	return fmt.Sprintf("Any %d of %d eligible items", source.RemainingRequiredQuantity, source.Alternatives)
}

func hasActiveLocalRequirement(item localprogress.Item) bool {
	for _, source := range item.Sources {
		if source.Origin == localprogress.OriginDev && !source.Completed && !source.Canceled && !source.Optional && source.RemainingRequiredQuantity > 0 {
			if source.AllocationKnown && source.Alternatives > 1 && source.GroupRemainingQuantity <= 0 {
				continue
			}
			return true
		}
	}
	return false
}

func localStockDestination(item localprogress.Item) string {
	quota := item.MarketRequiredQuantity
	if item.CollectionCalculated {
		quota = item.CollectionMarketRequiredQuantity
	}
	if item.MarketTracked && (item.MarketAvailableQuantity+item.MarketFallbackAvailableQuantity < quota || !hasActiveLocalRequirement(item)) {
		return "market"
	}
	return "local"
}

func localTrackedView(item market.Item, snapshot localprogress.Snapshot) *ItemTrackingState {
	if allocated, err := localprogress.Allocate(snapshot); err == nil {
		snapshot = allocated
	}
	view := trackedView(item, localMarketSnapshot(snapshot))
	view.Local = true
	view.Storage = "local"
	for _, row := range snapshot.Items {
		if row.ItemID == item.BSGID {
			view.Storage = localStockDestination(row)
			view.MarketAvailableQuantity = row.MarketAvailableQuantity
			view.LocalAvailableQuantity = row.LocalAvailableQuantity
			view.MarketRequiredQuantity = row.MarketRequiredQuantity
			view.HasLocalRequirements = hasActiveLocalRequirement(row)
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
	if len(view.Variants) != 0 {
		for _, variant := range view.Variants {
			variant.Local, variant.Storage = true, view.Storage
		}
		return view
	}
	if view.Storage == "local" && view.InventorySeparate {
		// A Market külön FiR készlete csak a Market soraira vonatkozik.
		// A saját helyi forrás továbbra is egy közös készlet.
		view.InventorySeparate = false
		view.Error = ""
	}
	notes := make(map[string]bool)
	for _, row := range snapshot.Items {
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
		simplifyLocalTrackingView(view, row, snapshot)
		if !view.InventorySeparate && view.Error == "" {
			view.AddRegularAllowed, view.AddFIRAllowed, _ = localprogress.AddOptions(snapshot, item.BSGID)
		}
		break
	}
	return view
}

func localLists(snapshot localprogress.Snapshot, at time.Time, warning string) *LocalProgressState {
	if allocated, err := localprogress.Allocate(snapshot); err == nil {
		snapshot = allocated
	}
	state := &LocalProgressState{UpdatedAt: at, Warning: warning, QuestItems: []LocalProgressItem{}, HideoutItems: []LocalProgressItem{}}
	for _, item := range snapshot.Items {
		for _, kind := range []string{"quest", "hideout"} {
			row := LocalProgressItem{ItemID: item.ItemID, Name: item.Name, Type: kind, AvailableQuantity: item.AvailableQuantity, Storage: localStockDestination(item),
				MarketAvailableQuantity: item.MarketAvailableQuantity, LocalAvailableQuantity: item.LocalAvailableQuantity}
			groups := make(map[string]bool)
			for _, source := range item.Sources {
				if completedSourceType(source.TrackedSource) != kind || source.Canceled || source.Optional {
					continue
				}
				if source.Completed {
					row.Completed += source.Amount
					continue
				}
				count := source.RemainingRequiredQuantity
				if source.AllocationKnown && source.Alternatives > 1 {
					count = 0
					if source.GroupRemainingQuantity > 0 {
						count = source.AllocatedQuantity + source.GroupRemainingQuantity
					}
				}
				row.RequiredQuantity += count
				if source.FIR {
					row.FIR += count
				} else {
					row.Regular += count
				}
				if note := alternativeNote(source); note != "" && !groups[source.GroupID] {
					groups[source.GroupID] = true
					row.AlternativeNotes = append(row.AlternativeNotes, note)
				}
			}
			if row.RequiredQuantity <= 0 {
				continue
			}
			if kind == "quest" {
				state.QuestItems = append(state.QuestItems, row)
			} else {
				state.HideoutItems = append(state.HideoutItems, row)
			}
		}
	}
	less := func(rows []LocalProgressItem) {
		sort.SliceStable(rows, func(i, j int) bool { return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name) })
	}
	less(state.QuestItems)
	less(state.HideoutItems)
	return state
}

// A három Market-válasz ugyanahhoz a revízióhoz tartozzon: egy közben kipipált
// küldetés nem kerülhet be régi állapottal az új tárgylistába.
func (a *ItemWindowApp) loadLocalSources(ctx context.Context, key string, opts ItemOptions) (localprogress.Snapshot, string, error) {
	progress, ok := a.tracker.(characterProgressAPI)
	if !ok || a.devRequirements == nil || a.localStore == nil {
		return localprogress.Snapshot{}, "", errors.New("local progress unavailable")
	}
	var latest market.TrackedSnapshot
	for attempt := 0; attempt < 2; attempt++ {
		tracked, err := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
		if err != nil {
			return localprogress.Snapshot{}, "", localProgressFailure("tracked items", err)
		}
		if tracked.CharUID != opts.CharacterUID || !strings.EqualFold(tracked.GameMode, opts.Mode) {
			return localprogress.Snapshot{}, "", localProgressFailure("tracked items", market.ErrProgressNotFound)
		}
		latest = tracked
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
		dev, err := a.devRequirements.Load(ctx, opts.Mode)
		if err != nil && (!dev.Stale || len(dev.Tasks) == 0 || len(dev.Hideout) == 0) {
			return marketProgressOnly(tracked), "", localProgressFailure("tarkov.dev", err)
		}
		if dev.Tasks == nil || dev.Hideout == nil {
			return marketProgressOnly(tracked), "", localProgressFailure("tarkov.dev", errors.New("incomplete requirements catalog"))
		}
		merged, err := localprogress.Merge(tracked, quests, hideout, dev)
		if err != nil {
			return marketProgressOnly(tracked), "", localProgressFailure("merge", err)
		}
		warning := ""
		if dev.Stale {
			warning = "Using cached tarkov.dev requirements; refresh when the service is available."
		}
		if a.client != nil {
			catalog, loadErr := a.client.Load(ctx, key, opts.Mode, opts.Language)
			if loadErr == nil {
				metadata := make(map[string]market.Item)
				for _, item := range catalog.Items {
					if _, exists := metadata[item.BSGID]; !exists {
						metadata[item.BSGID] = item
					}
				}
				for i := range merged.Items {
					row := &merged.Items[i]
					if item, exists := metadata[row.ItemID]; exists && (row.Name == "" || row.Name == row.ItemID) {
						row.Name, row.ShortName, row.Icon, row.Slots = item.Name, item.ShortName, item.Icon, item.Slots
					}
				}
			}
		}
		return merged, warning, nil
	}
	return marketProgressOnly(latest), "", localProgressFailure("synchronization", market.ErrProgressConflict)
}

func (a *ItemWindowApp) localCurrentLocked(key string, opts ItemOptions) bool {
	return a.apiKey == key && a.state.ItemOptions.revision == opts.revision && a.state.CharacterUID == opts.CharacterUID
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
			a.state.Tracking = localTrackedView(item, a.localSnapshot)
		}
		a.mu.Unlock()
		a.emit()
		return nil
	}
	a.state.Local = &LocalProgressState{Loading: true}
	a.mu.Unlock()
	a.emit()
	snapshot, warning, err := a.loadLocalSources(ctx, key, opts)
	if err == nil {
		err = a.rememberProgressSources(key, opts, snapshot, warning)
	}
	a.mu.Lock()
	if !a.localCurrentLocked(key, opts) {
		a.mu.Unlock()
		return context.Canceled
	}
	if err == nil && (snapshot.CharUID != opts.CharacterUID || !strings.EqualFold(snapshot.GameMode, opts.Mode)) {
		err = localProgressFailure("tracked items", market.ErrProgressNotFound)
	}
	if err == nil {
		applied, saveErr := a.applyProgressStock(snapshot)
		if saveErr == nil {
			snapshot = applied
		} else {
			err = localProgressFailure("save", saveErr)
		}
	}
	if err != nil {
		if temporaryMarketReadFailure(err) && a.localRevision == opts.revision && a.localSnapshot.CharUID == opts.CharacterUID &&
			strings.EqualFold(a.localSnapshot.GameMode, opts.Mode) && a.localSnapshot.Revision != "" {
			a.state.Local = localLists(a.localSnapshot, time.Now(), "Market is unavailable. Showing the last confirmed requirements; additions are saved locally.")
			if a.localItemCurrentLocked(item) {
				a.state.Tracking = localTrackedView(item, a.localSnapshot)
				a.state.Tracking.Status = "Market is unavailable. Added items will be kept locally for verified upload."
			}
			a.localAt = time.Time{}
			a.mu.Unlock()
			a.emit()
			return err
		}
		message := localProgressErrorText(err)
		var failure *localProgressLoadError
		stockError := errors.As(err, &failure) && failure.stage == "save"
		a.state.Local = &LocalProgressState{Error: message, StockError: stockError}
		// A helyi fájl hibája nem írja felül a külön Market forrást.
		// Sikeres forrásbetöltés után annak élő nézete továbbra is használható.
		if snapshot.CharUID == opts.CharacterUID && strings.EqualFold(snapshot.GameMode, opts.Mode) {
			a.localSnapshot = snapshot
			a.state.Local = localLists(snapshot, time.Now(), warning)
			a.state.Local.Error = message
			a.state.Local.StockError = stockError
		}
		if a.localItemCurrentLocked(item) {
			view := localTrackedView(item, a.localSnapshot)
			if snapshot.CharUID == opts.CharacterUID && strings.EqualFold(snapshot.GameMode, opts.Mode) {
				a.state.Tracking = view
			} else {
				a.state.Tracking = &ItemTrackingState{Local: true, ItemUID: item.UID, ItemID: item.BSGID, CharUID: opts.CharacterUID, Error: trackingError(err)}
			}
		}
		a.localAt = time.Time{}
		a.mu.Unlock()
		a.emit()
		return err
	}
	a.localSnapshot, a.localRevision, a.localAt = snapshot, opts.revision, time.Now()
	a.state.Local = localLists(snapshot, a.localAt, warning)
	if a.localItemCurrentLocked(item) {
		a.state.Tracking = localTrackedView(item, snapshot)
	}
	a.mu.Unlock()
	a.emit()
	return nil
}

func (a *ItemWindowApp) localItemCurrentLocked(item market.Item) bool {
	return item.UID != "" && a.state.Item != nil && a.state.Item.UID == item.UID && a.state.Item.BSGID == item.BSGID
}

func (a *ItemWindowApp) RefreshLocalProgress() ItemState {
	a.mu.Lock()
	a.progressRefreshRequested = true
	if a.state.ProgressSync == nil {
		a.state.ProgressSync = &ItemProgressSyncState{}
	}
	a.state.ProgressSync.Status = "Progress refresh queued. Pending uploads have been kept."
	if a.localSnapshot.CharUID != a.state.CharacterUID || a.localSnapshot.Revision == "" {
		a.state.Local = &LocalProgressState{Loading: true}
	}
	a.mu.Unlock()
	a.emit()
	a.queueProgressWork()
	return a.GetState()
}

// A Market saját igényéig az API készlete nő; a további dev-követelményhez
// tartozó készlet ugyanahhoz a tárgyhoz, külön helyi forrásként kerül mentésre.
func (a *ItemWindowApp) addLocalTrackedItem(uid string, target *trackedAddHotkeyTarget) ItemState {
	return a.addLocalTrackedItemKind(uid, target, "regular")
}

func (a *ItemWindowApp) addLocalTrackedItemKind(uid string, target *trackedAddHotkeyTarget, kind string) ItemState {
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	if !a.canAddTrackedItemLocked() || a.state.Item.UID != uid || (target != nil &&
		(!a.trackedAddHotkeyTargetCurrent(target) || !a.state.AddHotkeyReady || !trackedAddHotkeyEnabled(opts) ||
			target.UID != uid || target.ItemID != a.state.Item.BSGID || target.CharUID != opts.CharacterUID || target.Revision != opts.revision)) {
		a.mu.Unlock()
		return a.GetState()
	}
	item := *a.state.Item
	if a.state.Tracking.Local && !a.allowTrackedItemKindLocked(item.BSGID, kind) {
		a.mu.Unlock()
		a.emit()
		return a.GetState()
	}
	a.state.Tracking.Adding = true
	a.localAt = time.Time{}
	ctx, cancel := context.WithTimeout(a.ctx, 40*time.Second)
	a.trackingWriteCancel = cancel
	a.mu.Unlock()
	a.emit()
	defer func() {
		cancel()
		a.mu.Lock()
		a.trackingWriteCancel = nil
		a.mu.Unlock()
	}()
	refreshErr := a.refreshLocalProgress(ctx, key, opts, item)
	if temporaryMarketReadFailure(refreshErr) {
		a.mu.Lock()
		row, unique := stockEditorItem(a.localSnapshot, item.BSGID)
		marketDestination := unique && localStockDestination(row) == "market"
		a.mu.Unlock()
		if marketDestination && a.saveMarketFallbackAdd(key, opts, item, kind, "Market is unavailable. Saved locally for verified upload when Market recovers.") {
			a.emit()
			return a.GetState()
		}
	}
	a.mu.Lock()
	if !a.localCurrentLocked(key, opts) || !a.canAddTrackedItemLocked() || a.state.Item == nil || a.state.Item.UID != uid {
		a.mu.Unlock()
		return a.GetState()
	}
	if a.state.Tracking.Storage == "market" {
		a.mu.Unlock()
		return a.addMarketTrackedItemKind(uid, target, &opts, kind)
	}
	if ctx.Err() == nil {
		a.commitLocalStockKindLocked(opts, item, kind)
	}
	a.mu.Unlock()
	a.emit()
	return a.GetState()
}

func (a *ItemWindowApp) commitLocalStockLocked(opts ItemOptions, item market.Item) {
	a.commitLocalStockKindLocked(opts, item, "regular")
}

func (a *ItemWindowApp) commitLocalStockKindLocked(opts ItemOptions, item market.Item, kind string) {
	if a.state.Local == nil || a.state.Local.Error != "" || a.state.Tracking == nil ||
		a.state.Tracking.Storage != "local" || !a.state.Tracking.Tracked || a.state.Tracking.RequiredQuantity <= 0 ||
		!a.state.Tracking.HasLocalRequirements || len(a.state.Tracking.Variants) > 0 || a.state.Tracking.Error != "" ||
		a.state.Tracking.AvailableQuantity >= localprogress.MaxQuantity {
		if a.state.Tracking != nil {
			a.state.Tracking.Adding = false
		}
		return
	}
	if !a.allowTrackedItemKindLocked(item.BSGID, kind) {
		return
	}
	a.state.Tracking.Adding = false
	snapshot, err := a.localStore.AddKind(opts.CharacterUID, strings.ToUpper(opts.Mode), item.BSGID, kind)
	if err != nil {
		a.state.Tracking.Error = trackingError(err)
	} else {
		snapshot = mergeLocalStock(a.localSnapshot, snapshot)
		a.localSnapshot = snapshot
		warning := a.state.Local.Warning
		a.state.Local = localLists(snapshot, a.localAt, warning)
		a.state.Tracking = localTrackedView(item, snapshot)
		a.state.Tracking.Status = "Added 1 item to Tracked Items stock."
	}
}

// A PATCH előtti friss Market revízióhoz a kiegészített igénylista is tartozzon.
// Közben lezárt célra nem írunk régi dev követelmény alapján készletet.
func (a *ItemWindowApp) ensureLocalMarketRevision(ctx context.Context, key string, opts ItemOptions, item market.Item, revision string) string {
	a.mu.Lock()
	refresh := a.localSnapshot.Revision != revision
	if refresh {
		a.localAt = time.Time{}
	}
	a.mu.Unlock()
	if refresh {
		a.refreshLocalProgress(ctx, key, opts, item)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil || !a.localCurrentLocked(key, opts) || !a.localItemCurrentLocked(item) || a.state.Local == nil ||
		a.localSnapshot.Revision != revision || a.state.Tracking == nil || !a.state.Tracking.Tracked || a.state.Tracking.RequiredQuantity <= 0 ||
		a.state.Tracking.Error != "" || len(a.state.Tracking.Variants) > 0 || a.state.Tracking.InventorySeparate || a.state.Tracking.AvailableQuantity >= localprogress.MaxQuantity ||
		a.state.Local.Error != "" {
		if a.localItemCurrentLocked(item) && a.state.Tracking != nil {
			a.state.Tracking.Adding = false
			if a.state.Local != nil && a.state.Local.Error == "" && a.localSnapshot.Revision != revision {
				a.state.Tracking.Error = trackingError(market.ErrProgressConflict)
			}
		}
		return ""
	}
	a.state.Tracking.Adding = true
	return a.state.Tracking.Storage
}

// A visszaigazolt Market készlet az egyesített nézetben is azonnal megjelenik.
// A dev-only készlethez nem nyúlunk; új revíziónál a következő betöltés újra
// ellenőrzi a küldetések és fejlesztések állapotát.
func (a *ItemWindowApp) applyLocalMarketStockLocked(item market.Item, remote market.TrackedSnapshot, status string) {
	snapshot := localprogress.Clone(a.localSnapshot)
	if snapshot.CharUID != remote.CharUID || !strings.EqualFold(snapshot.GameMode, remote.GameMode) {
		return
	}
	byID := make(map[string][]market.TrackedItem)
	for _, row := range remote.Items {
		byID[row.ItemID] = append(byID[row.ItemID], row)
	}
	for i := range snapshot.Items {
		row := &snapshot.Items[i]
		if !row.MarketTracked {
			continue
		}
		// Az előző nézet jelölései nem validálhatók az új nyers mennyiséggel.
		// A Store a saját lenyomat alapján tölti vissza vagy törli őket.
		row.MarketRegularQuantity, row.MarketFIRQuantity = 0, 0
		matches := byID[row.ItemID]
		if len(matches) == 1 {
			row.MarketAvailableQuantity = matches[0].AvailableQuantity
			row.MarketRequiredQuantity = matches[0].RequiredQuantity
			row.AvailableQuantity = row.MarketAvailableQuantity + row.LocalAvailableQuantity
			row.SpentQuantity = matches[0].SpentQuantity
			row.RemainingQuantity = max(0, row.RequiredQuantity-row.AvailableQuantity)
		}
	}
	if a.localStore != nil {
		if applied, err := a.applyProgressStock(snapshot); err == nil {
			snapshot = applied
		} else {
			if a.state.Local == nil {
				a.state.Local = &LocalProgressState{}
			}
			a.state.Local.Error = localProgressErrorText(localProgressFailure("save", err))
			a.state.Local.StockError = true
		}
	}
	a.localAt = time.Time{}
	a.localSnapshot = snapshot
	warning, localError, stockError := "", "", false
	if a.state.Local != nil {
		warning = a.state.Local.Warning
		localError = a.state.Local.Error
		stockError = a.state.Local.StockError
	}
	a.state.Local = localLists(snapshot, time.Now(), warning)
	a.state.Local.Error = localError
	a.state.Local.StockError = stockError
	a.state.Tracking = localTrackedView(item, snapshot)
	remoteView := trackedView(item, remote)
	if len(remoteView.Variants) > 0 {
		// Egyetlen Market-sort nem helyettesíthetünk találomra két változattal.
		// A friss, csak olvasható változatnézet megmarad a teljes újraolvasásig.
		remoteView.Local = true
		remoteView.Storage = "market"
		a.state.Tracking = remoteView
	}
	if a.state.Tracking.Storage == "market" {
		a.state.Tracking.Error = remoteView.Error
	}
	a.state.Tracking.Status = status
}

// A helyi készlet saját forrás. Csak a megjelenítés élő pillanatképébe illesztjük,
// a Market sorokat és a küldetésállapotot a helyi fájl nem tartalmazza.
func mergeLocalStock(live, stock localprogress.Snapshot) localprogress.Snapshot {
	result := localprogress.Clone(live)
	if result.CharUID != stock.CharUID || result.GameMode != stock.GameMode {
		return result
	}
	byID := make(map[string]localprogress.Item)
	for _, row := range stock.Items {
		byID[row.ItemID] = row
	}
	for i := range result.Items {
		row := &result.Items[i]
		if saved, exists := byID[row.ItemID]; exists {
			quantity := saved.LocalAvailableQuantity
			row.LocalAvailableQuantity = quantity
			row.LocalRegularQuantity, row.LocalFIRQuantity = saved.LocalRegularQuantity, saved.LocalFIRQuantity
			row.LocalCollectedQuantity, row.LocalCollectedRegularQuantity, row.LocalCollectedFIRQuantity = saved.LocalCollectedQuantity, saved.LocalCollectedRegularQuantity, saved.LocalCollectedFIRQuantity
			row.LocalUsedQuantity, row.LocalUsedRegularQuantity, row.LocalUsedFIRQuantity = saved.LocalUsedQuantity, saved.LocalUsedRegularQuantity, saved.LocalUsedFIRQuantity
			row.MarketRegularQuantity, row.MarketFIRQuantity = saved.MarketRegularQuantity, saved.MarketFIRQuantity
			row.AvailableQuantity = row.MarketAvailableQuantity + quantity + row.MarketFallbackAvailableQuantity
			row.RemainingQuantity = max(0, row.RequiredQuantity-row.AvailableQuantity)
		}
	}
	return result
}

// Csak a sikeres PATCH után jegyezzük meg az általunk hozzáadott darab típusát.
// A Market készlet darabszáma továbbra is kizárólag az API-ból származik.
func (a *ItemWindowApp) recordLocalMarketAdd(key string, opts ItemOptions, item market.Item, kind string, before, after int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.localCurrentLocked(key, opts) || a.localStore == nil {
		return
	}
	stock, err := a.localStore.RecordMarketAdd(opts.CharacterUID, strings.ToUpper(opts.Mode), item.BSGID, kind, before, after)
	if err != nil {
		if a.state.Local == nil {
			a.state.Local = &LocalProgressState{}
		}
		a.state.Local.Error = localProgressErrorText(localProgressFailure("save", err))
		a.state.Local.StockError = true
		return
	}
	a.localSnapshot = mergeLocalStock(a.localSnapshot, stock)
}
