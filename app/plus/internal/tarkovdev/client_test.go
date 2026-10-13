package tarkovdev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const tasksFixture = `{"data":{"tasks":{"task-a":{"id":"task-a","name":"task-a name","normalizedName":"task-name","objectives":[
{"id":"find-a","type":"findItem","count":3,"foundInRaid":true,"optional":false,"items":["salewa"]},
{"id":"give-a","type":"giveItem","count":3,"foundInRaid":true,"optional":false,"items":["salewa","afak","salewa"]},
{"id":"mark-a","type":"mark","count":1,"markerItem":"marker","optional":false},
{"id":"use-a","type":"useItem","count":1,"useAny":["stim-a","stim-b"],"optional":true},
{"id":"build-a","type":"buildWeapon","count":1,"item":"weapon","optional":false},
{"id":"visit-a","type":"visit","optional":false}
]}}}}`

const hideoutFixture = `{"data":{"station-a":{"id":"station-a","name":"station_name","normalizedName":"medstation","levels":[
{"level":2,"itemRequirements":[{"id":"requirement-2","item":"salewa","count":4,"attributes":{"foundInRaid":true}}]},
{"level":1,"itemRequirements":[{"id":"requirement-1","item":"wire","count":6,"attributes":{"foundInRaid":false}}]}
]}}}`

func fixtureHandler(t *testing.T, paths *[]string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" || r.URL.RawQuery != "" {
			t.Errorf("A publikus kérés váratlan tartalma: %s %s", r.Method, r.URL)
		}
		if paths != nil {
			*paths = append(*paths, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/endpoints":
			fmt.Fprint(w, `{"data":{"gameModes":["regular","pve","pvp-season"]}}`)
		case strings.HasSuffix(r.URL.Path, "/tasks_en"):
			fmt.Fprint(w, `{"data":{"task-a name":"A test task"}}`)
		case strings.HasSuffix(r.URL.Path, "/hideout_en"):
			fmt.Fprint(w, `{"data":{"station_name":"Medstation"}}`)
		case strings.HasSuffix(r.URL.Path, "/tasks"):
			fmt.Fprint(w, tasksFixture)
		case strings.HasSuffix(r.URL.Path, "/hideout"):
			fmt.Fprint(w, hideoutFixture)
		default:
			http.NotFound(w, r)
		}
	}
}

func testClient(server *httptest.Server, cacheDir string) *Client {
	client := NewClient(cacheDir)
	client.baseURL = server.URL
	return client
}

func TestLoadPreservesAlternativesTypesAndFIR(t *testing.T) {
	server := httptest.NewServer(fixtureHandler(t, nil))
	defer server.Close()
	client := testClient(server, t.TempDir())
	catalog, err := client.Load(context.Background(), "PVP")
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Stale || catalog.UpdatedAt.IsZero() || len(catalog.Tasks) != 1 || len(catalog.Hideout) != 1 {
		t.Fatalf("Hibás teljes lista: %+v", catalog)
	}
	task := catalog.Tasks[0]
	if task.Name != "A test task" || len(task.Objectives) != 6 {
		t.Fatalf("A név vagy a típusok elvesztek: %+v", task)
	}
	find, give := task.Objectives[0], task.Objectives[1]
	if find.Type != "findItem" || give.Type != "giveItem" || give.Count != 3 || !give.FoundInRaid || len(give.ItemIDs) != 2 {
		t.Fatalf("Az objektív csoport megváltozott: %+v", give)
	}
	if task.Objectives[2].ItemIDs[0] != "marker" || task.Objectives[3].ItemIDs[1] != "stim-b" || !task.Objectives[3].Optional || task.Objectives[4].ItemIDs[0] != "weapon" {
		t.Fatalf("A speciális tárgymezők elvesztek: %+v", task.Objectives)
	}
	station := catalog.Hideout[0]
	if station.Name != "Medstation" || station.NormalizedName != "medstation" || station.Levels[0].Level != 1 || station.Levels[1].Level != 2 {
		t.Fatalf("Hibás hideout megfeleltetés: %+v", station)
	}
	requirement := station.Levels[1].Requirements[0]
	if requirement.ID != "requirement-2" || requirement.ItemID != "salewa" || requirement.Count != 4 || !requirement.FoundInRaid {
		t.Fatalf("Hibás hideout FiR: %+v", requirement)
	}
}

