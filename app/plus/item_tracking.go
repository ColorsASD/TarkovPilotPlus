package plus

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/nextitem"
)

type trackingAPI interface {
	ListCharacters(context.Context, string) ([]market.Character, error)
	LoadTracked(context.Context, string, string, string) (market.TrackedSnapshot, error)
	SetTrackedQuantity(context.Context, string, string, string, string, int64) (market.ProgressWriteResult, error)
	LookupItem(context.Context, string, string, string, string) (market.Item, error)
}

type ItemTrackingState struct {
	ItemUID                 string                        `json:"itemUid"`
	ItemID                  string                        `json:"itemId"`
	CharUID                 string                        `json:"charUid"`
	VariantName             string                        `json:"variantName,omitempty"`
	Variants                []*ItemTrackingState          `json:"variants,omitempty"`
	Loading                 bool                          `json:"loading"`
	Adding                  bool                          `json:"adding"`
	Tracked                 bool                          `json:"tracked"`
	InventorySeparate       bool                          `json:"firInventorySeparate"`
	AvailableQuantity       int64                         `json:"availableQuantity"`
	RequiredQuantity        int64                         `json:"requiredQuantity"`
	RemainingQuantity       int64                         `json:"remainingQuantity"`
	Requirement             string                        `json:"requirement"`
	Requirements            []ItemTrackingRequirement     `json:"requirements"`
	CompletedSources        []ItemTrackingCompletedSource `json:"completedSources"`
	Error                   string                        `json:"error"`
	Status                  string                        `json:"status"`
	Storage                 string                        `json:"storage,omitempty"`
	MarketAvailableQuantity int64                         `json:"marketAvailableQuantity"`
	MarketRequiredQuantity  int64                         `json:"marketRequiredQuantity"`
	AddAllowed              bool                          `json:"addAllowed"`
	RemoveAllowed           bool                          `json:"removeAllowed"`
	MarketTracked           bool                          `json:"marketTracked"`
	AlternativeNotes        []string                      `json:"alternativeNotes,omitempty"`
	AnyGroups               []ItemTrackingAnyGroup        `json:"anyGroups,omitempty"`
	NextItem                *nextitem.Forecast            `json:"nextItem,omitempty"`
}

type ItemTrackingAnyGroup struct {
	GroupID           string                  `json:"groupId"`
	AvailableQuantity int64                   `json:"availableQuantity"`
	RequiredQuantity  int64                   `json:"requiredQuantity"`
	FIR               bool                    `json:"fir"`
	Sources           []ItemTrackingAnySource `json:"sources,omitempty"`
}

type ItemTrackingAnySource struct {
	GroupID           string                `json:"groupId"`
	QuestName         string                `json:"questName,omitempty"`
	Category          string                `json:"category,omitempty"`
	AvailableQuantity int64                 `json:"availableQuantity"`
	RequiredQuantity  int64                 `json:"requiredQuantity"`
	Items             []ItemTrackingAnyItem `json:"items,omitempty"`
}

type ItemTrackingAnyItem struct {
	ItemID            string `json:"itemId"`
	Name              string `json:"name"`
	AvailableQuantity int64  `json:"availableQuantity"`
}

type ItemTrackingRequirement struct {
	Kind              string `json:"kind"`
	AvailableQuantity int64  `json:"availableQuantity"`
	RequiredQuantity  int64  `json:"requiredQuantity"`
	Type              string `json:"type,omitempty"`
	AllocationUnknown bool   `json:"allocationUnknown,omitempty"`
}

type ItemTrackingCompletedSource struct {
	Type     string `json:"type"`
	Quantity int64  `json:"quantity"`
	Optional bool   `json:"optional"`
}

func completedSourceType(source market.TrackedSource) string {
	quest, hideout := strings.TrimSpace(source.QuestID) != "", strings.TrimSpace(source.UpgradeKey) != ""
	if quest && hideout {
		return "unknown"
	}
	switch strings.ToLower(strings.TrimSpace(source.Type)) {
	case "quest":
		if !hideout {
			return "quest"
		}
		return "unknown"
	case "hideout":
		if !quest {
			return "hideout"
		}
		return "unknown"
	}
	if quest {
		return "quest"
	}
	if hideout {
		return "hideout"
	}
	return "unknown"
}

