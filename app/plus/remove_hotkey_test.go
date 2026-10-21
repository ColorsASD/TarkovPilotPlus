package plus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQualityShortcutDefaultsPersistsAndRejectsCollisions(t *testing.T) {
	defaults := normalizeItemOptions(ItemOptions{})
	if got := qualityShortcutKeys(defaults); got != [4]string{"F1", "F2", "F3", "F4"} {
		t.Fatalf("unexpected shortcut defaults: %+v", got)
	}
	a, _ := newTrackingTestApp(t)
	opts := a.state.ItemOptions
	opts.RemoveFIRHotkey = "shift + ctrl + q"
	state := a.SetOptions(opts)
	if state.Error != "" || state.RemoveFIRHotkey != "Ctrl+Shift+Q" {
		t.Fatalf("typed Remove shortcut did not persist: %+v", state)
	}
	path := filepath.Join(a.dir, "item-settings.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := decodeSavedItemOptions(before)
	if err != nil || loaded.RemoveFIRHotkey != "Ctrl+Shift+Q" || loaded.AddRegularHotkey != "F2" {
		t.Fatalf("typed shortcut reload lost settings: %+v %v", loaded, err)
	}
	for _, key := range []string{"F1", "F2", "F4", "Ctrl++F2", "F13"} {
		opts.RemoveFIRHotkey = key
		state = a.SetOptions(opts)
		after, err := os.ReadFile(path)
		if err != nil || state.Error == "" || string(before) != string(after) || state.RemoveFIRHotkey != "Ctrl+Shift+Q" {
			t.Fatalf("invalid/conflicting shortcut changed preferences: key=%s state=%+v err=%v", key, state, err)
		}
	}
}

func TestQualityShortcutMigrationNeverKeepsGenericRemoveOnF2(t *testing.T) {
	opts, err := decodeSavedItemOptions([]byte(`{"addHotkey":"F1","removeHotkey":"F2","enabled":true,"automatic":true}`))
	if err != nil || qualityShortcutKeys(opts) != [4]string{"F1", "F2", "F3", "F4"} {
		t.Fatalf("legacy Remove must become typed F3/F4 while F2 adds Regular: %+v %v", opts, err)
	}
	data, err := json.Marshal(opts)
	if err != nil || strings.Contains(string(data), `"removeHotkey"`) || strings.Contains(string(data), `"addHotkey"`) {
		t.Fatalf("removed generic shortcut was encoded: %s %v", data, err)
	}
	legacyF2, err := decodeSavedItemOptions([]byte(`{"addHotkey":"F2","enabled":true,"automatic":true}`))
	if err != nil || legacyF2.AddFIRHotkey != "F2" || legacyF2.AddRegularHotkey != "off" || legacyF2.RemoveFIRHotkey != "F3" {
		t.Fatalf("legacy custom Add must remain nondestructive: %+v %v", legacyF2, err)
	}
}

func TestQualityShortcutCanBeDisabledIndependently(t *testing.T) {
	a, _ := newTrackingTestApp(t)
	opts := a.state.ItemOptions
	opts.RemoveFIRHotkey = "off"
	state := a.SetOptions(opts)
	if state.Error != "" || state.AddFIRHotkey != "F1" || state.RemoveFIRHotkey != "off" || state.RemoveFIRHotkeyReady || state.RemoveFIRHotkeyError != "" || state.RemoveRegularHotkey != "F4" {
		t.Fatalf("typed Remove disable affected other actions: %+v", state)
	}
}

func TestShortcutCapturePausesAllFourNativeBindings(t *testing.T) {
	a, _ := newTrackingTestApp(t)
	a.state.ItemOptions = normalizeItemOptions(a.state.ItemOptions)
	for i := range a.qualityHotkeys {
		a.qualityHotkeys[i] = &trackedAddHotkey{statuses: make(chan struct{}, 1)}
	}
	a.configureTrackedAddHotkey()
	for i, hotkey := range a.qualityHotkeys {
		if got := hotkey.key.Load(); got != uint32(0x70+i) {
			t.Fatalf("binding %d has key %#x", i, got)
		}
	}
	state := a.SetHotkeyCapture(true)
	if !state.HotkeyCapture || state.AddFIRHotkeyReady || state.AddRegularHotkeyReady || state.RemoveFIRHotkeyReady || state.RemoveRegularHotkeyReady {
		t.Fatalf("capture kept ready actions: %+v", state)
	}
	for i, hotkey := range a.qualityHotkeys {
		if hotkey.key.Load() != 0 || a.qualityHotkeyTargets[i].Load() != nil {
			t.Fatalf("capture kept binding %d active", i)
		}
	}
	state = a.SetHotkeyCapture(false)
	if state.HotkeyCapture {
		t.Fatal("capture did not stop")
	}
	for i, hotkey := range a.qualityHotkeys {
		if got := hotkey.key.Load(); got != uint32(0x70+i) {
			t.Fatalf("binding %d did not restore", i)
		}
	}
}
