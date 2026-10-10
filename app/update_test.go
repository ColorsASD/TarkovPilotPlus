package main

import (
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus"
)

func TestOriginalAndPlusVersionsAreIndependent(t *testing.T) {
	app := NewApp()
	state := app.GetState()
	status := app.currentAppStatus()
	if state.Version != Version || state.PlusVersion != plus.Version {
		t.Fatalf("component versions were mixed: %#v", state)
	}
	if status.Version != Version {
		t.Fatalf("Pilot heartbeat must use its original version, got %q", status.Version)
	}
}

func TestUpstreamVersionCannotShowForkUpdateBanner(t *testing.T) {
	app := NewApp()
	app.setLatestVersion("999.0.0")
	state := app.GetState()
	if state.UpdateReady || state.LatestVersion != "" || app.notifiedVersion != "" {
		t.Fatalf("upstream version enabled a fork update: %#v", state)
	}
	if len(state.EventLog) != 0 {
		t.Fatal("upstream version emitted a misleading update event")
	}
}
