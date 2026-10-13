package localprogress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/tarkovdev"
)

func storeSnapshot() Snapshot {
	return Snapshot{CharUID: "char-1", GameMode: "PVP", Revision: "private-market-revision", Items: []Item{{ItemID: "dev-only", Name: "Transient local item name", AvailableQuantity: 999, RequiredQuantity: 7, RemainingQuantity: 7, Sources: []Source{{TrackedSource: source("q1", 3, true), Origin: OriginDev}, {TrackedSource: source("q2", 4, false), Origin: OriginDev}}}}}
}

func rawStore(t *testing.T, store *Store) []byte {
	t.Helper()
	path, err := store.path("char-1", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertPureStockFile(t *testing.T, data []byte, forbidden ...string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 10 || fields["version"] == nil || fields["charUid"] == nil || fields["gameMode"] == nil || fields["stock"] == nil ||
		fields["regularStock"] == nil || fields["firStock"] == nil || fields["marketKinds"] == nil || fields["localUsed"] == nil || fields["handoverReceipts"] == nil || fields["transferReceipts"] == nil {
		t.Fatalf("file has more than local stock, quality annotations and identity: %s", data)
	}
	for _, text := range append([]string{"snapshot", "sources", "completed", "canceled", "remainingRequiredQuantity", "spentQuantity", "lastSpent", "revision", "foundInRaid", "firInventorySeparate", "marketRequiredQuantity", "marketAvailableQuantity", "localAvailableQuantity", "Transient local item name", "private-market-revision"}, forbidden...) {
		if bytes.Contains(data, []byte(text)) {
			t.Fatalf("personal Market data or requirements entered local stock file (%q): %s", text, data)
		}
	}
}

func TestOnlyOwnStockIsSavedAndSourcesStayTransient(t *testing.T) {
	store := NewStore(t.TempDir())
	incoming := storeSnapshot()
	remote := Item{ItemID: "market-remote-item", MarketTracked: true, Name: "REMOTE-NAME-SECRET", AvailableQuantity: 987654321, SpentQuantity: 456789123, RequiredQuantity: 123456789, RemainingQuantity: 123456788, Sources: []Source{{TrackedSource: market.TrackedSource{Type: "quest", QuestID: "REMOTE-QUEST-ID", Amount: 123456789, RemainingRequiredQuantity: 123456788, Completed: true, Canceled: true}, Origin: OriginMarket}}}
	incoming.Items = append(incoming.Items, remote)
	merged, err := store.Apply(incoming)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(merged.Items[1], remote) || merged.Revision != incoming.Revision {
		t.Fatalf("Market row/revision was altered: %+v", merged)
	}
	if merged.Items[0].AvailableQuantity != 0 || merged.Items[0].RemainingQuantity != 7 {
		t.Fatalf("incoming counter became own stock: %+v", merged.Items[0])
	}
	data := rawStore(t, store)
	assertPureStockFile(t, data, remote.ItemID, remote.Name, "REMOTE-QUEST-ID", "987654321", "456789123", "123456789", "tarkov-market")
	added, err := store.Add("char-1", "PVP", "dev-only")
	if err != nil {
		t.Fatal(err)
	}
	if added.Revision != "local" || len(added.Items) != 1 || added.Items[0].AvailableQuantity != 1 || added.Items[0].LocalAvailableQuantity != 1 || added.Items[0].Name != "" || added.Items[0].RequiredQuantity != 0 || len(added.Items[0].Sources) != 0 {
		t.Fatalf("Add returned more than local stock: %+v", added)
	}
	added.Items[0].AvailableQuantity = 999
	loaded, err := NewStore(store.dir).Load("char-1", "PVP")
	if err != nil || loaded.Items[0].AvailableQuantity != 1 {
		t.Fatalf("persisted stock changed through returned data: %+v %v", loaded, err)
	}
	merged, err = NewStore(store.dir).Apply(incoming)
	if err != nil || merged.Items[0].AvailableQuantity != 1 || !reflect.DeepEqual(merged.Items[1], remote) {
		t.Fatalf("refresh reset own stock or altered Market row: %+v %v", merged, err)
	}
	incoming.Items[0].Sources[0].Completed = true
	incoming.Items[0].Sources[0].RemainingRequiredQuantity = 0
	incoming.Items[0].SpentQuantity = 99999
	merged, err = store.Apply(incoming)
	if err != nil || merged.Items[0].AvailableQuantity != 1 {
		t.Fatalf("source flags/spent changed local-only stock: %+v %v", merged, err)
	}
	assertPureStockFile(t, rawStore(t, store), "99999", "REMOTE-NAME-SECRET")
	entries, err := os.ReadDir(store.dir)
	if err != nil || len(entries) != 1 || strings.Contains(entries[0].Name(), "char-1") || filepath.Ext(entries[0].Name()) != ".json" {
		t.Fatalf("unexpected private files: %v %v", entries, err)
	}
}

func TestRemoteRefreshNeverPersistsOrDebitsMarketStock(t *testing.T) {
	store := NewStore(t.TempDir())
	incoming := storeSnapshot()
	remote := Item{ItemID: "remote-empty-source", MarketTracked: true, AvailableQuantity: 5, RequiredQuantity: 10, RemainingQuantity: 5, Sources: []Source{}}
	incoming.Items = append(incoming.Items, remote)
	for _, stock := range []int64{5, 9, 1} {
		incoming.Items[1].AvailableQuantity = stock
		incoming.Items[1].SpentQuantity++
		for range 2 {
			merged, err := store.Apply(incoming)
			if err != nil || !reflect.DeepEqual(merged.Items[1], incoming.Items[1]) {
				t.Fatalf("remote stock was changed or debited: %+v %v", merged, err)
			}
			assertPureStockFile(t, rawStore(t, store), "remote-empty-source")
		}
	}
	if _, err := store.Add("char-1", "PVP", "remote-empty-source"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("remote row became local Add-eligible: %v", err)
	}
}

func TestLocalItemBecomingRemoteOnlyPreservesItsOwnStock(t *testing.T) {
	store := NewStore(t.TempDir())
	snapshot := storeSnapshot()
	if _, err := store.Apply(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("char-1", "PVP", "dev-only"); err != nil {
		t.Fatal(err)
	}
	snapshot.Items[0].MarketTracked = true
	snapshot.Items[0].Sources = []Source{{TrackedSource: source("q1", 7, false), Origin: OriginMarket}}
	snapshot.Items[0].AvailableQuantity = 8
	snapshot.Items[0].MarketAvailableQuantity = 8
	merged, err := store.Apply(snapshot)
	if err != nil || merged.Items[0].AvailableQuantity != 9 || merged.Items[0].LocalAvailableQuantity != 1 {
		t.Fatalf("Market authority was overwritten: %+v %v", merged, err)
	}
	assertPureStockFile(t, rawStore(t, store), "q1", "tarkov-market")
	local, err := store.Load("char-1", "PVP")
	if err != nil || len(local.Items) != 1 || local.Items[0].LocalAvailableQuantity != 1 || local.Items[0].MarketAvailableQuantity != 0 {
		t.Fatalf("own stock disappeared or Market quantity entered local progress: %+v %v", local, err)
	}
	if _, err := store.Add("char-1", "PVP", "dev-only"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("Market item was changed locally: %v", err)
	}
}

func TestMixedMarketAndLocalStockAreAddedWithoutDuplicatingRemotePortion(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "first-in-line"}, {QuestID: "pets-wont-need-it"}}
	tracked.Items = []market.TrackedItem{{ItemID: "augmentin", Name: "Augmentin antibiotic pills", RequiredQuantity: 3, RemainingQuantity: 3, Sources: []market.TrackedSource{source("first-in-line", 3, false)}}}
	dev := tarkovdev.Catalog{Tasks: []tarkovdev.Task{
		{ID: "first-in-line", Objectives: []tarkovdev.Objective{{ID: "give-three", Type: "giveItem", ItemIDs: []string{"augmentin"}, Count: 3}}},
		{ID: "pets-wont-need-it", Objectives: []tarkovdev.Objective{{ID: "give-five", Type: "giveItem", ItemIDs: []string{"augmentin"}, Count: 5}}},
	}}
	merged, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, merged, "augmentin")
	if item.RequiredQuantity != 8 || item.MarketRequiredQuantity != 3 || item.MarketAvailableQuantity != 0 || len(item.Sources) != 2 {
		t.Fatalf("raw Market quantities were overwritten or overlapping goal doubled: %+v", item)
	}
	store := NewStore(t.TempDir())
	if _, err := store.Apply(merged); err != nil {
		t.Fatal(err)
	}
	tracked.Items[0].AvailableQuantity = 3
	merged, err = Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	merged, err = store.Apply(merged)
	if err != nil || merged.Items[0].AvailableQuantity != 3 || merged.Items[0].MarketAvailableQuantity != 3 || merged.Items[0].LocalAvailableQuantity != 0 {
		t.Fatalf("initial remote portion was not preserved: %+v %v", merged, err)
	}
	added, err := store.Add("char-1", "PVP", "augmentin")
	if err != nil || added.Items[0].AvailableQuantity != 1 || added.Items[0].LocalAvailableQuantity != 1 || added.Items[0].MarketAvailableQuantity != 0 {
		t.Fatalf("mixed item Add did not produce local portion only: %+v %v", added, err)
	}
	for range 2 {
		merged, err = store.Apply(merged)
		if err != nil || merged.Items[0].AvailableQuantity != 4 || merged.Items[0].MarketAvailableQuantity != 3 || merged.Items[0].LocalAvailableQuantity != 1 || merged.Items[0].RequiredQuantity != 8 || merged.Items[0].RemainingQuantity != 4 {
			t.Fatalf("3 remote + 1 local was duplicated or lost: %+v %v", merged, err)
		}
	}
	tracked.Items[0].AvailableQuantity = 2
	merged, err = Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	merged, err = store.Apply(merged)
	if err != nil || merged.Items[0].AvailableQuantity != 3 || merged.Items[0].MarketAvailableQuantity != 2 || merged.Items[0].LocalAvailableQuantity != 1 {
		t.Fatalf("changed remote portion reset or duplicated local stock: %+v %v", merged, err)
	}
	assertPureStockFile(t, rawStore(t, store), "Augmentin antibiotic pills", "first-in-line", "pets-wont-need-it", "tarkov-market")
	path, _ := store.path("char-1", "PVP")
	state, err := readState(path, "char-1", "PVP")
	if err != nil || state.Stock["augmentin"] != 1 {
		t.Fatalf("disk contains combined stock rather than own portion: %+v %v", state, err)
	}
}

