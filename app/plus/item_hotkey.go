package plus

// A shortcut is bound to the item and character visible at keydown. Replacing
// this immutable target invalidates an activation that has not started yet.
type trackedAddHotkeyTarget struct {
	UID, ItemID, CharUID string
	Revision             uint64
}

func (a *ItemWindowApp) canAddTrackedItemLocked() bool {
	t := a.state.Tracking
	item := a.state.Item
	return a.ctx != nil && a.ctx.Err() == nil && a.tracker != nil && a.apiKey != "" && a.state.HasAPIKey &&
		!a.state.Busy && a.state.Error == "" && item != nil && item.UID != "" && item.BSGID != "" &&
		a.state.CharacterUID != "" && t != nil && t.ItemUID == item.UID && t.ItemID == item.BSGID &&
		t.CharUID == a.state.CharacterUID && t.Tracked && t.RequiredQuantity > 0 && !t.InventorySeparate &&
		len(t.Variants) == 0 && !t.Loading && !t.Adding && t.Error == ""
}

func (a *ItemWindowApp) publishTrackedAddHotkey() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.state.AddHotkeyReady || normalizeTrackedAddHotkey(a.state.AddHotkey) == "off" || !a.canAddTrackedItemLocked() {
		a.addHotkeyTarget.Store(nil)
		return
	}
	a.addHotkeyTarget.Store(&trackedAddHotkeyTarget{
		UID: a.state.Item.UID, ItemID: a.state.Item.BSGID,
		CharUID: a.state.CharacterUID, Revision: a.state.ItemOptions.revision,
	})
}

// This runs in the Windows keyboard callback: no locks, API calls or Wails
// events. An occupied worker drops another press instead of replaying it later.
func (a *ItemWindowApp) requestTrackedAddHotkey() {
	target := a.addHotkeyTarget.Load()
	if target == nil || !a.addHotkeyPending.CompareAndSwap(false, true) {
		return
	}
	select {
	case a.addHotkeyRequests <- target:
	default:
		a.addHotkeyPending.Store(false)
	}
}

func (a *ItemWindowApp) runTrackedAddHotkeys() {
	for {
		select {
		case <-a.ctx.Done():
			a.addHotkeyTarget.Store(nil)
			a.addHotkeyPending.Store(false)
			return
		case target := <-a.addHotkeyRequests:
			if a.ctx.Err() == nil && target != nil {
				a.addTrackedItem(target.UID, target)
			}
			a.addHotkeyPending.Store(false)
		}
	}
}

func (a *ItemWindowApp) startTrackedAddHotkey() {
	a.addHotkeyRequests = make(chan *trackedAddHotkeyTarget, 1)
	go a.runTrackedAddHotkeys()
	a.hotkeyConfigMu.Lock()
	hotkey := newTrackedAddHotkey(a.ctx, a.requestTrackedAddHotkey, a.setTrackedAddHotkeyStatus)
	a.mu.Lock()
	a.addHotkey = hotkey
	key := a.state.AddHotkey
	a.mu.Unlock()
	hotkey.SetKey(key)
	a.hotkeyConfigMu.Unlock()
}

func (a *ItemWindowApp) configureTrackedAddHotkey() {
	a.hotkeyConfigMu.Lock()
	defer a.hotkeyConfigMu.Unlock()
	a.mu.Lock()
	hotkey, key := a.addHotkey, a.state.AddHotkey
	a.mu.Unlock()
	if hotkey != nil {
		hotkey.SetKey(key)
	}
}

func (a *ItemWindowApp) setTrackedAddHotkeyStatus(ready bool, message string) {
	a.mu.Lock()
	a.state.AddHotkeyReady = ready && normalizeTrackedAddHotkey(a.state.AddHotkey) != "off" && a.ctx != nil && a.ctx.Err() == nil
	a.state.AddHotkeyError = message
	a.mu.Unlock()
	a.emit()
}

func (a *ItemWindowApp) stopTrackedAddHotkey() {
	a.hotkeyConfigMu.Lock()
	defer a.hotkeyConfigMu.Unlock()
	a.mu.Lock()
	hotkey := a.addHotkey
	a.addHotkey = nil
	a.state.AddHotkeyReady = false
	a.addHotkeyTarget.Store(nil)
	a.mu.Unlock()
	if hotkey != nil {
		hotkey.Close()
	}
}
