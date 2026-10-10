package plus

import "errors"

// The upstream executable cannot replace the application with Plus features.
// Plus releases are installed manually from their own release page.
const AutomaticUpdatesEnabled = false

const ReleasesURL = "https://github.com/ColorsASD/TarkovPilotPlus/releases"

var ErrManualUpdateRequired = errors.New("TarkovPilot+ can be updated manually from its own GitHub releases")

// Update keeps the original caller contract without downloading or replacing
// executables, writing files, or restarting the application.
func Update(_ string) error { return ErrManualUpdateRequired }
