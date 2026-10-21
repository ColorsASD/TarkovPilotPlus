package plus

// A shortcut is bound to the item and character visible at keydown. Replacing
// this immutable target invalidates an activation that has not started yet.
type trackedAddHotkeyTarget struct {
	UID, ItemID, CharUID string
	Revision             uint64
	Remove               bool
	FIR                  bool
	Typed                bool
}

// An unrelated state publication keeps the accepted keydown valid. Once a
// target changes or is cleared, returning to its values creates a new target;
// an activation captured before that transition must never become current again.
func retainTrackedHotkeyTarget(current *trackedAddHotkeyTarget, next trackedAddHotkeyTarget) *trackedAddHotkeyTarget {
	if current != nil && *current == next {
		return current
	}
	return &next
}

func trackedAddHotkeyEnabled(opts ItemOptions) bool {
	return normalizeTrackedAddHotkey(opts.AddHotkey) != "off"
}

func trackedRemoveHotkeyEnabled(opts ItemOptions) bool {
	return normalizeTrackedRemoveHotkey(opts.RemoveHotkey) != "off"
}

func (a *ItemWindowApp) trackedAddHotkeyTargetCurrent(target *trackedAddHotkeyTarget) bool {
	if target == nil {
		return false
	}
	if target.Typed {
		i := qualityActionIndex(target.Remove, target.FIR)
		return qualityShortcutKeys(a.state.ItemOptions)[i] != "off" && a.qualityHotkeyTargets[i].Load() == target
	}
	if target.Remove {
		return trackedRemoveHotkeyEnabled(a.state.ItemOptions) && a.removeHotkeyTarget.Load() == target
	}
	return trackedAddHotkeyEnabled(a.state.ItemOptions) && a.addHotkeyTarget.Load() == target
}

func (a *ItemWindowApp) canRemoveTrackedItemLocked() bool {
	if a.state.HotkeyCapture || a.pendingExitApproved() || a.state.Local == nil || a.state.Local.Loading || a.state.Local.Error != "" {
		return false
	}
	t, item := a.state.Tracking, a.state.Item
	return a.ctx != nil && a.ctx.Err() == nil && a.tracker != nil && a.apiKey != "" && a.state.HasAPIKey &&
		!a.state.Busy && a.state.Error == "" && item != nil && item.UID != "" && item.BSGID != "" &&
		a.state.CharacterUID != "" && t != nil && t.ItemUID == item.UID && t.ItemID == item.BSGID &&
		t.CharUID == a.state.CharacterUID && t.AvailableQuantity > 0 && !t.InventorySeparate &&
		len(t.Variants) == 0 && !t.Loading && !t.Adding && t.Error == ""
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
	a.publishQualityHotkeysLocked()
	if a.state.AddHotkeyReady && trackedAddHotkeyEnabled(a.state.ItemOptions) && a.canAddTrackedItemLocked() && a.state.Tracking.AddAllowed {
		a.addHotkeyTarget.Store(retainTrackedHotkeyTarget(a.addHotkeyTarget.Load(), trackedAddHotkeyTarget{
			UID: a.state.Item.UID, ItemID: a.state.Item.BSGID,
			CharUID: a.state.CharacterUID, Revision: a.state.ItemOptions.revision,
		}))
	} else {
		a.addHotkeyTarget.Store(nil)
	}
	if a.state.RemoveHotkeyReady && trackedRemoveHotkeyEnabled(a.state.ItemOptions) && a.canRemoveTrackedItemLocked() && a.state.Tracking.RemoveAllowed {
		a.removeHotkeyTarget.Store(retainTrackedHotkeyTarget(a.removeHotkeyTarget.Load(), trackedAddHotkeyTarget{
			UID: a.state.Item.UID, ItemID: a.state.Item.BSGID,
			CharUID: a.state.CharacterUID, Revision: a.state.ItemOptions.revision, Remove: true,
		}))
	} else {
		a.removeHotkeyTarget.Store(nil)
	}
}

// This runs in the Windows keyboard callback: no locks, API calls or Wails
// events. An occupied worker drops another press instead of replaying it later.
func (a *ItemWindowApp) requestTrackedAddHotkey() {
	a.queueTrackedAddHotkey(a.addHotkeyTarget.Load())
}

func (a *ItemWindowApp) requestTrackedRemoveHotkey() {
	a.queueTrackedAddHotkey(a.removeHotkeyTarget.Load())
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
			for i := range a.qualityHotkeyTargets {
				a.qualityHotkeyTargets[i].Store(nil)
			}
			a.addHotkeyTarget.Store(nil)
			a.removeHotkeyTarget.Store(nil)
			a.addHotkeyPending.Store(false)
			return
		case target := <-a.addHotkeyRequests:
			if a.ctx.Err() == nil && target != nil {
				if target.Typed {
					delta := int64(1)
					if target.Remove {
						delta = -1
					}
					a.reservePendingTypedMarketChange(target.UID, target, delta, target.FIR)
				} else if target.Remove {
					a.removeTrackedItem(target.UID, target)
				} else {
					a.addTrackedItem(target.UID, target)
				}
			}
			a.addHotkeyPending.Store(false)
		}
	}
}

