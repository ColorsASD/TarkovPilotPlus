package plus

import (
	"strconv"
	"testing"
)

func TestTrackedAddHotkeyNormalization(t *testing.T) {
	for number := 1; number <= 12; number++ {
		key := "F" + strconv.Itoa(number)
		if got := normalizeTrackedAddHotkey(key); got != key {
			t.Fatalf("%s normalized to %s", key, got)
		}
		if got := trackedAddHotkeyVK(key); got != uint32(0x70+number-1) {
			t.Fatalf("%s has unexpected Windows key: %#x", key, got)
		}
	}
	for input, expected := range map[string]string{
		"": "F1", "F0": "F1", "F13": "F1", "F01": "F1", "F+2": "F1",
		"Ctrl+F2": "Ctrl+F2", "Space": "Space", " f8 ": "F8", " Off ": "off",
	} {
		if got := normalizeTrackedAddHotkey(input); got != expected {
			t.Errorf("%q normalized to %q, want %q", input, got, expected)
		}
	}
	if trackedAddHotkeyVK("off") != 0 {
		t.Fatal("disabled shortcut still reserves a key")
	}
}

func TestTrackedShortcutCanonicalChordsAndDefaults(t *testing.T) {
	for input, expected := range map[string]string{
		"shift + CTRL + alt + f12": "Ctrl+Alt+Shift+F12",
		"alt+a":                    "Alt+A", "ctrl+1": "Ctrl+1", " shift + space ": "Shift+Space",
		"a": "A", "9": "9", "ctrl+insert": "Ctrl+Insert", "Alt+Delete": "Alt+Delete",
		"Home": "Home", "End": "End", "pageup": "PageUp", "Shift+PageDown": "Shift+PageDown",
		"OFF": "off",
	} {
		if got := normalizeTrackedShortcut(input, "F2"); got != expected {
			t.Errorf("%q normalized to %q, want %q", input, got, expected)
		}
	}
	for _, input := range []string{"", "Ctrl", "Ctrl+", "+A", "Ctrl++A", "Ctrl+Ctrl+A", "Shift+Alt+Shift+F1", "Win+A", "A+Ctrl", "Ctrl+off", "F01", "F13", "F0", "AA", "Enter", "é", "Ctrl+A+B"} {
		if _, _, valid := parseTrackedShortcut(input); valid || normalizeTrackedAddHotkey(input) != "F1" {
			t.Errorf("invalid chord %q accepted or defaults reversed", input)
		}
	}
	if normalizeTrackedAddHotkey(" off ") != "off" || normalizeTrackedShortcut("bad", "shift+f4") != "Shift+F4" {
		t.Fatal("disabled or caller-provided fallback did not normalize")
	}
	for _, letter := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" {
		name := string(letter)
		if normalizeTrackedAddHotkey(name) != name || trackedAddHotkeyVK(name) != uint32(letter) {
			t.Fatalf("manual unmodified key %q was not supported", name)
		}
	}
	if trackedAddHotkeyVK("Ctrl+Alt+Shift+F12") != 0x7b {
		t.Fatal("chord returned modifier bits as a Windows base key")
	}
}

func mustTrackedChord(t *testing.T, value string) uint32 {
	t.Helper()
	_, chord, valid := parseTrackedShortcut(value)
	if !valid {
		t.Fatalf("invalid test chord: %s", value)
	}
	return chord
}

func assertTrackedKeyEvent(t *testing.T, keys *trackedHotkeyKeys, key uint32, down, injected, alt bool, configured uint32, consume, trigger bool) {
	t.Helper()
	gotConsume, gotTrigger := keys.event(key, down, injected, alt, configured)
	if gotConsume != consume || gotTrigger != trigger {
		t.Fatalf("key=%#x down=%v injected=%v alt=%v configured=%#x: consume/trigger=%v/%v, want %v/%v", key, down, injected, alt, configured, gotConsume, gotTrigger, consume, trigger)
	}
}

func TestTrackedAddHotkeyConsumesPressAndReleaseWithoutRepeats(t *testing.T) {
	var keys trackedHotkeyKeys
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, true, true)
	for repeat := 0; repeat < 50; repeat++ {
		assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, true, false)
	}
	assertTrackedKeyEvent(t, &keys, 0x41, true, false, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0x71, true, false, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, false, false, false, 0x70, true, false)
	assertTrackedKeyEvent(t, &keys, 0x70, false, false, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, true, true)
}

