package plus

// Az indexek a négy, egymástól független készletművelet sorrendjét követik.
func qualityActionIndex(remove, fir bool) int {
	i := 0
	if !fir {
		i = 1
	}
	if remove {
		i += 2
	}
	return i
}

func qualityShortcutKeys(opts ItemOptions) [4]string {
	return [4]string{
		normalizeTrackedShortcut(opts.AddFIRHotkey, "F1"),
		normalizeTrackedShortcut(opts.AddRegularHotkey, "F2"),
		normalizeTrackedShortcut(opts.RemoveFIRHotkey, "F3"),
		normalizeTrackedShortcut(opts.RemoveRegularHotkey, "F4"),
	}
}

func (a *ItemWindowApp) qualityHotkeyReadyLocked(i int) bool {
	return [4]bool{a.state.AddFIRHotkeyReady, a.state.AddRegularHotkeyReady, a.state.RemoveFIRHotkeyReady, a.state.RemoveRegularHotkeyReady}[i]
}

func (a *ItemWindowApp) publishQualityHotkeysLocked() {
	keys := qualityShortcutKeys(a.state.ItemOptions)
	for i := range a.qualityHotkeyTargets {
		remove, fir := i >= 2, i%2 == 0
		allowed := a.canAddTrackedItemLocked()
		if remove {
			allowed = a.canRemoveTrackedItemLocked()
		}
		if allowed {
			t := a.state.Tracking
			allowed = [4]bool{t.AddFIRAllowed, t.AddRegularAllowed, t.RemoveFIRAllowed, t.RemoveRegularAllowed}[i]
		}
		if !allowed || !a.qualityHotkeyReadyLocked(i) || keys[i] == "off" {
			a.qualityHotkeyTargets[i].Store(nil)
			continue
		}
		a.qualityHotkeyTargets[i].Store(retainTrackedHotkeyTarget(a.qualityHotkeyTargets[i].Load(), trackedAddHotkeyTarget{
			UID: a.state.Item.UID, ItemID: a.state.Item.BSGID,
			CharUID: a.state.CharacterUID, Revision: a.state.ItemOptions.revision,
			Typed: true, FIR: fir, Remove: remove,
		}))
	}
}

func (a *ItemWindowApp) configureQualityHotkeyKeys(opts ItemOptions, capture bool) {
	keys := qualityShortcutKeys(opts)
	for i, hotkey := range a.qualityHotkeys {
		if hotkey != nil {
			key := keys[i]
			if capture {
				key = "off"
			}
			hotkey.SetKey(key)
		}
	}
}

func (a *ItemWindowApp) setQualityHotkeyStatus(i int, ready bool, message string) {
	a.mu.Lock()
	ready = ready && !a.state.HotkeyCapture && qualityShortcutKeys(a.state.ItemOptions)[i] != "off" && a.ctx != nil && a.ctx.Err() == nil
	switch i {
	case 0:
		a.state.AddFIRHotkeyReady, a.state.AddFIRHotkeyError = ready, message
	case 1:
		a.state.AddRegularHotkeyReady, a.state.AddRegularHotkeyError = ready, message
	case 2:
		a.state.RemoveFIRHotkeyReady, a.state.RemoveFIRHotkeyError = ready, message
	case 3:
		a.state.RemoveRegularHotkeyReady, a.state.RemoveRegularHotkeyError = ready, message
	}
	a.mu.Unlock()
	a.emit()
}