func TestOwnStockSurvivesDevDeduplicationCompletionAndDisappearance(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "first-in-line"}, {QuestID: "pets-wont-need-it"}}
	tracked.Items = []market.TrackedItem{{ItemID: "augmentin", Name: "Augmentin antibiotic pills", RequiredQuantity: 3, AvailableQuantity: 3, Sources: []market.TrackedSource{source("first-in-line", 3, false)}}}
	dev := tarkovdev.Catalog{Tasks: []tarkovdev.Task{{ID: "pets-wont-need-it", Objectives: []tarkovdev.Objective{{ID: "give-five", Type: "giveItem", ItemIDs: []string{"augmentin"}, Count: 5}}}}}
	store := NewStore(t.TempDir())
	merged, err := Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(merged); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if _, err := store.Add("char-1", "PVP", "augmentin"); err != nil {
			t.Fatal(err)
		}
	}
	tracked.Items[0].Sources = append(tracked.Items[0].Sources, source("pets-wont-need-it", 5, false))
	tracked.Items[0].RequiredQuantity = 8
	merged, err = Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	if hasDevSource(merged.Items[0]) {
		t.Fatal("Market accepting the second goal did not deduplicate dev source")
	}
	merged, err = store.Apply(merged)
	if err != nil || merged.Items[0].LocalAvailableQuantity != 4 || merged.Items[0].MarketAvailableQuantity != 3 || merged.Items[0].AvailableQuantity != 7 {
		t.Fatalf("dev deduplication deleted own stock: %+v %v", merged, err)
	}
	if _, err := store.Add("char-1", "PVP", "augmentin"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("item without active dev goal accepted a local Add: %v", err)
	}
	tracked.Items[0].Sources = tracked.Items[0].Sources[:1]
	tracked.Items[0].RequiredQuantity = 3
	tracked.Items[0].AvailableQuantity = 2
	merged, err = Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	merged, err = store.Apply(merged)
	if err != nil || !hasDevSource(merged.Items[0]) || merged.Items[0].LocalAvailableQuantity != 4 || merged.Items[0].AvailableQuantity != 6 {
		t.Fatalf("returning dev goal lost own stock: %+v %v", merged, err)
	}
	quests.Quests[1].Completed = true
	merged, err = Merge(tracked, quests, hideout, dev)
	if err != nil {
		t.Fatal(err)
	}
	merged, err = store.Apply(merged)
	if err != nil || merged.Items[0].LocalAvailableQuantity != 4 {
		t.Fatalf("completed goal deleted own stock: %+v %v", merged, err)
	}
	if _, err := store.Add("char-1", "PVP", "augmentin"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("completed dev goal accepted a local Add: %v", err)
	}
	merged.Items = []Item{}
	if _, err := store.Apply(merged); err != nil {
		t.Fatal(err)
	}
	local, err := NewStore(store.dir).Load("char-1", "PVP")
	if err != nil || len(local.Items) != 1 || local.Items[0].LocalAvailableQuantity != 4 || local.Items[0].MarketAvailableQuantity != 0 {
		t.Fatalf("item disappearing from current view erased stored own stock: %+v %v", local, err)
	}
	assertPureStockFile(t, rawStore(t, store), "Augmentin antibiotic pills", "first-in-line", "pets-wont-need-it", "tarkov-market")
	path, _ := store.path("char-1", "PVP")
	state, err := readState(path, "char-1", "PVP")
	if err != nil || state.Stock["augmentin"] != 4 {
		t.Fatalf("disk did not retain exactly own four: %+v %v", state, err)
	}
}

