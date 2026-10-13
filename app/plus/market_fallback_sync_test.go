package plus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func marketFallbackSyncFixture(t *testing.T) (*ItemWindowApp, *fakeTrackingAPI, ItemOptions, string) {
	t.Helper()
	a, fake := newTrackingTestApp(t)
	mainDir := t.TempDir()
	a.localStore = localprogress.NewStore(mainDir)
	fake.snapshot.Items[0].AvailableQuantity = 2
	fake.snapshot.Items[0].RemainingQuantity = 5
	opts := a.state.ItemOptions
	a.state.CharacterUID = opts.CharacterUID
	mainKinds := localprogress.StockAmounts{Regular: 1, Unknown: 1}
	local := localprogress.StockAmounts{Regular: 3}
	if _, err := a.localStore.CorrectStock(opts.CharacterUID, "PVP", localprogress.ManualStockCorrection{
		ItemID: trackingBSGID, LocalAvailable: &local, MarketKinds: &mainKinds, ObservedMarketQuantity: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.localStore.RecordHandover(opts.CharacterUID, "PVP", "already-used-local", []localprogress.HandoverEntry{{ItemID: trackingBSGID, Regular: 1}}); err != nil {
		t.Fatal(err)
	}
	fallback := localprogress.StockAmounts{Regular: 3, FIR: 2, Unknown: 1}
	if _, err := a.marketFallbackStock().CorrectStock(opts.CharacterUID, "PVP", localprogress.ManualStockCorrection{
		ItemID: trackingBSGID, LocalAvailable: &fallback}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.marketFallbackStock().RecordHandover(opts.CharacterUID, "PVP", "already-used-fallback", []localprogress.HandoverEntry{{ItemID: trackingBSGID, Regular: 1, FIR: 1}}); err != nil {
		t.Fatal(err)
	}
	return a, fake, opts, mainDir
}

func fallbackSyncFileForTest(t *testing.T, directory string) string {
	t.Helper()
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if filepath.Ext(file.Name()) == ".json" {
			return filepath.Join(directory, file.Name())
		}
	}
	t.Fatal("fixture stock file is missing")
	return ""
}

func fallbackSyncAvailableForTest(t *testing.T, a *ItemWindowApp, opts ItemOptions) localprogress.StockAmounts {
	t.Helper()
	stock, err := marketFallbackSyncAmounts(a.marketFallbackStock(), opts, trackingBSGID)
	if err != nil {
		t.Fatal(err)
	}
	return stock
}

func TestMarketFallbackSyncTransfersTypedStockOnceAndPreservesUsageAndDevStock(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	current, warning, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID)
	if err != nil || warning == "" || current.Items[0].AvailableQuantity != 6 || len(fake.writes) != 1 || fake.writes[0].Quantity != 6 {
		t.Fatalf("verified upload failed: %v %q %+v", err, warning, fake.writes)
	}
	if pending, err := a.marketFallbackSyncPending(opts); pending || err != nil {
		t.Fatalf("verified upload left a pending intent: %v %v", pending, err)
	}
	stock, err := a.marketFallbackStock().Load(opts.CharacterUID, "PVP")
	row, unique := stockEditorItem(stock, trackingBSGID)
	if err != nil || !unique || row.LocalAvailableQuantity != 0 || row.LocalCollectedQuantity != 2 || row.LocalUsedQuantity != 2 || row.LocalUsedRegularQuantity != 1 || row.LocalUsedFIRQuantity != 1 {
		t.Fatalf("transfer fabricated usage or lost old usage: %+v %v", row, err)
	}
	main, err := a.localStore.Load(opts.CharacterUID, "PVP")
	row, unique = stockEditorItem(main, trackingBSGID)
	if err != nil || !unique || row.LocalCollectedQuantity != 3 || row.LocalAvailableQuantity != 2 || row.LocalUsedQuantity != 1 || row.MarketRegularQuantity != 3 || row.MarketFIRQuantity != 1 {
		t.Fatalf("upload changed dev stock or lost Market quality: %+v %v", row, err)
	}
	if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, current, trackingBSGID); err != nil || len(fake.writes) != 1 {
		t.Fatalf("empty fallback was uploaded a second time: %v %v", fake.writes, err)
	}
}

func TestMarketFallbackSyncNoOpKeepsFallbackAndClearsOnlyIntent(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	path := fallbackSyncFileForTest(t, filepath.Join(a.dir, "market-fallback"))
	before, _ := os.ReadFile(path)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{OK: true, Changed: false, Revision: fake.snapshot.Revision, NotificationDelivered: true}, nil
	}
	current, warning, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID)
	after, _ := os.ReadFile(path)
	if err != nil || warning == "" || current.Items[0].AvailableQuantity != 2 || len(fake.writes) != 1 || !bytes.Equal(before, after) {
		t.Fatalf("confirmed no-op consumed fallback: %q %v", warning, err)
	}
	if pending, err := a.marketFallbackSyncPending(opts); pending || err != nil {
		t.Fatal("no-op kept an already proven failed intent")
	}
}

