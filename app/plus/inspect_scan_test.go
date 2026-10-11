package plus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/scanner"
)

type inspectScannerFixture struct {
	probe, result   scanner.Result
	probes, reads   int
	languages       []string
	onProbe, onRead func()
}

func (f *inspectScannerFixture) Probe(context.Context, bool) (scanner.Result, error) {
	f.probes++
	if f.onProbe != nil {
		f.onProbe()
	}
	return f.probe, nil
}

func (f *inspectScannerFixture) Scan(_ context.Context, language string, _ bool) (scanner.Result, error) {
	f.reads++
	f.languages = append(f.languages, language)
	if f.onRead != nil {
		f.onRead()
	}
	return f.result, nil
}

func (f *inspectScannerFixture) Close() error { return nil }

func inspectTestApp(f *inspectScannerFixture) *ItemWindowApp {
	return &ItemWindowApp{
		ctx: context.Background(), ocr: f,
		state: ItemState{HasAPIKey: true, ItemOptions: ItemOptions{Enabled: true, Automatic: true, Language: "en", Mode: "pvp", revision: 1}},
	}
}

func TestAutomaticScanWithoutInspectNeverReadsTextOrCallsAPI(t *testing.T) {
	for _, probe := range []scanner.Result{{}, {Active: true}} {
		f := &inspectScannerFixture{probe: probe}
		a := inspectTestApp(f)
		f.onProbe = func() {
			if a.GetState().Busy {
				t.Fatal("Inspect absence made the UI busy before the pixel probe")
			}
		}
		a.state.Item = &market.Item{UID: "previous", Name: "Previous item"}
		a.scan(true, 0) // A nil API client would panic if the gate failed.
		st := a.GetState()
		if f.probes != 1 || f.reads != 0 || st.Busy || st.Error != "" {
			t.Fatalf("Inspect absence triggered work: reads=%d state=%+v", f.reads, st)
		}
		if st.Item == nil || st.Item.UID != "previous" {
			t.Fatal("waiting for Inspect erased the last recognized item")
		}
		if probe.Active && st.Status != "Waiting for an item Inspect window." {
			t.Fatal("Inspect absence was reported as a failed name search")
		}
	}
}

func TestAutomaticUnchangedInspectReadsOnEveryClick(t *testing.T) {
	f := &inspectScannerFixture{
		probe:  scanner.Result{Active: true, InspectOpen: true, Fingerprint: "unchanged-title"},
		result: scanner.Result{Active: true, InspectOpen: true, Fingerprint: "unchanged-title"},
	}
	a := inspectTestApp(f)
	for range 3 {
		a.scan(true, 0)
	}
	if f.probes != 3 || f.reads != 3 || a.GetState().Busy {
		t.Fatal("an unchanged Inspect title suppressed a new click-triggered read")
	}
}

func TestInspectClosingBeforeOCRDoesNotSearchCatalog(t *testing.T) {
	f := &inspectScannerFixture{
		probe:  scanner.Result{Active: true, InspectOpen: true, Fingerprint: "opening"},
		result: scanner.Result{Active: true},
	}
	a := inspectTestApp(f)
	a.scan(true, 0)
	if f.reads != 1 || a.GetState().Busy || a.GetState().Status != "Waiting for an item Inspect window." {
		t.Fatal("Inspect closing did not stop the recognition workflow")
	}
}

func TestOptionsChangingDuringProbeSkipsOCR(t *testing.T) {
	f := &inspectScannerFixture{probe: scanner.Result{Active: true, InspectOpen: true}}
	a := inspectTestApp(f)
	f.onProbe = func() { a.state.ItemOptions.revision++ }
	a.scan(true, 0)
	if f.reads != 0 || a.GetState().Busy {
		t.Fatal("an obsolete probe triggered OCR or left the new options busy")
	}
}