func TestRememberedOwnStockCombinesWithSourceLessMarketRow(t *testing.T) {
	store := NewStore(t.TempDir())
	snapshot := storeSnapshot()
	if _, err := store.Apply(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("char-1", "PVP", "dev-only"); err != nil {
		t.Fatal(err)
	}
	snapshot.Items = []Item{{ItemID: "dev-only", MarketTracked: true, MarketAvailableQuantity: 2, AvailableQuantity: 2, RequiredQuantity: 3, RemainingQuantity: 1, Sources: []Source{}}}
	merged, err := store.Apply(snapshot)
	if err != nil || merged.Items[0].AvailableQuantity != 3 || merged.Items[0].LocalAvailableQuantity != 1 || merged.Items[0].RemainingQuantity != 0 {
		t.Fatalf("source-less row miscounted remembered stock: %+v %v", merged, err)
	}
	if _, err := store.Add("char-1", "PVP", "dev-only"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("source-less goal accepted Add: %v", err)
	}
}

func TestCombinedStockOverflowDoesNotChangePureStockFile(t *testing.T) {
	store := NewStore(t.TempDir())
	snapshot := storeSnapshot()
	snapshot.Items[0].MarketTracked = true
	snapshot.Items[0].MarketAvailableQuantity = MaxQuantity
	if _, err := store.Apply(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("char-1", "PVP", "dev-only"); err != nil {
		t.Fatal(err)
	}
	before := rawStore(t, store)
	if _, err := store.Apply(snapshot); !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("combined quantity overflow accepted: %v", err)
	}
	if !bytes.Equal(before, rawStore(t, store)) {
		t.Fatal("failed combined stock calculation changed the file")
	}
}

func TestLegacyMigrationPreservesOnlyVerifiedLocalStock(t *testing.T) {
	store := NewStore(t.TempDir())
	snapshot := storeSnapshot()
	snapshot.Items[0].AvailableQuantity = 4
	remote := Item{ItemID: "legacy-market-row", Name: "LEGACY-MARKET-NAME", AvailableQuantity: 100, SpentQuantity: 75, Sources: []Source{{TrackedSource: source("legacy-private-quest", 3, true), Origin: OriginMarket}}}
	snapshot.Items = append(snapshot.Items, remote)
	legacy := legacyProgress{Version: 1, CharUID: "char-1", GameMode: "PVP", Stock: map[string]int64{"dev-only": 4, "legacy-market-row": 100}, LastSpent: map[string]int64{"dev-only": 0, "legacy-market-row": 75}, Snapshot: snapshot}
	path, _ := store.path("char-1", "PVP")
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("char-1", "PVP"); !errors.Is(err, ErrCorruptStore) {
		t.Fatalf("legacy cached progress was returned without fresh sync: %v", err)
	}
	if _, err := store.Add("char-1", "PVP", "dev-only"); !errors.Is(err, ErrCorruptStore) {
		t.Fatalf("legacy was written without fresh sync: %v", err)
	}
	fresh := storeSnapshot()
	fresh.Items = append(fresh.Items, Item{ItemID: "legacy-market-row", MarketTracked: true, AvailableQuantity: 2, Sources: []Source{}})
	merged, err := store.Apply(fresh)
	if err != nil || merged.Items[0].AvailableQuantity != 4 || merged.Items[1].AvailableQuantity != 2 {
		t.Fatalf("safe local stock migration failed: %+v %v", merged, err)
	}
	assertPureStockFile(t, rawStore(t, store), "LEGACY-MARKET-NAME", "legacy-market-row", "legacy-private-quest", "tarkov-market")
	state, err := readState(path, "char-1", "PVP")
	if err != nil || state.Version != storeVersion || len(state.Stock) != 1 || state.Stock["dev-only"] != 4 || len(state.RegularStock) != 0 || len(state.FIRStock) != 0 {
		t.Fatalf("migration retained personal Market state: %+v %v", state, err)
	}
}

func TestStoreSurvivesActualProcessRestart(t *testing.T) {
	if dir := os.Getenv("TARKOV_LOCAL_PROGRESS_RESTART_FIXTURE"); dir != "" {
		snapshot, err := NewStore(dir).Load("char-1", "PVP")
		if err != nil || len(snapshot.Items) != 1 || snapshot.Items[0].AvailableQuantity != 1 || snapshot.Revision != "local" || len(snapshot.Items[0].Sources) != 0 {
			t.Fatalf("new process could not read pure local stock: %+v %v", snapshot, err)
		}
		return
	}
	store := NewStore(t.TempDir())
	if _, err := store.Apply(storeSnapshot()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("char-1", "PVP", "dev-only"); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestStoreSurvivesActualProcessRestart$")
	child.Env = append(os.Environ(), "TARKOV_LOCAL_PROGRESS_RESTART_FIXTURE="+store.dir)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("restart fixture failed: %v\n%s", err, output)
	}
}

func TestCharacterAndModeStockIsIsolated(t *testing.T) {
	store := NewStore(t.TempDir())
	identities := [][2]string{{"char-1", "PVP"}, {"char-2", "PVP"}, {"char-1", "PVE"}, {"char-1", "SEASON"}}
	for _, id := range identities {
		snapshot := storeSnapshot()
		snapshot.CharUID, snapshot.GameMode = id[0], id[1]
		if _, err := store.Apply(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Add("char-1", "PVP", "dev-only"); err != nil {
		t.Fatal(err)
	}
	for _, id := range identities[1:] {
		snapshot, err := store.Load(id[0], id[1])
		if err != nil || snapshot.Items[0].AvailableQuantity != 0 {
			t.Fatalf("stock crossed identity: %+v %v", snapshot, err)
		}
	}
}

func TestAmbiguousLocalRowsNeverBecomeAddEligible(t *testing.T) {
	store := NewStore(t.TempDir())
	snapshot := storeSnapshot()
	snapshot.Items = append(snapshot.Items, snapshot.Items[0])
	if _, err := store.Apply(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("char-1", "PVP", "dev-only"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("unsafe item became Add-eligible: %v", err)
	}
	local, err := store.Load("char-1", "PVP")
	if err != nil || len(local.Items) != 0 {
		t.Fatalf("ambiguous stock was invented: %+v %v", local, err)
	}
}

func TestMarketSeparateFIRFlagDoesNotChangeOwnLocalStock(t *testing.T) {
	store := NewStore(t.TempDir())
	snapshot := storeSnapshot()
	remote := Item{ItemID: "separate-market-item", MarketTracked: true, AvailableQuantity: 9, Sources: []Source{}}
	snapshot.Items = append(snapshot.Items, remote)
	if _, err := store.Apply(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("char-1", "PVP", "dev-only"); err != nil {
		t.Fatal(err)
	}
	snapshot.FIRInventorySeparate = true
	refreshed, err := store.Apply(snapshot)
	if err != nil || refreshed.Items[0].AvailableQuantity != 1 || !reflect.DeepEqual(refreshed.Items[1], remote) {
		t.Fatalf("Market inventory mode deleted own stock or changed remote row: %+v %v", refreshed, err)
	}
	added, err := store.Add("char-1", "PVP", "dev-only")
	if err != nil || added.Items[0].AvailableQuantity != 2 || added.FIRInventorySeparate {
		t.Fatalf("Market inventory mode disabled own shared stock: %+v %v", added, err)
	}
	if _, err := store.Add("char-1", "PVP", remote.ItemID); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("remote item became locally Add-eligible: %v", err)
	}
	assertPureStockFile(t, rawStore(t, store), remote.ItemID)
}

func TestCorruptMismatchedOversizedStoresAreNeverReset(t *testing.T) {
	for _, mutation := range []string{"json", "character", "mode", "large", "negative", "version", "unknown", "missing-stock", "missing-regular", "missing-fir", "missing-market", "orphan-type", "negative-type", "local-types-exceed-stock", "bad-market-hash", "negative-market-type", "market-types-overflow"} {
		t.Run(mutation, func(t *testing.T) {
			store := NewStore(t.TempDir())
			snapshot := storeSnapshot()
			if _, err := store.Apply(snapshot); err != nil {
				t.Fatal(err)
			}
			path, _ := store.path("char-1", "PVP")
			state, err := readState(path, "char-1", "PVP")
			if err != nil {
				t.Fatal(err)
			}
			var data []byte
			switch mutation {
			case "json":
				data = []byte("{truncated")
			case "character":
				state.CharUID = "char-2"
			case "mode":
				state.GameMode = "PVE"
			case "large":
				data = bytes.Repeat([]byte(" "), int(maxStoreBytes+1))
			case "negative":
				state.Stock["dev-only"] = -1
			case "version":
				state.Version = 999
			case "unknown":
				data = []byte("{\"version\":2,\"apiKey\":\"fixture-secret\"}")
			case "missing-stock":
				state.Stock = nil
			case "missing-regular":
				state.RegularStock = nil
			case "missing-fir":
				state.FIRStock = nil
			case "missing-market":
				state.MarketKinds = nil
			case "orphan-type":
				state.FIRStock["not-in-local-stock"] = 1
			case "negative-type":
				state.RegularStock["dev-only"] = -1
			case "local-types-exceed-stock":
				state.Stock["dev-only"] = 1
				state.RegularStock["dev-only"] = 1
				state.FIRStock["dev-only"] = 1
			case "bad-market-hash":
				state.MarketKinds["remote-item"] = marketKindAnnotation{FIR: 1, StockHash: "not-a-stock-hash"}
			case "negative-market-type":
				state.MarketKinds["remote-item"] = marketKindAnnotation{Regular: -1, StockHash: marketStockHash("remote-item", 1)}
			case "market-types-overflow":
				state.MarketKinds["remote-item"] = marketKindAnnotation{Regular: MaxQuantity, FIR: 1, StockHash: marketStockHash("remote-item", MaxQuantity)}
			}
			if data == nil {
				data, err = json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Apply(snapshot); err == nil {
				t.Fatal("invalid file was silently replaced")
			}
			if _, err := store.Add("char-1", "PVP", "dev-only"); err == nil {
				t.Fatal("invalid file accepted Add")
			}
			if _, err := store.Load("char-1", "PVP"); err == nil {
				t.Fatal("invalid file accepted Load")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, data) {
				t.Fatal("broken file changed")
			}
		})
	}
}

func TestWriteFailureNeverAdvancesLocalStock(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Apply(storeSnapshot()); err != nil {
		t.Fatal(err)
	}
	// Az Apply hibáját ténylegesen szükséges minőségi jelölés-törlésen
	// vizsgáljuk; a változatlan, csak átmeneti nézethez nincs lemezírás.
	if _, err := store.RecordMarketAdd("char-1", "PVP", "dev-only", "regular", 0, 1); err != nil {
		t.Fatal(err)
	}
	before := rawStore(t, store)
	injected := errors.New("disk full fixture")
	store.writeFile = func(string, []byte) error { return injected }
	if result, err := store.Add("char-1", "PVP", "dev-only"); !errors.Is(err, injected) || result.Items != nil {
		t.Fatalf("failed Add was visible: %+v %v", result, err)
	}
	snapshot := storeSnapshot()
	snapshot.Items[0].MarketTracked = true
	if result, err := store.Apply(snapshot); !errors.Is(err, injected) || result.Items != nil {
		t.Fatalf("failed Apply was visible: %+v %v", result, err)
	}
	if !bytes.Equal(before, rawStore(t, store)) {
		t.Fatal("failed commit changed file")
	}
	store.writeFile = atomicWrite
	added, err := store.Add("char-1", "PVP", "dev-only")
	if err != nil || added.Items[0].AvailableQuantity != 1 {
		t.Fatalf("failed Add was repeated: %+v %v", added, err)
	}
}

func TestQuantityAndIdentityLimits(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, id := range []string{"../escape", "..\\escape", "C:\\escape", "char/1", "", "char\x00suffix"} {
		if _, err := store.Add(id, "PVP", "dev-only"); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("unsafe identity accepted: %q %v", id, err)
		}
	}
	if _, err := store.Apply(storeSnapshot()); err != nil {
		t.Fatal(err)
	}
	path, _ := store.path("char-1", "PVP")
	state := legacyStockProgress{Version: 2, CharUID: "char-1", GameMode: "PVP", Stock: map[string]int64{"dev-only": MaxQuantity}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("char-1", "PVP", "dev-only"); !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("quantity limit ignored: %v", err)
	}
	snapshot := storeSnapshot()
	snapshot.Items[0].Sources[0].Amount = MaxQuantity + 1
	if _, err := store.Apply(snapshot); !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("bad transient requirement accepted: %v", err)
	}
}

func TestTypedLocalStockRoundTripsWithoutInventingMarketStock(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Apply(storeSnapshot()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("char-1", "PVP", "dev-only"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddKind("char-1", "PVP", "dev-only", "fir"); err != nil {
		t.Fatal(err)
	}
	restarted := NewStore(store.dir)
	loaded, err := restarted.Load("char-1", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, loaded, "dev-only")
	if item.AvailableQuantity != 2 || item.LocalAvailableQuantity != 2 || item.LocalRegularQuantity != 1 || item.LocalFIRQuantity != 1 ||
		item.MarketAvailableQuantity != 0 || item.MarketRegularQuantity != 0 || item.MarketFIRQuantity != 0 {
		t.Fatalf("typed local stock was lost or counted remotely: %+v", item)
	}
	merged, err := restarted.Apply(storeSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	item = requireItem(t, merged, "dev-only")
	if item.AvailableQuantity != 2 || item.LocalRegularQuantity != 1 || item.LocalFIRQuantity != 1 {
		t.Fatalf("fresh requirements replaced own typed stock: %+v", item)
	}
	assertPureStockFile(t, rawStore(t, restarted))
}

func TestVersionTwoStockMigratesAsUnclassified(t *testing.T) {
	store := NewStore(t.TempDir())
	path, _ := store.path("char-1", "PVP")
	legacy := legacyStockProgress{Version: 2, CharUID: "char-1", GameMode: "PVP", Stock: map[string]int64{"dev-only": 4}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("char-1", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, loaded, "dev-only")
	if item.LocalAvailableQuantity != 4 || item.LocalRegularQuantity != 0 || item.LocalFIRQuantity != 0 {
		t.Fatalf("old untyped stock acquired an invented quality: %+v", item)
	}
	if _, err := store.Apply(storeSnapshot()); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.AddKind("char-1", "PVP", "dev-only", "fir")
	if err != nil {
		t.Fatal(err)
	}
	item = requireItem(t, loaded, "dev-only")
	if item.LocalAvailableQuantity != 5 || item.LocalFIRQuantity != 1 || item.LocalRegularQuantity != 0 {
		t.Fatalf("typed Add reclassified old stock: %+v", item)
	}
	state, err := readState(path, "char-1", "PVP")
	if err != nil || state.Version != storeVersion || state.Stock["dev-only"] != 5 || state.FIRStock["dev-only"] != 1 || len(state.RegularStock) != 0 {
		t.Fatalf("migration did not persist only the confirmed type: %+v %v", state, err)
	}
	assertPureStockFile(t, rawStore(t, store))
}

func marketStockFixture(itemID string, quantity int64) Snapshot {
	return Snapshot{CharUID: "char-1", GameMode: "PVP", Revision: "private-market-revision", Items: []Item{{
		ItemID: itemID, MarketTracked: true, MarketRequiredQuantity: 7, MarketAvailableQuantity: quantity,
		AvailableQuantity: quantity, RequiredQuantity: 7, RemainingQuantity: max(0, 7-quantity),
		Sources: []Source{{TrackedSource: source("market-private-quest", 7, true), Origin: OriginMarket}},
	}}}
}

func TestMarketQualityAnnotationsNeverDuplicateRemoteQuantity(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Apply(storeSnapshot()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddKind("char-1", "PVP", "dev-only", "fir"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordMarketAdd("char-1", "PVP", "dev-only", "regular", 40, 41); err != nil {
		t.Fatal(err)
	}
	saved, err := store.RecordMarketAdd("char-1", "PVP", "dev-only", "fir", 41, 42)
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, saved, "dev-only")
	if item.AvailableQuantity != 1 || item.LocalAvailableQuantity != 1 || item.LocalFIRQuantity != 1 || item.MarketAvailableQuantity != 0 ||
		item.MarketRegularQuantity != 1 || item.MarketFIRQuantity != 1 || item.RequiredQuantity != 0 || len(item.Sources) != 0 {
		t.Fatalf("annotation became duplicate Market stock: %+v", item)
	}
	incoming := marketStockFixture("dev-only", 42)
	incoming.Items[0].Sources = append(incoming.Items[0].Sources, Source{TrackedSource: source("dev-extra", 3, false), Origin: OriginDev})
	merged, err := NewStore(store.dir).Apply(incoming)
	if err != nil {
		t.Fatal(err)
	}
	item = requireItem(t, merged, "dev-only")
	if item.AvailableQuantity != 43 || item.MarketAvailableQuantity != 42 || item.LocalAvailableQuantity != 1 ||
		item.MarketRegularQuantity != 1 || item.MarketFIRQuantity != 1 || item.LocalFIRQuantity != 1 || item.LocalRegularQuantity != 0 {
		t.Fatalf("live merge lost types or added remote stock twice: %+v", item)
	}
	path, _ := store.path("char-1", "PVP")
	state, err := readState(path, "char-1", "PVP")
	if err != nil || state.Stock["dev-only"] != 1 || state.FIRStock["dev-only"] != 1 || len(state.MarketKinds) != 1 {
		t.Fatalf("disk duplicated remote quantity: %+v %v", state, err)
	}
	assertPureStockFile(t, rawStore(t, store), "market-private-quest", "dev-extra")
}

func TestExternalMarketQuantityChangeClearsBothKnownQualities(t *testing.T) {
	for _, live := range []int64{0, 9, 11} {
		t.Run(fmt.Sprintf("live-%d", live), func(t *testing.T) {
			store := NewStore(t.TempDir())
			if _, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "fir", 8, 9); err != nil {
				t.Fatal(err)
			}
			if _, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "regular", 9, 10); err != nil {
				t.Fatal(err)
			}
			unchanged, err := store.Apply(marketStockFixture("remote-item", 10))
			if err != nil || unchanged.Items[0].MarketRegularQuantity != 1 || unchanged.Items[0].MarketFIRQuantity != 1 || unchanged.Items[0].AvailableQuantity != 10 {
				t.Fatalf("matching live stock lost known types: %+v %v", unchanged, err)
			}
			changed, err := store.Apply(marketStockFixture("remote-item", live))
			if err != nil || changed.Items[0].MarketRegularQuantity != 0 || changed.Items[0].MarketFIRQuantity != 0 || changed.Items[0].AvailableQuantity != live {
				t.Fatalf("changed stock retained stale quality or changed remote count: %+v %v", changed, err)
			}
			loaded, err := NewStore(store.dir).Load("char-1", "PVP")
			if err != nil || len(loaded.Items) != 0 {
				t.Fatalf("stale Market quality remained on disk: %+v %v", loaded, err)
			}
		})
	}
}

func TestConfirmedMarketAddRebasesQualityWithoutReclassifyingUnknownStock(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "fir", 0, 1); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "regular", 5, 6)
	if err != nil {
		t.Fatal(err)
	}
	item := requireItem(t, loaded, "remote-item")
	if item.MarketRegularQuantity != 1 || item.MarketFIRQuantity != 0 || item.AvailableQuantity != 0 || item.LocalAvailableQuantity != 0 {
		t.Fatalf("old or unknown Market stock acquired a quality: %+v", item)
	}
	merged, err := store.Apply(marketStockFixture("remote-item", 6))
	if err != nil || merged.Items[0].AvailableQuantity != 6 || merged.Items[0].MarketRegularQuantity != 1 || merged.Items[0].MarketFIRQuantity != 0 {
		t.Fatalf("unknown remainder was not kept separate: %+v %v", merged, err)
	}
}

func TestMarketQualityRecordOnlyAcceptsBoundedSingleItemConfirmation(t *testing.T) {
	for _, test := range []struct {
		name, kind    string
		before, after int64
	}{
		{"no-kind", "", 0, 1}, {"unknown-kind", "FiR", 0, 1}, {"negative-before", "fir", -1, 0},
		{"no-increment", "fir", 1, 1}, {"two-items", "regular", 0, 2}, {"decrease", "regular", 2, 1},
		{"quantity-overflow", "fir", MaxQuantity, MaxQuantity + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			if _, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "regular", 0, 1); err != nil {
				t.Fatal(err)
			}
			before := rawStore(t, store)
			if result, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", test.kind, test.before, test.after); !errors.Is(err, ErrInvalidQuantity) || result.Items != nil {
				t.Fatalf("unconfirmed or invalid increment was annotated: %+v %v", result, err)
			}
			if !bytes.Equal(before, rawStore(t, store)) {
				t.Fatal("invalid confirmation changed file")
			}
		})
	}
	store := NewStore(t.TempDir())
	if _, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "fir", MaxQuantity-1, MaxQuantity); err != nil {
		t.Fatalf("upper valid confirmation boundary rejected: %v", err)
	}
	if _, err := store.RecordMarketAdd("../escape", "PVP", "remote-item", "fir", 0, 1); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("unsafe character annotation accepted: %v", err)
	}
	if _, err := store.RecordMarketAdd("char-1", "PVP", "../escape", "fir", 0, 1); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("unsafe item annotation accepted: %v", err)
	}
}