func cloneCharacters(characters []market.Character) []market.Character {
	copy := append([]market.Character(nil), characters...)
	for i := range copy {
		if copy[i].Level != nil {
			level := *copy[i].Level
			copy[i].Level = &level
		}
	}
	return copy
}

func (a *ItemWindowApp) clearTrackingLocked() {
	a.state.CharacterUID = ""
	a.state.Characters = nil
	a.state.CharactersLoading = false
	a.state.CharactersError = ""
	a.state.Tracking = nil
	a.state.Local = nil
	a.localSnapshot = localprogress.Snapshot{}
	a.confirmedProgress = localprogress.Snapshot{}
	a.localAt, a.charactersAt, a.trackedAt = time.Time{}, time.Time{}, time.Time{}
	a.localRevision, a.trackedRevision = 0, 0
	a.trackedSnapshot = market.TrackedSnapshot{}
}

func (a *ItemWindowApp) requestTracking() {
	// Az Inspect a már letöltött, csak memóriában tartott Market-nézetet
	// azonnal használhatja. A letöltés időpontját az új megjelenítés nem tolja ki.
	a.mu.Lock()
	if a.state.Item != nil && !a.localAt.IsZero() && a.localRevision == a.state.ItemOptions.revision &&
		a.localSnapshot.CharUID == a.state.CharacterUID && a.localSnapshot.Revision != "" {
		a.state.Tracking = a.pendingTrackedViewLocked(*a.state.Item)
	}
	a.mu.Unlock()
	if a.tracker == nil || a.trackingRequests == nil || a.ctx == nil {
		return
	}
	select {
	case a.trackingRequests <- struct{}{}:
	default:
	}
}

func (a *ItemWindowApp) runTracking() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.trackingRequests:
		case <-ticker.C:
		}
		if a.trackingMu.TryLock() {
			a.refreshTracking()
			a.trackingMu.Unlock()
		}
	}
}

func (a *ItemWindowApp) trackingCurrent(key string, opts ItemOptions, uid string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.apiKey == key && a.state.ItemOptions.revision == opts.revision &&
		a.state.CharacterUID == opts.CharacterUID && a.state.Item != nil && a.state.Item.UID == uid
}