func TestMarketFallbackSyncDefiniteRejectionKeepsFallbackAndDoesNotRetry(t *testing.T) {
	for _, rejected := range []error{market.ErrUnauthorized, market.ErrProgressForbidden, market.ErrProgressNotFound, market.ErrProgressConflict, market.ErrProgressRevisionRequired, market.ErrRateLimited} {
		t.Run(rejected.Error(), func(t *testing.T) {
			a, fake, opts, _ := marketFallbackSyncFixture(t)
			fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) { return market.ProgressWriteResult{}, rejected }
			_, warning, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID)
			if err != nil || warning == "" || len(fake.writes) != 1 || fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{Regular: 2, FIR: 1, Unknown: 1}) {
				t.Fatalf("definite rejection changed/retried stock: %q %v", warning, err)
			}
			if pending, err := a.marketFallbackSyncPending(opts); pending || err != nil {
				t.Fatal("definite rejection kept a new unapplied intent")
			}
		})
	}
}

func TestMarketFallbackSyncUncertainAppliedUploadResumesByGETAfterRestart(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		fake.commitWrite(write)
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	fake.loadHook = func(int) (market.TrackedSnapshot, error) {
		return market.TrackedSnapshot{}, market.ErrProgressUnavailable
	}
	_, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID)
	if err == nil || len(fake.writes) != 1 {
		t.Fatal("readback failure was treated as a settled upload")
	}
	if pending, err := a.marketFallbackSyncPending(opts); !pending || err != nil {
		t.Fatal("uncertain write lost its intent")
	}
	if fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{Regular: 2, FIR: 1, Unknown: 1}) {
		t.Fatal("unverified write cleared fallback")
	}
	fake.loadHook = nil
	restarted := &ItemWindowApp{dir: a.dir, tracker: fake, localStore: a.localStore}
	current, warning, err := restarted.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), "another-selected-item")
	if err != nil || warning == "" || current.Items[0].AvailableQuantity != 6 || len(fake.writes) != 1 || fallbackSyncAvailableForTest(t, restarted, opts) != (localprogress.StockAmounts{}) {
		t.Fatalf("restart replayed PATCH or lost original pending item: %q %v", warning, err)
	}
}

func TestMarketFallbackSyncUncertainUnappliedOrUnknownResponseNeverReplays(t *testing.T) {
	for _, writeError := range []error{market.ErrProgressWriteUncertain, market.ErrProgressInvalidRequest, errors.Join(market.ErrProgressWriteUncertain, market.ErrProgressConflict)} {
		t.Run(writeError.Error(), func(t *testing.T) {
			a, fake, opts, _ := marketFallbackSyncFixture(t)
			fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) { return market.ProgressWriteResult{}, writeError }
			before := cloneTrackedSnapshot(fake.snapshot)
			if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, before, trackingBSGID); err == nil {
				t.Fatal("unapplied uncertain write was considered finished")
			}
			path, _ := a.marketFallbackSyncPath(opts, trackingBSGID)
			original, _ := os.ReadFile(path)
			if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, before, trackingBSGID); err == nil || len(fake.writes) != 1 {
				t.Fatal("pending upload was retried")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(original, after) || fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{Regular: 2, FIR: 1, Unknown: 1}) {
				t.Fatal("pending mismatch modified intent or fallback")
			}
		})
	}
}

func TestMarketFallbackSyncRejectsUnverifiedReadbackAndKeepsIntent(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*market.TrackedSnapshot)
	}{
		{"wrong-character", func(snapshot *market.TrackedSnapshot) { snapshot.CharUID = "other" }},
		{"wrong-mode", func(snapshot *market.TrackedSnapshot) { snapshot.GameMode = "PVE" }},
		{"same-revision", func(snapshot *market.TrackedSnapshot) { snapshot.Revision = "revision-1" }},
		{"wrong-quantity", func(snapshot *market.TrackedSnapshot) { snapshot.Items[0].AvailableQuantity++ }},
		{"separate-fir", func(snapshot *market.TrackedSnapshot) { snapshot.FIRInventorySeparate = true }},
		{"ambiguous", func(snapshot *market.TrackedSnapshot) { snapshot.Items = append(snapshot.Items, snapshot.Items[0]) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, fake, opts, _ := marketFallbackSyncFixture(t)
			fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) { return fake.commitWrite(write), nil }
			fake.loadHook = func(int) (market.TrackedSnapshot, error) {
				observed := cloneTrackedSnapshot(fake.snapshot)
				test.edit(&observed)
				return observed, nil
			}
			if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID); err == nil || len(fake.writes) != 1 {
				t.Fatal("unverified readback confirmed upload")
			}
			if pending, err := a.marketFallbackSyncPending(opts); !pending || err != nil || fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{Regular: 2, FIR: 1, Unknown: 1}) {
				t.Fatal("unverified upload lost intent or consumed fallback")
			}
		})
	}
}

func TestMarketFallbackSyncChangedFallbackFailsClosedBeforeClearing(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	changed := localprogress.StockAmounts{Regular: 5}
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		result := fake.commitWrite(write)
		_, err := a.marketFallbackStock().CorrectStock(opts.CharacterUID, "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &changed})
		if err != nil {
			t.Fatal(err)
		}
		return result, nil
	}
	if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID); err == nil {
		t.Fatal("changed fallback was blindly cleared")
	}
	if fallbackSyncAvailableForTest(t, a, opts) != changed {
		t.Fatal("pending upload cleared newer fallback stock")
	}
	if pending, err := a.marketFallbackSyncPending(opts); !pending || err != nil {
		t.Fatal("changed fallback lost immutable upload intent")
	}
}

