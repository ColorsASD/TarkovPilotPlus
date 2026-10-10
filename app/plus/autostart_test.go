package plus

import "testing"

func TestMatchesPreviousAutoStart(t *testing.T) {
	const current = `C:\Program Files\TarkovPilotPlus\TarkovPilotPlus.exe`
	const previous = `"C:\Program Files\TarkovPilotPlus\TarkovPilot.exe"`
	cases := []struct {
		name       string
		value      string
		executable string
		want       bool
	}{
		{name: "previous with hidden", value: previous + " --hidden", executable: current, want: true},
		{name: "previous without hidden", value: previous, executable: current, want: true},
		{name: "other directory", value: `"C:\Other\TarkovPilot.exe" --hidden`, executable: current},
		{name: "other executable", value: `"C:\Program Files\TarkovPilotPlus\Other.exe" --hidden`, executable: current},
		{name: "missing quotes", value: `C:\Program Files\TarkovPilotPlus\TarkovPilot.exe --hidden`, executable: current},
		{name: "unexpected arguments", value: previous + " --hidden --other", executable: current},
		{name: "unexpected spacing", value: previous + "  --hidden", executable: current},
		{name: "old executable cannot migrate", value: previous + " --hidden", executable: `C:\Program Files\TarkovPilotPlus\TarkovPilot.exe`},
		{name: "other executable cannot migrate", value: previous + " --hidden", executable: `C:\Program Files\TarkovPilotPlus\Other.exe`},
		{name: "current filename is not a legacy value", value: `"C:\Program Files\TarkovPilotPlus\TarkovPilotPlus.exe" --hidden`, executable: current},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchesPreviousAutoStart(tc.value, tc.executable); got != tc.want {
				t.Fatalf("MatchesPreviousAutoStart() = %v, want %v", got, tc.want)
			}
		})
	}
}
