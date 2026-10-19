package plus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestSingleModeLegacyLocalProgressOptionCannotChangeRouting(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy-false", true: "legacy-true"}[legacy], func(t *testing.T) {
			a, fake, _ := newMarketQuotaTestApp(t, trackingItemUID, trackingBSGID)
			encoded, err := json.Marshal(a.state.ItemOptions)
			if err != nil {
				t.Fatal(err)
			}
			var legacyFields map[string]any
			if err := json.Unmarshal(encoded, &legacyFields); err != nil {
				t.Fatal(err)
			}
			legacyFields["localProgress"] = legacy
			encoded, err = json.Marshal(legacyFields)
			if err != nil {
				t.Fatal(err)
			}
			var options ItemOptions
			if err := json.Unmarshal(encoded, &options); err != nil {
				t.Fatal(err)
			}
			a.SetOptions(options)
			a.refreshTracking()
			for i := 0; i < 4; i++ {
				addMarketKindForTest(t, a, trackingItemUID, "regular")
			}
			state := a.GetState()
			if state.Local == nil || state.Local.Error != "" || state.Tracking.RequiredQuantity != 8 ||
				state.Tracking.AvailableQuantity != 4 || len(fake.writes) != 4 || state.Tracking.Storage != "market" {
				t.Fatalf("legacy mode flag selected a separate source path: state=%+v writes=%+v", state, fake.writes)
			}
			row := memoryMarketStockForTest(t, a, trackingBSGID)
			if row.MarketAvailableQuantity != 4 || row.MarketRegularQuantity != 4 {
				t.Fatalf("legacy mode flag changed confirmed Market quantities: %+v", row)
			}
			assertNoMarketStockFilesForTest(t, a.dir)
			persisted, err := os.ReadFile(filepath.Join(a.dir, "item-settings.json"))
			if err != nil || strings.Contains(string(persisted), "localProgress") {
				t.Fatalf("removed mode option was still persisted: %s %v", persisted, err)
			}
			stateJSON, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			var stateFields map[string]json.RawMessage
			if err := json.Unmarshal(stateJSON, &stateFields); err != nil {
				t.Fatal(err)
			}
			if _, exists := stateFields["localProgress"]; exists {
				t.Fatalf("removed mode flag survived in frontend state: %s", stateJSON)
			}
		})
	}
}

func TestSingleModeTrackedOnlyMarketClientCanSaveStock(t *testing.T) {
	for _, kind := range []string{"regular", "fir"} {
		t.Run(kind, func(t *testing.T) {
			a, fake, _ := newMarketQuotaTestApp(t, trackingItemUID, trackingBSGID)
			a.tracker = fake.fakeTrackingAPI
			a.localAt = time.Time{}
			if err := a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item); err != nil {
				t.Fatal(err)
			}
			state := addMarketKindForTest(t, a, trackingItemUID, kind)
			if state.Local == nil || state.Local.Error != "" || state.Tracking.Error != "" || state.Tracking.AvailableQuantity != 1 || len(fake.writes) != 1 {
				t.Fatalf("complete tracked-only Market response could not be saved: state=%+v writes=%+v", state, fake.writes)
			}
			assertNoMarketStockFilesForTest(t, a.dir)
		})
	}
}

func TestSingleModeMarketSourcesAreMandatoryBeforeStockChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ItemWindowApp, *fakeCharacterProgress)
	}{
		{name: "quests-unavailable", change: func(a *ItemWindowApp, f *fakeCharacterProgress) {
			a.tracker = &marketProgressReadFailureForTest{fakeCharacterProgress: f, stage: "quests"}
		}},
		{name: "hideout-unavailable", change: func(a *ItemWindowApp, f *fakeCharacterProgress) {
			a.tracker = &marketProgressReadFailureForTest{fakeCharacterProgress: f, stage: "hideout"}
		}},
		{name: "market-service-error-without-current-snapshot", change: func(a *ItemWindowApp, f *fakeCharacterProgress) {
			a.localRevision++
			f.loadHook = func(int) (market.TrackedSnapshot, error) {
				return market.TrackedSnapshot{}, market.ErrProgressUnavailable
			}
		}},
	} {
		for _, kind := range []string{"regular", "fir"} {
			t.Run(tc.name+"-"+kind, func(t *testing.T) {
				a, fake, _ := newMarketQuotaTestApp(t, trackingItemUID, trackingBSGID)
				tc.change(a, fake)
				// Ellenőrzött Market-forrás nélkül nincs készletmódosítás.
				a.localRevision++
				a.localAt = time.Time{}
				a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
				addMarketKindForTest(t, a, trackingItemUID, kind)
				state := a.GetState()
				if len(fake.writes) != 0 || fake.snapshot.Items[0].AvailableQuantity != 0 {
					t.Fatalf("unavailable Market source allowed a stock write: writes=%+v", fake.writes)
				}
				assertNoMarketStockFilesForTest(t, a.dir)
				if state.Local == nil || state.Local.Error == "" || a.canAddLocalForTest() {
					t.Fatalf("source failure was invisible or left Add available: %+v", state.Local)
				}
			})
		}
	}
}