func TestMarketFallbackSyncResumesAfterFallbackClearedButQualitySaveFailed(t *testing.T) {
	a, fake, opts, mainDir := marketFallbackSyncFixture(t)
	mainPath := fallbackSyncFileForTest(t, mainDir)
	before, _ := os.ReadFile(mainPath)
	fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
		result := fake.commitWrite(write)
		if err := os.WriteFile(mainPath, []byte("corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		return result, nil
	}
	if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID); err == nil {
		t.Fatal("failed quality save was considered finished")
	}
	if fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{}) {
		t.Fatal("verified uploaded stock remained available twice")
	}
	if err := os.WriteFile(mainPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := &ItemWindowApp{dir: a.dir, tracker: fake, localStore: localprogress.NewStore(mainDir)}
	if _, _, err := restarted.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID); err != nil || len(fake.writes) != 1 {
		t.Fatalf("already-cleared fallback could not be resumed without PATCH: %v", err)
	}
	stock, err := restarted.marketFallbackStock().Load(opts.CharacterUID, "PVP")
	row, _ := stockEditorItem(stock, trackingBSGID)
	if err != nil || row.LocalUsedQuantity != 2 || row.LocalCollectedQuantity != 2 {
		t.Fatalf("resume cleared immutable fallback history: %+v %v", row, err)
	}
}

func TestMarketFallbackSyncInitialIdentityVariantsAndQuantityLimitDoNotWrite(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*market.TrackedSnapshot)
	}{
		{"wrong-character", func(snapshot *market.TrackedSnapshot) { snapshot.CharUID = "other" }},
		{"wrong-mode", func(snapshot *market.TrackedSnapshot) { snapshot.GameMode = "PVE" }},
		{"missing-items", func(snapshot *market.TrackedSnapshot) { snapshot.Items = nil }},
		{"missing-revision", func(snapshot *market.TrackedSnapshot) { snapshot.Revision = "" }},
		{"duplicate", func(snapshot *market.TrackedSnapshot) { snapshot.Items = append(snapshot.Items, snapshot.Items[0]) }},
		{"separate-fir", func(snapshot *market.TrackedSnapshot) { snapshot.FIRInventorySeparate = true }},
		{"max-quantity", func(snapshot *market.TrackedSnapshot) {
			snapshot.Items[0].AvailableQuantity = localprogress.MaxQuantity
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, fake, opts, _ := marketFallbackSyncFixture(t)
			remote := cloneTrackedSnapshot(fake.snapshot)
			test.edit(&remote)
			if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, remote, trackingBSGID); err == nil || len(fake.writes) != 0 {
				t.Fatal("invalid/unsupported snapshot wrote stock")
			}
			if pending, err := a.marketFallbackSyncPending(opts); pending || err != nil {
				t.Fatal("rejected preflight created an intent")
			}
		})
	}
}

