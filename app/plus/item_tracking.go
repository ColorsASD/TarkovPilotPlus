package plus

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

type trackingAPI interface {
	ListCharacters(context.Context, string) ([]market.Character, error)
	LoadTracked(context.Context, string, string, string) (market.TrackedSnapshot, error)
	SetTrackedQuantity(context.Context, string, string, string, string, int64) (market.ProgressWriteResult, error)
	LookupItem(context.Context, string, string, string, string) (market.Item, error)
}

type ItemTrackingState struct {
	ItemUID           string                        `json:"itemUid"`
	ItemID            string                        `json:"itemId"`
	CharUID           string                        `json:"charUid"`
	VariantName       string                        `json:"variantName,omitempty"`
	Variants          []*ItemTrackingState          `json:"variants,omitempty"`
	Loading           bool                          `json:"loading"`
	Adding            bool                          `json:"adding"`
	Tracked           bool                          `json:"tracked"`
	InventorySeparate bool                          `json:"firInventorySeparate"`
	AvailableQuantity int64                         `json:"availableQuantity"`
	RequiredQuantity  int64                         `json:"requiredQuantity"`
	RemainingQuantity int64                         `json:"remainingQuantity"`
	Requirement       string                        `json:"requirement"`
	Requirements      []ItemTrackingRequirement     `json:"requirements"`
	CompletedSources  []ItemTrackingCompletedSource `json:"completedSources"`
	Error             string                        `json:"error"`
	Status            string                        `json:"status"`
}

type ItemTrackingRequirement struct {
	Kind             string `json:"kind"`
	RequiredQuantity int64  `json:"requiredQuantity"`
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
	a.cancelTrackingWriteLocked()
	a.state.CharacterUID = ""
	a.state.Characters = nil
	a.state.CharactersLoading = false
	a.state.CharactersError = ""
	a.state.Tracking = nil
	a.charactersAt = time.Time{}
	a.trackedAt = time.Time{}
	a.trackedSnapshot = market.TrackedSnapshot{}
}

func (a *ItemWindowApp) cancelTrackingWriteLocked() {
	if a.trackingWriteCancel != nil {
		a.trackingWriteCancel()
		a.trackingWriteCancel = nil
	}
}

func (a *ItemWindowApp) requestTracking() {
	if a.tracker == nil || a.trackingRequests == nil || a.ctx == nil {
		return
	}
	select {
	case a.trackingRequests <- struct{}{}:
	default:
	}
}

func (a *ItemWindowApp) runTracking() {
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.trackingRequests:
			a.trackingMu.Lock()
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
		a.state.Tracking = &ItemTrackingState{ItemUID: item.UID, CharUID: opts.CharacterUID, Loading: true}
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
		a.emit()
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
	a.mu.Lock()
	snapshot := cloneTrackedSnapshot(a.trackedSnapshot)
	fetchedAt := a.trackedAt
	fresh := a.trackedRevision == opts.revision && snapshot.CharUID == opts.CharacterUID && !fetchedAt.IsZero() && time.Since(fetchedAt) < 5*time.Second
	a.mu.Unlock()
	if !fresh {
		var err error
		snapshot, err = a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
		if err != nil {
			a.trackingFailure(key, opts, item.UID, err)
			return
		}
		fetchedAt = time.Now()
	}
	a.applyTracking(key, opts, item, snapshot, fetchedAt, "")
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
	if tracking.Variants != nil {
		copy.Variants = make([]*ItemTrackingState, len(tracking.Variants))
		for i, variant := range tracking.Variants {
			copy.Variants[i] = cloneTrackingState(variant)
		}
	}
	return &copy
}

func cloneTrackedSnapshot(snapshot market.TrackedSnapshot) market.TrackedSnapshot {
	snapshot.Items = append([]market.TrackedItem(nil), snapshot.Items...)
	for i := range snapshot.Items {
		snapshot.Items[i].Sources = append([]market.TrackedSource(nil), snapshot.Items[i].Sources...)
	}
	return snapshot
}