func TestLoadModesUseSeparatePathsAndCaches(t *testing.T) {
	var paths []string
	server := httptest.NewServer(fixtureHandler(t, &paths))
	defer server.Close()
	client := testClient(server, t.TempDir())
	for _, mode := range []string{"PVP", "PVE", "SEASON"} {
		if _, err := client.Load(context.Background(), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"regular", "pve", "pvp-season"} {
		if _, err := os.Stat(client.cachePath(mode)); err != nil {
			t.Fatalf("A %s cache hiányzik: %v", mode, err)
		}
		found := false
		for _, path := range paths {
			found = found || path == "/"+mode+"/tasks"
		}
		if !found {
			t.Fatalf("A %s végpont nem lett lekérve: %v", mode, paths)
		}
	}
	if _, err := client.Load(context.Background(), "unknown"); !errors.Is(err, ErrUnsupportedMode) {
		t.Fatalf("Ismeretlen mód elfogadva: %v", err)
	}
}

func TestSeasonNeedsExplicitSupportAndNeverFallsBack(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.URL.Path != "/endpoints" {
			t.Errorf("Nem támogatott szezon után új kérés: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"data":{"gameModes":["regular","pve"]}}`)
	}))
	defer server.Close()
	catalog, err := testClient(server, t.TempDir()).Load(context.Background(), "SEASON")
	if !errors.Is(err, ErrUnsupportedMode) || len(catalog.Tasks) != 0 || count.Load() != 1 {
		t.Fatalf("A nem támogatott szezon viselkedése hibás: %+v %v", catalog, err)
	}
}

func TestMemoryAndDiskCacheAreIndependentOfReturnedSlices(t *testing.T) {
	var count atomic.Int32
	fixture := fixtureHandler(t, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		fixture(w, r)
	}))
	defer server.Close()
	client := testClient(server, t.TempDir())
	catalog, err := client.Load(context.Background(), "PVP")
	if err != nil {
		t.Fatal(err)
	}
	catalog.Tasks[0].Objectives[1].ItemIDs[0] = "mutated"
	catalog.Hideout[0].Levels[1].Requirements[0].Count = 999
	for _, cachedClient := range []*Client{client, testClient(server, client.cacheDir)} {
		cached, err := cachedClient.Load(context.Background(), "PVP")
		if err != nil || cached.Tasks[0].Objectives[1].ItemIDs[0] != "salewa" || cached.Hideout[0].Levels[1].Requirements[0].Count != 4 {
			t.Fatalf("A hívó felülírta a cache-t: %+v %v", cached, err)
		}
	}
	if count.Load() != 4 {
		t.Fatalf("A friss cache új hálózati kérést indított: %d", count.Load())
	}
}

func TestNetworkFailureReturnsStaleCatalogAndError(t *testing.T) {
	var fail atomic.Bool
	fixture := fixtureHandler(t, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() && strings.HasSuffix(r.URL.Path, "/hideout") {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		fixture(w, r)
	}))
	defer server.Close()
	client := testClient(server, t.TempDir())
	initial, err := client.Load(context.Background(), "PVP")
	if err != nil {
		t.Fatal(err)
	}
	initialBytes, err := os.ReadFile(client.cachePath("regular"))
	if err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	client.now = func() time.Time { return initial.UpdatedAt.Add(2 * time.Hour) }
	stale, err := client.Load(context.Background(), "PVP")
	if err == nil || !stale.Stale || len(stale.Tasks) != 1 || len(stale.Hideout) != 1 || !stale.UpdatedAt.Equal(initial.UpdatedAt) {
		t.Fatalf("A frissítési hiba elveszett: %+v %v", stale, err)
	}
	currentBytes, err := os.ReadFile(client.cachePath("regular"))
	if err != nil || string(currentBytes) != string(initialBytes) {
		t.Fatal("A részleges válasz felülírta a cache-t")
	}
	client = testClient(server, client.cacheDir)
	client.now = func() time.Time { return initial.UpdatedAt.Add(2 * time.Hour) }
	stale, err = client.Load(context.Background(), "PVP")
	if err == nil || !stale.Stale || len(stale.Tasks) != 1 {
		t.Fatalf("Lemezcache-re nincs hiba melletti visszaesés: %+v %v", stale, err)
	}
}

