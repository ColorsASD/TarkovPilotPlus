package plus

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/scanner"
)

func TestAPIKeyProtectionAndPrivateState(t *testing.T) {
	const key = "local-test-key-not-a-real-credential"
	sealed, err := protectAPIKey([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), key) {
		t.Fatal("DPAPI output contained plaintext")
	}
	opened, err := unprotectAPIKey(sealed)
	if err != nil || string(opened) != key {
		t.Fatal("encrypted key did not round trip")
	}
	a := &ItemWindowApp{apiKey: key, state: ItemState{HasAPIKey: true}}
	if a.RevealAPIKey() != key {
		t.Fatal("the explicit reveal request did not return the saved key")
	}
	data, err := json.Marshal(a.GetState())
	if err != nil || strings.Contains(string(data), key) {
		t.Fatal("state exposed the API key")
	}
	if !strings.Contains(string(data), `"item":null`) {
		t.Fatal("empty item must reset an existing frontend selection")
	}
}

func TestOnlyInspectTitlesCanMatchAndWindowsAreProcessedSeparately(t *testing.T) {
	items := []market.Item{
		{UID: "helmet", Name: "Galvion Caiman Hybrid helmet (MultiCam Alpine)"},
		{UID: "drive", Name: "Secure Flash drive", ShortName: "Flash drive"},
	}
	tests := []struct {
		name   string
		result scanner.Result
		want   string
	}{
		{"inspect title variants", scanner.Result{InspectOpen: true, Groups: [][]string{{"Galvion Caiman Hybrid", "helmet (MultiCam Alpine)"}, {items[0].Name}}}, "helmet"},
		{"equal variant scores prefer primary", scanner.Result{InspectOpen: true, Lines: []string{items[0].Name}, Groups: [][]string{{items[0].Name}, {items[1].Name}}}, "helmet"},
		{"multiple different inspect windows", scanner.Result{InspectOpen: true, Groups: [][]string{{items[0].Name}, {items[0].Name}, {items[1].Name}, {items[1].Name}}}, "drive"},
		{"empty inspect title", scanner.Result{InspectOpen: true, Groups: [][]string{{}, {}}, Lines: []string{items[0].Name}}, ""},
		{"best OCR variant", scanner.Result{InspectOpen: true, TitleVariants: [][]string{{"Secure Flash drlve", "Secure Flash drive", "ecure Flas driye"}}}, "drive"},
		{"explicit windows do not merge their titles", scanner.Result{InspectOpen: true, TitleVariants: [][]string{{items[0].Name}, {items[1].Name}}}, "drive"},
		{"cropped title lines", scanner.Result{InspectOpen: true, Lines: []string{items[1].Name}}, "drive"},
		{"inventory text without inspect", scanner.Result{Lines: []string{items[0].Name}, Groups: [][]string{{items[0].Name}}}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			item, found := matchScan(tc.result, items)
			if found != (tc.want != "") || item.UID != tc.want {
				t.Fatalf("got %q, found=%v; want %q", item.UID, found, tc.want)
			}
		})
	}
}

func TestKeyOrOptionsChangeDiscardsOldMatch(t *testing.T) {
	old := ItemOptions{Language: "en", Mode: "pvp", revision: 1}
	a := &ItemWindowApp{state: ItemState{HasAPIKey: true, ItemOptions: old}}
	a.state.ItemOptions.revision = 2
	catalog := market.Catalog{Items: []market.Item{{UID: "test", Name: "Secure Flash drive"}}}
	a.acceptMatch([]string{"Secure Flash drive"}, catalog, old, nil)
	if a.GetState().Item != nil {
		t.Fatal("a result from an old API key/options replaced current state")
	}
	a.state.Status = "kikapcsolva"
	a.errorRevision(old.revision, "old request failed")
	a.updateRevision(old.revision, func(st *ItemState) { st.Status = "old scan finished" })
	if a.GetState().Status != "kikapcsolva" || a.GetState().Error != "" {
		t.Fatal("an old asynchronous completion replaced the new options state")
	}
}

func TestPrivateSettingsReplaceAndNormalize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "item-settings.json")
	if err := writePrivateFile(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "second" {
		t.Fatal("settings replacement failed")
	}
	o := normalizeItemOptions(ItemOptions{Mode: "PVE", Language: "unexpected"})
	if o.Mode != "pve" || o.Language != "en" {
		t.Fatal("options were not normalized")
	}
}

