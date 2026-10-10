package main

import "testing"

func TestAutoStartValueMatches(t *testing.T) {
	exe := `C:\Program Files\TarkovPilot\TarkovPilot.exe`
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "current", value: `"C:\Program Files\TarkovPilot\TarkovPilot.exe" --hidden`, want: true},
		{name: "legacy", value: `"C:\Program Files\TarkovPilot\TarkovPilot.exe"`, want: true},
		{name: "other executable", value: `"C:\TarkovPilot.exe" --hidden`, want: false},
		{name: "missing argument", value: `C:\Program Files\TarkovPilot\TarkovPilot.exe`, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoStartValueMatches(tc.value, exe); got != tc.want {
				t.Fatalf("autoStartValueMatches() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAutoStartValueMatchesRenamedPlus(t *testing.T) {
	const current = `C:\Program Files\TarkovPilotPlus\TarkovPilotPlus.exe`
	cases := []struct {
		name       string
		value      string
		executable string
		want       bool
	}{
		{name: "current with hidden", value: `"C:\Program Files\TarkovPilotPlus\TarkovPilotPlus.exe" --hidden`, executable: current, want: true},
		{name: "current without hidden", value: `"C:\Program Files\TarkovPilotPlus\TarkovPilotPlus.exe"`, executable: current, want: true},
		{name: "previous with hidden", value: `"C:\Program Files\TarkovPilotPlus\TarkovPilot.exe" --hidden`, executable: current, want: true},
		{name: "previous without hidden", value: `"C:\Program Files\TarkovPilotPlus\TarkovPilot.exe"`, executable: current, want: true},
		{name: "previous in another directory", value: `"C:\Other\TarkovPilot.exe" --hidden`, executable: current},
		{name: "another executable", value: `"C:\Program Files\TarkovPilotPlus\Other.exe" --hidden`, executable: current},
		{name: "current in another directory", value: `"C:\Other\TarkovPilotPlus.exe" --hidden`, executable: current},
		{name: "current missing quotes", value: `C:\Program Files\TarkovPilotPlus\TarkovPilotPlus.exe --hidden`, executable: current},
		{name: "previous missing quotes", value: `C:\Program Files\TarkovPilotPlus\TarkovPilot.exe --hidden`, executable: current},
		{name: "previous unexpected argument", value: `"C:\Program Files\TarkovPilotPlus\TarkovPilot.exe" --other`, executable: current},
		{name: "current unexpected argument", value: `"C:\Program Files\TarkovPilotPlus\TarkovPilotPlus.exe" --other`, executable: current},
		{name: "another executable cannot migrate", value: `"C:\Program Files\TarkovPilotPlus\TarkovPilot.exe" --hidden`, executable: `C:\Program Files\TarkovPilotPlus\Other.exe`},
		{name: "old executable cannot migrate new value", value: `"C:\Program Files\TarkovPilotPlus\TarkovPilotPlus.exe" --hidden`, executable: `C:\Program Files\TarkovPilotPlus\TarkovPilot.exe`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoStartValueMatches(tc.value, tc.executable); got != tc.want {
				t.Fatalf("autoStartValueMatches() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHasLaunchArg(t *testing.T) {
	args := []string{"updated", "--hidden"}
	if !hasLaunchArg(args, "updated") {
		t.Fatal("updated argument was not found")
	}
	if !hasLaunchArg(args, "--hidden") {
		t.Fatal("hidden argument was not found")
	}
	if hasLaunchArg(args, "--other") {
		t.Fatal("unexpected argument was found")
	}
}
