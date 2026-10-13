package plus

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

// A teljes helyi mentés cseréje után az előző kiosztásból nem engedünk új
// készletírást. A következő frissítés építi fel az aktuális nézetet.
// A hívó trackingMu, scanMu és mu zárolást tart; hálózati művelet itt nincs.
func (a *ItemWindowApp) resetAfterLocalProgressImportLocked() {
	open := a.state.StockEditor != nil && a.state.StockEditor.Open
	a.cancelTrackingWriteLocked()
	a.state.ItemOptions.revision++
	for {
		select {
		case <-a.trackingRequests:
			continue
		default:
		}
		break
	}
	a.localStore = localprogress.NewStore(filepath.Join(a.dir, "local-progress"))
	a.fallbackStoreMu.Lock()
	a.fallbackStore = nil
	a.fallbackStoreMu.Unlock()
	a.fallbackSyncAt = nil
	a.localSnapshot = localprogress.Snapshot{}
	a.progressBaseSnapshot = localprogress.Snapshot{}
	a.progressBaseRevision = 0
	a.progressBaseWarning = ""
	a.progressBaseAt = time.Time{}
	a.progressUnsafeItems = nil
	a.localAt, a.trackedAt = time.Time{}, time.Time{}
	a.localRevision, a.trackedRevision = 0, 0
	a.trackedSnapshot = market.TrackedSnapshot{}
	a.state.Local, a.state.Tracking = nil, nil
	a.restoreHandoverLocked()
	a.restoreStockEditorLocked()
	if open {
		if a.state.StockEditor == nil {
			a.state.StockEditor = &ItemStockEditorState{
				Generation: a.stockEditorGeneration, CharUID: a.state.CharacterUID,
				GameMode: strings.ToUpper(a.state.Mode), Rows: []ItemStockEditorRow{},
			}
		}
		a.state.StockEditor.Open = true
		a.state.StockEditor.Status = "Local progress imported. Refresh to load current Market requirements."
	}
	a.queueProgressWork()
}
