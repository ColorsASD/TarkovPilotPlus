package plus

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

// A visszaigazolt Market-alap kizárólag memóriában él. A helyi készlet és
// a még függő írások mindig a tartós saját tárolóból kerülnek a nézetbe.
func (a *ItemWindowApp) rememberProgressSources(key string, opts ItemOptions, snapshot localprogress.Snapshot, warning string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.localCurrentLocked(key, opts) || snapshot.CharUID != opts.CharacterUID || !strings.EqualFold(snapshot.GameMode, opts.Mode) {
		return context.Canceled
	}
	a.progressBaseSnapshot = localprogress.Clone(snapshot)
	a.progressUnsafeItems = nil
	a.progressBaseRevision, a.progressBaseWarning, a.progressBaseAt = opts.revision, warning, time.Now()
	return nil
}

func (a *ItemWindowApp) localFirstSnapshot(key string, opts ItemOptions) (localprogress.Snapshot, string, error) {
	a.mu.Lock()
	if !a.localCurrentLocked(key, opts) {
		a.mu.Unlock()
		return localprogress.Snapshot{}, "", context.Canceled
	}
	base, warning := localprogress.Clone(a.progressBaseSnapshot), a.progressBaseWarning
	if a.progressBaseRevision != opts.revision || base.CharUID != opts.CharacterUID || !strings.EqualFold(base.GameMode, opts.Mode) || base.Revision == "" {
		// Régi, már felépített alkalmazásállapotot az első használatkor átveszünk.
		// Új v2 művelet előtt ez történik, így a vetített cél nem válik alappá.
		if a.localRevision != opts.revision || a.localSnapshot.CharUID != opts.CharacterUID || !strings.EqualFold(a.localSnapshot.GameMode, opts.Mode) || a.localSnapshot.Revision == "" {
			a.mu.Unlock()
			return localprogress.Snapshot{}, "", errors.New("Progress is loading. Wait for the first confirmed requirements.")
		}
		base = localprogress.Clone(a.localSnapshot)
		a.progressBaseSnapshot, a.progressBaseRevision = localprogress.Clone(base), opts.revision
		if a.state.Local != nil {
			warning = a.state.Local.Warning
		}
		a.progressBaseWarning, a.progressBaseAt = warning, a.localAt
	}
	a.mu.Unlock()
	if a.localStore == nil {
		return base, warning, localprogress.ErrInvalidSnapshot
	}
	projected, err := a.applyProgressStock(base)
	if err != nil {
		return base, warning, err
	}
	allocated, err := localprogress.Allocate(projected)
	return allocated, warning, err
}

// A hívó rövid trackingMu-szakaszban publikálhat. Nincs hálózati kérés vagy
// várakozás a háttérfeltöltésre, és az Inspect közben kiválasztott tárgyat mutatjuk.
func (a *ItemWindowApp) publishLocalFirstProgress(key string, opts ItemOptions, status string) {
	snapshot, warning, err := a.localFirstSnapshot(key, opts)
	a.mu.Lock()
	refreshEditor := a.state.StockEditor != nil && a.state.StockEditor.Open && !a.state.StockEditor.Saving
	a.mu.Unlock()
	var rows []ItemStockEditorRow
	var rowsErr error
	if err == nil && refreshEditor {
		rows, rowsErr = a.stockEditorRows(context.Background(), key, opts, snapshot)
	}
	a.mu.Lock()
	if !a.localCurrentLocked(key, opts) {
		a.mu.Unlock()
		return
	}
	if err != nil {
		if a.state.Local == nil {
			a.state.Local = &LocalProgressState{}
		}
		a.state.Local.Error, a.state.Local.StockError = localProgressErrorText(localProgressFailure("save", err)), true
	} else {
		a.localSnapshot, a.localRevision, a.localAt = snapshot, opts.revision, time.Now()
		a.state.Local = localLists(snapshot, a.localAt, warning)
		if a.state.Item != nil && a.state.Item.BSGID != "" {
			a.state.Tracking = localTrackedView(*a.state.Item, snapshot)
			a.state.Tracking.Status = status
			if reason := a.progressUnsafeItemLocked(a.state.Item.BSGID); reason != "" {
				a.state.Tracking.Error = reason
				a.state.Tracking.AddRegularAllowed, a.state.Tracking.AddFIRAllowed = false, false
			}
		}
		if refreshEditor && rowsErr == nil && a.state.StockEditor != nil && a.state.StockEditor.Open && !a.state.StockEditor.Saving {
			a.stockEditorGeneration++
			a.state.StockEditor.Generation = a.stockEditorGeneration
			a.state.StockEditor.Rows = rows
			a.state.StockEditor.Loading = false
			a.stockEditorSnapshot, a.stockEditorRevision = localprogress.Clone(snapshot), opts.revision
		}
	}
	_ = a.refreshMarketFallbackPendingLocked(opts)
	a.mu.Unlock()
	a.refreshProgressSyncState(opts, "", "", nil)
	a.emit()
}

