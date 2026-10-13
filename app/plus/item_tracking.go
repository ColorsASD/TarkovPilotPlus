package plus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
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
	Local                   bool                          `json:"local"`
	Storage                 string                        `json:"storage,omitempty"`
	MarketAvailableQuantity int64                         `json:"marketAvailableQuantity"`
	LocalAvailableQuantity  int64                         `json:"localAvailableQuantity"`
	MarketRequiredQuantity  int64                         `json:"marketRequiredQuantity"`
	HasLocalRequirements    bool                          `json:"hasLocalRequirements"`
	AddRegularAllowed       bool                          `json:"addRegularAllowed"`
	AddFIRAllowed           bool                          `json:"addFIRAllowed"`
	MarketTracked           bool                          `json:"marketTracked"`
	AlternativeNotes        []string                      `json:"alternativeNotes,omitempty"`
	AnyGroups               []ItemTrackingAnyGroup        `json:"anyGroups,omitempty"`
}

type ItemTrackingAnyGroup struct {
	GroupID           string `json:"groupId"`
	AvailableQuantity int64  `json:"availableQuantity"`
	RequiredQuantity  int64  `json:"requiredQuantity"`
	FIR               bool   `json:"fir"`
}

type ItemTrackingRequirement struct {
	Kind              string `json:"kind"`
	AvailableQuantity int64  `json:"availableQuantity"`
	RequiredQuantity  int64  `json:"requiredQuantity"`
	Type              string `json:"type,omitempty"`
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
	a.state.Local = nil
	a.restoreStockEditorLocked()
	a.localAt = time.Time{}
	a.charactersAt = time.Time{}
	a.trackedAt = time.Time{}
	a.trackedSnapshot = market.TrackedSnapshot{}
	a.progressBaseSnapshot = localprogress.Snapshot{}
	a.progressUnsafeItems = nil
	a.progressBaseRevision = 0
	a.progressBaseAt = time.Time{}
	a.state.ProgressSync = nil
}

func (a *ItemWindowApp) cancelTrackingWriteLocked() {
	if a.progressWriteCancel != nil {
		a.progressWriteCancel()
		a.progressWriteCancel = nil
	}
	if a.trackingWriteCancel != nil {
		a.trackingWriteCancel()
		a.trackingWriteCancel = nil
	}
}

func (a *ItemWindowApp) requestTracking() {
	// Egy új Inspect azonnal a már ellenőrzött forrásokra épül. A hálózati
	// frissítés ettől függetlenül, a közös háttérsorban történik.
	if a.trackingMu.TryLock() {
		a.mu.Lock()
		key, opts := a.apiKey, a.state.ItemOptions
		warm := a.progressBaseRevision == opts.revision && a.progressBaseSnapshot.CharUID == opts.CharacterUID && a.progressBaseSnapshot.Revision != ""
		a.mu.Unlock()
		if warm {
			a.publishLocalFirstProgress(key, opts, "")
		}
		a.trackingMu.Unlock()
	}
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
			a.mu.Lock()
			if a.progressBaseRevision != a.state.ItemOptions.revision || a.progressBaseSnapshot.Revision == "" || a.progressBaseAt.IsZero() || time.Since(a.progressBaseAt) >= time.Minute {
				a.progressRefreshRequested = true
			}
			a.mu.Unlock()
			a.queueProgressWork()
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
	pending := a.state.MarketFallbackPending
	a.mu.Unlock()
	if pending && opts.CharacterUID != "" {
		ctx, cancel := context.WithTimeout(a.ctx, 40*time.Second)
		_ = a.checkPendingMarketFallback(ctx, key, opts)
		cancel()
	}
	a.mu.Lock()
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
		a.restoreHandoverLocked()
		a.restoreStockEditorLocked()
		a.state.Local = nil
		a.localAt = time.Time{}
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
	a.applyLocalMarketStockLocked(item, snapshot, status)
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
	return a.addTrackedItemKind(uid, nil, "regular")
}

func (a *ItemWindowApp) AddTrackedItemFIR(uid string) ItemState {
	return a.addTrackedItemKind(uid, nil, "fir")
}

func (a *ItemWindowApp) addTrackedItem(uid string, hotkeyTarget *trackedAddHotkeyTarget) ItemState {
	return a.addTrackedItemKind(uid, hotkeyTarget, "regular")
}