func TestTypedStockFailuresNeverAdvanceCountsOrAnnotations(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Apply(storeSnapshot()); err != nil {
		t.Fatal(err)
	}
	before := rawStore(t, store)
	injected := errors.New("typed stock disk full fixture")
	store.writeFile = func(string, []byte) error { return injected }
	if result, err := store.AddKind("char-1", "PVP", "dev-only", "fir"); !errors.Is(err, injected) || result.Items != nil {
		t.Fatalf("failed typed Add became visible: %+v %v", result, err)
	}
	if result, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "fir", 7, 8); !errors.Is(err, injected) || result.Items != nil {
		t.Fatalf("failed Market annotation became visible: %+v %v", result, err)
	}
	if !bytes.Equal(before, rawStore(t, store)) {
		t.Fatal("failed quality persistence changed the existing file")
	}
	store.writeFile = atomicWrite
	loaded, err := store.Load("char-1", "PVP")
	if err != nil || len(loaded.Items) != 1 || loaded.Items[0].AvailableQuantity != 0 || loaded.Items[0].LocalFIRQuantity != 0 {
		t.Fatalf("failed operation was retained in memory: %+v %v", loaded, err)
	}
}

func TestKnownMarketQualityCannotExceedMatchingLiveCount(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "fir", 0, 1); err != nil {
		t.Fatal(err)
	}
	path, _ := store.path("char-1", "PVP")
	state, err := readState(path, "char-1", "PVP")
	if err != nil {
		t.Fatal(err)
	}
	state.MarketKinds["remote-item"] = marketKindAnnotation{Regular: 1, FIR: 1, StockHash: marketStockHash("remote-item", 1)}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := store.Apply(marketStockFixture("remote-item", 1)); !errors.Is(err, ErrCorruptStore) || snapshot.Items != nil {
		t.Fatalf("two classified items fitted into one live item: %+v %v", snapshot, err)
	}
	if snapshot, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "regular", 1, 2); !errors.Is(err, ErrCorruptStore) || snapshot.Items != nil {
		t.Fatalf("invalid existing quality was accepted before recording an Add: %+v %v", snapshot, err)
	}
	if !bytes.Equal(data, rawStore(t, store)) {
		t.Fatal("invalid matching annotations were silently repaired or overwritten")
	}
}

