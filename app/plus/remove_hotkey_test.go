package plus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveShortcutDefaultsPersistsAndRejectsCollisions(t *testing.T) {
	defaults := normalizeItemOptions(ItemOptions{})
	if defaults.AddHotkey != "F1" || defaults.RemoveHotkey != "F2" {
		t.Fatalf("unexpected shortcut defaults: %+v", defaults)
	}
	a, _ := newTrackingTestApp(t)
	opts := a.state.ItemOptions
	opts.AddHotkey, opts.RemoveHotkey = "F1", "shift + ctrl + q"
	state := a.SetOptions(opts)
	if state.Error != "" || state.RemoveHotkey != "Ctrl+Shift+Q" {
		t.Fatalf("Remove shortcut did not persist: %+v", state)
	}
	path := filepath.Join(a.dir, "item-settings.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := decodeSavedItemOptions(before)
	if err != nil || loaded.RemoveHotkey != "Ctrl+Shift+Q" || loaded.AddHotkey != "F1" {
		t.Fatalf("Remove shortcut reload lost settings: %+v %v", loaded, err)
	}
	for _, key := range []string{"F1", "Ctrl++F2", "F13"} {
		opts.RemoveHotkey = key
		state = a.SetOptions(opts)
		after, err := os.ReadFile(path)
		if err != nil || state.Error == "" || string(before) != string(after) || state.RemoveHotkey != "Ctrl+Shift+Q" {
			t.Fatalf("invalid/conflicting Remove shortcut changed preferences: key=%s state=%+v err=%v", key, state, err)
		}
	}
}

func TestRemoveShortcutLegacyF2AddChoiceStaysSafe(t *testing.T) {
	opts, err := decodeSavedItemOptions([]byte(`{"addHotkey":"F2","enabled":true,"automatic":true}`))
	if err != nil || opts.AddHotkey != "F2" || opts.RemoveHotkey != "off" {
		t.Fatalf("legacy F2 Add must not become destructive: %+v %v", opts, err)
	}
	data, err := json.Marshal(opts)
	if err != nil || !strings.Contains(string(data), `"removeHotkey":"off"`) {
		t.Fatalf("disabled conflicting shortcut was not encoded: %s %v", data, err)
	}
}

func TestRemoveShortcutCanBeDisabledIndependently(t *testing.T) {
	a, _ := newTrackingTestApp(t)
	opts := a.state.ItemOptions
	opts.AddHotkey, opts.RemoveHotkey = "F1", "off"
	state := a.SetOptions(opts)
	if state.Error != "" || state.AddHotkey != "F1" || state.RemoveHotkey != "off" || state.RemoveHotkeyReady || state.RemoveHotkeyError != "" {
		t.Fatalf("Remove disable affected Add: %+v", state)
	}
}

func TestShortcutCapturePausesBothNativeBindings(t *testing.T) {
	a, _ := newTrackingTestApp(t)
	a.state.AddHotkey, a.state.RemoveHotkey = "Ctrl+K", "Alt+J"
	a.state.AddHotkeyReady, a.state.RemoveHotkeyReady = true, true
	a.addHotkey = &trackedAddHotkey{statuses: make(chan struct{}, 1)}
	a.removeHotkey = &trackedAddHotkey{statuses: make(chan struct{}, 1)}
	a.configureTrackedAddHotkey()
	_, wantAdd, _ := parseTrackedShortcut("Ctrl+K")
	_, wantRemove, _ := parseTrackedShortcut("Alt+J")
	if a.addHotkey.key.Load() != wantAdd || a.removeHotkey.key.Load() != wantRemove {
		t.Fatal("separate native shortcut configuration was not applied")
	}
	state := a.SetHotkeyCapture(true)
	if !state.HotkeyCapture || state.AddHotkeyReady || state.RemoveHotkeyReady || a.addHotkey.key.Load() != 0 || a.removeHotkey.key.Load() != 0 || a.addHotkeyTarget.Load() != nil || a.removeHotkeyTarget.Load() != nil {
		t.Fatalf("shortcut capture did not pause both native actions: %+v", state)
	}
	state = a.SetHotkeyCapture(false)
	if state.HotkeyCapture || !state.AddHotkeyReady || !state.RemoveHotkeyReady || a.addHotkey.key.Load() != wantAdd || a.removeHotkey.key.Load() != wantRemove {
		t.Fatalf("shortcut capture did not restore both bindings: %+v", state)
	}
}
