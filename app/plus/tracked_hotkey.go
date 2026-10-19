package plus

import (
	"strconv"
	"strings"
)

const defaultTrackedAddHotkey = "F1"
const defaultTrackedRemoveHotkey = "F2"

const (
	trackedHotkeyCtrl    uint32 = 1 << 8
	trackedHotkeyAlt     uint32 = 1 << 9
	trackedHotkeyShift   uint32 = 1 << 10
	trackedHotkeyWin     uint32 = 1 << 11
	trackedHotkeyKeyMask uint32 = 0xff
)

func normalizeTrackedAddHotkey(value string) string {
	return normalizeTrackedShortcut(value, defaultTrackedAddHotkey)
}

func normalizeTrackedRemoveHotkey(value string) string {
	return normalizeTrackedShortcut(value, defaultTrackedRemoveHotkey)
}

func normalizeTrackedShortcut(value, fallback string) string {
	if canonical, _, ok := parseTrackedShortcut(value); ok {
		return canonical
	}
	if canonical, _, ok := parseTrackedShortcut(fallback); ok {
		return canonical
	}
	return defaultTrackedAddHotkey
}

// Chords use the base Windows key in the low byte and the modifier mask above
// it. A canonical string is persisted; the packed value is hook-thread state.
func parseTrackedShortcut(value string) (canonical string, chord uint32, ok bool) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "OFF" {
		return "off", 0, true
	}
	parts := strings.Split(value, "+")
	var modifiers uint32
	for _, part := range parts[:len(parts)-1] {
		var modifier uint32
		switch strings.TrimSpace(part) {
		case "CTRL":
			modifier = trackedHotkeyCtrl
		case "ALT":
			modifier = trackedHotkeyAlt
		case "SHIFT":
			modifier = trackedHotkeyShift
		default:
			return "", 0, false
		}
		if modifiers&modifier != 0 {
			return "", 0, false
		}
		modifiers |= modifier
	}
	name, key := trackedShortcutBaseKey(strings.TrimSpace(parts[len(parts)-1]))
	if key == 0 {
		return "", 0, false
	}
	var prefix strings.Builder
	for _, modifier := range []struct {
		bit  uint32
		name string
	}{{trackedHotkeyCtrl, "Ctrl"}, {trackedHotkeyAlt, "Alt"}, {trackedHotkeyShift, "Shift"}} {
		if modifiers&modifier.bit != 0 {
			prefix.WriteString(modifier.name)
			prefix.WriteByte('+')
		}
	}
	prefix.WriteString(name)
	return prefix.String(), key | modifiers, true
}

func trackedShortcutBaseKey(value string) (string, uint32) {
	if len(value) == 1 && (value[0] >= 'A' && value[0] <= 'Z' || value[0] >= '0' && value[0] <= '9') {
		return value, uint32(value[0])
	}
	if strings.HasPrefix(value, "F") {
		if number, err := strconv.Atoi(value[1:]); err == nil && number >= 1 && number <= 12 && value == "F"+strconv.Itoa(number) {
			return value, uint32(0x70 + number - 1)
		}
	}
	switch value {
	case "SPACE":
		return "Space", 0x20
	case "INSERT":
		return "Insert", 0x2d
	case "DELETE":
		return "Delete", 0x2e
	case "HOME":
		return "Home", 0x24
	case "END":
		return "End", 0x23
	case "PAGEUP":
		return "PageUp", 0x21
	case "PAGEDOWN":
		return "PageDown", 0x22
	}
	return "", 0
}

func trackedAddHotkeyVK(value string) uint32 {
	_, chord, _ := parseTrackedShortcut(normalizeTrackedAddHotkey(value))
	return chord & trackedHotkeyKeyMask
}

type trackedHotkeyPress struct {
	down     bool
	captured bool
}

