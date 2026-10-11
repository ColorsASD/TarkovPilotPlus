package plus

import (
	"strconv"
	"strings"
)

const defaultTrackedAddHotkey = "F1"

func normalizeTrackedAddHotkey(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "OFF" {
		return "off"
	}
	if strings.HasPrefix(value, "F") {
		if number, err := strconv.Atoi(value[1:]); err == nil && number >= 1 && number <= 12 && value == "F"+strconv.Itoa(number) {
			return value
		}
	}
	return defaultTrackedAddHotkey
}

func trackedAddHotkeyVK(value string) uint32 {
	value = normalizeTrackedAddHotkey(value)
	if value == "off" {
		return 0
	}
	number, _ := strconv.Atoi(value[1:])
	return uint32(0x70 + number - 1) // VK_F1 .. VK_F12.
}

type trackedHotkeyPress struct {
	down     bool
	captured bool
}

// trackedHotkeyKeys belongs to the hook thread. It retains only the reserved
// function keys and modifier states; ordinary keyboard input is never stored.
// Physical and injected presses are independent, so a synthetic key-up cannot
// release a physical hold or produce another stock increment through repeats.
type trackedHotkeyKeys struct {
	physical         [12]trackedHotkeyPress
	injected         [12]trackedHotkeyPress
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
	for index := range k.modifiers {
		if k.modifiers[index] || k.virtualModifiers[index] {
			return true
		}
	}
	return false
}

// seed records keys already held when the hook starts. Their first observed
// repeats/releases must not be treated as a new press or leave a target window
// with a key-down that no longer receives its corresponding key-up.
func (k *trackedHotkeyKeys) seed(key uint32, down bool) {
	if index := trackedHotkeyModifierIndex(key); index >= 0 {
		k.modifiers[index] = down
	} else if key >= 0x70 && key <= 0x7b {
		press := &k.physical[key-0x70]
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
	if key < 0x70 || key > 0x7b {
		return false, false
	}
	press := &k.physical[key-0x70]
	if injected {
		press = &k.injected[key-0x70]
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
	press.captured = configured != 0 && key == configured && !altDown && !k.modified()
	return press.captured, press.captured && !injected
}