func (a *ItemWindowApp) refreshTracking() {
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	if key == "" || a.tracker == nil {
		a.mu.Unlock()
		return
	}
	if !a.localCurrentLocked(key, opts) {
		a.mu.Unlock()
		return
	}
	var item market.Item
	if a.state.Item != nil {
		item = *a.state.Item
	}
	characters := cloneCharacters(a.state.Characters)
	loadCharacters := a.charactersRevision != opts.revision || a.charactersAt.IsZero() || time.Since(a.charactersAt) >= time.Minute
	if loadCharacters {
		a.state.CharactersLoading = true
	}
	if item.UID != "" {
		previous := a.state.Tracking
		if previous != nil && previous.ItemUID == item.UID && previous.ItemID == item.BSGID && previous.CharUID == opts.CharacterUID {
			a.state.Tracking = cloneTrackingState(previous)
			a.state.Tracking.Loading = true
		} else {
			a.state.Tracking = &ItemTrackingState{ItemUID: item.UID, ItemID: item.BSGID, CharUID: opts.CharacterUID, Loading: true}
		}
	}
	a.mu.Unlock()
	a.emit()
	ctx, cancel := context.WithTimeout(a.ctx, 25*time.Second)
	defer cancel()
	if loadCharacters {
		var err error
		characters, err = a.tracker.ListCharacters(ctx, key)
		a.mu.Lock()
		if a.apiKey != key || a.state.ItemOptions.revision != opts.revision {
			a.mu.Unlock()
			return
		}
		a.state.CharactersLoading = false
		if err != nil {
			a.state.CharactersError = trackingError(err)
			if a.state.Tracking != nil && a.state.Tracking.ItemUID == item.UID {
				a.state.Tracking.Loading = false
				a.state.Tracking.Error = trackingError(err)
			}
			a.mu.Unlock()
			a.emit()
			return
		}
		a.state.Characters = cloneCharacters(characters)
		a.state.CharactersError = ""
		a.charactersAt, a.charactersRevision = time.Now(), opts.revision
		a.mu.Unlock()
	}
	compatible := make([]market.Character, 0)
	valid := false
	for _, character := range characters {
		if strings.EqualFold(character.GameMode, opts.Mode) {
			compatible = append(compatible, character)
			if character.CharUID == opts.CharacterUID {
				valid = true
			}
		}
	}
	if !valid {
		opts.CharacterUID = ""
		if len(compatible) == 1 {
			opts.CharacterUID = compatible[0].CharUID
		}
	}
	a.mu.Lock()
	if a.apiKey != key || a.state.ItemOptions.revision != opts.revision {
		a.mu.Unlock()
		return
	}
	if a.state.CharacterUID != opts.CharacterUID {
		a.state.CharacterUID = opts.CharacterUID

		a.state.Local = nil
		a.localSnapshot = localprogress.Snapshot{}
		a.confirmedProgress = localprogress.Snapshot{}
		a.localAt = time.Time{}
		if item.UID != "" {
			a.state.Tracking = &ItemTrackingState{ItemUID: item.UID, ItemID: item.BSGID, CharUID: opts.CharacterUID, Loading: true}
		}
		if a.dir != "" {
			data, _ := json.MarshalIndent(a.state.ItemOptions, "", "    ")
			_ = writePrivateFile(filepath.Join(a.dir, "item-settings.json"), data)
		}
	}
	if a.state.Tracking != nil && a.state.Tracking.ItemUID == item.UID {
		a.state.Tracking.CharUID = opts.CharacterUID
	}
	if opts.CharacterUID == "" {
		message := "Select a Tracked Items character in settings."
		if len(compatible) == 0 {
			message = "No Tracked Items character is available for this game mode."
		}
		a.state.CharactersError = message
		if a.state.Tracking != nil && a.state.Tracking.ItemUID == item.UID {
			a.state.Tracking.Loading = false
			a.state.Tracking.Error = message
		}
		a.mu.Unlock()
		a.emit()
		return
	}
	a.state.CharactersError = ""
	a.mu.Unlock()
	if item.UID == "" {
		a.refreshLocalProgress(ctx, key, opts, item)
		return
	}
	if item.BSGID == "" {
		resolved, err := a.tracker.LookupItem(ctx, key, item.UID, opts.Mode, opts.Language)
		if err != nil || resolved.BSGID == "" {
			if err == nil {
				err = market.ErrProgressNotFound
			}
			a.trackingFailure(key, opts, item.UID, err)
			return
		}
		item.BSGID = resolved.BSGID
		a.mu.Lock()
		if a.apiKey == key && a.state.ItemOptions.revision == opts.revision && a.state.Item != nil && a.state.Item.UID == item.UID {
			copy := *a.state.Item
			copy.BSGID = item.BSGID
			a.state.Item = &copy
		}
		a.mu.Unlock()
	}
	if !a.trackingCurrent(key, opts, item.UID) {
		return
	}
	a.refreshLocalProgress(ctx, key, opts, item)
}

func trackedView(item market.Item, snapshot market.TrackedSnapshot) *ItemTrackingState {
	view := &ItemTrackingState{ItemUID: item.UID, ItemID: item.BSGID, CharUID: snapshot.CharUID, Requirement: "unknown"}
	var matches []market.TrackedItem
	matchedName, matchingNames := -1, 0
	for _, tracked := range snapshot.Items {
		if tracked.ItemID != item.BSGID || item.BSGID == "" {
			continue
		}
		matches = append(matches, tracked)
		if strings.EqualFold(strings.TrimSpace(tracked.Name), strings.TrimSpace(item.Name)) && strings.TrimSpace(item.Name) != "" {
			matchingNames++
			matchedName = len(matches) - 1
		}
	}
	if len(matches) == 0 {
		return view
	}
	if len(matches) == 1 {
		return trackedRecordView(item, snapshot, matches[0])
	}
	// Each API row keeps its own stock and requirements. The documented PATCH
	// accepts only the shared item ID, so these separate rows remain read-only.
	variants := make([]*ItemTrackingState, 0, len(matches))
	for _, tracked := range matches {
		variant := trackedRecordView(item, snapshot, tracked)
		variant.VariantName = tracked.Name
		variant.Error = "Separate stock updates are unavailable for variants sharing an item ID in the current Tarkov Market API."
		variants = append(variants, variant)
	}
	if matchingNames == 1 {
		*view = *variants[matchedName]
	}
	view.Error = "Tarkov Market returned multiple item variants with the same ID. The target is ambiguous, so stock changes cannot be sent for this item."
	view.Variants = variants
	return view
}

