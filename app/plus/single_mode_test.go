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

func TestSingleAddShortcutDefaultsAndPersistsCustomChoice(t *testing.T) {
	if options := normalizeItemOptions(ItemOptions{}); options.AddHotkey != "F1" {
		t.Fatalf("the only Add shortcut did not default to F1: %+v", options)
	}
	a, _ := newTrackingTestApp(t)
	opts := a.state.ItemOptions
	opts.AddHotkey = "shift + ctrl + alt + q"
	state := a.SetOptions(opts)
	if state.Error != "" || state.AddHotkey != "Ctrl+Alt+Shift+Q" {
		t.Fatalf("custom Add shortcut was not canonicalized: %+v", state)
	}
	data, err := os.ReadFile(filepath.Join(a.dir, "item-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if json.Unmarshal(data, &saved) != nil || string(saved["addHotkey"]) != `"Ctrl+Alt+Shift+Q"` {
		t.Fatalf("single Add shortcut was not persisted: %s", data)
	}
	if _, exists := saved["firHotkey"]; exists {
		t.Fatal("removed FiR shortcut returned to saved options")
	}
}

func TestSingleAddInvalidShortcutCannotChangeSavedSettings(t *testing.T) {
	for _, invalid := range []string{"Ctrl++F3", "Win+F1", "F13", "Ctrl+Ctrl+Q", "Alt+", "Esc", "Ctrl+off"} {
		t.Run(invalid, func(t *testing.T) {
			a, _ := newTrackingTestApp(t)
			a.SetOptions(a.state.ItemOptions)
			path := filepath.Join(a.dir, "item-settings.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			previous := a.state.ItemOptions
			opts := previous
			opts.AddHotkey = invalid
			state := a.SetOptions(opts)
			after, err := os.ReadFile(path)
			if err != nil || state.Error == "" || a.state.ItemOptions != previous || string(before) != string(after) {
				t.Fatalf("invalid Add shortcut changed active or saved settings: state=%+v err=%v", state, err)
			}
		})
	}
}

func TestSingleAddSavedShortcutMigrationUsesLegacyFIRChoiceAndRewritesFile(t *testing.T) {
	for _, tc := range []struct {
		name, fields, want string
		legacy             bool
	}{
		{"two-defaults", `"addHotkey":"F2","firHotkey":"F1"`, "F1", true},
		{"custom-fir", `"addHotkey":"Ctrl+F2","firHotkey":"shift+alt+q"`, "Alt+Shift+Q", true},
		{"fir-disabled", `"addHotkey":"Ctrl+Q","firHotkey":"off"`, "off", true},
		{"regular-disabled", `"addHotkey":"off","firHotkey":"F8"`, "F8", true},
		{"legacy-empty", `"addHotkey":"F2","firHotkey":""`, "F1", true},
		{"legacy-null", `"addHotkey":"F2","firHotkey":null`, "F1", true},
		{"legacy-invalid", `"addHotkey":"F2","firHotkey":"F13"`, "F1", true},
		{"single-custom", `"addHotkey":"Ctrl+F2"`, "Ctrl+F2", false},
		{"single-disabled", `"addHotkey":"off"`, "off", false},
		{"missing-shortcuts", `"unrelatedLegacyField":false`, "F1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(`{"enabled":true,"automatic":false,"language":"en","mode":"pve","characterUid":"character-a",` + tc.fields + `}`)
			path := filepath.Join(t.TempDir(), "item-settings.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			options, err := migrateSavedItemOptions(path, data)
			if err != nil || options.AddHotkey != tc.want || !options.Enabled || options.Automatic || options.Mode != "pve" || options.CharacterUID != "character-a" || options.Language != "en" {
				t.Fatalf("migration lost the chosen shortcut or other preferences: %+v err=%v", options, err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.legacy && string(after) != string(data) {
				t.Fatal("loading current preferences unnecessarily rewrote their file")
			}
			if tc.legacy {
				var fields map[string]json.RawMessage
				if json.Unmarshal(after, &fields) != nil || string(fields["addHotkey"]) != `"`+tc.want+`"` {
					t.Fatalf("startup did not migrate the actual file: %s", after)
				}
				if _, exists := fields["firHotkey"]; exists {
					t.Fatal("legacy FiR preference survived the automatic file migration")
				}
				reloaded, err := decodeSavedItemOptions(after)
				if err != nil || reloaded != options {
					t.Fatalf("migrated file did not reload the same preferences: %+v err=%v", reloaded, err)
				}
			}
		})
	}
}

func TestSingleAddMigrationWriteFailureRetainsLoadedPreferences(t *testing.T) {
	data := []byte(`{"enabled":true,"automatic":true,"mode":"season","characterUid":"character-a","addHotkey":"F2","firHotkey":"Alt+Q"}`)
	// Egy könyvtár nem írható felül beállításfájlként; valódi appadat nem érintett.
	options, err := migrateSavedItemOptions(t.TempDir(), data)
	if err == nil || options.AddHotkey != "Alt+Q" || options.Mode != "season" || options.CharacterUID != "character-a" || !options.Automatic {
		t.Fatalf("migration write failure discarded readable preferences: %+v err=%v", options, err)
	}
}

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
				addMarketItemForTest(t, a, trackingItemUID)
			}
			state := a.GetState()
			if state.Local == nil || state.Local.Error != "" || state.Tracking.RequiredQuantity != 8 ||
				state.Tracking.AvailableQuantity != 4 || len(fake.writes) != 4 || state.Tracking.Storage != "market" {
				t.Fatalf("legacy mode flag selected a separate source path: state=%+v writes=%+v", state, fake.writes)
			}
			row := memoryMarketStockForTest(t, a, trackingBSGID)
			if row.MarketAvailableQuantity != 4 || row.AvailableQuantity != 4 {
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
	a, fake, _ := newMarketQuotaTestApp(t, trackingItemUID, trackingBSGID)
	a.tracker = fake.fakeTrackingAPI
	a.localAt = time.Time{}
	if err := a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item); err != nil {
		t.Fatal(err)
	}
	state := addMarketItemForTest(t, a, trackingItemUID)
	if state.Local == nil || state.Local.Error != "" || state.Tracking.Error != "" || state.Tracking.AvailableQuantity != 1 || len(fake.writes) != 1 {
		t.Fatalf("complete tracked-only Market response could not be saved: state=%+v writes=%+v", state, fake.writes)
	}
	assertNoMarketStockFilesForTest(t, a.dir)
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
		t.Run(tc.name, func(t *testing.T) {
			a, fake, _ := newMarketQuotaTestApp(t, trackingItemUID, trackingBSGID)
			tc.change(a, fake)
			// Ellenőrzött Market-forrás nélkül nincs készletmódosítás.
			a.localRevision++
			a.localAt = time.Time{}
			a.refreshLocalProgress(a.ctx, a.apiKey, a.state.ItemOptions, *a.state.Item)
			addMarketItemForTest(t, a, trackingItemUID)
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
