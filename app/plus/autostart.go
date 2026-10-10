package plus

import "path/filepath"

// MatchesPreviousAutoStart preserves an existing opt-in when the Plus
// executable replaces its former filename in the same installation directory.
// Values for other executables and directories must never enable autostart.
func MatchesPreviousAutoStart(value, executable string) bool {
	if filepath.Base(executable) != "TarkovPilotPlus.exe" {
		return false
	}
	previousExecutable := filepath.Join(filepath.Dir(executable), "TarkovPilot.exe")
	quoted := `"` + previousExecutable + `"`
	return value == quoted || value == quoted+" --hidden"
}