func (a *ItemWindowApp) startTrackedAddHotkey() {
	a.addHotkeyRequests = make(chan *trackedAddHotkeyTarget, 1)
	go a.runTrackedAddHotkeys()
	a.hotkeyConfigMu.Lock()
	a.mu.Lock()
	opts := a.state.ItemOptions
	capture := a.state.HotkeyCapture
	a.mu.Unlock()
	for i := range a.qualityHotkeys {
		i := i
		a.qualityHotkeys[i] = newTrackedAddHotkey(a.ctx,
			func() { a.queueTrackedAddHotkey(a.qualityHotkeyTargets[i].Load()) },
			func(ready bool, message string) { a.setQualityHotkeyStatus(i, ready, message) })
	}
	a.configureQualityHotkeyKeys(opts, capture)
	a.hotkeyConfigMu.Unlock()
}

func (a *ItemWindowApp) configureTrackedAddHotkey() {
	a.hotkeyConfigMu.Lock()
	defer a.hotkeyConfigMu.Unlock()
	a.mu.Lock()
	hotkey, opts := a.addHotkey, a.state.ItemOptions
	remove := a.removeHotkey
	capture := a.state.HotkeyCapture
	a.mu.Unlock()
	a.configureQualityHotkeyKeys(opts, capture)
	if hotkey != nil {
		configureTrackedHotkeyKeys(hotkey, opts, capture)
	}
	if remove != nil {
		configureTrackedRemoveHotkeyKeys(remove, opts, capture)
	}
}

func configureTrackedRemoveHotkeyKeys(hotkey *trackedAddHotkey, opts ItemOptions, capture bool) {
	if capture || normalizeTrackedRemoveHotkey(opts.RemoveHotkey) == normalizeTrackedAddHotkey(opts.AddHotkey) {
		hotkey.SetKey("off")
		return
	}
	hotkey.SetKey(normalizeTrackedRemoveHotkey(opts.RemoveHotkey))
}

func configureTrackedHotkeyKeys(hotkey *trackedAddHotkey, opts ItemOptions, capture ...bool) {
	if len(capture) > 0 && capture[0] {
		hotkey.SetKey("off")
		return
	}
	hotkey.SetKey(normalizeTrackedAddHotkey(opts.AddHotkey))
}

func (a *ItemWindowApp) SetHotkeyCapture(enabled bool) ItemState {
	a.mu.Lock()
	if enabled && (a.state.Busy) {
		a.mu.Unlock()
		return a.GetState()
	}
	a.state.HotkeyCapture = enabled
	for i := range a.qualityHotkeyTargets {
		a.qualityHotkeyTargets[i].Store(nil)
	}
	a.addHotkeyTarget.Store(nil)
	a.removeHotkeyTarget.Store(nil)
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

func (a *ItemWindowApp) setTrackedRemoveHotkeyStatus(ready bool, message string) {
	a.mu.Lock()
	a.state.RemoveHotkeyReady = ready && !a.state.HotkeyCapture && trackedRemoveHotkeyEnabled(a.state.ItemOptions) && a.ctx != nil && a.ctx.Err() == nil
	a.state.RemoveHotkeyError = message
	a.mu.Unlock()
	a.emit()
}

func (a *ItemWindowApp) stopTrackedAddHotkey() {
	a.hotkeyConfigMu.Lock()
	defer a.hotkeyConfigMu.Unlock()
	a.mu.Lock()
	hotkey := a.addHotkey
	remove := a.removeHotkey
	a.addHotkey = nil
	a.removeHotkey = nil
	a.state.AddHotkeyReady = false
	a.state.RemoveHotkeyReady = false
	a.addHotkeyTarget.Store(nil)
	a.removeHotkeyTarget.Store(nil)
	for i := range a.qualityHotkeyTargets {
		a.qualityHotkeyTargets[i].Store(nil)
	}
	a.state.AddFIRHotkeyReady, a.state.AddRegularHotkeyReady = false, false
	a.state.RemoveFIRHotkeyReady, a.state.RemoveRegularHotkeyReady = false, false
	a.mu.Unlock()
	for i, hotkey := range a.qualityHotkeys {
		if hotkey != nil {
			hotkey.Close()
			a.qualityHotkeys[i] = nil
		}
	}
	if hotkey != nil {
		hotkey.Close()
	}
	if remove != nil {
		remove.Close()
	}
}