func TestTrackedAddHotkeyPassesModifierChordsForTheWholePress(t *testing.T) {
	for _, modifier := range trackedHotkeyModifierKeys {
		t.Run(strconv.FormatUint(uint64(modifier), 16), func(t *testing.T) {
			var keys trackedHotkeyKeys
			assertTrackedKeyEvent(t, &keys, modifier, true, false, false, 0x70, false, false)
			assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, false, false)
			assertTrackedKeyEvent(t, &keys, modifier, false, false, false, 0x70, false, false)
			// Releasing Ctrl while still holding F1 must not turn its repeat
			// into a new Add action or suppress its corresponding release.
			assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, false, false)
			assertTrackedKeyEvent(t, &keys, 0x70, false, false, false, 0x70, false, false)
			assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, true, true)
		})
	}
	var keys trackedHotkeyKeys
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, true, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, false, false, false, 0x70, false, false)
}

func TestTrackedAddHotkeyKeepsCapturedReleaseWhenSettingsChange(t *testing.T) {
	var keys trackedHotkeyKeys
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, true, true)
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x71, true, false)
	assertTrackedKeyEvent(t, &keys, 0x71, true, false, false, 0x71, true, true)
	assertTrackedKeyEvent(t, &keys, 0xa2, true, false, false, 0x71, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, false, false, false, 0, true, false)
	assertTrackedKeyEvent(t, &keys, 0x71, false, false, false, 0, true, false)
	assertTrackedKeyEvent(t, &keys, 0xa2, false, false, false, 0, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, false, false, false, 0, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x71, false, false)
	assertTrackedKeyEvent(t, &keys, 0x71, true, false, false, 0x71, true, true)
}

func TestTrackedAddHotkeyDoesNotClaimAnAlreadyHeldUncapturedKey(t *testing.T) {
	var keys trackedHotkeyKeys
	assertTrackedKeyEvent(t, &keys, 0x71, true, false, false, 0x70, false, false)
	// The target application has already received F2 down before selection.
	assertTrackedKeyEvent(t, &keys, 0x71, true, false, false, 0x71, false, false)
	assertTrackedKeyEvent(t, &keys, 0x71, false, false, false, 0x71, false, false)
	assertTrackedKeyEvent(t, &keys, 0x71, true, false, false, 0x71, true, true)
}

func TestTrackedAddHotkeySeparatesInjectedAndPhysicalPresses(t *testing.T) {
	var keys trackedHotkeyKeys
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, true, true)
	assertTrackedKeyEvent(t, &keys, 0x70, true, true, false, 0x70, true, false)
	assertTrackedKeyEvent(t, &keys, 0x70, false, true, false, 0x70, true, false)
	// An injected release must not clear the still-held physical F1.
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, true, false)
	assertTrackedKeyEvent(t, &keys, 0x70, false, false, false, 0x70, true, false)
	// A synthetic held key also must not consume the first physical action.
	assertTrackedKeyEvent(t, &keys, 0x70, true, true, false, 0x70, true, false)
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, true, true)
	assertTrackedKeyEvent(t, &keys, 0x70, false, false, false, 0x70, true, false)
	assertTrackedKeyEvent(t, &keys, 0x70, false, true, false, 0x70, true, false)
}

func TestTrackedAddHotkeyTracksIndependentPhysicalAndInjectedModifiers(t *testing.T) {
	var keys trackedHotkeyKeys
	assertTrackedKeyEvent(t, &keys, 0xa2, true, false, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0xa2, false, true, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, false, false, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0xa2, false, false, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0xa4, true, true, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, false, false, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0xa4, false, true, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, true, true)
}

func TestTrackedAddHotkeyStartupHeldKeysAndModifierAliases(t *testing.T) {
	var keys trackedHotkeyKeys
	keys.seed(0x70, true)
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, false, false, false, 0x70, false, false)
	keys.seed(0x11, true)
	keys.seed(0xa2, true)
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, false, false, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0xa2, false, false, false, 0x70, false, false)
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, true, true)
	// A callback delivered during installation already owns its press pair;
	// startup sampling must not release it and let the next repeat add again.
	keys.seed(0x70, false)
	assertTrackedKeyEvent(t, &keys, 0x70, true, false, false, 0x70, true, false)
	assertTrackedKeyEvent(t, &keys, 0x70, false, false, false, 0x70, true, false)
}

