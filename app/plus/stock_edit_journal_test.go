package plus

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
)

func stockEditorPendingFixture() stockEditorPending {
	return stockEditorPending{Version: 1, CharUID: "char-1", Mode: "PVP", ItemID: "item-1", Amounts: localprogress.StockAmounts{Regular: 2, FIR: 3, Unknown: 1}, Revision: "opaque-revision-for-intent"}
}

func TestStockEditorPendingAtomicRoundTripIsPrivateAndSurvivesRestart(t *testing.T) {
	app := &ItemWindowApp{dir: t.TempDir(), apiKey: "api-key-never-journaled"}
	fixture := stockEditorPendingFixture()
	opts := ItemOptions{CharacterUID: fixture.CharUID, Mode: "pvp"}
	if pending, err := app.readStockEditorPending(opts); err != nil || pending != nil {
		t.Fatalf("missing journal is not empty: %+v %v", pending, err)
	}
	if err := app.saveStockEditorPending(&fixture); err != nil {
		t.Fatal(err)
	}
	path := app.stockEditorPendingPath(fixture.CharUID, "pvp")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 6 {
		t.Fatalf("intent has extra fields: %s %v", data, err)
	}
	for _, name := range []string{"version", "charUid", "mode", "itemId", "amounts", "revision"} {
		if fields[name] == nil {
			t.Fatalf("intent is missing %s", name)
		}
	}
	for _, forbidden := range []string{app.apiKey, "beforeStock", "availableQuantity", "marketAvailableQuantity", "requiredQuantity", "spentQuantity", "sources", "snapshot", "name", "apiKey"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("journal contains forbidden data %q: %s", forbidden, data)
		}
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 || files[0].Name() != filepath.Base(path) || strings.Contains(files[0].Name(), fixture.CharUID) {
		t.Fatalf("atomic save left a temporary file or exposed identity: %v %v", files, err)
	}
	restarted := &ItemWindowApp{dir: app.dir}
	loaded, err := restarted.readStockEditorPending(opts)
	if err != nil || !reflect.DeepEqual(loaded, &fixture) {
		t.Fatalf("durable intent changed after restart: %+v %v", loaded, err)
	}
	loaded.Amounts.FIR = 99
	loaded, err = restarted.readStockEditorPending(opts)
	if err != nil || loaded.Amounts.FIR != 3 {
		t.Fatalf("returned pointer mutated durable intent: %+v %v", loaded, err)
	}
	if err := restarted.saveStockEditorPending(&fixture); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(data, after) {
		t.Fatal("same pending intent was rewritten differently")
	}
	if err := restarted.removeStockEditorPending(opts); err != nil {
		t.Fatal(err)
	}
	if pending, err := restarted.readStockEditorPending(opts); err != nil || pending != nil {
		t.Fatalf("completed/discarded intent remains: %+v %v", pending, err)
	}
	if err := restarted.removeStockEditorPending(opts); err != nil {
		t.Fatalf("repeated removal is not idempotent: %v", err)
	}
}

func TestStockEditorPendingKeepsUnresolvedIntentAndIsolatesCharacterModes(t *testing.T) {
	app := &ItemWindowApp{dir: t.TempDir()}
	first := stockEditorPendingFixture()
	fixtures := []stockEditorPending{first, first, first, first}
	fixtures[1].Mode = "PVE"
	fixtures[2].Mode = "SEASON"
	fixtures[3].CharUID = "char-2"
	for i := range fixtures {
		fixtures[i].Amounts.Unknown += int64(i)
		if err := app.saveStockEditorPending(&fixtures[i]); err != nil {
			t.Fatal(err)
		}
	}
	conflict := fixtures[0]
	conflict.Amounts.Regular++
	if err := app.saveStockEditorPending(&conflict); !errors.Is(err, errStockEditorPendingConflict) {
		t.Fatalf("unresolved intent was overwritten: %v", err)
	}
	if err := app.removeStockEditorPending(ItemOptions{CharacterUID: first.CharUID, Mode: "pvp"}); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures[1:] {
		loaded, err := app.readStockEditorPending(ItemOptions{CharacterUID: fixture.CharUID, Mode: strings.ToLower(fixture.Mode)})
		if err != nil || !reflect.DeepEqual(loaded, &fixture) {
			t.Fatalf("remove or conflict crossed character/mode: %+v %v", loaded, err)
		}
	}
	if app.stockEditorPendingPath("char-1", "pvp") != app.stockEditorPendingPath("char-1", "PVP") {
		t.Fatal("mode case changed the journal filename")
	}
	if path := app.stockEditorPendingPath("../../outside", "pvp"); filepath.Dir(path) != filepath.Join(app.dir, "stock-edits") {
		t.Fatalf("hashed filename permits path traversal: %s", path)
	}
}

