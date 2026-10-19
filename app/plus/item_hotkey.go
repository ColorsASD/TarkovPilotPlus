package plus

// A shortcut is bound to the item and character visible at keydown. Replacing
// this immutable target invalidates an activation that has not started yet.
type trackedAddHotkeyTarget struct {
	UID, ItemID, CharUID string
	Revision             uint64
	Kind                 string
}

func trackedAddHotkeyEnabled(opts ItemOptions) bool {
	return trackedActionHotkeyEnabled(opts, "regular") || trackedActionHotkeyEnabled(opts, "fir")
}

func trackedActionHotkeyEnabled(opts ItemOptions, kind string) bool {
	if kind == "fir" {
		return normalizeTrackedFIRHotkey(opts.FIRHotkey) != "off"
	}
	return normalizeTrackedAddHotkey(opts.AddHotkey) != "off"
}

func (a *ItemWindowApp) trackedAddHotkeyTargetCurrent(target *trackedAddHotkeyTarget) bool {
	if target == nil {
		return false
	}
	if target.Kind == "fir" {
		return trackedActionHotkeyEnabled(a.state.ItemOptions, "fir") && a.addHotkeyFIRTarget.Load() == target
	}
	return (target.Kind == "" || target.Kind == "regular") && trackedActionHotkeyEnabled(a.state.ItemOptions, "regular") && a.addHotkeyTarget.Load() == target
}

func (a *ItemWindowApp) canAddTrackedItemLocked() bool {
	if a.state.HotkeyCapture || a.pendingExitApproved() {
		return false
	}
	if a.state.Local == nil || a.state.Local.Loading || a.state.Local.Error != "" {
		return false
	}
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
	if !a.state.AddHotkeyReady || !trackedAddHotkeyEnabled(a.state.ItemOptions) || !a.canAddTrackedItemLocked() {
		a.addHotkeyTarget.Store(nil)
		a.addHotkeyFIRTarget.Store(nil)
		return
	}
	if trackedActionHotkeyEnabled(a.state.ItemOptions, "regular") && a.state.Tracking.AddRegularAllowed {
		a.addHotkeyTarget.Store(&trackedAddHotkeyTarget{
			UID: a.state.Item.UID, ItemID: a.state.Item.BSGID,
			CharUID: a.state.CharacterUID, Revision: a.state.ItemOptions.revision, Kind: "regular",
		})
	} else {
		a.addHotkeyTarget.Store(nil)
	}
	if trackedActionHotkeyEnabled(a.state.ItemOptions, "fir") && a.state.Tracking.AddFIRAllowed {
		a.addHotkeyFIRTarget.Store(&trackedAddHotkeyTarget{
			UID: a.state.Item.UID, ItemID: a.state.Item.BSGID,
			CharUID: a.state.CharacterUID, Revision: a.state.ItemOptions.revision, Kind: "fir",
		})
	} else {
		a.addHotkeyFIRTarget.Store(nil)
	}
}

// This runs in the Windows keyboard callback: no locks, API calls or Wails
// events. An occupied worker drops another press instead of replaying it later.
func (a *ItemWindowApp) requestTrackedAddHotkey() {
	a.queueTrackedAddHotkey(a.addHotkeyTarget.Load())
}

func (a *ItemWindowApp) requestTrackedAddFIRHotkey() {
	a.queueTrackedAddHotkey(a.addHotkeyFIRTarget.Load())
}

func (a *ItemWindowApp) queueTrackedAddHotkey(target *trackedAddHotkeyTarget) {
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
			a.addHotkeyFIRTarget.Store(nil)
			a.addHotkeyPending.Store(false)
			return
		case target := <-a.addHotkeyRequests:
			if a.ctx.Err() == nil && target != nil {
				a.addTrackedItemKind(target.UID, target, target.Kind)
			}
			a.addHotkeyPending.Store(false)
		}
	}
}

func (a *ItemWindowApp) startTrackedAddHotkey() {
	a.addHotkeyRequests = make(chan *trackedAddHotkeyTarget, 1)
	go a.runTrackedAddHotkeys()
	a.hotkeyConfigMu.Lock()
	hotkey := newTrackedAddHotkey(a.ctx, a.requestTrackedAddHotkey, a.setTrackedAddHotkeyStatus, a.requestTrackedAddFIRHotkey)
	a.mu.Lock()
	a.addHotkey = hotkey
	opts := a.state.ItemOptions
	capture := a.state.HotkeyCapture
	a.mu.Unlock()
	configureTrackedHotkeyKeys(hotkey, opts, capture)
	a.hotkeyConfigMu.Unlock()
}

func (a *ItemWindowApp) configureTrackedAddHotkey() {
	a.hotkeyConfigMu.Lock()
	defer a.hotkeyConfigMu.Unlock()
	a.mu.Lock()
	hotkey, opts := a.addHotkey, a.state.ItemOptions
	capture := a.state.HotkeyCapture
	a.mu.Unlock()
	if hotkey != nil {
		configureTrackedHotkeyKeys(hotkey, opts, capture)
	}
}

func configureTrackedHotkeyKeys(hotkey *trackedAddHotkey, opts ItemOptions, capture ...bool) {
	if len(capture) > 0 && capture[0] {
		hotkey.SetKeys("off", "off")
		return
	}
	hotkey.SetKeys(normalizeTrackedAddHotkey(opts.AddHotkey), normalizeTrackedFIRHotkey(opts.FIRHotkey))
}

func (a *ItemWindowApp) SetHotkeyCapture(enabled bool) ItemState {
	a.mu.Lock()
	if enabled && (a.state.Busy) {
		a.mu.Unlock()
		return a.GetState()
	}
	a.state.HotkeyCapture = enabled
	a.addHotkeyTarget.Store(nil)
	a.addHotkeyFIRTarget.Store(nil)
	a.mu.Unlock()
	a.configureTrackedAddHotkey()
	a.emit()
	return a.GetState()
}

func (a *ItemWindowApp) setTrackedAddHotkeyStatus(ready bool, message string) {
	a.mu.Lock()
	a.state.AddHotkeyReady = ready && !a.state.HotkeyCapture && trackedAddHotkeyEnabled(a.state.ItemOptions) && a.ctx != nil && a.ctx.Err() == nil
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
	a.addHotkeyFIRTarget.Store(nil)
	a.mu.Unlock()
	if hotkey != nil {
		hotkey.Close()
	}
}