// trackedHotkeyKeys belongs to the hook thread. It retains only press/release
// booleans for supported shortcut keys and modifiers; no typed text is stored.
// Physical and injected presses are independent, so a synthetic key-up cannot
// release a physical hold or produce another stock increment through repeats.
type trackedHotkeyKeys struct {
	physical         [256]trackedHotkeyPress
	injected         [256]trackedHotkeyPress
	modifiers        [11]bool
	virtualModifiers [11]bool
}

var trackedHotkeyModifierKeys = [...]uint32{
	0x10, 0x11, 0x12, // Generic Shift, Control, Menu (Alt).
	0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, // Left/right Shift, Control, Alt.
	0x5b, 0x5c, // Left/right Windows.
}

func trackedHotkeyModifierIndex(key uint32) int {
	for index, modifier := range trackedHotkeyModifierKeys {
		if key == modifier {
			return index
		}
	}
	return -1
}

func (k *trackedHotkeyKeys) modified() bool {
	return k.modifierMask(false) != 0
}

func (k *trackedHotkeyKeys) modifierMask(altDown bool) uint32 {
	var mask uint32
	for index := range k.modifiers {
		if k.modifiers[index] || k.virtualModifiers[index] {
			switch trackedHotkeyModifierKeys[index] {
			case 0x10, 0xa0, 0xa1:
				mask |= trackedHotkeyShift
			case 0x11, 0xa2, 0xa3:
				mask |= trackedHotkeyCtrl
			case 0x12, 0xa4, 0xa5:
				mask |= trackedHotkeyAlt
			case 0x5b, 0x5c:
				mask |= trackedHotkeyWin
			}
		}
	}
	if altDown {
		mask |= trackedHotkeyAlt
	}
	return mask
}

func (k *trackedHotkeyKeys) hasInjectedModifiers() bool {
	for _, held := range k.virtualModifiers {
		if held {
			return true
		}
	}
	return false
}

func trackedHotkeySupportedKey(key uint32) bool {
	return key >= 0x70 && key <= 0x7b || key >= 'A' && key <= 'Z' || key >= '0' && key <= '9' ||
		key == 0x20 || key == 0x2d || key == 0x2e || key == 0x24 || key == 0x23 || key == 0x21 || key == 0x22
}

// seed records keys already held when the hook starts. Their first observed
// repeats/releases must not be treated as a new press or leave a target window
// with a key-down that no longer receives its corresponding key-up.
func (k *trackedHotkeyKeys) seed(key uint32, down bool) {
	if index := trackedHotkeyModifierIndex(key); index >= 0 {
		k.modifiers[index] = down
	} else if trackedHotkeySupportedKey(key) {
		press := &k.physical[key]
		if !press.down && !press.captured {
			press.down = down
		}
	}
}

// event returns whether to consume this event and whether to notify the Add
// action. Capture is decided on the first key-down and retained for its entire
// pair even if the configured key or modifiers change while the key is held.
func (k *trackedHotkeyKeys) event(key uint32, down, injected, altDown bool, configured uint32) (consume, trigger bool) {
	if index := trackedHotkeyModifierIndex(key); index >= 0 {
		// Startup can observe the generic modifier alias as well as its sides.
		// A later left/right event owns that state, so the alias must not remain
		// stuck after the physical key is released.
		generic := -1
		if index >= 3 && index <= 8 {
			generic = (index - 3) / 2
		}
		if injected {
			k.virtualModifiers[index] = down
			if generic >= 0 {
				k.virtualModifiers[generic] = false
			}
		} else {
			k.modifiers[index] = down
			if generic >= 0 {
				k.modifiers[generic] = false
			}
		}
		return false, false
	}
	if !trackedHotkeySupportedKey(key) {
		return false, false
	}
	press := &k.physical[key]
	if injected {
		press = &k.injected[key]
	}
	if !down {
		consume = press.captured
		*press = trackedHotkeyPress{}
		return consume, false
	}
	if press.down {
		return press.captured, false
	}
	press.down = true
	press.captured = configured != 0 && key|k.modifierMask(altDown) == configured
	return press.captured, press.captured && !injected && !k.hasInjectedModifiers()
}