func TestAutomaticClickWaitsForActiveOperationInsteadOfBeingLost(t *testing.T) {
	probed := make(chan struct{})
	f := &inspectScannerFixture{probe: scanner.Result{Active: true}}
	f.onProbe = func() { close(probed) }
	a := inspectTestApp(f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.ctx = ctx
	requests := make(chan struct{})
	done := make(chan struct{})
	a.scanMu.Lock()
	go func() { a.runAutomaticInspects(requests); close(done) }()
	requests <- struct{}{}
	select {
	case <-probed:
		a.scanMu.Unlock()
		t.Fatal("queued click bypassed the active scan")
	case <-time.After(50 * time.Millisecond):
	}
	a.scanMu.Unlock()
	select {
	case <-probed:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("the click made while scanning was discarded")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the automatic worker did not stop")
	}
}

func TestOpenedInspectMatchesLocalCatalogOnEveryClick(t *testing.T) {
	f := &inspectScannerFixture{
		probe:  scanner.Result{Active: true, InspectOpen: true, Fingerprint: "title-one"},
		result: scanner.Result{Active: true, InspectOpen: true, Fingerprint: "title-one", Lines: []string{"Secure Flash drive"}, Languages: []string{"en", "ru"}},
	}
	a := inspectTestApp(f)
	a.apiKey = "local-fixture-placeholder"
	cacheDir := t.TempDir()
	data, err := json.Marshal(map[string]any{
		"version": 1, "mode": "pvp", "lang": "en", "updatedAt": time.Now(),
		"items": []market.Item{{UID: "drive", Name: "Secure Flash drive", Avg24hPrice: 100000, HaveMarketData: true}, {UID: "ledx", Name: "LEDX Skin Transilluminator"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cacheDir, "catalog-pvp-en.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	a.client = market.NewClient(cacheDir)
	a.scan(true, 0)
	a.scan(true, 0)
	st := a.GetState()
	if f.reads != 2 || st.Item == nil || st.Item.UID != "drive" || st.Busy || st.Error != "" {
		t.Fatalf("Inspect workflow did not recognize on each click: reads=%d state=%+v", f.reads, st)
	}
	if st.ScanDetails == nil || st.ScanDetails.InspectCount != 1 || len(st.ScanDetails.Titles) != 1 || st.ScanDetails.Titles[0] != "Secure Flash drive" {
		t.Fatalf("recognized title details missing: %+v", st.ScanDetails)
	}
	a.SearchItem("LEDX Skin Transilluminator")
	if a.GetState().Item == nil || a.GetState().Item.UID != "ledx" {
		t.Fatal("manual search did not select its item")
	}
	a.scan(true, 0)
	if f.reads != 3 || a.GetState().Item.UID != "drive" {
		t.Fatal("an unchanged Inspect did not replace the manually searched item")
	}
	f.onRead = func() { a.state.ItemOptions.revision++ }
	a.scan(false, 0)
	if a.GetState().Busy {
		t.Fatal("options changing during OCR left the new revision busy")
	}
}

func TestAutomaticAwlInspectionMatchesThreeCharacterCatalogName(t *testing.T) {
	f := &inspectScannerFixture{
		probe: scanner.Result{Active: true, InspectOpen: true, InspectCount: 1, Fingerprint: "awl-header"},
		result: scanner.Result{
			Active: true, InspectOpen: true, InspectCount: 1, Fingerprint: "awl-header",
			TitleVariants: [][]string{{"Awl", "Awi"}}, Languages: []string{"en"},
		},
	}
	a := inspectTestApp(f)
	a.apiKey = "local-fixture-placeholder"
	cacheDir := t.TempDir()
	data, err := json.Marshal(map[string]any{
		"version": 1, "mode": "pvp", "lang": "en", "updatedAt": time.Now(),
		"items": []market.Item{
			{UID: "awl", Name: "Awl"},
			{UID: "splint", Name: "Immobilizing splint"},
			{UID: "bayonet", Name: "ER FULCRUM BAYONET"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cacheDir, "catalog-pvp-en.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	a.client = market.NewClient(cacheDir)
	a.scan(true, 0)
	st := a.GetState()
	if f.probes != 1 || f.reads != 1 || st.Item == nil || st.Item.UID != "awl" || st.Busy || st.Error != "" {
		t.Fatalf("the automatic workflow rejected or mismatched the three-character Awl title: probes=%d reads=%d state=%+v", f.probes, f.reads, st)
	}
	if st.ScanDetails == nil || st.ScanDetails.InspectCount != 1 || st.ScanDetails.Outcome != "Item recognized: Awl" {
		t.Fatalf("the automatic Awl recognition details are missing: %+v", st.ScanDetails)
	}
}

func TestAutomaticSwitchOffKeepsManualScanAndNameSearch(t *testing.T) {
	f := &inspectScannerFixture{
		probe:  scanner.Result{Active: true, InspectOpen: true},
		result: scanner.Result{Active: true, InspectOpen: true, Lines: []string{"Secure Flash drive"}},
	}
	a := inspectTestApp(f)
	a.state.Automatic = false
	a.apiKey = "local-fixture-placeholder"
	cacheDir := t.TempDir()
	data, err := json.Marshal(map[string]any{
		"version": 1, "mode": "pvp", "lang": "en", "updatedAt": time.Now(),
		"items": []market.Item{{UID: "drive", Name: "Secure Flash drive"}, {UID: "ledx", Name: "LEDX Skin Transilluminator"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cacheDir, "catalog-pvp-en.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	a.client = market.NewClient(cacheDir)
	a.scan(true, 0)
	if f.probes != 0 || f.reads != 0 || a.GetState().Busy {
		t.Fatal("the disabled automatic switch started screen recognition")
	}
	a.SearchItem("LEDX Skin Transilluminator")
	if st := a.GetState(); st.Item == nil || st.Item.UID != "ledx" || st.Error != "" {
		t.Fatalf("the disabled automatic switch blocked name search: %+v", st)
	}
	a.scan(false, 0)
	if st := a.GetState(); f.probes != 1 || f.reads != 1 || st.Item == nil || st.Item.UID != "drive" || st.Busy || st.Error != "" {
		t.Fatalf("the disabled automatic switch blocked manual recognition: %+v", st)
	}
}

func TestRecognitionAndNameSearchAlwaysUseEnglish(t *testing.T) {
	f := &inspectScannerFixture{
		probe:  scanner.Result{Active: true, InspectOpen: true},
		result: scanner.Result{Active: true, InspectOpen: true, Lines: []string{"Secure Flash drive"}, Languages: []string{"ru", "en"}},
	}
	a := inspectTestApp(f)
	// A régi belső állapot sem választhat orosz OCR-t vagy katalógust.
	a.state.Language = "ru"
	a.apiKey = "local-fixture-placeholder"
	cacheDir := t.TempDir()
	for _, language := range []string{"en", "ru"} {
		data, err := json.Marshal(map[string]any{
			"version": 1, "mode": "pvp", "lang": language, "updatedAt": time.Now(),
			"items": []market.Item{
				{UID: language + "-drive", Name: "Secure Flash drive"},
				{UID: language + "-ledx", Name: "LEDX Skin Transilluminator"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(cacheDir, "catalog-pvp-"+language+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	a.client = market.NewClient(cacheDir)
	for _, automatic := range []bool{false, true} {
		a.scan(automatic, 0)
		if st := a.GetState(); st.Language != "en" || st.Item == nil || st.Item.UID != "en-drive" || st.Error != "" || len(st.OCRLanguages) != 1 || st.OCRLanguages[0] != "en" {
			t.Fatalf("automatic=%v did not use English OCR/catalog: %+v", automatic, st)
		}
	}
	if len(f.languages) != 2 || f.languages[0] != "en" || f.languages[1] != "en" {
		t.Fatalf("OCR was called with a selectable language: %v", f.languages)
	}
	if st := a.SearchItem("LEDX Skin Transilluminator"); st.Item == nil || st.Item.UID != "en-ledx" || st.Error != "" {
		t.Fatalf("name search did not use the English catalog: %+v", st)
	}
}

func TestFailedInspectReadIsRetriedOnNextClick(t *testing.T) {
	f := &inspectScannerFixture{
		probe:  scanner.Result{Active: true, InspectOpen: true, Fingerprint: "same-pixels"},
		result: scanner.Result{Active: true, InspectOpen: true, Fingerprint: "same-pixels"},
	}
	a := inspectTestApp(f)
	a.apiKey = "local-fixture-placeholder"
	cacheDir := t.TempDir()
	data, err := json.Marshal(map[string]any{
		"version": 1, "mode": "pvp", "lang": "en", "updatedAt": time.Now(),
		"items": []market.Item{{UID: "drive", Name: "Secure Flash drive"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cacheDir, "catalog-pvp-en.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	a.client = market.NewClient(cacheDir)
	a.scan(true, 0)
	st := a.GetState()
	if st.ScanDetails == nil || st.ScanDetails.Outcome != "The Inspect window is open, but its title could not be read." {
		t.Fatalf("unreadable title was cached as a success: %+v", st)
	}
	f.result.Lines = []string{"Secure Flash drive"}
	a.scan(true, 0)
	if f.reads != 2 || a.GetState().Item == nil || a.GetState().Item.UID != "drive" {
		t.Fatal("unchanged pixels prevented retrying a failed OCR read")
	}
	a.scan(true, 0)
	if f.reads != 3 {
		t.Fatal("the next click did not read the recognized Inspect again")
	}
}

func TestScanDetailsArePrivateCopiesAndRespectOptionsRevision(t *testing.T) {
	a := inspectTestApp(&inspectScannerFixture{})
	result := scanner.Result{InspectOpen: true, InspectCount: 1, Groups: [][]string{{"Title one"}, {"Title one"}}}
	a.setScanDetails(result, a.state.ItemOptions, "Eredmény")
	st := a.GetState()
	if st.ScanDetails == nil || len(st.ScanDetails.Titles) != 1 {
		t.Fatal("duplicate OCR variants were not grouped")
	}
	st.ScanDetails.Titles[0] = "modified"
	if a.GetState().ScanDetails.Titles[0] != "Title one" {
		t.Fatal("state exposed its mutable title backing array")
	}
	old := a.state.ItemOptions
	a.state.ItemOptions.revision++
	a.setScanDetails(result, old, "obsolete")
	if a.GetState().ScanDetails.Outcome != "Eredmény" {
		t.Fatal("obsolete scan details changed current state")
	}
}

func TestInspectCardChangesWhenAnotherWindowOpensAndCloses(t *testing.T) {
	f := &inspectScannerFixture{probe: scanner.Result{Active: true, InspectOpen: true}}
	a := inspectTestApp(f)
	a.apiKey = "local-fixture-placeholder"
	cacheDir := t.TempDir()
	data, err := json.Marshal(map[string]any{
		"version": 1, "mode": "pvp", "lang": "en", "updatedAt": time.Now(),
		"items": []market.Item{
			{UID: "splint", Name: "Immobilizing splint"},
			{UID: "tourniquet", Name: "Esmarch tourniquet"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cacheDir, "catalog-pvp-en.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	a.client = market.NewClient(cacheDir)
	steps := []struct {
		titles [][]string
		want   string
	}{
		{[][]string{{"Immobilizing splint"}}, "splint"},
		{[][]string{{"Immobilizing splint"}, {"Esmarch tourniquet"}}, "tourniquet"},
		{[][]string{{"Immobilizing splint"}}, "splint"},
	}
	for i, step := range steps {
		f.result = scanner.Result{Active: true, InspectOpen: true, InspectCount: len(step.titles), TitleVariants: step.titles}
		a.scan(true, 0)
		st := a.GetState()
		if st.Item == nil || st.Item.UID != step.want || st.Busy || st.Error != "" {
			t.Fatalf("step %d did not replace the item card: %+v", i, st)
		}
		if st.ScanDetails == nil || st.ScanDetails.InspectCount != len(step.titles) || len(st.ScanDetails.Titles) != len(step.titles) {
			t.Fatalf("step %d retained obsolete inspection details: %+v", i, st.ScanDetails)
		}
	}
	if f.reads != len(steps) {
		t.Fatal("opening or closing an inspection suppressed the next click-triggered read")
	}
}