func TestSavedScanSwitchesMigrateWithoutDisablingManualUse(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, automatic := range []bool{false, true} {
			saved := ItemOptions{Enabled: enabled, Automatic: automatic, Language: "ru", Mode: "SEASON", CharacterUID: " season-character "}
			got := normalizeSavedItemOptions(saved)
			if !got.Enabled || got.Automatic != (enabled && automatic) {
				t.Fatalf("old enabled=%v automatic=%v migrated to %+v", enabled, automatic, got)
			}
			if got.Language != "en" || got.Mode != "season" || got.CharacterUID != "season-character" {
				t.Fatalf("switch migration did not enforce English or preserve the selected character: %+v", got)
			}
		}
	}
}

func TestSetOptionsAlwaysPersistsEnglish(t *testing.T) {
	for _, language := range []string{"ru", "en", "hu", ""} {
		t.Run("language="+language, func(t *testing.T) {
			a := &ItemWindowApp{dir: t.TempDir(), state: ItemState{ItemOptions: ItemOptions{Language: "en", Mode: "pvp"}, OCRLanguages: []string{"en", "ru"}}}
			st := a.SetOptions(ItemOptions{Enabled: true, Automatic: true, Language: language, Mode: "PVE"})
			if st.Language != "en" || st.Mode != "pve" || st.Error != "" || len(st.OCRLanguages) != 1 || st.OCRLanguages[0] != "en" {
				t.Fatalf("programmatic language changed the English-only state: %+v", st)
			}
			data, err := os.ReadFile(filepath.Join(a.dir, "item-settings.json"))
			if err != nil {
				t.Fatal(err)
			}
			var saved ItemOptions
			if err := json.Unmarshal(data, &saved); err != nil || saved.Language != "en" {
				t.Fatalf("programmatic language was persisted: %+v, %v", saved, err)
			}
		})
	}
}

func TestEnglishOCRInventoryDoesNotAdvertiseUnavailableModel(t *testing.T) {
	for _, tc := range []struct {
		name      string
		languages []string
		wantEn    bool
	}{
		{"both models", []string{"en", "ru"}, true},
		{"English only", []string{"en"}, true},
		{"English repeated", []string{"ru", "en", "en"}, true},
		{"Russian only", []string{"ru"}, false},
		{"no models", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &ItemWindowApp{state: ItemState{ItemOptions: ItemOptions{Language: "ru"}, OCRLanguages: tc.languages}}
			st := a.GetState()
			if st.Language != "en" || len(st.OCRLanguages) > 1 || (len(st.OCRLanguages) == 1) != tc.wantEn {
				t.Fatalf("wrong English-only language inventory: %+v", st)
			}
			if tc.wantEn {
				if st.OCRLanguages[0] != "en" {
					t.Fatal("another OCR language was exposed")
				}
				st.OCRLanguages[0] = "modified"
				if a.GetState().OCRLanguages[0] != "en" {
					t.Fatal("OCR language state exposed its backing array")
				}
			}
		})
	}
}

func TestSavedKeyRestoresTheAutomaticStartupStatus(t *testing.T) {
	sealed, err := protectAPIKey([]byte("local-fixture-placeholder"))
	if err != nil {
		t.Fatal(err)
	}
	for _, automatic := range []bool{false, true} {
		base := t.TempDir()
		t.Setenv("APPDATA", base)
		dir := filepath.Join(base, "TarkovPilotPlus")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		options, err := json.Marshal(ItemOptions{Enabled: true, Automatic: automatic, Language: "ru", Mode: "pvp"})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "item-settings.json"), options, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "api-key.bin"), sealed, 0600); err != nil {
			t.Fatal(err)
		}
		a, err := NewItemWindowApp()
		if err != nil {
			t.Fatal(err)
		}
		want := "Automatic scanning is off."
		if automatic {
			want = "Waiting for an item Inspect window."
		}
		if st := a.GetState(); !st.HasAPIKey || st.Status != want || st.Language != "en" || len(st.OCRLanguages) != 1 || st.OCRLanguages[0] != "en" {
			t.Fatalf("saved key/automatic=%v restored the wrong status: %+v", automatic, st)
		}
	}
}

func TestItemAssetsServeSeparateFrontend(t *testing.T) {
	frontend, err := fs.Sub(itemAssets, "frontend/dist")
	if err != nil {
		t.Fatal(err)
	}
	f, err := (itemAssetsFS{frontend}).Open("index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data := make([]byte, 2048)
	n, _ := f.Read(data)
	if !strings.Contains(string(data[:n]), "item.css") || strings.Contains(string(data[:n]), `href="style.css"`) {
		t.Fatal("item window served the main frontend")
	}
}