func trackedRecordView(item market.Item, snapshot market.TrackedSnapshot, tracked market.TrackedItem) *ItemTrackingState {
	view := &ItemTrackingState{ItemUID: item.UID, ItemID: item.BSGID, CharUID: snapshot.CharUID, Requirement: "unknown"}
	view.Tracked = true
	view.InventorySeparate = snapshot.FIRInventorySeparate
	if snapshot.FIRInventorySeparate && view.Error == "" {
		view.Error = "This character has separate Found in Raid stock. The documented API does not support changing separate stock."
	}
	view.AvailableQuantity, view.RequiredQuantity, view.RemainingQuantity = tracked.AvailableQuantity, tracked.RequiredQuantity, tracked.RemainingQuantity
	var firRequired, regularRequired int64
	for _, source := range tracked.Sources {
		if source.Canceled {
			continue
		}
		if source.Completed {
			// A teljesített forrás eredeti mennyisége nem a jelenlegi készlet.
			// Forrásonként őrizzük meg, az aktív tárgyigényhez nem adjuk hozzá.
			view.CompletedSources = append(view.CompletedSources, ItemTrackingCompletedSource{
				Type: completedSourceType(source), Quantity: source.Amount, Optional: source.Optional,
			})
			continue
		}
		if source.Optional || source.RemainingRequiredQuantity <= 0 {
			continue
		}
		if source.FIR {
			firRequired += source.RemainingRequiredQuantity
		} else {
			regularRequired += source.RemainingRequiredQuantity
		}
	}
	// A forrásonkénti követelmény már figyelembe veszi a leadott lépéseket,
	// de a közös készletet nem. A készletet nem osztjuk szét a két típus között,
	// és nem vonjuk le ugyanazt a mennyiséget mindkét követelményből.
	if firRequired > 0 {
		view.Requirements = append(view.Requirements, ItemTrackingRequirement{Kind: "fir", RequiredQuantity: firRequired})
	}
	if regularRequired > 0 {
		view.Requirements = append(view.Requirements, ItemTrackingRequirement{Kind: "regular", RequiredQuantity: regularRequired})
	}
	switch {
	case firRequired > 0 && regularRequired > 0:
		view.Requirement = "mixed"
	case firRequired > 0:
		view.Requirement = "fir"
	case regularRequired > 0:
		view.Requirement = "regular"
	}
	return view
}

func cloneTrackingState(tracking *ItemTrackingState) *ItemTrackingState {
	if tracking == nil {
		return nil
	}
	copy := *tracking
	copy.Requirements = append([]ItemTrackingRequirement(nil), tracking.Requirements...)
	copy.CompletedSources = append([]ItemTrackingCompletedSource(nil), tracking.CompletedSources...)
	copy.AlternativeNotes = append([]string(nil), tracking.AlternativeNotes...)
	copy.AnyGroups = append([]ItemTrackingAnyGroup(nil), tracking.AnyGroups...)
	if tracking.NextItem != nil {
		forecast := *tracking.NextItem
		forecast.PossibleCategories = append([]string(nil), tracking.NextItem.PossibleCategories...)
		forecast.Changes = append([]nextitem.Change(nil), tracking.NextItem.Changes...)
		forecast.Targets = append([]nextitem.Target(nil), tracking.NextItem.Targets...)
		copy.NextItem = &forecast
	}
	for i := range copy.AnyGroups {
		copy.AnyGroups[i].Sources = append([]ItemTrackingAnySource(nil), tracking.AnyGroups[i].Sources...)
		for j := range copy.AnyGroups[i].Sources {
			copy.AnyGroups[i].Sources[j].Items = append([]ItemTrackingAnyItem(nil), tracking.AnyGroups[i].Sources[j].Items...)
		}
	}
	if tracking.Variants != nil {
		copy.Variants = make([]*ItemTrackingState, len(tracking.Variants))
		for i, variant := range tracking.Variants {
			copy.Variants[i] = cloneTrackingState(variant)
		}
	}
	return &copy
}