func (a *ItemWindowApp) updateLocalFirstMarketSnapshot(key string, opts ItemOptions, remote market.TrackedSnapshot) error {
	if !marketFallbackSyncSnapshot(remote, opts) {
		return market.ErrInvalidProgress
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.localCurrentLocked(key, opts) {
		return context.Canceled
	}
	base := localprogress.Clone(a.progressBaseSnapshot)
	if base.CharUID != opts.CharacterUID || a.progressBaseRevision != opts.revision {
		base = localprogress.Clone(a.localSnapshot)
	}
	if base.CharUID != opts.CharacterUID || base.Revision == "" {
		return localprogress.ErrInvalidSnapshot
	}
	byID := make(map[string][]market.TrackedItem)
	for _, row := range remote.Items {
		byID[row.ItemID] = append(byID[row.ItemID], row)
	}
	items := make([]localprogress.Item, 0, len(base.Items))
	seen := make(map[string]bool)
	if a.progressUnsafeItems == nil {
		a.progressUnsafeItems = make(map[string]string)
	}
	changedRequirements := false
	for _, original := range base.Items {
		row := original
		seen[row.ItemID] = true
		matches := byID[row.ItemID]
		if !row.MarketTracked {
			if len(matches) > 0 {
				// A korábban csak dev-ből ismert ID új Market-forrását is
				// frissen mutatjuk. Az átfedő igényeket a teljes gráf egyesíti.
				a.progressUnsafeItems[row.ItemID] = "New Market requirements for this item are being refreshed."
				if !containsProgressItem(items, row.ItemID) {
					replacement := marketProgressOnly(market.TrackedSnapshot{CharUID: remote.CharUID, GameMode: remote.GameMode, Revision: remote.Revision, Items: matches})
					items = append(items, replacement.Items...)
				}
				changedRequirements = true
				continue
			}
			items = append(items, row)
			continue
		}
		if len(matches) == 0 {
			a.progressUnsafeItems[row.ItemID] = "This item is no longer in the current Market list. Requirements are being refreshed; its saved local stock has been kept."
			row.MarketTracked, row.MarketRequiredQuantity, row.MarketAvailableQuantity = false, 0, 0
			row.MarketRegularQuantity, row.MarketFIRQuantity = 0, 0
			sources := make([]localprogress.Source, 0, len(row.Sources))
			row.RequiredQuantity = 0
			for _, source := range row.Sources {
				if source.Origin == localprogress.OriginDev {
					sources = append(sources, source)
					row.RequiredQuantity += source.RemainingRequiredQuantity
				}
			}
			row.Sources = sources
			row.AvailableQuantity = row.LocalAvailableQuantity
			row.RemainingQuantity = max(0, row.RequiredQuantity-row.AvailableQuantity)
			items = append(items, row)
			changedRequirements = true
			continue
		}
		if len(matches) > 1 {
			a.progressUnsafeItems[row.ItemID] = "Multiple Market item variants share this ID. Requirements are being refreshed; stock changes for this item are unavailable."
			// A már megismert változatokat azonnal mutatjuk; a korábbi
			// egyértelmű készlet nem örökölheti az új revíziót.
			if !containsProgressItem(items, row.ItemID) {
				replacement := marketProgressOnly(market.TrackedSnapshot{CharUID: remote.CharUID, GameMode: remote.GameMode, Revision: remote.Revision, Items: matches})
				items = append(items, replacement.Items...)
			}
			changedRequirements = true
			continue
		}
		if row.MarketRequiredQuantity != matches[0].RequiredQuantity || row.MarketRawSources != nil && !sameMarketRequirementsSources(row.MarketRawSources, matches[0].Sources) {
			a.progressUnsafeItems[row.ItemID] = "Market requirements for this item changed. The full requirement list is being refreshed."
			changedRequirements = true
		}
		row.MarketAvailableQuantity, row.SpentQuantity = matches[0].AvailableQuantity, matches[0].SpentQuantity
		row.MarketRegularQuantity, row.MarketFIRQuantity = 0, 0
		row.AvailableQuantity = row.MarketAvailableQuantity + row.LocalAvailableQuantity
		row.RemainingQuantity = max(0, row.RequiredQuantity-row.AvailableQuantity)
		items = append(items, row)
	}
	for _, incoming := range remote.Items {
		if !seen[incoming.ItemID] {
			seen[incoming.ItemID] = true
			matches := byID[incoming.ItemID]
			replacement := marketProgressOnly(market.TrackedSnapshot{CharUID: remote.CharUID, GameMode: remote.GameMode, Revision: remote.Revision, Items: matches})
			items = append(items, replacement.Items...)
			a.progressUnsafeItems[incoming.ItemID] = "New Market requirements for this item are being refreshed."
			changedRequirements = true
		}
	}
	base.Items = items
	if base.Revision != remote.Revision || changedRequirements {
		// A stock gyors visszaigazolása után a küldetés/hideout gráfot is
		// frissítjük; a régi követelményeket nem nevezzük újonnan ellenőrzöttnek.
		a.progressRefreshRequested = true
		a.queueProgressWork()
	}
	base.Revision, base.FIRInventorySeparate = remote.Revision, remote.FIRInventorySeparate
	a.progressBaseSnapshot, a.progressBaseRevision = base, opts.revision
	a.trackedSnapshot, a.trackedRevision, a.trackedAt = cloneTrackedSnapshot(remote), opts.revision, time.Now()
	return nil
}

// A hívó mu-t tart. Csak az érintett ID áll meg a teljes, koherens
// követelménygráf frissítéséig; a többi tárgy helyi műveletei folytatódnak.
func (a *ItemWindowApp) progressUnsafeItemLocked(id string) string {
	return a.progressUnsafeItems[id]
}

func containsProgressItem(items []localprogress.Item, id string) bool {
	for _, item := range items {
		if item.ItemID == id {
			return true
		}
	}
	return false
}

// A források sorrendje és a közös stock-revízió változhat anélkül, hogy
// a követelmény változna. A nyers források halmazát hasonlítjuk, darabszámmal.
func sameMarketRequirementsSources(previous, incoming []market.TrackedSource) bool {
	if len(previous) != len(incoming) {
		return false
	}
	counts := make(map[market.TrackedSource]int, len(previous))
	for _, source := range previous {
		counts[source]++
	}
	for _, source := range incoming {
		if counts[source] == 0 {
			return false
		}
		counts[source]--
	}
	return true
}
