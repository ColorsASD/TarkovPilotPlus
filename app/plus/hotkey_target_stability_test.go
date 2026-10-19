package plus

import "testing"

func newHotkeyTargetStabilityTestApp(t *testing.T) (*ItemWindowApp, *fakeTrackingAPI) {
	t.Helper()
	a, fake := newTrackedHotkeyTestApp(t)
	fake.snapshot.Items[0].AvailableQuantity = 1
	fake.snapshot.Items[0].RemainingQuantity = 6
	setTrackingTestSnapshot(t, a, fake.snapshot)
	a.state.RemoveHotkey, a.state.RemoveHotkeyReady = "F2", true
	a.publishTrackedAddHotkey()
	return a, fake
}

func hotkeyStabilityTarget(a *ItemWindowApp, remove bool) *trackedAddHotkeyTarget {
	if remove {
		return a.removeHotkeyTarget.Load()
	}
	return a.addHotkeyTarget.Load()
}

func queueHotkeyStabilityAction(a *ItemWindowApp, remove bool) {
	if remove {
		a.requestTrackedRemoveHotkey()
	} else {
		a.requestTrackedAddHotkey()
	}
}

func TestHotkeyTargetSurvivesUnrelatedEmits(t *testing.T) {
	for _, action := range []struct {
		name   string
		remove bool
		want   int64
	}{{"F1 Add", false, 2}, {"F2 Remove", true, 0}} {
		t.Run(action.name, func(t *testing.T) {
			a, fake := newHotkeyTargetStabilityTestApp(t)
			original := hotkeyStabilityTarget(a, action.remove)
			if original == nil {
				t.Fatal("eligible shortcut did not publish a target")
			}
			queueHotkeyStabilityAction(a, action.remove)
			for _, status := range []string{"API save pending", "API save confirmed", "Ready"} {
				a.mu.Lock()
				a.state.Status = status
				a.mu.Unlock()
				a.emit()
			}
			preserved := hotkeyStabilityTarget(a, action.remove) == original
			runTrackedHotkeyTestWorker(t, a)
			waitTrackedHotkeyIdle(t, a)
			reads, writes := trackedHotkeyAPICalls(fake)
			state := a.GetState()
			if !preserved || reads != 2 || len(writes) != 1 || writes[0].Quantity != action.want || state.Tracking.AvailableQuantity != action.want {
				t.Fatalf("unrelated emits discarded or repeated an accepted shortcut: targetPreserved=%v reads=%d writes=%+v tracking=%+v", preserved, reads, writes, state.Tracking)
			}
		})
	}
}

func TestHotkeyInvalidatedTargetDoesNotRevive(t *testing.T) {
	for _, action := range []struct {
		name   string
		remove bool
	}{{"F1 Add", false}, {"F2 Remove", true}} {
		for _, change := range []struct {
			name string
			set  func(*ItemWindowApp, bool, bool)
		}{
			{"item UID", func(a *ItemWindowApp, _, invalid bool) {
				uid := trackingItemUID
				if invalid {
					uid = "another-item"
				}
				a.state.Item.UID, a.state.Tracking.ItemUID = uid, uid
			}},
			{"game item ID", func(a *ItemWindowApp, _, invalid bool) {
				id := trackingBSGID
				if invalid {
					id = "another-game-item"
				}
				a.state.Item.BSGID, a.state.Tracking.ItemID = id, id
			}},
			{"character", func(a *ItemWindowApp, _, invalid bool) {
				id := "character-a"
				if invalid {
					id = "character-b"
				}
				a.state.CharacterUID, a.state.Tracking.CharUID = id, id
			}},
			{"options revision", func(a *ItemWindowApp, _, invalid bool) {
				a.state.ItemOptions.revision = 8
				if invalid {
					a.state.ItemOptions.revision++
				}
			}},
			{"capture", func(a *ItemWindowApp, _, invalid bool) { a.state.HotkeyCapture = invalid }},
			{"search", func(a *ItemWindowApp, _, invalid bool) { a.state.Busy = invalid }},
			{"shortcut disabled", func(a *ItemWindowApp, remove, invalid bool) {
				key := "F1"
				if remove {
					key = "F2"
				}
				if invalid {
					key = "off"
				}
				if remove {
					a.state.RemoveHotkey = key
				} else {
					a.state.AddHotkey = key
				}
			}},
			{"shortcut unavailable", func(a *ItemWindowApp, remove, invalid bool) {
				if remove {
					a.state.RemoveHotkeyReady = !invalid
				} else {
					a.state.AddHotkeyReady = !invalid
				}
			}},
		} {
			t.Run(action.name+"/"+change.name, func(t *testing.T) {
				a, fake := newHotkeyTargetStabilityTestApp(t)
				original := hotkeyStabilityTarget(a, action.remove)
				queueHotkeyStabilityAction(a, action.remove)
				a.mu.Lock()
				change.set(a, action.remove, true)
				a.mu.Unlock()
				a.emit()
				if hotkeyStabilityTarget(a, action.remove) == original {
					t.Fatal("changed identity or disabled action retained its queued target")
				}
				a.mu.Lock()
				change.set(a, action.remove, false)
				a.mu.Unlock()
				a.emit()
				if current := hotkeyStabilityTarget(a, action.remove); current == nil || current == original || a.trackedAddHotkeyTargetCurrent(original) {
					t.Fatal("returning to the original identity revived an invalidated target")
				}
				runTrackedHotkeyTestWorker(t, a)
				waitTrackedHotkeyIdle(t, a)
				reads, writes := trackedHotkeyAPICalls(fake)
				if reads != 0 || len(writes) != 0 || a.GetPendingAddCount() != 0 {
					t.Fatalf("invalidated queued action reached Market: reads=%d writes=%+v", reads, writes)
				}
			})
		}
	}
}

func TestHotkeySameIdentityExhaustedQuotaStillRejects(t *testing.T) {
	for _, action := range []struct {
		name   string
		remove bool
		stock  int64
	}{{"F1 Add", false, 7}, {"F2 Remove", true, 0}} {
		t.Run(action.name, func(t *testing.T) {
			a, fake := newHotkeyTargetStabilityTestApp(t)
			queueHotkeyStabilityAction(a, action.remove)
			fake.snapshot.Items[0].AvailableQuantity = action.stock
			fake.snapshot.Items[0].RemainingQuantity = 7 - action.stock
			setTrackingTestSnapshot(t, a, fake.snapshot)
			a.emit()
			if hotkeyStabilityTarget(a, action.remove) != nil {
				t.Fatal("same identity retained a shortcut after its quota was exhausted")
			}
			runTrackedHotkeyTestWorker(t, a)
			waitTrackedHotkeyIdle(t, a)
			reads, writes := trackedHotkeyAPICalls(fake)
			if reads != 0 || len(writes) != 0 || a.GetPendingAddCount() != 0 {
				t.Fatalf("exhausted quota executed a queued shortcut: reads=%d writes=%+v", reads, writes)
			}
		})
	}
}
