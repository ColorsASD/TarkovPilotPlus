package plus

import "github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"

// A gomb és a gyorsbillentyű ugyanazt a teljes, típusos követelménylistát
// ellenőrzi. Elutasításkor nincs készletírás, és a másik típus használható marad.
// A hívó birtokolja az a.mu zárolást.
func (a *ItemWindowApp) allowTrackedItemKindLocked(itemID, kind string) bool {
	regular, fir, err := localprogress.AddOptions(a.localSnapshot, itemID)
	if a.state.Tracking == nil {
		return false
	}
	a.state.Tracking.AddRegularAllowed, a.state.Tracking.AddFIRAllowed = regular, fir
	if err == nil && (kind == "regular" && regular || kind == "fir" && fir) {
		return true
	}
	a.state.Tracking.Adding = false
	a.state.Tracking.Status = "No remaining requirement can use another Regular item."
	if kind == "fir" {
		a.state.Tracking.Status = "No remaining requirement can use another Found in Raid item."
	}
	if err != nil {
		a.state.Tracking.Status = "Current requirements could not be verified. Refresh progress again."
	}
	return false
}