func cloneTrackedSnapshot(snapshot market.TrackedSnapshot) market.TrackedSnapshot {
	if snapshot.Items != nil {
		snapshot.Items = append([]market.TrackedItem{}, snapshot.Items...)
	}
	for i := range snapshot.Items {
		snapshot.Items[i].Sources = append([]market.TrackedSource(nil), snapshot.Items[i].Sources...)
	}
	snapshot.Groups = append([]market.TrackedGroup(nil), snapshot.Groups...)
	for i := range snapshot.Groups {
		snapshot.Groups[i].ItemIDs = append([]string(nil), snapshot.Groups[i].ItemIDs...)
	}
	return snapshot
}

func trackingError(err error) string {
	switch {
	case errors.Is(err, market.ErrProgressForbidden):
		return "Tracked Items requires Tarkov Market PRO access and an enabled character."
	case errors.Is(err, market.ErrProgressNotFound):
		return "The character or tracked item is not available on Tarkov Market."
	case errors.Is(err, market.ErrProgressConflict):
		return "Tracked Items stock changed during the operation. Scan the item again."
	case errors.Is(err, market.ErrProgressWriteUncertain):
		return "The add result could not be verified. No automatic retry was sent; check the stock on Tarkov Market."
	case errors.Is(err, context.DeadlineExceeded):
		return "The Tracked Items request timed out. Scan the item again."
	case errors.Is(err, context.Canceled):
		return "Tracked Items loading was canceled."
	case errors.Is(err, market.ErrInvalidProgress):
		return "The Tarkov Market Tracked Items response is incomplete or invalid. Stock could not be loaded."
	case errors.Is(err, market.ErrProgressRevisionRequired):
		return "The Tracked Items stock revision is missing or invalid. Scan the item again."
	case errors.Is(err, market.ErrProgressUnavailable):
		return "The Tarkov Market Tracked Items service is not responding. Try again later."
	case errors.Is(err, market.ErrInvalidCatalog):
		return "The Tarkov Market item ID response is incomplete or invalid; Tracked Items cannot be linked to this item."
	case errors.Is(err, market.ErrItemNotFound):
		return "The Tracked Items ID for the recognized item was not found on Tarkov Market."
	case errors.Is(err, market.ErrProgressInvalidRequest):
		return "The character or item ID required for the Tracked Items request is invalid."
	case errors.Is(err, market.ErrProgressPayloadTooLarge):
		return "Tarkov Market rejected the request because it was too large."
	case errors.Is(err, market.ErrResponseTooBig):
		return "The Tarkov Market Tracked Items response is too large; stock could not be loaded."
	case errors.Is(err, market.ErrUnauthorized), errors.Is(err, market.ErrAPIKeyRequired), errors.Is(err, market.ErrRateLimited):
		return marketError(err)
	default:
		return "Tracked Items data is not available right now. Scan the item again."
	}
}

func (a *ItemWindowApp) trackingFailure(key string, opts ItemOptions, uid string, err error) {
	a.mu.Lock()
	if a.apiKey == key && a.state.ItemOptions.revision == opts.revision && a.state.CharacterUID == opts.CharacterUID && a.state.Item != nil && a.state.Item.UID == uid {
		if a.state.Tracking == nil {
			a.state.Tracking = &ItemTrackingState{ItemUID: uid, CharUID: opts.CharacterUID}
		}
		a.state.Tracking.Loading, a.state.Tracking.Adding = false, false
		a.state.Tracking.Error = trackingError(err)
		a.localAt, a.trackedAt = time.Time{}, time.Time{}
	}
	a.mu.Unlock()
	a.emit()
}

// One explicit button click or shortcut press adds one to freshly read stock. A rejected revision
// may be reconciled once. An uncertain write is only read back, never replayed.
func (a *ItemWindowApp) AddTrackedItem(uid string) ItemState {
	return a.addTrackedItem(uid, nil)
}

func (a *ItemWindowApp) addTrackedItem(uid string, hotkeyTarget *trackedAddHotkeyTarget) ItemState {
	return a.reservePendingMarketAdd(uid, hotkeyTarget)
}

func (a *ItemWindowApp) RemoveTrackedItem(uid string) ItemState {
	return a.removeTrackedItem(uid, nil)
}

func (a *ItemWindowApp) removeTrackedItem(uid string, hotkeyTarget *trackedAddHotkeyTarget) ItemState {
	return a.reservePendingMarketChange(uid, hotkeyTarget, -1)
}
