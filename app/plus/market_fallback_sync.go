package plus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

const maxMarketFallbackSyncBytes = 8 << 10

var (
	marketFallbackSyncMu         sync.Mutex
	errMarketFallbackSyncInvalid = errors.New("The pending Market supplementary stock upload is invalid. It has been kept; stock changes are disabled.")
	errMarketFallbackSyncPending = errors.New("Market supplementary stock upload is awaiting verification. Check Market stock, then refresh or use Retry saved upload.")
)

// A fájl csak az egyetlen feltöltés írási szándéka. A lezárt művelet fájlja
// törlődik; az API-kulcs és a többi tárgy Market-készlete nem kerül bele.
type marketFallbackSyncIntent struct {
	Version     int                        `json:"version"`
	OperationID string                     `json:"operationId,omitempty"`
	CharUID     string                     `json:"charUid"`
	Mode        string                     `json:"mode"`
	ItemID      string                     `json:"itemId"`
	Revision    string                     `json:"revision"`
	Before      int64                      `json:"before"`
	Target      int64                      `json:"target"`
	Fallback    localprogress.StockAmounts `json:"fallback"`
	MarketAfter localprogress.StockAmounts `json:"marketAfter"`
}

func (a *ItemWindowApp) marketFallbackSyncLegacyPath(opts ItemOptions) (string, error) {
	mode := strings.ToUpper(opts.Mode)
	if a.dir == "" || !handoverItemID(opts.CharacterUID) || !stockEditorPendingMode(mode) {
		return "", errMarketFallbackSyncInvalid
	}
	hash := sha256.Sum256([]byte(opts.CharacterUID + "\x00" + mode))
	return filepath.Join(a.dir, "market-fallback-sync", hex.EncodeToString(hash[:])+".json"), nil
}