func TestTrackedChordCapturesExactlyConfiguredModifiersAndRetainsThePress(t *testing.T) {
	chord := mustTrackedChord(t, "Ctrl+Shift+A")
	var keys trackedHotkeyKeys
	assertTrackedKeyEvent(t, &keys, 0xa2, true, false, false, chord, false, false)
	assertTrackedKeyEvent(t, &keys, 'A', true, false, false, chord, false, false)
	assertTrackedKeyEvent(t, &keys, 0xa0, true, false, false, chord, false, false)
	// Adding a modifier to an already forwarded A must not claim its repeat.
	assertTrackedKeyEvent(t, &keys, 'A', true, false, false, chord, false, false)
	assertTrackedKeyEvent(t, &keys, 'A', false, false, false, chord, false, false)
	assertTrackedKeyEvent(t, &keys, 'A', true, false, false, chord, true, true)
	// Releasing modifiers and changing the setting cannot recapture a hold.
	assertTrackedKeyEvent(t, &keys, 0xa2, false, false, false, chord, false, false)
	assertTrackedKeyEvent(t, &keys, 0xa0, false, false, false, chord, false, false)
	for range 50 {
		assertTrackedKeyEvent(t, &keys, 'A', true, false, false, uint32('A'), true, false)
	}
	assertTrackedKeyEvent(t, &keys, 'A', false, false, false, 0, true, false)
	assertTrackedKeyEvent(t, &keys, 'A', true, false, false, uint32('A'), true, true)
	assertTrackedKeyEvent(t, &keys, 'A', false, false, false, uint32('A'), true, false)
	assertTrackedKeyEvent(t, &keys, 0xa4, true, false, false, chord, false, false)
	assertTrackedKeyEvent(t, &keys, 'A', true, false, true, uint32('A'), false, false)
	assertTrackedKeyEvent(t, &keys, 'A', false, false, true, uint32('A'), false, false)
}

func TestTrackedChordModifierMasksAreExactIncludingWindowsKeys(t *testing.T) {
	modifiers := []struct{ key, bit uint32 }{{0xa2, trackedHotkeyCtrl}, {0xa4, trackedHotkeyAlt}, {0xa0, trackedHotkeyShift}, {0x5b, trackedHotkeyWin}}
	for actual := 0; actual < 16; actual++ {
		for configured := 0; configured < 8; configured++ {
			var keys trackedHotkeyKeys
			actualMask, configuredMask := uint32(0), uint32(0)
			for index, modifier := range modifiers {
				if actual&(1<<index) != 0 {
					keys.event(modifier.key, true, false, false, 0)
					actualMask |= modifier.bit
				}
				if configured&(1<<index) != 0 {
					configuredMask |= modifier.bit
				}
			}
			want := actualMask == configuredMask
			assertTrackedKeyEvent(t, &keys, '9', true, false, false, uint32('9')|configuredMask, want, want)
			assertTrackedKeyEvent(t, &keys, '9', false, false, false, uint32('9')|configuredMask, want, false)
		}
	}
}

func TestTrackedChordInjectedModifierCannotSupplyPhysicalShortcut(t *testing.T) {
	chord := mustTrackedChord(t, "Ctrl+A")
	var keys trackedHotkeyKeys
	assertTrackedKeyEvent(t, &keys, 0xa2, true, true, false, chord, false, false)
	assertTrackedKeyEvent(t, &keys, 'A', true, false, false, chord, true, false)
	assertTrackedKeyEvent(t, &keys, 0xa2, false, true, false, chord, false, false)
	assertTrackedKeyEvent(t, &keys, 'A', true, false, false, uint32('A'), true, false)
	assertTrackedKeyEvent(t, &keys, 'A', false, false, false, 0, true, false)
	assertTrackedKeyEvent(t, &keys, 0xa2, true, false, false, chord, false, false)
	assertTrackedKeyEvent(t, &keys, 'A', true, true, false, chord, true, false)
	assertTrackedKeyEvent(t, &keys, 'A', true, false, false, chord, true, true)
	assertTrackedKeyEvent(t, &keys, 'A', false, true, false, chord, true, false)
	assertTrackedKeyEvent(t, &keys, 'A', true, false, false, chord, true, false)
	assertTrackedKeyEvent(t, &keys, 'A', false, false, false, chord, true, false)
}

func TestTrackedChordStartupSeedsLettersAndCommonKeysWithoutClaimingHeldPress(t *testing.T) {
	for _, name := range []string{"A", "0", "Space", "Insert", "Delete", "Home", "End", "PageUp", "PageDown"} {
		t.Run(name, func(t *testing.T) {
			var keys trackedHotkeyKeys
			chord := mustTrackedChord(t, "Alt+"+name)
			key := chord & trackedHotkeyKeyMask
			keys.seed(0x12, true)
			keys.seed(0xa4, true)
			keys.seed(key, true)
			assertTrackedKeyEvent(t, &keys, key, true, false, true, chord, false, false)
			assertTrackedKeyEvent(t, &keys, key, false, false, true, chord, false, false)
			assertTrackedKeyEvent(t, &keys, key, true, false, true, chord, true, true)
			keys.seed(key, false)
			assertTrackedKeyEvent(t, &keys, key, true, false, true, chord, true, false)
			assertTrackedKeyEvent(t, &keys, 0xa4, false, false, false, chord, false, false)
			assertTrackedKeyEvent(t, &keys, key, false, false, false, 0, true, false)
			assertTrackedKeyEvent(t, &keys, key, true, false, false, chord, false, false)
		})
	}
}