func TestInvalidCachesCannotBecomeSuccessfulEmptyCatalog(t *testing.T) {
	for _, content := range []string{"{broken", `{"version":1,"mode":"regular","catalog":{"updatedAt":"2026-10-11T00:00:00Z"}}`} {
		t.Run(content[:7], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "unavailable", http.StatusBadGateway)
			}))
			defer server.Close()
			client := testClient(server, t.TempDir())
			if err := os.WriteFile(client.cachePath("regular"), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			catalog, err := client.Load(context.Background(), "PVP")
			if err == nil || !catalog.Stale || len(catalog.Tasks) > 0 {
				t.Fatalf("A hibás cache sikernek számított: %+v %v", catalog, err)
			}
		})
	}
}

func TestInvalidResponsesAreRejected(t *testing.T) {
	for name, body := range map[string]string{
		"empty":           `{"data":{"tasks":{}}}`,
		"bad JSON":        `{"data":`,
		"wrong task ID":   strings.Replace(tasksFixture, `"id":"task-a"`, `"id":"other-task"`, 1),
		"negative amount": strings.Replace(tasksFixture, `"count":3`, `"count":-3`, 1),
		"fraction amount": strings.Replace(tasksFixture, `"count":3`, `"count":1.5`, 1),
		"zero item count": strings.Replace(tasksFixture, `"count":3`, `"count":0`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			fixture := fixtureHandler(t, nil)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/tasks") {
					fmt.Fprint(w, body)
					return
				}
				fixture(w, r)
			}))
			defer server.Close()
			catalog, err := testClient(server, t.TempDir()).Load(context.Background(), "PVP")
			if !errors.Is(err, ErrInvalidCatalog) || len(catalog.Tasks) != 0 {
				t.Fatalf("Hibás válasz elfogadva: %+v %v", catalog, err)
			}
		})
	}
}

func TestResponseSizeRedirectAndCancellation(t *testing.T) {
	t.Run("size", func(t *testing.T) {
		server := httptest.NewServer(fixtureHandler(t, nil))
		defer server.Close()
		client := testClient(server, t.TempDir())
		client.maxBytes = 16
		if _, err := client.Load(context.Background(), "PVP"); !errors.Is(err, ErrResponseTooBig) {
			t.Fatalf("A túl nagy válasz elfogadva: %v", err)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		var targetCalls atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			targetCalls.Add(1)
		}))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusFound)
		}))
		defer server.Close()
		if _, err := testClient(server, t.TempDir()).Load(context.Background(), "PVP"); err == nil || targetCalls.Load() != 0 {
			t.Fatalf("Az átirányítás követve lett: %v, %d", err, targetCalls.Load())
		}
	})
	t.Run("cancel", func(t *testing.T) {
		server := httptest.NewServer(fixtureHandler(t, nil))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := testClient(server, t.TempDir()).Load(ctx, "PVP"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Megszakítás elveszett: %v", err)
		}
	})
}

func TestCacheReplaceIsComplete(t *testing.T) {
	server := httptest.NewServer(fixtureHandler(t, nil))
	defer server.Close()
	client := testClient(server, t.TempDir())
	initial, err := client.Load(context.Background(), "PVP")
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return initial.UpdatedAt.Add(2 * time.Hour) }
	refreshed, err := client.Load(context.Background(), "PVP")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(client.cachePath("regular"))
	if err != nil {
		t.Fatal(err)
	}
	var cache diskCatalog
	if json.Unmarshal(body, &cache) != nil || !cache.Catalog.UpdatedAt.Equal(refreshed.UpdatedAt) || !validCatalog(cache.Catalog) {
		t.Fatal("Az új cache hiányos")
	}
	files, err := filepath.Glob(filepath.Join(client.cacheDir, ".tarkovdev-requirements-*.tmp"))
	if err != nil || len(files) != 0 {
		t.Fatalf("Ideiglenes fájl maradt: %v %v", files, err)
	}
}

// Külön bekapcsolható, kizárólag nyilvános adatokkal végzett élő szerződéspróba.
func TestLivePublicCatalog(t *testing.T) {
	if os.Getenv("TARKOVDEV_LIVE") != "1" {
		t.Skip("TARKOVDEV_LIVE=1 szükséges az élő, publikus lekéréshez")
	}
	client := NewClient(t.TempDir())
	for _, mode := range []string{"PVP", "PVE", "SEASON"} {
		catalog, err := client.Load(context.Background(), mode)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if !validCatalog(catalog) || catalog.Stale {
			t.Fatalf("%s: érvénytelen élő katalógus", mode)
		}
		t.Logf("%s: %d küldetés, %d hideout állomás", mode, len(catalog.Tasks), len(catalog.Hideout))
	}
}