func TestMarketAnnotationsAreIsolatedAndRequireUniqueLiveRows(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(fmt.Sprintf("ambiguous-%t", ambiguous), func(t *testing.T) {
			store := NewStore(t.TempDir())
			if _, err := store.RecordMarketAdd("char-1", "PVP", "remote-item", "fir", 0, 1); err != nil {
				t.Fatal(err)
			}
			for _, identity := range [][2]string{{"char-2", "PVP"}, {"char-1", "PVE"}, {"char-1", "SEASON"}} {
				other := marketStockFixture("remote-item", 1)
				other.CharUID, other.GameMode = identity[0], identity[1]
				merged, err := store.Apply(other)
				if err != nil || merged.Items[0].MarketFIRQuantity != 0 || merged.Items[0].AvailableQuantity != 1 {
					t.Fatalf("quality crossed character or mode: %+v %v", merged, err)
				}
			}
			incoming := marketStockFixture("remote-item", 1)
			if ambiguous {
				incoming.Items = append(incoming.Items, incoming.Items[0])
			} else {
				incoming.Items = []Item{}
			}
			if _, err := store.Apply(incoming); err != nil {
				t.Fatal(err)
			}
			loaded, err := NewStore(store.dir).Load("char-1", "PVP")
			if err != nil || len(loaded.Items) != 0 {
				t.Fatalf("missing or ambiguous live row retained a quality annotation: %+v %v", loaded, err)
			}
		})
	}
}