// Az új naplók tárgyanként külön fájlt kapnak. A régi profilfájlt olvasáskor
// megőrizzük, ezért a visszaállítás nem igényel bizonytalan migrációs írást.
func (a *ItemWindowApp) marketFallbackSyncPath(opts ItemOptions, itemIDs ...string) (string, error) {
	legacy, err := a.marketFallbackSyncLegacyPath(opts)
	if err != nil || len(itemIDs) > 1 {
		return "", errMarketFallbackSyncInvalid
	}
	if len(itemIDs) == 0 {
		intents, err := a.readMarketFallbackSyncIntents(opts)
		if err != nil {
			return legacy, err
		}
		if len(intents) == 1 {
			path, _ := a.marketFallbackSyncPath(opts, intents[0].ItemID)
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
		return legacy, nil
	}
	if !handoverItemID(itemIDs[0]) {
		return "", errMarketFallbackSyncInvalid
	}
	hash := sha256.Sum256([]byte(itemIDs[0]))
	return strings.TrimSuffix(legacy, ".json") + "-" + hex.EncodeToString(hash[:]) + ".json", nil
}

// A rövid, atomikus fájlolvasás a visszaállítás alatt is hívható. Nem vár
// a hálózati művelet zárolására, és nem kér alkalmazásállapot-zárolást.
func (a *ItemWindowApp) marketFallbackSyncPending(opts ItemOptions) (bool, error) {
	pending, err := a.readMarketFallbackSync(opts)
	return pending != nil || err != nil, err
}

// syncMarketFallback egy tárgy elérhető kiegészítő készletét helyezi át.
// A hívó friss Market-pillanatképet ad, és tiltja a versengő készletírásokat.
// Függő műveletből kizárólag GET-ellenőrzéssel folytatunk, új PATCH nélkül.
func (a *ItemWindowApp) syncMarketFallback(ctx context.Context, key string, opts ItemOptions, remote market.TrackedSnapshot, itemID string) (market.TrackedSnapshot, string, error) {
	if ctx == nil || a.tracker == nil || a.localStore == nil || key == "" {
		return remote, "", errMarketFallbackSyncInvalid
	}
	store := a.marketFallbackStock()
	if store == nil {
		return remote, "", errMarketFallbackSyncInvalid
	}
	// A megőrzött szinkron hívó tarthat trackingMu-t. Nem várhatunk a
	// workerre, mert az a hálózat után ugyanennek a rövid locknak a sorát kéri.
	if !marketFallbackSyncMu.TryLock() {
		return remote, "", errMarketFallbackSyncPending
	}
	defer marketFallbackSyncMu.Unlock()
	pending, err := a.readMarketFallbackSync(opts)
	if err != nil {
		return remote, "", err
	}
	if pending != nil {
		current, err := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
		if err != nil {
			return remote, "", errors.Join(errMarketFallbackSyncPending, err)
		}
		if !marketFallbackSyncVerified(current, opts, pending) {
			if released, err := a.releaseObsoleteMarketFallbackSync(current, opts, pending, store); released || err != nil {
				return current, marketFallbackSyncKeptMessage, err
			}
			return current, "", errMarketFallbackSyncPending
		}
		if err := a.finishMarketFallbackSync(opts, pending, store); err != nil {
			return current, "", err
		}
		return current, "Market supplementary stock uploaded and verified.", nil
	}
	if !handoverItemID(itemID) || !marketFallbackSyncSnapshot(remote, opts) {
		return remote, "", errMarketFallbackSyncInvalid
	}
	row, unique := marketFallbackSyncItem(remote, itemID)
	if !unique {
		return remote, "", errMarketFallbackSyncInvalid
	}
	if remote.FIRInventorySeparate {
		return remote, "", localprogress.ErrSeparateInventory
	}
	fallback, err := marketFallbackSyncAmounts(store, opts, itemID)
	if err != nil {
		return remote, "", err
	}
	quantity, err := stockEditorTotal(fallback)
	if err != nil {
		return remote, "", err
	}
	if quantity == 0 {
		return remote, "", nil
	}
	if row.AvailableQuantity < 0 || row.AvailableQuantity > localprogress.MaxQuantity-quantity {
		return remote, "", localprogress.ErrInvalidQuantity
	}
	// Csak az élő Markethez illeszkedő típusjelölés vihető tovább.
	projected, err := a.localStore.Apply(marketProgressOnly(remote))
	if err != nil {
		return remote, "", err
	}
	current, unique := stockEditorItem(projected, itemID)
	if !unique {
		return remote, "", errMarketFallbackSyncInvalid
	}
	after := localprogress.StockAmounts{
		Regular: current.MarketRegularQuantity + fallback.Regular,
		FIR:     current.MarketFIRQuantity + fallback.FIR,
		Unknown: row.AvailableQuantity - current.MarketRegularQuantity - current.MarketFIRQuantity + fallback.Unknown,
	}
	if err := ctx.Err(); err != nil {
		return remote, "", err
	}
	if !a.handoverCurrent(key, opts) {
		return remote, "", context.Canceled
	}
	pending = &marketFallbackSyncIntent{Version: 1, CharUID: opts.CharacterUID, Mode: strings.ToUpper(opts.Mode), ItemID: itemID,
		Revision: remote.Revision, Before: row.AvailableQuantity, Target: row.AvailableQuantity + quantity, Fallback: fallback, MarketAfter: after}
	if err := a.saveMarketFallbackSync(opts, pending); err != nil {
		return remote, "", err
	}
	// A napló létrehozása közben is változhat a karakter vagy a beállítás.
	// A PATCH itt bizonyítottan nem indult el, ezért a szándék törölhető.
	// Törlési hiba esetén a függő állapot továbbra is tiltja az írást.
	if err := ctx.Err(); err != nil {
		return remote, "", a.cancelUnsentMarketFallbackSync(opts, err, pending.ItemID)
	}
	if !a.handoverCurrent(key, opts) {
		return remote, "", a.cancelUnsentMarketFallbackSync(opts, context.Canceled, pending.ItemID)
	}
	result, writeErr := a.tracker.SetTrackedQuantity(ctx, key, opts.CharacterUID, itemID, pending.Revision, pending.Target)
	if progressWriteDefinitelyNotApplied(writeErr) {
		if err := a.removeMarketFallbackSync(opts, pending.ItemID); err != nil {
			return remote, "", err
		}
		if errors.Is(writeErr, context.Canceled) || errors.Is(writeErr, context.DeadlineExceeded) {
			return remote, "", writeErr
		}
		return remote, "Market did not accept the supplementary stock upload. Supplementary stock was kept locally.", nil
	}
	observed, readErr := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
	if readErr != nil {
		// Ez új szándék: a kifejezett no-op válasz önmagában bizonyítja,
		// hogy ehhez nem tartozik bizonytalanul alkalmazott korábbi PATCH.
		if marketFallbackSyncNoOp(result, writeErr, pending) {
			return remote, "Market reported no change. Supplementary stock was kept locally.", a.removeMarketFallbackSync(opts, pending.ItemID)
		}
		return remote, "", errors.Join(errMarketFallbackSyncPending, readErr)
	}
	if marketFallbackSyncVerified(observed, opts, pending) {
		if err := a.finishMarketFallbackSync(opts, pending, store); err != nil {
			return observed, "", err
		}
		return observed, "Market supplementary stock uploaded and verified.", nil
	}
	if released, err := a.releaseObsoleteMarketFallbackSync(observed, opts, pending, store); released || err != nil {
		return observed, marketFallbackSyncKeptMessage, err
	}
	// A kifejezett no-op és a változatlan, friss visszaolvasás bizonyítja,
	// hogy ez a szándék nem alkalmazódott. A kiegészítő készlet megmarad.
	noChange, unique := marketFallbackSyncItem(observed, itemID)
	if marketFallbackSyncNoOp(result, writeErr, pending) &&
		marketFallbackSyncSnapshot(observed, opts) && !observed.FIRInventorySeparate && unique &&
		observed.Revision == pending.Revision && noChange.AvailableQuantity == pending.Before {
		if err := a.removeMarketFallbackSync(opts, pending.ItemID); err != nil {
			return observed, "", err
		}
		return observed, "Market reported no change. Supplementary stock was kept locally.", nil
	}
	return observed, "", errors.Join(errMarketFallbackSyncPending, writeErr)
}

// A háttérfolyamat nem ismétel írást. Ez a külön felhasználói kérés mindig
// a mentett abszolút célt használja, és előtte frissen ellenőrzi a Marketet.
// A régi revisionnel elindult bizonytalan írás no-op válaszból nem törölhető.
func (a *ItemWindowApp) retrySavedMarketFallback(ctx context.Context, key string, opts ItemOptions) (market.TrackedSnapshot, string, error) {
	var remote market.TrackedSnapshot
	if ctx == nil || a.tracker == nil || a.localStore == nil || key == "" {
		return remote, "", errMarketFallbackSyncInvalid
	}
	store := a.marketFallbackStock()
	if store == nil {
		return remote, "", errMarketFallbackSyncInvalid
	}
	marketFallbackSyncMu.Lock()
	defer marketFallbackSyncMu.Unlock()
	intents, err := a.readMarketFallbackSyncIntents(opts)
	if err != nil {
		return remote, "", err
	}
	if len(intents) == 0 {
		return remote, "No saved Market supplementary stock upload remains.", nil
	}
	a.mu.Lock()
	sort.SliceStable(intents, func(i, j int) bool {
		left := a.fallbackSyncAt[localFirstFallbackAttemptKey(opts, intents[i].ItemID)]
		right := a.fallbackSyncAt[localFirstFallbackAttemptKey(opts, intents[j].ItemID)]
		return left.Before(right)
	})
	a.mu.Unlock()
	pending := intents[0]
	if err := ctx.Err(); err != nil {
		return remote, "", err
	}
	if !a.handoverCurrent(key, opts) {
		return remote, "", context.Canceled
	}
	remote, err = a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
	if err != nil {
		return remote, "", errors.Join(errMarketFallbackSyncPending, err)
	}
	if err := ctx.Err(); err != nil {
		return remote, "", err
	}
	if !a.handoverCurrent(key, opts) {
		return remote, "", context.Canceled
	}
	if marketFallbackSyncVerified(remote, opts, pending) || remote.Revision != pending.Revision {
		status, err := a.settleLocalFirstMarketFallback(ctx, key, opts, remote, pending)
		return remote, status, err
	}
	row, unique := marketFallbackSyncItem(remote, pending.ItemID)
	if !marketFallbackSyncSnapshot(remote, opts) || remote.FIRInventorySeparate || !unique || row.AvailableQuantity != pending.Before {
		return remote, "", errMarketFallbackSyncPending
	}
	fallback, err := marketFallbackSyncAmounts(store, opts, pending.ItemID)
	if err != nil {
		return remote, "", err
	}
	// A nulla csak egy korábbi részleges lezárás eredménye lehet. Onnan
	// kizárólag a már igazolt cél ellenőrzése folytatható, új PATCH nem.
	if pending.Version == 2 {
		recorded, receiptErr := store.TransferRecorded(opts.CharacterUID, strings.ToUpper(opts.Mode), pending.OperationID, pending.ItemID, pending.Fallback)
		if receiptErr != nil {
			return remote, "", receiptErr
		}
		if recorded || !marketFallbackContains(fallback, pending.Fallback) {
			return remote, "", errMarketFallbackSyncPending
		}
	} else if fallback != pending.Fallback {
		return remote, "", errMarketFallbackSyncPending
	}
	// Ezen a ponton csak az eredeti revisionhöz tartozó szándék maradhat.
	// Kontextusváltásnál megőrizzük: egy korábbi bizonytalan kérés létezett.
	if err := ctx.Err(); err != nil {
		return remote, "", errors.Join(errMarketFallbackSyncPending, err)
	}
	if !a.handoverCurrent(key, opts) {
		return remote, "", errors.Join(errMarketFallbackSyncPending, context.Canceled)
	}
	// A retry ACK/no-op önmagában nem bizonyítja az eredeti kérés eredményét.
	a.markLocalFirstFallbackAttempt(opts, pending)
	if err := a.marketFallbackLocalFirstContext(ctx, key, opts); err != nil {
		return remote, "", errors.Join(errMarketFallbackSyncPending, err)
	}
	_, writeErr := a.tracker.SetTrackedQuantity(ctx, key, opts.CharacterUID, pending.ItemID, pending.Revision, pending.Target)
	observed, readErr := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
	if readErr != nil {
		return remote, "", errors.Join(errMarketFallbackSyncPending, writeErr, readErr)
	}
	status, settleErr := a.settleLocalFirstMarketFallback(ctx, key, opts, observed, pending)
	if settleErr == nil {
		return observed, status, nil
	}
	return observed, status, errors.Join(settleErr, writeErr)
}

const marketFallbackSyncKeptMessage = "The old Market upload is no longer applicable. Supplementary stock was kept locally."

// A friss, változatlan before készlet és az eltérő revision együtt azt
// bizonyítja, hogy a régi If-Match ezen az állapoton már nem alkalmazható.
// A teljes kiegészítő készletnek még meg kell lennie: részleges helyi
// lezárást vagy közben átírt készletet ebből nem szabad visszaállítani.
// A lezárás csak naplót töröl; történeti használatot nem talál ki.
func (a *ItemWindowApp) releaseObsoleteMarketFallbackSync(remote market.TrackedSnapshot, opts ItemOptions, pending *marketFallbackSyncIntent, store *localprogress.Store) (bool, error) {
	row, unique := marketFallbackSyncItem(remote, pending.ItemID)
	if !marketFallbackSyncSnapshot(remote, opts) || remote.FIRInventorySeparate || !unique ||
		remote.Revision == pending.Revision || row.AvailableQuantity != pending.Before {
		return false, nil
	}
	current, err := marketFallbackSyncAmounts(store, opts, pending.ItemID)
	if err != nil {
		return false, err
	}
	if pending.Version == 2 {
		recorded, receiptErr := store.TransferRecorded(opts.CharacterUID, strings.ToUpper(opts.Mode), pending.OperationID, pending.ItemID, pending.Fallback)
		if receiptErr != nil {
			return false, receiptErr
		}
		if recorded || !marketFallbackContains(current, pending.Fallback) {
			return false, nil
		}
	} else if current != pending.Fallback {
		return false, nil
	}
	if err := a.removeMarketFallbackSync(opts, pending.ItemID); err != nil {
		return false, errors.Join(errMarketFallbackSyncPending, err)
	}
	return true, nil
}

func marketFallbackSyncNoOp(result market.ProgressWriteResult, err error, pending *marketFallbackSyncIntent) bool {
	return err == nil && result.OK && !result.Changed && result.Revision == pending.Revision
}

func (a *ItemWindowApp) cancelUnsentMarketFallbackSync(opts ItemOptions, canceled error, itemIDs ...string) error {
	if err := a.removeMarketFallbackSync(opts, itemIDs...); err != nil {
		return errors.Join(errMarketFallbackSyncPending, canceled, err)
	}
	return canceled
}

func marketFallbackSyncSnapshot(snapshot market.TrackedSnapshot, opts ItemOptions) bool {
	return snapshot.CharUID == opts.CharacterUID && strings.EqualFold(snapshot.GameMode, opts.Mode) &&
		snapshot.Items != nil && snapshot.Revision != ""
}

func marketFallbackSyncItem(snapshot market.TrackedSnapshot, id string) (market.TrackedItem, bool) {
	var item market.TrackedItem
	count := 0
	for _, candidate := range snapshot.Items {
		if candidate.ItemID == id {
			item = candidate
			count++
		}
	}
	return item, count == 1
}

func marketFallbackSyncVerified(snapshot market.TrackedSnapshot, opts ItemOptions, pending *marketFallbackSyncIntent) bool {
	item, unique := marketFallbackSyncItem(snapshot, pending.ItemID)
	return marketFallbackSyncSnapshot(snapshot, opts) && !snapshot.FIRInventorySeparate && unique &&
		snapshot.Revision != pending.Revision && item.AvailableQuantity == pending.Target
}

func marketFallbackSyncAmounts(store *localprogress.Store, opts ItemOptions, id string) (localprogress.StockAmounts, error) {
	stock, err := store.Load(opts.CharacterUID, strings.ToUpper(opts.Mode))
	if errors.Is(err, os.ErrNotExist) {
		return localprogress.StockAmounts{}, nil
	}
	if err != nil {
		return localprogress.StockAmounts{}, err
	}
	item, unique := stockEditorItem(stock, id)
	if !unique {
		return localprogress.StockAmounts{}, nil
	}
	return stockEditorLocalAmounts(item), nil
}

func (a *ItemWindowApp) finishMarketFallbackSync(opts ItemOptions, pending *marketFallbackSyncIntent, store *localprogress.Store) error {
	if pending.Version == 2 {
		if _, err := store.TransferAvailable(opts.CharacterUID, strings.ToUpper(opts.Mode), pending.OperationID, pending.ItemID, pending.Fallback); err != nil {
			return errors.Join(errMarketFallbackSyncPending, err)
		}
		if _, err := a.localStore.CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode), localprogress.ManualStockCorrection{
			ItemID: pending.ItemID, MarketKinds: &pending.MarketAfter, ObservedMarketQuantity: pending.Target}); err != nil {
			return errors.Join(errMarketFallbackSyncPending, err)
		}
		return a.removeMarketFallbackSync(opts, pending.ItemID)
	}
	current, err := marketFallbackSyncAmounts(store, opts, pending.ItemID)
	if err != nil {
		return err
	}
	zero := localprogress.StockAmounts{}
	if current != pending.Fallback && current != zero {
		return errors.New("Supplementary stock changed during the pending upload. The saved upload was kept; no stock was cleared.")
	}
	// Először eltűnik a már feltöltött helyi elérhető készlet. Ha a későbbi
	// minőségi mentés megszakad, a nyers Market mellé nem számoljuk kétszer.
	if current != zero {
		if _, err := store.CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode), localprogress.ManualStockCorrection{
			ItemID: pending.ItemID, LocalAvailable: &zero}); err != nil {
			return errors.Join(errMarketFallbackSyncPending, err)
		}
	}
	if _, err := a.localStore.CorrectStock(opts.CharacterUID, strings.ToUpper(opts.Mode), localprogress.ManualStockCorrection{
		ItemID: pending.ItemID, MarketKinds: &pending.MarketAfter, ObservedMarketQuantity: pending.Target}); err != nil {
		return errors.Join(errMarketFallbackSyncPending, err)
	}
	return a.removeMarketFallbackSync(opts, pending.ItemID)
}

