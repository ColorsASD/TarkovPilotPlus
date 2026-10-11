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
		"Ctrl+F2": "F1", "Space": "F1", " f8 ": "F8", " Off ": "off",
	} {
		if got := normalizeTrackedAddHotkey(input); got != expected {
			t.Errorf("%q normalized to %q, want %q", input, got, expected)
		}
	}
	if trackedAddHotkeyVK("off") != 0 {
		t.Fatal("disabled shortcut still reserves a key")
	}
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