func TestMarketFallbackSyncIntentIsStrictPrivateAndCorruptionNeverOverwrites(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"duplicate", func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"before": 2`), []byte(`"before": 2, "before": 3`), 1)
		}},
		{"duplicate-case-alias", func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"before": 2`), []byte(`"before": 2, "Before": 3`), 1)
		}},
		{"typed-case-alias", func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"regular": 2`), []byte(`"regular": 2, "Regular": 3`), 1)
		}},
		{"null-typed", func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"regular": 2`), []byte(`"regular": null`), 1)
		}},
		{"wrong-owner", func(data []byte) []byte { return bytes.ReplaceAll(data, []byte("character-a"), []byte("character-b")) }},
		{"wrong-mode", func(data []byte) []byte { return bytes.ReplaceAll(data, []byte("PVP"), []byte("PVE")) }},
		{"bad-target", func(data []byte) []byte { return bytes.Replace(data, []byte(`"target": 6`), []byte(`"target": 7`), 1) }},
		{"unknown-field", func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"version": 1`), []byte(`"version": 1, "apiKey": "extra"`), 1)
		}},
		{"trailing", func(data []byte) []byte { return append(data, []byte(` {}`)...) }},
		{"oversized", func([]byte) []byte { return bytes.Repeat([]byte(" "), maxMarketFallbackSyncBytes+1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, fake, opts, _ := marketFallbackSyncFixture(t)
			fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
				return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
			}
			if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID); err == nil {
				t.Fatal("fixture did not become pending")
			}
			path, _ := a.marketFallbackSyncPath(opts, trackingBSGID)
			data, _ := os.ReadFile(path)
			var fields map[string]json.RawMessage
			if json.Unmarshal(data, &fields) != nil || len(fields) != 9 || strings.Contains(string(data), a.apiKey) || strings.Contains(string(data), `"requiredQuantity"`) {
				t.Fatal("intent schema contains data beyond the bounded operation")
			}
			corrupt := test.edit(data)
			if err := os.WriteFile(path, corrupt, 0o600); err != nil {
				t.Fatal(err)
			}
			if pending, err := a.marketFallbackSyncPending(opts); !pending || !errors.Is(err, errMarketFallbackSyncInvalid) {
				t.Fatal("corrupt intent did not fail closed")
			}
			if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID); err == nil || len(fake.writes) != 1 {
				t.Fatal("corrupt intent was replaced or replayed")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(corrupt, after) {
				t.Fatal("corrupt intent bytes changed")
			}
		})
	}
}

func TestMarketFallbackSyncMissingOrEmptyFallbackDoesNotWrite(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	zero := localprogress.StockAmounts{}
	if _, err := a.marketFallbackStock().CorrectStock(opts.CharacterUID, "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &zero}); err != nil {
		t.Fatal(err)
	}
	if _, warning, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID); err != nil || warning != "" || len(fake.writes) != 0 {
		t.Fatal("empty fallback wrote Market")
	}
	a.dir = t.TempDir()
	a.fallbackStore = nil
	if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID); err != nil || len(fake.writes) != 0 {
		t.Fatal("missing fallback wrote Market")
	}
}

func TestMarketFallbackSyncIntentSaveFailureSendsNoPATCH(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	if err := os.WriteFile(filepath.Join(a.dir, "market-fallback-sync"), []byte("blocked-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID); err == nil || len(fake.writes) != 0 {
		t.Fatal("failed durable preparation still sent PATCH")
	}
	if fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{Regular: 2, FIR: 1, Unknown: 1}) {
		t.Fatal("failed preparation consumed fallback")
	}
}

func TestMarketFallbackSyncChangedContextDoesNotCreateIntentOrWrite(t *testing.T) {
	for _, change := range []string{"key", "character", "settings", "canceled"} {
		t.Run(change, func(t *testing.T) {
			a, fake, opts, _ := marketFallbackSyncFixture(t)
			key := a.apiKey
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch change {
			case "key":
				a.apiKey = "another-test-key"
			case "character":
				a.state.CharacterUID = "another-character"
			case "settings":
				a.state.ItemOptions.revision++
			case "canceled":
				cancel()
			}
			_, _, err := a.syncMarketFallback(ctx, key, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID)
			if !errors.Is(err, context.Canceled) || len(fake.writes) != 0 || fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{Regular: 2, FIR: 1, Unknown: 1}) {
				t.Fatalf("stale context sent a write or changed fallback: %v %+v", err, fake.writes)
			}
			if pending, pendingErr := a.marketFallbackSyncPending(opts); pending || pendingErr != nil {
				t.Fatalf("stale context created an upload intent: %v %v", pending, pendingErr)
			}
		})
	}
}

// Az Err-hívás határán jelzett váltás időzítésfüggő várakozás nélkül
// ellenőrzi a tartós mentés és a PATCH közötti második védelmet.
type fallbackSyncFenceContext struct {
	context.Context
	afterPrepare func()
	checks       int
}

func (ctx *fallbackSyncFenceContext) Err() error {
	ctx.checks++
	if ctx.checks == 2 {
		ctx.afterPrepare()
	}
	return ctx.Context.Err()
}

func TestMarketFallbackSyncContextChangesAfterPrepareRemoveUnsentIntentAndAllowFreshAttempt(t *testing.T) {
	for _, change := range []string{"key", "character", "settings", "canceled"} {
		t.Run(change, func(t *testing.T) {
			a, fake, opts, _ := marketFallbackSyncFixture(t)
			key := a.apiKey
			stockPath := fallbackSyncFileForTest(t, filepath.Join(a.dir, "market-fallback"))
			beforeStock, err := os.ReadFile(stockPath)
			if err != nil {
				t.Fatal(err)
			}
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &fallbackSyncFenceContext{Context: base, afterPrepare: func() {
				switch change {
				case "key":
					a.apiKey = "another-test-key"
				case "character":
					a.state.CharacterUID = "another-character"
				case "settings":
					a.state.ItemOptions.revision++
				case "canceled":
					cancel()
				}
			}}
			_, _, err = a.syncMarketFallback(ctx, key, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID)
			afterStock, readErr := os.ReadFile(stockPath)
			if !errors.Is(err, context.Canceled) || errors.Is(err, errMarketFallbackSyncPending) || len(fake.writes) != 0 || readErr != nil || !bytes.Equal(beforeStock, afterStock) {
				t.Fatalf("changed prepared context sent a write or lost fallback: %v %+v", err, fake.writes)
			}
			pending, err := a.readMarketFallbackSync(opts)
			if err != nil || pending != nil {
				t.Fatalf("never-sent upload kept a blocking intent: %+v %v", pending, err)
			}
			a.apiKey, a.state.CharacterUID, a.state.ItemOptions = key, opts.CharacterUID, opts
			current, _, err := a.syncMarketFallback(context.Background(), key, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID)
			if err != nil || len(fake.writes) != 1 || current.Items[0].AvailableQuantity != 6 || fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{}) {
				t.Fatalf("restored context could not start one fresh upload: %v %+v", err, fake.writes)
			}
		})
	}
}

func TestMarketFallbackSyncUnsentIntentRemovalFailureKeepsStockChangesBlocked(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	path, err := a.marketFallbackSyncPath(opts, trackingBSGID)
	if err != nil {
		t.Fatal(err)
	}
	stockPath := fallbackSyncFileForTest(t, filepath.Join(a.dir, "market-fallback"))
	beforeStock, err := os.ReadFile(stockPath)
	if err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &fallbackSyncFenceContext{Context: base, afterPrepare: func() {
		// A nem üres könyvtár eltávolítása minden támogatott rendszeren hibás.
		// Ezzel a journal elérési útjának helyi hibáját modellezzük.
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "keep"), []byte("blocked"), 0o600); err != nil {
			t.Fatal(err)
		}
		cancel()
	}}
	_, _, err = a.syncMarketFallback(ctx, a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID)
	afterStock, readErr := os.ReadFile(stockPath)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, errMarketFallbackSyncPending) || len(fake.writes) != 0 || readErr != nil || !bytes.Equal(beforeStock, afterStock) {
		t.Fatalf("failed intent cleanup changed stock or allowed a write: %v %+v", err, fake.writes)
	}
	if pending, err := a.marketFallbackSyncPending(opts); !pending || err == nil {
		t.Fatalf("failed cleanup did not remain blocked: %v %v", pending, err)
	}
}

func fallbackSyncPendingFixtureForTest(t *testing.T) (*ItemWindowApp, *fakeTrackingAPI, ItemOptions, string, *marketFallbackSyncIntent) {
	t.Helper()
	a, fake, opts, mainDir := marketFallbackSyncFixture(t)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
	}
	if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID); !errors.Is(err, errMarketFallbackSyncPending) {
		t.Fatalf("fixture did not preserve uncertain upload: %v", err)
	}
	pending, err := a.readMarketFallbackSync(opts)
	if err != nil || pending == nil {
		t.Fatalf("fixture intent missing: %+v %v", pending, err)
	}
	return a, fake, opts, mainDir, pending
}

func TestMarketFallbackSyncInitialNoOpReadbackFailureKeepsStockWithoutPending(t *testing.T) {
	a, fake, opts, _ := marketFallbackSyncFixture(t)
	path := fallbackSyncFileForTest(t, filepath.Join(a.dir, "market-fallback"))
	before, _ := os.ReadFile(path)
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{OK: true, Changed: false, Revision: fake.snapshot.Revision}, nil
	}
	fake.loadHook = func(int) (market.TrackedSnapshot, error) {
		return market.TrackedSnapshot{}, market.ErrProgressUnavailable
	}
	_, status, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID)
	after, _ := os.ReadFile(path)
	pending, pendingErr := a.marketFallbackSyncPending(opts)
	if err != nil || status == "" || pending || pendingErr != nil || len(fake.writes) != 1 || !bytes.Equal(before, after) {
		t.Fatalf("definite new no-op was left permanently pending or changed fallback: %q %v %v %v", status, err, pending, pendingErr)
	}
}

func TestMarketFallbackSyncExplicitRetryUsesSavedAbsoluteTargetAfterRestart(t *testing.T) {
	a, fake, opts, mainDir, pending := fallbackSyncPendingFixtureForTest(t)
	fake.writeHook = nil
	restarted := &ItemWindowApp{dir: a.dir, apiKey: a.apiKey, tracker: fake, localStore: localprogress.NewStore(mainDir), state: a.state}
	current, status, err := restarted.retrySavedMarketFallback(context.Background(), a.apiKey, opts)
	if err != nil || status == "" || current.Items[0].AvailableQuantity != pending.Target || len(fake.writes) != 2 ||
		fake.writes[1].Quantity != pending.Target || fake.writes[1].Revision != pending.Revision || fallbackSyncAvailableForTest(t, restarted, opts) != (localprogress.StockAmounts{}) {
		t.Fatalf("explicit retry recomputed or lost original target: %q %v %+v", status, err, fake.writes)
	}
	stock, err := restarted.marketFallbackStock().Load(opts.CharacterUID, "PVP")
	row, _ := stockEditorItem(stock, trackingBSGID)
	if err != nil || row.LocalUsedQuantity != 2 || row.LocalCollectedQuantity != 2 || row.LocalUsedRegularQuantity != 1 || row.LocalUsedFIRQuantity != 1 {
		t.Fatalf("retry changed transfer into usage: %+v %v", row, err)
	}
	if _, _, err := restarted.retrySavedMarketFallback(context.Background(), a.apiKey, opts); err != nil || len(fake.writes) != 2 {
		t.Fatal("settled retry issued another PATCH")
	}
}

func TestMarketFallbackSyncExplicitRetryOriginalAndRetryCannotBothApply(t *testing.T) {
	for _, winner := range []string{"original", "retry"} {
		t.Run(winner, func(t *testing.T) {
			a, fake, opts, _, pending := fallbackSyncPendingFixtureForTest(t)
			original := fake.writes[0]
			applyCAS := func(write stockWrite) bool {
				if write.Revision != fake.snapshot.Revision {
					return false
				}
				fake.commitWrite(write)
				return true
			}
			fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
				if write.Revision != pending.Revision || write.Quantity != pending.Target {
					t.Fatal("retry did not use the original absolute intent")
				}
				if winner == "original" {
					if !applyCAS(original) || applyCAS(write) {
						t.Fatal("revision fence allowed two applications")
					}
					return market.ProgressWriteResult{}, market.ErrProgressConflict
				}
				if !applyCAS(write) || applyCAS(original) {
					t.Fatal("delayed original applied after the retry")
				}
				return market.ProgressWriteResult{OK: true, Changed: true, Revision: fake.snapshot.Revision}, nil
			}
			current, _, err := a.retrySavedMarketFallback(context.Background(), a.apiKey, opts)
			if err != nil || len(fake.writes) != 2 || fake.commits != 1 || current.Items[0].AvailableQuantity != pending.Target || fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{}) {
				t.Fatalf("late original doubled or prevented verified settlement: %v %+v", err, fake.writes)
			}
		})
	}
}

func TestMarketFallbackSyncExplicitSameRevisionNoOpOrUncertainKeepsOriginalIntent(t *testing.T) {
	for _, response := range []string{"noop", "noop-read-failure", "conflict", "unknown"} {
		t.Run(response, func(t *testing.T) {
			a, fake, opts, _, _ := fallbackSyncPendingFixtureForTest(t)
			path, _ := a.marketFallbackSyncPath(opts, trackingBSGID)
			beforeIntent, _ := os.ReadFile(path)
			stockPath := fallbackSyncFileForTest(t, filepath.Join(a.dir, "market-fallback"))
			beforeStock, _ := os.ReadFile(stockPath)
			reads := fake.loads
			fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
				switch response {
				case "conflict":
					return market.ProgressWriteResult{}, market.ErrProgressConflict
				case "unknown":
					return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
				default:
					return market.ProgressWriteResult{OK: true, Changed: false, Revision: fake.snapshot.Revision}, nil
				}
			}
			if response == "noop-read-failure" {
				fake.loadHook = func(count int) (market.TrackedSnapshot, error) {
					if count > reads+1 {
						return market.TrackedSnapshot{}, market.ErrProgressUnavailable
					}
					return cloneTrackedSnapshot(fake.snapshot), nil
				}
			}
			if _, _, err := a.retrySavedMarketFallback(context.Background(), a.apiKey, opts); !errors.Is(err, errMarketFallbackSyncPending) || len(fake.writes) != 2 {
				t.Fatalf("unverified original retry was cleared: %v %+v", err, fake.writes)
			}
			afterIntent, _ := os.ReadFile(path)
			afterStock, _ := os.ReadFile(stockPath)
			if !bytes.Equal(beforeIntent, afterIntent) || !bytes.Equal(beforeStock, afterStock) {
				t.Fatal("retry acknowledgment rewrote original uncertain intent or stock")
			}
		})
	}
}

func TestMarketFallbackSyncExplicitRetryVerifiedTargetNeedsNoPATCH(t *testing.T) {
	a, fake, opts, _, pending := fallbackSyncPendingFixtureForTest(t)
	fake.commitWrite(fake.writes[0])
	current, _, err := a.retrySavedMarketFallback(context.Background(), a.apiKey, opts)
	if err != nil || len(fake.writes) != 1 || current.Items[0].AvailableQuantity != pending.Target || fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{}) {
		t.Fatalf("verified original was sent again: %v %+v", err, fake.writes)
	}
}

func TestMarketFallbackSyncExplicitRetryRejectsAmbiguousOrConflictingCurrentStock(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*market.TrackedSnapshot)
	}{
		{"different-quantity", func(snapshot *market.TrackedSnapshot) { snapshot.Items[0].AvailableQuantity++ }},
		{"different-revision-and-quantity", func(snapshot *market.TrackedSnapshot) {
			snapshot.Revision = "external"
			snapshot.Items[0].AvailableQuantity++
		}},
		{"target-with-original-revision", func(snapshot *market.TrackedSnapshot) { snapshot.Items[0].AvailableQuantity = 6 }},
		{"wrong-character", func(snapshot *market.TrackedSnapshot) { snapshot.CharUID = "another" }},
		{"wrong-mode", func(snapshot *market.TrackedSnapshot) { snapshot.GameMode = "PVE" }},
		{"duplicate", func(snapshot *market.TrackedSnapshot) { snapshot.Items = append(snapshot.Items, snapshot.Items[0]) }},
		{"separate-fir", func(snapshot *market.TrackedSnapshot) { snapshot.FIRInventorySeparate = true }},
		{"missing-revision", func(snapshot *market.TrackedSnapshot) { snapshot.Revision = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, fake, opts, _, _ := fallbackSyncPendingFixtureForTest(t)
			path, _ := a.marketFallbackSyncPath(opts, trackingBSGID)
			before, _ := os.ReadFile(path)
			test.edit(&fake.snapshot)
			if _, _, err := a.retrySavedMarketFallback(context.Background(), a.apiKey, opts); err == nil || len(fake.writes) != 1 {
				t.Fatal("conflicting current stock sent another PATCH")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) || fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{Regular: 2, FIR: 1, Unknown: 1}) {
				t.Fatal("conflicting current stock discarded saved intent or fallback")
			}
		})
	}
}

func TestMarketFallbackSyncObsoleteBeforeNewRevisionReleasesReadOnlyAndPreservesAllStock(t *testing.T) {
	for _, path := range []string{"refresh", "explicit-retry"} {
		t.Run(path, func(t *testing.T) {
			a, fake, opts, mainDir, original := fallbackSyncPendingFixtureForTest(t)
			fake.snapshot.Revision = "external-revision"
			fallbackPath := fallbackSyncFileForTest(t, filepath.Join(a.dir, "market-fallback"))
			mainPath := fallbackSyncFileForTest(t, mainDir)
			beforeFallback, _ := os.ReadFile(fallbackPath)
			beforeMain, _ := os.ReadFile(mainPath)
			fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
				t.Fatal("obsolete intent resolution sent another PATCH")
				return market.ProgressWriteResult{}, nil
			}
			var current market.TrackedSnapshot
			var status string
			var err error
			if path == "refresh" {
				current, status, err = a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID)
			} else {
				current, status, err = a.retrySavedMarketFallback(context.Background(), a.apiKey, opts)
			}
			afterFallback, _ := os.ReadFile(fallbackPath)
			afterMain, _ := os.ReadFile(mainPath)
			pending, pendingErr := a.readMarketFallbackSync(opts)
			if err != nil || status != marketFallbackSyncKeptMessage || len(fake.writes) != 1 || pendingErr != nil || pending != nil ||
				current.Items[0].AvailableQuantity != original.Before || !bytes.Equal(beforeFallback, afterFallback) || !bytes.Equal(beforeMain, afterMain) {
				t.Fatalf("obsolete intent changed independent stock or stayed blocked: %q %v %+v %v", status, err, pending, pendingErr)
			}
			// A késve beérkező eredeti PATCH az aktuális revisionnél CAS-conflict.
			if fake.writes[0].Revision == fake.snapshot.Revision {
				fake.commitWrite(fake.writes[0])
			}
			if fake.commits != 0 || fake.snapshot.Items[0].AvailableQuantity != original.Before {
				t.Fatal("delayed original changed stock after obsolete intent resolution")
			}
		})
	}
}

func TestMarketFallbackSyncPostWriteBeforeNewRevisionReleasesWithoutInventingUsage(t *testing.T) {
	for _, attempt := range []string{"new-changed-ack", "new-uncertain", "retry-changed-ack", "retry-uncertain"} {
		t.Run(attempt, func(t *testing.T) {
			a, fake, opts, mainDir := marketFallbackSyncFixture(t)
			if strings.HasPrefix(attempt, "retry-") {
				fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
					return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
				}
				if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID); err == nil {
					t.Fatal("retry fixture was not pending")
				}
			}
			fallbackPath := fallbackSyncFileForTest(t, filepath.Join(a.dir, "market-fallback"))
			beforeFallback, _ := os.ReadFile(fallbackPath)
			beforeMain, err := a.localStore.Load(opts.CharacterUID, "PVP")
			if err != nil {
				t.Fatal(err)
			}
			fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
				fake.snapshot.Revision = "external-revision"
				if strings.HasSuffix(attempt, "uncertain") {
					return market.ProgressWriteResult{}, market.ErrProgressWriteUncertain
				}
				return market.ProgressWriteResult{OK: true, Changed: true, Revision: fake.snapshot.Revision}, nil
			}
			var status string
			if strings.HasPrefix(attempt, "retry-") {
				_, status, err = a.retrySavedMarketFallback(context.Background(), a.apiKey, opts)
			} else {
				_, status, err = a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID)
			}
			afterFallback, _ := os.ReadFile(fallbackPath)
			afterMain, loadErr := localprogress.NewStore(mainDir).Load(opts.CharacterUID, "PVP")
			oldRow, _ := stockEditorItem(beforeMain, trackingBSGID)
			newRow, _ := stockEditorItem(afterMain, trackingBSGID)
			pending, pendingErr := a.readMarketFallbackSync(opts)
			writes := 1
			if strings.HasPrefix(attempt, "retry-") {
				writes = 2
			}
			if err != nil || status != marketFallbackSyncKeptMessage || pendingErr != nil || pending != nil || len(fake.writes) != writes ||
				fake.snapshot.Items[0].AvailableQuantity != 2 || !bytes.Equal(beforeFallback, afterFallback) || loadErr != nil ||
				newRow.LocalCollectedQuantity != oldRow.LocalCollectedQuantity || newRow.LocalUsedQuantity != oldRow.LocalUsedQuantity ||
				newRow.LocalRegularQuantity != oldRow.LocalRegularQuantity || newRow.MarketRegularQuantity != oldRow.MarketRegularQuantity || newRow.MarketFIRQuantity != oldRow.MarketFIRQuantity {
				t.Fatalf("unapplied old-revision attempt changed stock or usage: %q %v %+v %+v", status, err, oldRow, newRow)
			}
		})
	}
}

func TestMarketFallbackSyncObsoleteIntentChangedFallbackOrIncompleteSnapshotRemainsPending(t *testing.T) {
	for _, change := range []string{"typed-change", "zero-partial", "wrong-character", "wrong-mode", "ambiguous", "separate-fir", "missing-items", "read-failure"} {
		t.Run(change, func(t *testing.T) {
			a, fake, opts, _, _ := fallbackSyncPendingFixtureForTest(t)
			fake.snapshot.Revision = "external-revision"
			if change == "typed-change" || change == "zero-partial" {
				amounts := localprogress.StockAmounts{FIR: 3, Unknown: 1}
				if change == "zero-partial" {
					amounts = localprogress.StockAmounts{}
				}
				if _, err := a.marketFallbackStock().CorrectStock(opts.CharacterUID, "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &amounts}); err != nil {
					t.Fatal(err)
				}
			} else {
				switch change {
				case "wrong-character":
					fake.snapshot.CharUID = "another"
				case "wrong-mode":
					fake.snapshot.GameMode = "PVE"
				case "ambiguous":
					fake.snapshot.Items = append(fake.snapshot.Items, fake.snapshot.Items[0])
				case "separate-fir":
					fake.snapshot.FIRInventorySeparate = true
				case "missing-items":
					fake.snapshot.Items = nil
				case "read-failure":
					fake.loadHook = func(int) (market.TrackedSnapshot, error) {
						return market.TrackedSnapshot{}, market.ErrProgressUnavailable
					}
				}
			}
			intentPath, _ := a.marketFallbackSyncPath(opts, trackingBSGID)
			beforeIntent, _ := os.ReadFile(intentPath)
			stockPath := fallbackSyncFileForTest(t, filepath.Join(a.dir, "market-fallback"))
			beforeStock, _ := os.ReadFile(stockPath)
			if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID); err == nil || len(fake.writes) != 1 {
				t.Fatal("incomplete obsolete proof cleared or wrote pending stock")
			}
			afterIntent, _ := os.ReadFile(intentPath)
			afterStock, _ := os.ReadFile(stockPath)
			if !bytes.Equal(beforeIntent, afterIntent) || !bytes.Equal(beforeStock, afterStock) {
				t.Fatal("incomplete obsolete proof changed intent or independent fallback")
			}
		})
	}
}

func TestMarketFallbackSyncObsoleteIntentRemovalFailureKeepsPendingAndStock(t *testing.T) {
	a, fake, opts, _, _ := fallbackSyncPendingFixtureForTest(t)
	intentPath, _ := a.marketFallbackSyncPath(opts, trackingBSGID)
	stockPath := fallbackSyncFileForTest(t, filepath.Join(a.dir, "market-fallback"))
	beforeStock, _ := os.ReadFile(stockPath)
	fake.loadHook = func(int) (market.TrackedSnapshot, error) {
		// A napló már beolvasható volt, de a fájl lezárása most lemezhibás.
		if err := os.Remove(intentPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(intentPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(intentPath, "keep"), []byte("blocked"), 0o600); err != nil {
			t.Fatal(err)
		}
		current := cloneTrackedSnapshot(fake.snapshot)
		current.Revision = "external-revision"
		return current, nil
	}
	if _, _, err := a.syncMarketFallback(context.Background(), a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID); !errors.Is(err, errMarketFallbackSyncPending) || len(fake.writes) != 1 {
		t.Fatal("obsolete journal deletion failure allowed stock changes or another PATCH")
	}
	afterStock, _ := os.ReadFile(stockPath)
	if pending, err := a.marketFallbackSyncPending(opts); !pending || err == nil || !bytes.Equal(beforeStock, afterStock) {
		t.Fatal("failed obsolete intent cleanup consumed stock or silently released pending")
	}
}

func TestMarketFallbackSyncExplicitRetryPartialSettlementOnlyVerifiesAndPreservesUsed(t *testing.T) {
	for _, observed := range []string{"target", "before-new-revision"} {
		t.Run(observed, func(t *testing.T) {
			a, fake, opts, _, original := fallbackSyncPendingFixtureForTest(t)
			zero := localprogress.StockAmounts{}
			if _, err := a.marketFallbackStock().CorrectStock(opts.CharacterUID, "PVP", localprogress.ManualStockCorrection{ItemID: trackingBSGID, LocalAvailable: &zero}); err != nil {
				t.Fatal(err)
			}
			fake.snapshot.Revision = "external-revision"
			if observed == "target" {
				fake.snapshot.Items[0].AvailableQuantity = original.Target
			}
			_, _, err := a.retrySavedMarketFallback(context.Background(), a.apiKey, opts)
			if len(fake.writes) != 1 || (observed == "target" && err != nil) || (observed != "target" && !errors.Is(err, errMarketFallbackSyncPending)) {
				t.Fatalf("partial settlement issued PATCH or guessed a completed transfer: %v %+v", err, fake.writes)
			}
			stock, loadErr := a.marketFallbackStock().Load(opts.CharacterUID, "PVP")
			row, _ := stockEditorItem(stock, trackingBSGID)
			if loadErr != nil || row.LocalAvailableQuantity != 0 || row.LocalCollectedQuantity != 2 || row.LocalUsedQuantity != 2 || row.LocalUsedRegularQuantity != 1 || row.LocalUsedFIRQuantity != 1 {
				t.Fatalf("partial settlement changed typed usage: %+v %v", row, loadErr)
			}
		})
	}
}

func TestMarketFallbackSyncExplicitRetryContextFenceKeepsExistingIntent(t *testing.T) {
	for _, change := range []string{"key", "character", "settings", "cancel"} {
		t.Run(change, func(t *testing.T) {
			a, fake, opts, _, _ := fallbackSyncPendingFixtureForTest(t)
			path, _ := a.marketFallbackSyncPath(opts, trackingBSGID)
			before, _ := os.ReadFile(path)
			key := a.apiKey
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake.loadHook = func(int) (market.TrackedSnapshot, error) {
				switch change {
				case "key":
					a.apiKey = "another-key"
				case "character":
					a.state.CharacterUID = "another-character"
				case "settings":
					a.state.ItemOptions.revision++
				case "cancel":
					cancel()
				}
				return cloneTrackedSnapshot(fake.snapshot), nil
			}
			if _, _, err := a.retrySavedMarketFallback(base, key, opts); !errors.Is(err, context.Canceled) || len(fake.writes) != 1 {
				t.Fatalf("stale retry context wrote: %v %+v", err, fake.writes)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) || fallbackSyncAvailableForTest(t, a, opts) != (localprogress.StockAmounts{Regular: 2, FIR: 1, Unknown: 1}) {
				t.Fatal("stale retry deleted uncertain original intent or stock")
			}
		})
	}
}