func marketFallbackSyncDefiniteRejection(err error) bool {
	if err == nil || errors.Is(err, market.ErrProgressWriteUncertain) {
		return false
	}
	switch market.ProgressWriteFailure(err).HTTPStatus {
	case 400, 401, 403, 404, 409, 428, 429:
		return true
	}
	return errors.Is(err, market.ErrUnauthorized) || errors.Is(err, market.ErrAPIKeyRequired) ||
		errors.Is(err, market.ErrProgressForbidden) || errors.Is(err, market.ErrProgressNotFound) ||
		errors.Is(err, market.ErrProgressConflict) || errors.Is(err, market.ErrProgressRevisionRequired) ||
		errors.Is(err, market.ErrProgressInvalidQuantity) || errors.Is(err, market.ErrRateLimited)
}

func validMarketFallbackSync(pending marketFallbackSyncIntent) bool {
	quantity, err := stockEditorTotal(pending.Fallback)
	marketTotal, marketErr := stockEditorTotal(pending.MarketAfter)
	if pending.Version != 1 && pending.Version != 2 || pending.Version == 1 && pending.OperationID != "" || pending.Version == 2 && !stockUsageOperationID(pending.OperationID) ||
		!handoverItemID(pending.CharUID) || !stockEditorPendingMode(pending.Mode) || !handoverItemID(pending.ItemID) ||
		pending.Revision == "" || len(pending.Revision) > 256 || err != nil || marketErr != nil || quantity == 0 ||
		pending.Before < 0 || pending.Before > localprogress.MaxQuantity-quantity || pending.Target != pending.Before+quantity || marketTotal != pending.Target ||
		pending.MarketAfter.Regular < pending.Fallback.Regular || pending.MarketAfter.FIR < pending.Fallback.FIR || pending.MarketAfter.Unknown < pending.Fallback.Unknown {
		return false
	}
	for _, character := range pending.Revision {
		if character < 33 || character > 126 || character == '"' || character == '\\' {
			return false
		}
	}
	return true
}