func (a *ItemWindowApp) applyTracking(key string, opts ItemOptions, item market.Item, snapshot market.TrackedSnapshot, fetchedAt time.Time, status string) bool {
	if snapshot.CharUID != opts.CharacterUID || !strings.EqualFold(snapshot.GameMode, opts.Mode) {
		a.trackingFailure(key, opts, item.UID, market.ErrProgressNotFound)
		return false
	}
	a.mu.Lock()
	if a.apiKey != key || a.state.ItemOptions.revision != opts.revision || a.state.CharacterUID != opts.CharacterUID {
		a.mu.Unlock()
		return false
	}
	// Reusing a snapshot must not extend its lifetime beyond the API fetch.
	a.trackedSnapshot, a.trackedRevision, a.trackedAt = cloneTrackedSnapshot(snapshot), opts.revision, fetchedAt
	if a.state.Item == nil || a.state.Item.UID != item.UID {
		a.mu.Unlock()
		return false
	}
	a.state.Tracking = trackedView(item, snapshot)
	a.state.Tracking.Status = status
	a.mu.Unlock()
	a.emit()
	return true
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
	if !a.trackingMu.TryLock() {
		return a.GetState()
	}
	defer a.trackingMu.Unlock()
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	if !a.canAddTrackedItemLocked() || a.state.Item.UID != uid || (hotkeyTarget != nil &&
		(a.addHotkeyTarget.Load() != hotkeyTarget || !a.state.AddHotkeyReady || normalizeTrackedAddHotkey(opts.AddHotkey) == "off" ||
			hotkeyTarget.UID != uid || hotkeyTarget.ItemID != a.state.Item.BSGID || hotkeyTarget.CharUID != opts.CharacterUID || hotkeyTarget.Revision != opts.revision)) {
		a.mu.Unlock()
		return a.GetState()
	}
	item := *a.state.Item
	if item.BSGID == "" || opts.CharacterUID == "" {
		a.mu.Unlock()
		return a.GetState()
	}
	ctx, cancel := context.WithTimeout(a.ctx, 40*time.Second)
	a.trackingWriteCancel = cancel
	defer func() {
		cancel()
		a.mu.Lock()
		a.trackingWriteCancel = nil
		a.mu.Unlock()
	}()
	a.state.Tracking.Adding = true
	a.state.Tracking.Status = "Adding…"
	a.mu.Unlock()
	a.emit()
	for attempt := 0; attempt < 2; attempt++ {
		snapshot, err := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
		if err != nil {
			a.trackingFailure(key, opts, uid, err)
			return a.GetState()
		}
		view := trackedView(item, snapshot)
		if snapshot.CharUID != opts.CharacterUID || !strings.EqualFold(snapshot.GameMode, opts.Mode) {
			a.trackingFailure(key, opts, uid, market.ErrProgressNotFound)
			return a.GetState()
		}
		if view.Error != "" {
			a.applyTracking(key, opts, item, snapshot, time.Now(), "")
			return a.GetState()
		}
		if !view.Tracked {
			a.trackingFailure(key, opts, uid, market.ErrProgressNotFound)
			return a.GetState()
		}
		if view.RequiredQuantity <= 0 {
			a.applyTracking(key, opts, item, snapshot, time.Now(), "")
			return a.GetState()
		}
		if view.AvailableQuantity >= 1_000_000_000 {
			a.trackingFailure(key, opts, uid, errors.New("quantity limit"))
			return a.GetState()
		}
		if !a.trackingCurrent(key, opts, uid) {
			return a.GetState()
		}
		target := view.AvailableQuantity + 1
		result, err := a.tracker.SetTrackedQuantity(ctx, key, opts.CharacterUID, item.BSGID, snapshot.Revision, target)
		if errors.Is(err, market.ErrProgressConflict) && attempt == 0 {
			continue
		}
		if errors.Is(err, market.ErrProgressWriteUncertain) {
			current, readErr := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
			if readErr == nil {
				fetchedAt := time.Now()
				observed := trackedView(item, current)
				a.applyTracking(key, opts, item, current, fetchedAt, "")
				if observed.Tracked && observed.Error == "" && observed.AvailableQuantity == target {
					a.applyTracking(key, opts, item, current, fetchedAt, "API stock verified: it contains the increased quantity.")
					return a.GetState()
				}
			}
			a.trackingFailure(key, opts, uid, market.ErrProgressWriteUncertain)
			return a.GetState()
		}
		if err != nil {
			a.trackingFailure(key, opts, uid, err)
			return a.GetState()
		}
		if !result.OK {
			a.trackingFailure(key, opts, uid, market.ErrProgressWriteUncertain)
			return a.GetState()
		}
		status := "Added 1 item to Tracked Items stock."
		if !result.NotificationDelivered {
			status += " Web notification was not confirmed."
		}
		// A successful PATCH already confirms the stock even if readback fails.
		snapshot = cloneTrackedSnapshot(snapshot)
		for i := range snapshot.Items {
			if snapshot.Items[i].ItemID == item.BSGID {
				snapshot.Items[i].AvailableQuantity = target
				snapshot.Items[i].RemainingQuantity = max(0, snapshot.Items[i].RequiredQuantity-target)
			}
		}
		snapshot.Revision = result.Revision
		current, readErr := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
		var fetchedAt time.Time
		if readErr == nil {
			snapshot = current
			fetchedAt = time.Now()
		}
		// A failed readback keeps confirmed stock visible, but forces a fresh GET
		// before the complete snapshot can be reused for another scan.
		a.applyTracking(key, opts, item, snapshot, fetchedAt, status)
		return a.GetState()
	}
	return a.GetState()
}
