package plus

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func completionQualityFixture(t *testing.T) (*ItemWindowApp, localprogress.Snapshot) {
	t.Helper()
	a := &ItemWindowApp{dir: t.TempDir(), apiKey: "offline-completion-test", state: ItemState{
		ItemOptions: ItemOptions{CharacterUID: "completion-character", Mode: "PVP"}}}
	source := func(id string) localprogress.Source {
		return localprogress.Source{Origin: localprogress.OriginMarket, TrackedSource: market.TrackedSource{
			Type: "quest", QuestID: id, Amount: 2, RemainingRequiredQuantity: 2}}
	}
	snapshot := localprogress.Snapshot{CharUID: a.state.CharacterUID, GameMode: a.state.Mode, Revision: "completion-before", MetadataComplete: true,
		MarketQuests:  []market.Quest{{QuestID: "goal", QuestIDAliases: []string{"goal-alias"}, Status: "available"}, {QuestID: "unrelated", Status: "available"}},
		MarketHideout: []market.HideoutUpgrade{}, Items: []localprogress.Item{
			{ItemID: "food", Name: "Food", MarketTracked: true, AvailableQuantity: 5, MarketAvailableQuantity: 5, Sources: []localprogress.Source{source("goal-alias")}},
			{ItemID: "other", Name: "Other", MarketTracked: true, AvailableQuantity: 5, MarketAvailableQuantity: 5, Sources: []localprogress.Source{source("unrelated")}},
		}}
	setCompletionQuality(t, a, snapshot, 2, 2)
	return a, snapshot
}