func TestStockEditorPendingRejectsAndPreservesMalformedFiles(t *testing.T) {
	fixture := stockEditorPendingFixture()
	valid, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"invalid JSON", []byte(`{"version":`)},
		{"unknown field", []byte(strings.Replace(string(valid), `"version":1`, `"version":1,"apiKey":"must-never-appear-in-error"`, 1))},
		{"unknown nested field", []byte(strings.Replace(string(valid), `"regular":2`, `"regular":2,"beforeStock":10`, 1))},
		{"duplicate revision", []byte(strings.Replace(string(valid), `"revision":`, `"revision":"another-revision","revision":`, 1))},
		{"duplicate amounts", []byte(strings.Replace(string(valid), `"amounts":`, `"amounts":{"regular":0,"fir":0,"unknown":0},"amounts":`, 1))},
		{"duplicate typed field", []byte(strings.Replace(string(valid), `"regular":2`, `"regular":0,"regular":2`, 1))},
		{"trailing JSON", append(append([]byte{}, valid...), []byte(` {"version":1}`)...)},
		{"null amounts", []byte(strings.Replace(string(valid), `{"regular":2,"fir":3,"unknown":1}`, `null`, 1))},
		{"missing typed field", []byte(strings.Replace(string(valid), `"unknown":1`, `"unknownX":1`, 1))},
		{"missing amounts", []byte(strings.Replace(string(valid), `"amounts":{"regular":2,"fir":3,"unknown":1},`, ``, 1))},
		{"null type", []byte(strings.Replace(string(valid), `"regular":2`, `"regular":null`, 1))},
		{"wrong version", []byte(strings.Replace(string(valid), `"version":1`, `"version":2`, 1))},
		{"wrong character", []byte(strings.Replace(string(valid), `"char-1"`, `"char-2"`, 1))},
		{"wrong mode", []byte(strings.Replace(string(valid), `"PVP"`, `"PVE"`, 1))},
		{"uncanonical mode", []byte(strings.Replace(string(valid), `"PVP"`, `"pvp"`, 1))},
		{"invalid item", []byte(strings.Replace(string(valid), `"item-1"`, `"../escape"`, 1))},
		{"negative type", []byte(strings.Replace(string(valid), `"regular":2`, `"regular":-1`, 1))},
		{"fractional type", []byte(strings.Replace(string(valid), `"regular":2`, `"regular":1.5`, 1))},
		{"quantity overflow", []byte(strings.Replace(string(valid), `"regular":2`, `"regular":1000000000`, 1))},
		{"int64 overflow", []byte(strings.Replace(string(valid), `"regular":2`, `"regular":9223372036854775808`, 1))},
		{"empty revision", []byte(strings.Replace(string(valid), fixture.Revision, ``, 1))},
		{"too long revision", []byte(strings.Replace(string(valid), fixture.Revision, strings.Repeat("r", 257), 1))},
		{"revision newline", []byte(strings.Replace(string(valid), fixture.Revision, `line\nbreak`, 1))},
		{"oversized", bytes.Repeat([]byte(" "), maxStockEditorPendingBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := &ItemWindowApp{dir: t.TempDir()}
			path := app.stockEditorPendingPath(fixture.CharUID, fixture.Mode)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, test.data, 0600); err != nil {
				t.Fatal(err)
			}
			opts := ItemOptions{CharacterUID: fixture.CharUID, Mode: "pvp"}
			pending, err := app.readStockEditorPending(opts)
			if !errors.Is(err, errStockEditorPendingInvalid) || pending != nil || strings.Contains(err.Error(), "must-never-appear-in-error") {
				t.Fatalf("malformed journal accepted or body exposed: %+v %v", pending, err)
			}
			if err := app.saveStockEditorPending(&fixture); err == nil {
				t.Fatal("save silently replaced malformed pending intent")
			}
			unchanged, _ := os.ReadFile(path)
			if !bytes.Equal(test.data, unchanged) {
				t.Fatal("reader/save changed malformed evidence")
			}
			if err := app.removeStockEditorPending(opts); err != nil {
				t.Fatalf("explicit discard cannot remove malformed file: %v", err)
			}
		})
	}
}