func TestSingleModeLoadsQuestAndHideoutWithoutCurrentItem(t *testing.T) {
	a, fake, _ := newMarketQuotaTestApp(t, trackingItemUID, trackingBSGID)
	questReads, hideoutReads := fake.questReads, fake.hideoutReads
	a.state.Item, a.state.Tracking = nil, nil
	a.localAt = time.Time{}
	a.refreshTracking()
	state := a.GetState()
	row := memoryMarketStockForTest(t, a, trackingBSGID)
	if fake.questReads != questReads+1 || fake.hideoutReads != hideoutReads+1 || state.Item != nil || state.Tracking != nil ||
		state.Local == nil || state.Local.Error != "" || row.RequiredQuantity != 8 || row.MarketRequiredQuantity != 8 || len(row.Sources) != 2 {
		t.Fatalf("source loading still depended on a current item or a mode switch: %+v reads=%d/%d", state, fake.questReads, fake.hideoutReads)
	}
}

func TestSingleModeShortcutDefaultsAndCustomDuplicateRejection(t *testing.T) {
	defaults := normalizeItemOptions(ItemOptions{})
	if defaults.AddHotkey != "F2" || defaults.FIRHotkey != "F1" {
		t.Fatalf("default actions are reversed: %+v", defaults)
	}
	a, _ := newTrackingTestApp(t)
	opts := a.state.ItemOptions
	opts.AddHotkey, opts.FIRHotkey = "shift + ctrl + alt + q", "Alt+F10"
	state := a.SetOptions(opts)
	if state.Error != "" || state.AddHotkey != "Ctrl+Alt+Shift+Q" || state.FIRHotkey != "Alt+F10" {
		t.Fatalf("custom shortcuts were not canonicalized independently: %+v", state)
	}
	path := filepath.Join(a.dir, "item-settings.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	previous := a.state.ItemOptions
	duplicate := previous
	duplicate.AddHotkey, duplicate.FIRHotkey = "alt+ctrl+shift+q", "Shift+Ctrl+Alt+Q"
	state = a.SetOptions(duplicate)
	after, err := os.ReadFile(path)
	if err != nil || state.Error == "" || a.state.ItemOptions != previous || string(before) != string(after) {
		t.Fatalf("duplicate exact chords changed the saved or active configuration: state=%+v err=%v", state, err)
	}
	// Egy alapkód eltérő módosítókkal két külön, engedélyezett action marad.
	distinct := previous
	distinct.AddHotkey, distinct.FIRHotkey = "Ctrl+Q", "Alt+Q"
	state = a.SetOptions(distinct)
	if state.Error != "" || state.AddHotkey != "Ctrl+Q" || state.FIRHotkey != "Alt+Q" {
		t.Fatalf("different exact chords on the same base key were rejected: %+v", state)
	}
}

func TestSingleModeInvalidShortcutCannotChangeSavedSettings(t *testing.T) {
	for _, invalid := range []string{"Ctrl++F3", "Win+F1", "F13", "Ctrl+Ctrl+Q", "Alt+", "Esc", "Ctrl+off"} {
		for _, kind := range []string{"regular", "fir"} {
			t.Run(kind+"-"+invalid, func(t *testing.T) {
				a, _ := newTrackingTestApp(t)
				a.SetOptions(a.state.ItemOptions)
				path := filepath.Join(a.dir, "item-settings.json")
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				previous := a.state.ItemOptions
				opts := previous
				if kind == "fir" {
					opts.FIRHotkey = invalid
				} else {
					opts.AddHotkey = invalid
				}
				state := a.SetOptions(opts)
				after, err := os.ReadFile(path)
				if err != nil || state.Error == "" || a.state.ItemOptions != previous || string(before) != string(after) {
					t.Fatalf("invalid shortcut changed active or saved settings: state=%+v err=%v", state, err)
				}
			})
		}
	}
}

func TestSingleModeSavedShortcutMigrationKeepsIndependentActions(t *testing.T) {
	for _, tc := range []struct {
		name, saved  string
		regular, fir string
	}{
		{name: "old-default", saved: `{"enabled":true,"automatic":true,"addHotkey":"F1","localProgress":false}`, regular: "F2", fir: "F1"},
		{name: "old-disabled", saved: `{"enabled":true,"automatic":true,"addHotkey":"off","localProgress":true}`, regular: "off", fir: "off"},
		{name: "duplicate-canonical", saved: `{"enabled":true,"automatic":true,"addHotkey":"shift+ctrl+q","firHotkey":"Ctrl+Shift+Q"}`, regular: "F2", fir: "F1"},
		{name: "regular-disabled", saved: `{"enabled":true,"automatic":true,"addHotkey":"off","firHotkey":"Alt+Q"}`, regular: "off", fir: "Alt+Q"},
		{name: "fir-disabled", saved: `{"enabled":true,"automatic":true,"addHotkey":"Ctrl+Q","firHotkey":"off"}`, regular: "Ctrl+Q", fir: "off"},
		{name: "both-disabled", saved: `{"enabled":true,"automatic":true,"addHotkey":"off","firHotkey":"off"}`, regular: "off", fir: "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var saved ItemOptions
			if err := json.Unmarshal([]byte(tc.saved), &saved); err != nil {
				t.Fatal(err)
			}
			normalized := normalizeSavedItemOptions(saved)
			if normalized.AddHotkey != tc.regular || normalized.FIRHotkey != tc.fir || !normalized.Enabled || !normalized.Automatic {
				t.Fatalf("saved shortcut migration lost defaults or disabled choices: %+v", normalized)
			}
			encoded, err := json.Marshal(normalized)
			if err != nil || strings.Contains(string(encoded), "localProgress") {
				t.Fatalf("removed mode choice returned through migration: %s %v", encoded, err)
			}
		})
	}
}
