package plus

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

// Egy abszolút setter OK válasza nem feltétlenül új darab mentése. A saját
// típusjelölést csak tényleges változás visszaigazolásával rögzítjük; egy régi
// vagy eltérő készletet visszaadó GET nem teszi sikeressé az Add műveletet.
func (a *ItemWindowApp) finishTrackedMarketAdd(ctx context.Context, key string, opts ItemOptions, item market.Item,
	before market.TrackedSnapshot, result market.ProgressWriteResult, target int64, kind string, writeErr error) ItemState {
	current, readErr := a.tracker.LoadTracked(ctx, key, opts.CharacterUID, opts.Language)
	changed := writeErr == nil && result.OK && result.Changed && result.Revision != "" && result.Revision != before.Revision
	noChange := writeErr == nil && result.OK && !result.Changed
	validSnapshot := readErr == nil && current.CharUID == opts.CharacterUID && strings.EqualFold(current.GameMode, opts.Mode) &&
		current.Items != nil && current.Revision != ""
	observed := trackedView(item, current)
	diagnostic := progressWriteDiagnostic(result, writeErr, before.Revision, current.Revision, readErr)
	if validSnapshot {
		diagnostic += progressItemMismatch(item, current)
	}
	validItem := validSnapshot && observed.Tracked && observed.Error == "" && len(observed.Variants) == 0 && !observed.InventorySeparate
	verified := validItem && observed.AvailableQuantity == target && current.Revision != before.Revision
	unchanged := validItem && observed.AvailableQuantity == target-1 && current.Revision == before.Revision
	if unchanged && (noChange && result.Revision == before.Revision || safeMarketFallbackRejection(writeErr)) {
		a.applyTracking(key, opts, item, current, time.Now(), "")
		if a.saveMarketFallbackAdd(key, opts, item, kind, "Market did not save this item. Saved locally for verified upload when Market recovers.") {
			a.emit()
			return a.GetState()
		}
	}
	if verified {
		status := "API stock verified: it contains the increased quantity."
		if noChange {
			status = fmt.Sprintf("Tarkov Market reported no change. Requested stock: %d; current stock: %d. No new item was added.", target, observed.AvailableQuantity)
		} else if changed {
			a.recordLocalMarketAdd(key, opts, item, kind, target-1, target)
			status = "Added 1 item to Tracked Items stock."
			if !result.NotificationDelivered {
				status += " Web notification was not confirmed."
			}
		}
		if writeErr != nil || noChange || !changed {
			status += " " + diagnostic
		}
		a.applyTracking(key, opts, item, current, time.Now(), status)
		return a.GetState()
	}
	status := fmt.Sprintf("The Market stock change could not be verified. Requested stock: %d; current stock: unavailable. Refresh progress before adding again.", target)
	shown := before
	if validSnapshot {
		shown = current
	}
	if validItem {
		status = fmt.Sprintf("Market did not confirm the increase. Requested stock: %d; current stock: %d. Refresh progress before adding again.", target, observed.AvailableQuantity)
		if current.Revision == before.Revision && observed.AvailableQuantity == target {
			status = fmt.Sprintf("The Market stock readback was outdated. Requested stock: %d; current stock: %d. Refresh progress before adding again.", target, observed.AvailableQuantity)
		}
	}
	if noChange {
		if validItem {
			status = fmt.Sprintf("Tarkov Market reported no change. Requested stock: %d; current stock: %d. No new item was confirmed; refresh progress before adding again.", target, observed.AvailableQuantity)
		} else {
			status = fmt.Sprintf("Tarkov Market reported no change. Requested stock: %d; current stock: unavailable. No new item was confirmed; refresh progress before adding again.", target)
		}
	} else if changed && readErr != nil {
		// A változást a szerver kifejezetten visszaigazolta, de még nem tudjuk
		// újraolvasni. A következő mentéshez előbb friss készlet szükséges.
		shown = cloneTrackedSnapshot(before)
		for i := range shown.Items {
			if shown.Items[i].ItemID == item.BSGID {
				shown.Items[i].AvailableQuantity = target
				shown.Items[i].RemainingQuantity = max(0, shown.Items[i].RequiredQuantity-target)
			}
		}
		shown.Revision = result.Revision
		a.recordLocalMarketAdd(key, opts, item, kind, target-1, target)
		status = fmt.Sprintf("Tarkov Market confirmed stock %d, but the stock readback is unavailable. Refresh progress before adding again.", target)
		if !result.NotificationDelivered {
			status += " Web notification was not confirmed."
		}
	}
	// Nem igazolt vagy elavult visszaolvasás nem hosszabbítja meg a cache-t.
	a.applyTracking(key, opts, item, shown, time.Time{}, status+" "+diagnostic)
	a.trackingFailure(key, opts, item.UID, market.ErrProgressWriteUncertain)
	return a.GetState()
}