func TestStockEditorPendingValidatesBeforeCreatingFileAndAllowsClearTarget(t *testing.T) {
	fixture := stockEditorPendingFixture()
	for _, mutate := range []func(*stockEditorPending){
		func(p *stockEditorPending) { p.Version = 0 },
		func(p *stockEditorPending) { p.CharUID = "../../escape" },
		func(p *stockEditorPending) { p.Mode = "unknown" },
		func(p *stockEditorPending) { p.ItemID = "" },
		func(p *stockEditorPending) { p.Amounts.FIR = -1 },
		func(p *stockEditorPending) { p.Amounts.Unknown = localprogress.MaxQuantity },
		func(p *stockEditorPending) { p.Revision = "" },
		func(p *stockEditorPending) { p.Revision = "invalid\"revision" },
	} {
		app := &ItemWindowApp{dir: t.TempDir()}
		pending := fixture
		mutate(&pending)
		if err := app.saveStockEditorPending(&pending); !errors.Is(err, errStockEditorPendingInvalid) {
			t.Fatalf("invalid intent accepted: %+v %v", pending, err)
		}
		if files, _ := os.ReadDir(app.dir); len(files) != 0 {
			t.Fatal("invalid save created journal files")
		}
	}
	app := &ItemWindowApp{dir: t.TempDir()}
	fixture.Amounts = localprogress.StockAmounts{}
	if err := app.saveStockEditorPending(&fixture); err != nil {
		t.Fatalf("zero clear target rejected: %v", err)
	}
	loaded, err := app.readStockEditorPending(ItemOptions{CharacterUID: fixture.CharUID, Mode: "pvp"})
	if err != nil || loaded.Amounts != (localprogress.StockAmounts{}) {
		t.Fatalf("zero target was not durable: %+v %v", loaded, err)
	}
	if _, err := app.readStockEditorPending(ItemOptions{CharacterUID: "../escape", Mode: "pvp"}); err == nil {
		t.Fatal("invalid read identity accepted")
	}
	if err := app.removeStockEditorPending(ItemOptions{CharacterUID: fixture.CharUID, Mode: "unknown"}); err == nil {
		t.Fatal("invalid remove mode accepted")
	}
}

func TestStockEditorPendingSaveFailureCannotPublishNewIntent(t *testing.T) {
	app := &ItemWindowApp{dir: t.TempDir()}
	blocked := filepath.Join(app.dir, "stock-edits")
	if err := os.WriteFile(blocked, []byte("existing ordinary file"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := stockEditorPendingFixture()
	if err := app.saveStockEditorPending(&fixture); err == nil {
		t.Fatal("save succeeded despite unusable journal directory")
	}
	data, err := os.ReadFile(blocked)
	if err != nil || string(data) != "existing ordinary file" {
		t.Fatalf("failed save changed existing data: %q %v", data, err)
	}
}

func TestStockEditorPendingV2ImmutableBaselineAndDurablePhaseTransitions(t *testing.T) {
	app := &ItemWindowApp{dir: t.TempDir()}
	fixture := stockEditorPendingFixture()
	baseline := localprogress.StockAmounts{Regular: 2, Unknown: 1}
	fixture.Version, fixture.Stage, fixture.Baseline = 2, "unsent", &baseline
	if err := app.saveStockEditorPending(&fixture); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"sent", "unsent", "conflict"} {
		fixture.Stage = stage
		if err := app.saveStockEditorPending(&fixture); err != nil {
			t.Fatal("valid phase transition failed", err)
		}
		loaded, err := app.readStockEditorPending(ItemOptions{CharacterUID: fixture.CharUID, Mode: fixture.Mode})
		if err != nil || !sameStockEditorPending(loaded, &fixture) {
			t.Fatal("phase target or baseline changed across restart", err)
		}
	}
	path := app.stockEditorPendingPath(fixture.CharUID, fixture.Mode)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*stockEditorPending){
		func(p *stockEditorPending) { p.Stage = "sent" },
		func(p *stockEditorPending) { p.Amounts.Regular++ },
		func(p *stockEditorPending) { q := *p.Baseline; q.Unknown++; p.Baseline = &q },
		func(p *stockEditorPending) { p.Revision = "another-baseline" },
	} {
		changed := fixture
		mutate(&changed)
		if err := app.saveStockEditorPending(&changed); err == nil {
			t.Fatal("immutable conflict plan was replaced")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("rejected transition changed the saved intent")
		}
	}
}