func setCompletionQuality(t *testing.T, a *ItemWindowApp, snapshot localprogress.Snapshot, fir, regular int64) {
	t.Helper()
	store, err := a.qualityStoreLocked()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.update(func(data *qualityData) error {
		rows := qualityRows(data, qualityProfile(a.apiKey, a.state.ItemOptions))
		for _, row := range snapshot.Items {
			rows[qualityRowKey(row)] = qualityRecord{Total: row.MarketAvailableQuantity, FIR: fir, Regular: regular}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func reconcileCompletionForTest(t *testing.T, a *ItemWindowApp, snapshot localprogress.Snapshot, fresh bool) localprogress.Snapshot {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	var out localprogress.Snapshot
	var err error
	if fresh {
		out, err = a.qualityReconcileFreshSnapshotLocked(a.apiKey, a.state.ItemOptions, snapshot)
	} else {
		out, err = a.qualityReconcileSnapshotLocked(a.apiKey, a.state.ItemOptions, snapshot)
	}
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertCompletionQuality(t *testing.T, snapshot localprogress.Snapshot, index int, fir, regular int64) {
	t.Helper()
	row := snapshot.Items[index]
	if row.MarketAvailableQuantity != 5 || row.AvailableQuantity != 5 || !row.QualityKnown || row.FIRQuantity != fir || row.RegularQuantity != regular {
		t.Fatalf("completion changed total stock or the wrong quality: %+v", row)
	}
}

func TestQualityCompletionTransitionsResetOnceAndAllowAnotherFalseTrueCycle(t *testing.T) {
	a, before := completionQualityFixture(t)
	assertCompletionQuality(t, reconcileCompletionForTest(t, a, before, true), 0, 2, 2)
	after := localprogress.Clone(before)
	after.Revision, after.MarketQuests[0].Status = "completion-after", "completed"
	result := reconcileCompletionForTest(t, a, after, true)
	assertCompletionQuality(t, result, 0, 0, 0)
	assertCompletionQuality(t, result, 1, 2, 2)
	setCompletionQuality(t, a, after, 1, 3)
	after.Revision = "completion-still-done"
	assertCompletionQuality(t, reconcileCompletionForTest(t, a, after, true), 0, 1, 3)
	again := localprogress.Clone(before)
	again.Revision = "completion-again-false"
	assertCompletionQuality(t, reconcileCompletionForTest(t, a, again, true), 0, 1, 3)
	after.Revision = "completion-second-cycle"
	assertCompletionQuality(t, reconcileCompletionForTest(t, a, after, true), 0, 0, 0)
}

func TestQualityCompletionBaselineAndIncompleteOrCanceledGoalsDoNotReset(t *testing.T) {
	for _, name := range []string{"first completed baseline", "missing metadata", "missing goal", "canceled", "source completed only", "full shared counter only"} {
		t.Run(name, func(t *testing.T) {
			a, before := completionQualityFixture(t)
			if name != "first completed baseline" {
				reconcileCompletionForTest(t, a, before, true)
			}
			after := localprogress.Clone(before)
			after.Revision = "not-a-completion"
			switch name {
			case "first completed baseline":
				after.MarketQuests[0].Completed = true
			case "missing metadata":
				after.MetadataComplete = false
				after.MarketQuests[0].Completed = true
			case "missing goal":
				after.MarketQuests = nil
			case "canceled":
				after.MarketQuests[0].Status = "canceled"
			case "source completed only":
				after.Items[0].Sources[0].Completed = true
			case "full shared counter only":
				after.MarketGroups = []market.TrackedGroup{{QuestID: "goal", RequirementID: "any", Amount: 5, RequiredQuantity: 5, AvailableQuantity: 5, ItemIDs: []string{"food"}}}
			}
			assertCompletionQuality(t, reconcileCompletionForTest(t, a, after, true), 0, 2, 2)
		})
	}
}

func TestQualityCompletionRestartAndRemovedSourcesKeepTheAffectedIdentity(t *testing.T) {
	a, before := completionQualityFixture(t)
	reconcileCompletionForTest(t, a, before, true)
	a.qualities = nil
	after := localprogress.Clone(before)
	after.Revision, after.MarketQuests[0].Completed = "restart-completed", true
	after.Items[0].Sources = nil
	result := reconcileCompletionForTest(t, a, after, true)
	assertCompletionQuality(t, result, 0, 0, 0)
	assertCompletionQuality(t, result, 1, 2, 2)
	old := a.qualities.data.Profiles[qualityProfile(a.apiKey, a.state.ItemOptions)][qualityRowKey(before.Items[0])]
	if old.FIR != 0 || old.Regular != 0 {
		t.Fatalf("removed source retained restorable old quality: %+v", old)
	}
}

func TestQualityCompletionAliasAndHideoutLevelIdentities(t *testing.T) {
	for _, trigger := range []string{"canonical alias", "hideout completed", "hideout prebuilt"} {
		t.Run(trigger, func(t *testing.T) {
			a, before := completionQualityFixture(t)
			if trigger != "canonical alias" {
				before.Items[0].Sources[0].QuestID, before.Items[0].Sources[0].UpgradeKey = "", "Workbench_2"
				before.Items[0].Sources[0].Type = "hideout"
				before.MarketHideout = []market.HideoutUpgrade{{UpgradeKey: "Workbench_1", Level: 1, Completed: true}, {UpgradeKey: "Workbench_2", Level: 2}}
				setCompletionQuality(t, a, before, 2, 2)
			}
			reconcileCompletionForTest(t, a, before, true)
			after := localprogress.Clone(before)
			after.Revision = "identity-complete"
			if trigger == "canonical alias" {
				after.MarketQuests[0].QuestID, after.MarketQuests[0].QuestIDAliases = "new-canonical", []string{"goal", "goal-alias"}
				after.MarketQuests[0].Status = "completed"
			} else if trigger == "hideout completed" {
				after.MarketHideout[1].Completed = true
			} else {
				after.MarketHideout[1].Prebuilt = true
			}
			assertCompletionQuality(t, reconcileCompletionForTest(t, a, after, true), 0, 0, 0)
		})
	}
}

func TestQualityCompletionSharedResetsEveryCandidateRegardlessOfQualityOrContribution(t *testing.T) {
	for _, fir := range []bool{false, true} {
		a, before := completionQualityFixture(t)
		before.Items = before.Items[:1]
		before.Items[0].Sources = nil
		ids := []string{"food", "second", "third", "fourth"}
		for _, id := range ids[1:] {
			row := before.Items[0]
			row.ItemID, row.Name = id, id
			before.Items = append(before.Items, row)
		}
		before.MarketGroups = []market.TrackedGroup{{QuestID: "goal-alias", RequirementID: "shared", FIR: fir, Amount: 15, RequiredQuantity: 15, AvailableQuantity: 0, ItemIDs: ids}}
		setCompletionQuality(t, a, before, 2, 2)
		reconcileCompletionForTest(t, a, before, true)
		after := localprogress.Clone(before)
		after.Revision, after.MarketQuests[0].Completed, after.MarketGroups = "shared-complete", true, nil
		result := reconcileCompletionForTest(t, a, after, true)
		for i := range result.Items {
			assertCompletionQuality(t, result, i, 0, 0)
		}
	}
}

func TestQualityCompletionCachedReconciliationDoesNotInventCycles(t *testing.T) {
	a, before := completionQualityFixture(t)
	reconcileCompletionForTest(t, a, before, true)
	after := localprogress.Clone(before)
	after.Revision, after.MarketQuests[0].Completed = "completed-new-revision", true
	reconcileCompletionForTest(t, a, after, true)
	setCompletionQuality(t, a, before, 2, 1)
	// Szerkesztő/export/import nem léptethet vissza egy régi API-pillanatképre.
	old := localprogress.Clone(before)
	old.Revision = "editor-old-metadata"
	reconcileCompletionForTest(t, a, old, false)
	reconcileCompletionForTest(t, a, before, false)
	after.Revision = "completed-still-current"
	assertCompletionQuality(t, reconcileCompletionForTest(t, a, after, true), 0, 2, 1)
}

func TestQualityCompletionFreshObservationsCanReuseOpaqueRevisionValues(t *testing.T) {
	a, pending := completionQualityFixture(t)
	pending.Revision = "opaque-A"
	reconcileCompletionForTest(t, a, pending, true)
	done := localprogress.Clone(pending)
	done.Revision, done.MarketQuests[0].Completed = "opaque-B", true
	assertCompletionQuality(t, reconcileCompletionForTest(t, a, done, true), 0, 0, 0)
	setCompletionQuality(t, a, done, 2, 1)
	// Egy valódi friss API-olvasás visszaállított célt mutathat ugyanazzal
	// az ETag-gel, majd az új teljesítés is visszakaphatja a korábbi ETag-et.
	assertCompletionQuality(t, reconcileCompletionForTest(t, a, pending, true), 0, 2, 1)
	result := reconcileCompletionForTest(t, a, done, true)
	assertCompletionQuality(t, result, 0, 0, 0)
	assertCompletionQuality(t, result, 1, 2, 1)
	if len(a.qualities.data.Completions[qualityProfile(a.apiKey, a.state.ItemOptions)].Revisions) != 0 {
		t.Fatal("fresh completion state retained an obsolete revision deduplication list")
	}
}

func TestQualityCompletionLegacySeenRevisionsCannotSuppressFreshCompletion(t *testing.T) {
	a, pending := completionQualityFixture(t)
	reconcileCompletionForTest(t, a, pending, true)
	profile := qualityProfile(a.apiKey, a.state.ItemOptions)
	if err := a.qualities.update(func(data *qualityData) error {
		history := data.Completions[profile]
		history.Revisions = []string{pending.Revision, "legacy-previously-seen"}
		data.Completions[profile] = history
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a.qualities = nil
	done := localprogress.Clone(pending)
	done.Revision, done.MarketQuests[0].Completed = "legacy-previously-seen", true
	assertCompletionQuality(t, reconcileCompletionForTest(t, a, done, true), 0, 0, 0)
	if len(a.qualities.data.Completions[profile].Revisions) != 0 {
		t.Fatal("legacy event deduplication survived a fresh observation")
	}
}

func TestQualityCompletionDiskFailurePreservesClassificationAndTransitionBaseline(t *testing.T) {
	a, before := completionQualityFixture(t)
	reconcileCompletionForTest(t, a, before, true)
	store := a.qualities
	path := store.path
	store.path = filepath.Join(t.TempDir(), "directory-target")
	if err := os.Mkdir(store.path, 0700); err != nil {
		t.Fatal(err)
	}
	after := localprogress.Clone(before)
	after.Revision, after.MarketQuests[0].Completed = "failed-persist-completion", true
	old, _ := json.Marshal(store.data)
	if _, err := a.qualityReconcileFreshSnapshotLocked(a.apiKey, a.state.ItemOptions, after); err == nil {
		t.Fatal("failed completion write was accepted")
	}
	unchanged, _ := json.Marshal(store.data)
	if !bytes.Equal(old, unchanged) {
		t.Fatal("failed persist advanced completion baseline or changed quality")
	}
	store.path = path
	assertCompletionQuality(t, reconcileCompletionForTest(t, a, after, true), 0, 0, 0)
}

func TestQualityCompletionInvalidatesPendingJournalWithoutMakingRemovalInvalid(t *testing.T) {
	for _, delta := range []int64{-1, 1} {
		a, before := completionQualityFixture(t)
		reconcileCompletionForTest(t, a, before, true)
		store, profile := a.qualities, qualityProfile(a.apiKey, a.state.ItemOptions)
		op := qualityOperation{Profile: profile, Item: "food", RowKey: qualityRowKey(before.Items[0]), BeforeRevision: before.Revision,
			Before: qualityRecord{Total: 5, FIR: 2, Regular: 2}, Delta: delta, FIR: true}
		if err := store.update(func(data *qualityData) error { data.Pending["old-session:1"] = op; return nil }); err != nil {
			t.Fatal(err)
		}
		after := localprogress.Clone(before)
		after.Revision, after.MarketQuests[0].Completed = "pending-completion", true
		// Az aktív művelet maradjon naplózott; a root integration teszt az ACK-ot is futtatja.
		a.pendingAddQueue = []pendingMarketAdd{{ID: 1, Typed: true}}
		deleteID := store.session + ":1"
		if err := store.update(func(data *qualityData) error {
			delete(data.Pending, "old-session:1")
			data.Pending[deleteID] = op
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		reconcileCompletionForTest(t, a, after, true)
		invalidated, ok := store.data.Pending[deleteID]
		if !ok || !invalidated.Invalidated || invalidated.Before != op.Before {
			t.Fatalf("completion lost a valid pending journal: %+v", invalidated)
		}
		a.qualities = nil
		loaded, err := a.qualityStoreLocked()
		if err != nil {
			t.Fatalf("invalidated removal/add journal could not reload: %v", err)
		}
		if err := loaded.update(func(data *qualityData) error {
			applyQualityOperation(data, deleteID, data.Pending[deleteID])
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		record := loaded.data.Profiles[profile][op.RowKey]
		if record.Total != 5+delta || record.FIR != 0 || record.Regular != 0 {
			t.Fatalf("late ACK resurrected old quality: %+v", record)
		}
	}
}

func TestQualityCompletionProfilesKeepKeysCharactersAndModesIndependent(t *testing.T) {
	for _, kind := range []string{"API key", "character", "mode"} {
		t.Run(kind, func(t *testing.T) {
			a, first := completionQualityFixture(t)
			reconcileCompletionForTest(t, a, first, true)
			key, opts := a.apiKey, a.state.ItemOptions
			second := localprogress.Clone(first)
			if kind == "API key" {
				a.apiKey += "-other"
			}
			if kind == "character" {
				a.state.ItemOptions.CharacterUID += "-other"
				second.CharUID = a.state.CharacterUID
			}
			if kind == "mode" {
				a.state.ItemOptions.Mode, second.GameMode = "PVE", "PVE"
			}
			setCompletionQuality(t, a, second, 1, 3)
			reconcileCompletionForTest(t, a, second, true)
			second.Revision, second.MarketQuests[0].Completed = "second-profile-complete", true
			assertCompletionQuality(t, reconcileCompletionForTest(t, a, second, true), 0, 0, 0)
			a.apiKey, a.state.ItemOptions = key, opts
			first.Revision = "first-profile-unchanged"
			assertCompletionQuality(t, reconcileCompletionForTest(t, a, first, true), 0, 2, 2)
		})
	}
}

func TestQualityCompletionLegacyStoreMigrationKeepsExistingClassifications(t *testing.T) {
	a, snapshot := completionQualityFixture(t)
	legacy, err := json.Marshal(a.qualities.data)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(legacy, []byte("completions")) {
		t.Fatal("fixture already contains a completion baseline")
	}
	a.qualities = nil
	snapshot.MarketQuests[0].Completed = true
	assertCompletionQuality(t, reconcileCompletionForTest(t, a, snapshot, true), 0, 2, 2)
	if a.qualities.data.Version != 1 || len(a.qualities.data.Completions) != 1 {
		t.Fatal("compatible v1 migration did not retain its version or establish a baseline")
	}
}

func TestQualityCompletionCorruptHistoryPreservesTheOriginalFile(t *testing.T) {
	a, snapshot := completionQualityFixture(t)
	reconcileCompletionForTest(t, a, snapshot, true)
	profile := qualityProfile(a.apiKey, a.state.ItemOptions)
	for _, change := range []struct {
		name string
		edit func(*qualityCompletionProfile)
	}{
		{"missing goals", func(h *qualityCompletionProfile) { h.Goals = nil }},
		{"blank goal", func(h *qualityCompletionProfile) { h.Goals["quest:"] = qualityCompletionGoal{} }},
		{"oversized goal", func(h *qualityCompletionProfile) {
			h.Goals["module:"+strings.Repeat("x", 129)] = qualityCompletionGoal{}
		}},
		{"duplicate legacy revisions", func(h *qualityCompletionProfile) { h.Revisions = []string{"legacy", "legacy"} }},
		{"legacy revision control character", func(h *qualityCompletionProfile) { h.Revisions = []string{"bad\nrevision"} }},
		{"invalid row", func(h *qualityCompletionProfile) {
			g := h.Goals["quest:goal"]
			g.Items[0].RowKey = "bad"
			h.Goals["quest:goal"] = g
		}},
		{"oversized item", func(h *qualityCompletionProfile) {
			g := h.Goals["quest:goal"]
			g.Items[0].ItemID = strings.Repeat("x", 129)
			h.Goals["quest:goal"] = g
		}},
		{"duplicate rows", func(h *qualityCompletionProfile) {
			g := h.Goals["quest:goal"]
			g.Items = append(g.Items, g.Items[0])
			h.Goals["quest:goal"] = g
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			encoded, _ := json.Marshal(a.qualities.data)
			var data qualityData
			if err := json.Unmarshal(encoded, &data); err != nil {
				t.Fatal(err)
			}
			history := data.Completions[profile]
			change.edit(&history)
			data.Completions[profile] = history
			body, _ := json.Marshal(data)
			dir := t.TempDir()
			path := filepath.Join(dir, "item-quality.json")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			other := &ItemWindowApp{dir: dir}
			if _, err := other.qualityStoreLocked(); err == nil {
				t.Fatal("corrupt completion history was accepted")
			}
			preserved, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(body, preserved) {
				t.Fatal("corrupt completion history was overwritten")
			}
		})
	}
}

func TestQualityCompletionStoreSizeGuardPreservesDiskAndMemory(t *testing.T) {
	a, snapshot := completionQualityFixture(t)
	reconcileCompletionForTest(t, a, snapshot, true)
	store := a.qualities
	oldMemory, _ := json.Marshal(store.data)
	oldDisk, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	err = store.update(func(data *qualityData) error {
		profile := qualityProfile(a.apiKey, a.state.ItemOptions)
		history := data.Completions[profile]
		goal := history.Goals["quest:goal"]
		goal.Items[0].Name = strings.Repeat("x", maxQualityStoreBytes)
		history.Goals["quest:goal"] = goal
		data.Completions[profile] = history
		return nil
	})
	newMemory, _ := json.Marshal(store.data)
	newDisk, readErr := os.ReadFile(store.path)
	if err == nil || readErr != nil || !bytes.Equal(oldMemory, newMemory) || !bytes.Equal(oldDisk, newDisk) {
		t.Fatal("oversized update wrote an unloadable quality file or advanced memory")
	}
}

func TestQualityCompletionFailedPersistenceBlocksInventoryReadAndEditingUntilFreshRecovery(t *testing.T) {
	a, before := completionQualityFixture(t)
	a.qualityTracking = true
	reconcileCompletionForTest(t, a, before, true)
	store := a.qualities
	originalPath := store.path
	beforeDisk, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeMemory, _ := json.Marshal(store.data)
	store.path = filepath.Join(t.TempDir(), "blocked-quality-target")
	if err := os.Mkdir(store.path, 0700); err != nil {
		t.Fatal(err)
	}
	after := localprogress.Clone(before)
	after.Revision, after.MarketQuests[0].Completed = "failed-completion-recovery", true
	a.mu.Lock()
	a.applyMergedMarketSnapshotLocked(market.Item{}, localMarketSnapshot(after), after, "")
	a.mu.Unlock()
	if a.confirmedProgress.MetadataComplete || a.GetState().Local.Error == "" {
		t.Fatal("failed completion persistence did not leave an explicitly unknown snapshot")
	}
	// A változatlan összkészlet miatt a régi store egyeztetése no-op lenne.
	// Ez nem teheti újra szerkeszthetővé a teljesítés előtti minőséget.
	if inv := a.GetTrackerInventory(); inv.Error == "" || len(inv.Rows) != 0 {
		t.Fatalf("failed completion exposed old typed inventory as current: %+v", inv)
	}
	profile := qualityProfile(a.apiKey, a.state.ItemOptions)
	state := a.SetTrackerItemQuality(profile, qualityRowKey(after.Items[0]), 5, 2, 2)
	if state.Local.Error == "" || a.confirmedProgress.MetadataComplete {
		t.Fatal("no-op editor save cleared a failed completion before fresh recovery")
	}
	unchangedMemory, _ := json.Marshal(store.data)
	unchangedDisk, err := os.ReadFile(originalPath)
	if err != nil || !bytes.Equal(beforeMemory, unchangedMemory) || !bytes.Equal(beforeDisk, unchangedDisk) {
		t.Fatal("failed completion/editor access advanced the baseline or changed saved quality")
	}
	store.path = originalPath
	a.mu.Lock()
	a.applyMergedMarketSnapshotLocked(market.Item{}, localMarketSnapshot(after), after, "")
	a.mu.Unlock()
	if !a.confirmedProgress.MetadataComplete || a.GetState().Local.Error != "" {
		t.Fatal("fresh successful completion recovery did not restore a usable snapshot")
	}
	inv := a.GetTrackerInventory()
	if inv.Error != "" || len(inv.Rows) != 2 {
		t.Fatalf("successfully recovered inventory remained blocked: %+v", inv)
	}
	for _, row := range inv.Rows {
		if row.ItemID == "food" && (row.FIR != 0 || row.Regular != 0 || row.Uncategorized != 5) {
			t.Fatalf("successful completion recovery restored old quality: %+v", row)
		}
	}
	// Egy javítható szerkesztési hiba nem maradhat állandó Local.Error-zár.
	invalid := a.SetTrackerItemQuality(profile, qualityRowKey(after.Items[0]), 5, 6, 0)
	if invalid.Local.Error == "" {
		t.Fatal("invalid editor quantity was accepted")
	}
	valid := a.SetTrackerItemQuality(profile, qualityRowKey(after.Items[0]), 5, 1, 2)
	if valid.Local.Error != "" {
		t.Fatalf("corrected editor save stayed blocked: %+v", valid.Local)
	}
	for _, row := range a.GetTrackerInventory().Rows {
		if row.ItemID == "food" && (row.FIR != 1 || row.Regular != 2 || row.Uncategorized != 2) {
			t.Fatalf("corrected editor save did not apply after recovery: %+v", row)
		}
	}
}
