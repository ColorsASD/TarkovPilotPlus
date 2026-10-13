package plus

const progressBusyMessage = "Progress is busy. Try again when it finishes."

// A foglalt előrehaladási zárolás nem tárol és nem sorol be műveletet.
// A visszajelzés csak a még aktuális, látható célhoz tartozik.
func (a *ItemWindowApp) notifyTrackedAddBusy(uid string, target *trackedAddHotkeyTarget) {
	a.mu.Lock()
	item, tracking := a.state.Item, a.state.Tracking
	current := a.ctx != nil && a.ctx.Err() == nil && item != nil && tracking != nil && uid != "" &&
		item.UID == uid && tracking.ItemUID == uid && item.BSGID != "" &&
		(tracking.ItemID == "" || tracking.ItemID == item.BSGID) &&
		a.state.CharacterUID != "" && tracking.CharUID == a.state.CharacterUID &&
		a.state.CharacterUID == a.state.ItemOptions.CharacterUID
	if current && target != nil {
		current = a.trackedAddHotkeyTargetCurrent(target) && target.UID == uid && target.ItemID == item.BSGID &&
			target.CharUID == a.state.CharacterUID && target.Revision == a.state.ItemOptions.revision
	}
	if current {
		tracking.Status = progressBusyMessage
	}
	a.mu.Unlock()
	if current {
		a.emit()
	}
}

func (a *ItemWindowApp) notifyHandoverBusy() {
	a.mu.Lock()
	st := a.state.Handover
	current := a.ctx != nil && a.ctx.Err() == nil && st != nil && st.Open && !st.Completed &&
		a.state.CharacterUID != "" && st.CharUID == a.state.CharacterUID &&
		a.state.CharacterUID == a.state.ItemOptions.CharacterUID && !(st.Pending && a.handover == nil)
	if current {
		if st.Pending || st.Busy {
			// Egy már rögzített vagy futó leadás hibáját és állapotát nem
			// változtatjuk meg egy újabb megerősítés visszautasításával.
			st.Status = progressBusyMessage
		} else {
			st.Error = progressBusyMessage
		}
	}
	a.mu.Unlock()
	if current {
		a.emit()
	}
}