func (a *ItemWindowApp) readMarketFallbackSync(opts ItemOptions, itemIDs ...string) (*marketFallbackSyncIntent, error) {
	if len(itemIDs) > 1 || len(itemIDs) == 1 && !handoverItemID(itemIDs[0]) {
		return nil, errMarketFallbackSyncInvalid
	}
	intents, err := a.readMarketFallbackSyncIntents(opts)
	if err != nil {
		return nil, errors.Join(errMarketFallbackSyncInvalid, err)
	}
	for _, pending := range intents {
		if len(itemIDs) == 0 || pending.ItemID == itemIDs[0] {
			return pending, nil
		}
	}
	return nil, nil
}

func (a *ItemWindowApp) readMarketFallbackSyncItem(opts ItemOptions, itemID string) (*marketFallbackSyncIntent, error) {
	if !handoverItemID(itemID) {
		return nil, errMarketFallbackSyncInvalid
	}
	return a.readMarketFallbackSync(opts, itemID)
}

func (a *ItemWindowApp) readMarketFallbackSyncIntents(opts ItemOptions) ([]*marketFallbackSyncIntent, error) {
	legacy, err := a.marketFallbackSyncLegacyPath(opts)
	if err != nil {
		return nil, err
	}
	directory, err := os.Open(filepath.Dir(legacy))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errMarketFallbackSyncInvalid
	}
	defer directory.Close()
	entries, err := directory.ReadDir(4097)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 4096 {
		return nil, errMarketFallbackSyncInvalid
	}
	prefix := strings.TrimSuffix(filepath.Base(legacy), ".json") + "-"
	seen := make(map[string]bool)
	var intents []*marketFallbackSyncIntent
	for _, entry := range entries {
		if entry.Name() != filepath.Base(legacy) && !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil, errMarketFallbackSyncInvalid
		}
		path := filepath.Join(filepath.Dir(legacy), entry.Name())
		pending, err := readMarketFallbackSyncFile(path, opts)
		if err != nil || pending == nil || seen[pending.ItemID] {
			return nil, errMarketFallbackSyncInvalid
		}
		expected, err := a.marketFallbackSyncPath(opts, pending.ItemID)
		if err != nil || path != legacy && path != expected {
			return nil, errMarketFallbackSyncInvalid
		}
		seen[pending.ItemID] = true
		intents = append(intents, pending)
	}
	sort.Slice(intents, func(i, j int) bool { return intents[i].ItemID < intents[j].ItemID })
	return intents, nil
}