func (a *ItemWindowApp) addTrackedItemKind(uid string, hotkeyTarget *trackedAddHotkeyTarget, kind string) ItemState {
	if kind != "regular" && kind != "fir" {
		return a.GetState()
	}
	if !a.trackingMu.TryLock() {
		a.notifyTrackedAddBusy(uid, hotkeyTarget)
		return a.GetState()
	}
	defer a.trackingMu.Unlock()
	return a.addCachedTrackedItemKind(uid, hotkeyTarget, kind)
}

// A Marketen listázott tárgyak a meglévő, revízióval védett API-írást használják.
// A hívó már birtokolja a trackingMu zárolást.
func (a *ItemWindowApp) addMarketTrackedItem(uid string, hotkeyTarget *trackedAddHotkeyTarget, expected *ItemOptions) ItemState {
	return a.addMarketTrackedItemKind(uid, hotkeyTarget, expected, "regular")
}

func (a *ItemWindowApp) addMarketTrackedItemKind(uid string, hotkeyTarget *trackedAddHotkeyTarget, expected *ItemOptions, kind string) ItemState {
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	if (expected != nil && (opts.revision != expected.revision || opts.CharacterUID != expected.CharacterUID)) ||
		!a.canAddTrackedItemLocked() || a.state.Item.UID != uid || (hotkeyTarget != nil &&
		(expected == nil && !a.trackedAddHotkeyTargetCurrent(hotkeyTarget) || !a.state.AddHotkeyReady || !trackedAddHotkeyEnabled(opts) ||
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
		if attempt == 0 {
			if _, syncErr := a.recoverMarketFallback(ctx, key, opts, item.BSGID, false); syncErr != nil {
				a.mu.Lock()
				pending := a.marketFallbackBlocksItemLocked(item.BSGID)
				a.mu.Unlock()
				if pending {
					a.trackingFailure(key, opts, uid, syncErr)
					return a.GetState()
				}
			}
		}
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
		destination := a.ensureLocalMarketRevision(ctx, key, opts, item, snapshot.Revision)
		if destination != "" {
			a.mu.Lock()
			if a.localCurrentLocked(key, opts) && a.localItemCurrentLocked(item) {
				// A friss API-készletet is ellenőrizzük. A követelmények már
				// ugyanahhoz a revízióhoz tartoznak, a készlet és a típusos
				// annotáció pedig a most olvasott mennyiséghez igazodik.
				a.applyLocalMarketStockLocked(item, snapshot, "")
				if a.state.Local.Error != "" || !a.allowTrackedItemKindLocked(item.BSGID, kind) {
					destination = ""
				} else {
					destination = a.state.Tracking.Storage
					a.state.Tracking.Adding = true
				}
			} else {
				destination = ""
			}
			a.mu.Unlock()
			if destination == "" {
				a.emit()
			}
		}
		if destination == "local" {
			a.mu.Lock()
			if ctx.Err() == nil && a.localCurrentLocked(key, opts) && a.localItemCurrentLocked(item) {
				a.commitLocalStockKindLocked(opts, item, kind)
			}
			a.mu.Unlock()
			a.emit()
			return a.GetState()
		}
		if destination != "market" {
			return a.GetState()
		}
		a.mu.Lock()
		fallbackRow, _ := stockEditorItem(a.localSnapshot, item.BSGID)
		a.mu.Unlock()
		if fallbackRow.MarketFallbackAvailableQuantity > 0 {
			if a.saveMarketFallbackAdd(key, opts, item, kind, "Added 1 item locally. Tarkov Market upload is pending.") {
				a.emit()
			}
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
		if err != nil && !errors.Is(err, market.ErrProgressWriteUncertain) {
			if safeMarketFallbackRejection(err) {
				return a.finishTrackedMarketAdd(ctx, key, opts, item, snapshot, result, target, kind, err)
			}
			status := fmt.Sprintf("Market did not save the increase. Requested stock: %d. %s", target,
				progressWriteDiagnostic(result, err, snapshot.Revision, "", nil))
			a.setTrackedWriteStatus(key, opts, item, status+progressItemMismatch(item, snapshot))
			a.trackingFailure(key, opts, uid, err)
			return a.GetState()
		}
		return a.finishTrackedMarketAdd(ctx, key, opts, item, snapshot, result, target, kind, err)
	}
	return a.GetState()
}
