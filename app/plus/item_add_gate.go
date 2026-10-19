package plus

import "github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"

// A gomb es a gyorsbillentyu ugyanazt a kozos keszletigenyt ellenorzi.
// A hivo birtokolja az a.mu zarolast.
func (a *ItemWindowApp) allowTrackedItemLocked(itemID string) bool {
	allowed, err := localprogress.AddAllowed(a.localSnapshot, itemID)
	if a.state.Tracking == nil {
		return false
	}
	a.state.Tracking.AddAllowed = allowed
	if err == nil && allowed {
		return true
	}
	a.state.Tracking.Adding = false
	a.state.Tracking.Status = "No remaining requirement can use another item."
	if err != nil {
		a.state.Tracking.Status = "Current requirements could not be verified. Refresh progress again."
	}
	return false
}

// A törléshez pozitív, egyértelmű közös készlet kell, aktív gyűjtési cél nem.
// A teljes művelet állapot- és hozzáférési kapuját a hívó ellenőrzi a.mu alatt.
func (a *ItemWindowApp) allowTrackedItemRemovalLocked(itemID string) bool {
	if a.state.Tracking == nil {
		return false
	}
	row, unique := stockItem(a.localSnapshot, itemID)
	allowed := unique && !a.localSnapshot.FIRInventorySeparate && row.AvailableQuantity > 0 && row.AvailableQuantity <= localprogress.MaxQuantity
	a.state.Tracking.RemoveAllowed = allowed
	if allowed {
		return true
	}
	a.state.Tracking.Adding = false
	a.state.Tracking.Status = "There is no available stock to remove."
	if !unique || a.localSnapshot.FIRInventorySeparate {
		a.state.Tracking.Status = "Current stock could not be verified. Refresh progress again."
	}
	return false
}