func readMarketFallbackSyncFile(path string, opts ItemOptions) (*marketFallbackSyncIntent, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errMarketFallbackSyncInvalid
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMarketFallbackSyncBytes+1))
	if err != nil || len(data) > maxMarketFallbackSyncBytes || !stockUsageUniqueJSON(data) || !marketFallbackSyncCanonicalJSON(data) {
		return nil, errMarketFallbackSyncInvalid
	}
	type amountsWire struct {
		Regular *int64 `json:"regular"`
		FIR     *int64 `json:"fir"`
		Unknown *int64 `json:"unknown"`
	}
	var wire struct {
		Version     int          `json:"version"`
		OperationID string       `json:"operationId"`
		CharUID     string       `json:"charUid"`
		Mode        string       `json:"mode"`
		ItemID      string       `json:"itemId"`
		Revision    string       `json:"revision"`
		Before      *int64       `json:"before"`
		Target      *int64       `json:"target"`
		Fallback    *amountsWire `json:"fallback"`
		MarketAfter *amountsWire `json:"marketAfter"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.Before == nil || wire.Target == nil || wire.Fallback == nil || wire.MarketAfter == nil ||
		wire.Fallback.Regular == nil || wire.Fallback.FIR == nil || wire.Fallback.Unknown == nil ||
		wire.MarketAfter.Regular == nil || wire.MarketAfter.FIR == nil || wire.MarketAfter.Unknown == nil {
		return nil, errMarketFallbackSyncInvalid
	}
	pending := &marketFallbackSyncIntent{Version: wire.Version, OperationID: wire.OperationID, CharUID: wire.CharUID, Mode: wire.Mode, ItemID: wire.ItemID,
		Revision: wire.Revision, Before: *wire.Before, Target: *wire.Target,
		Fallback:    localprogress.StockAmounts{Regular: *wire.Fallback.Regular, FIR: *wire.Fallback.FIR, Unknown: *wire.Fallback.Unknown},
		MarketAfter: localprogress.StockAmounts{Regular: *wire.MarketAfter.Regular, FIR: *wire.MarketAfter.FIR, Unknown: *wire.MarketAfter.Unknown}}
	if !validMarketFallbackSync(*pending) || pending.CharUID != opts.CharacterUID || pending.Mode != strings.ToUpper(opts.Mode) {
		return nil, errMarketFallbackSyncInvalid
	}
	return pending, nil
}

// A Go dekóder kis- és nagybetűs mezőaliasokat is elfogadna. A tartós
// írási szándékban csak a kanonikus neveket engedjük, így egy alias sem
// írhatja felül csendben a korábban megadott mennyiséget.
func marketFallbackSyncCanonicalJSON(data []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	var version int
	if json.Unmarshal(fields["version"], &version) != nil || version != 1 && version != 2 {
		return false
	}
	keys := []string{"version", "charUid", "mode", "itemId", "revision", "before", "target", "fallback", "marketAfter"}
	if version == 2 {
		keys = append(keys, "operationId")
	}
	if len(fields) != len(keys) {
		return false
	}
	for _, field := range keys {
		if _, exists := fields[field]; !exists {
			return false
		}
		if bytes.Equal(bytes.TrimSpace(fields[field]), []byte("null")) {
			return false
		}
	}
	for _, field := range []string{"fallback", "marketAfter"} {
		var amounts map[string]json.RawMessage
		if json.Unmarshal(fields[field], &amounts) != nil || len(amounts) != 3 {
			return false
		}
		for _, kind := range []string{"regular", "fir", "unknown"} {
			if _, exists := amounts[kind]; !exists {
				return false
			}
		}
	}
	return true
}

func marketFallbackContains(current, captured localprogress.StockAmounts) bool {
	return current.Regular >= captured.Regular && current.FIR >= captured.FIR && current.Unknown >= captured.Unknown
}

// Új darabok csak az új csomagos napló mellett fűzhetők hozzá. A még
// befoglalt minőségi részeket leadás és szerkesztés továbbra sem használhatja.
func (a *ItemWindowApp) marketFallbackIntentAllowsAppend(opts ItemOptions, itemID string) (bool, error) {
	if !handoverItemID(itemID) {
		return false, errMarketFallbackSyncInvalid
	}
	pending, err := a.readMarketFallbackSync(opts, itemID)
	if err != nil || pending == nil {
		return pending == nil && err == nil, err
	}
	if pending.ItemID != itemID {
		return true, nil
	}
	if pending.Version != 2 {
		return false, nil
	}
	store := a.marketFallbackStock()
	recorded, err := store.TransferRecorded(opts.CharacterUID, strings.ToUpper(opts.Mode), pending.OperationID, itemID, pending.Fallback)
	if err != nil {
		return false, err
	}
	if recorded {
		return true, nil
	}
	current, err := marketFallbackSyncAmounts(store, opts, itemID)
	return err == nil && marketFallbackContains(current, pending.Fallback), err
}

func (a *ItemWindowApp) saveMarketFallbackSync(opts ItemOptions, pending *marketFallbackSyncIntent) error {
	if pending == nil || !validMarketFallbackSync(*pending) || pending.CharUID != opts.CharacterUID || pending.Mode != strings.ToUpper(opts.Mode) {
		return errMarketFallbackSyncInvalid
	}
	path, err := a.marketFallbackSyncPath(opts, pending.ItemID)
	if err != nil {
		return err
	}
	previous, err := a.readMarketFallbackSync(opts, pending.ItemID)
	if err != nil {
		return err
	}
	if previous != nil {
		if *previous == *pending {
			return nil
		}
		return errMarketFallbackSyncPending
	}
	return a.writeMarketFallbackSync(path, pending)
}

func (a *ItemWindowApp) writeMarketFallbackSync(path string, pending *marketFallbackSyncIntent) error {
	data, err := json.MarshalIndent(pending, "", "  ")
	if err != nil || len(data) > maxMarketFallbackSyncBytes {
		return errMarketFallbackSyncInvalid
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".fallback-sync-*.tmp")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func (a *ItemWindowApp) removeMarketFallbackSync(opts ItemOptions, itemIDs ...string) error {
	pending, err := a.readMarketFallbackSync(opts, itemIDs...)
	if err != nil {
		return err
	}
	if pending == nil {
		return nil
	}
	path, err := a.marketFallbackSyncPath(opts, pending.ItemID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		path, err = a.marketFallbackSyncLegacyPath(opts)
		if err != nil {
			return err
		}
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
