package plus

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestMarketFallbackPreWireCancellationRemovesOnlyNewIntent(t *testing.T) {
	for _, workflow := range []string{"local-first", "legacy"} {
		for _, stopped := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(workflow+"/"+stopped.Error(), func(t *testing.T) {
				a, fake, opts, mainDir := marketFallbackSyncFixture(t)
				fallbackPath := fallbackSyncFileForTest(t, filepath.Join(a.dir, "market-fallback"))
				mainPath := fallbackSyncFileForTest(t, mainDir)
				var fallbackBefore, mainBefore []byte
				calls := 0
				fake.writeContextHook = func(context.Context, stockWrite) (market.ProgressWriteResult, error) {
					calls++
					pending, err := a.readMarketFallbackSyncItem(opts, trackingBSGID)
					if err != nil || pending == nil {
						t.Fatal("write gate was entered without a durable intent", err)
					}
					fallbackBefore, _ = os.ReadFile(fallbackPath)
					mainBefore, _ = os.ReadFile(mainPath)
					// A kliens a hálózati kapu megszakítását raw context hibával
					// jelzi; itt nincs HTTP-kérés, így fake.writes üres marad.
					return market.ProgressWriteResult{}, stopped
				}
				if workflow == "legacy" {
					_, _, _ = a.syncMarketFallback(a.ctx, a.apiKey, opts, cloneTrackedSnapshot(fake.snapshot), trackingBSGID)
				} else {
					_ = a.processLocalFirstMarketFallback(a.ctx, a.apiKey, opts)
				}
				if calls != 1 || len(fake.writes) != 0 || fake.snapshot.Items[0].AvailableQuantity != 2 {
					t.Fatalf("pre-wire cancellation sent or repeated an API write: calls=%d writes=%+v", calls, fake.writes)
				}
				if pending, err := a.readMarketFallbackSyncItem(opts, trackingBSGID); err != nil || pending != nil {
					t.Fatalf("known unsent intent remained pending: %+v %v", pending, err)
				}
				fallbackAfter, _ := os.ReadFile(fallbackPath)
				mainAfter, _ := os.ReadFile(mainPath)
				if !bytes.Equal(fallbackBefore, fallbackAfter) || !bytes.Equal(mainBefore, mainAfter) {
					t.Fatal("known unsent cancellation changed typed stock, usage, or receipts")
				}
			})
		}
	}
}

func TestLocalFirstMarketFallbackDefiniteRejectionClearsOldProfileIntentAfterContextChange(t *testing.T) {
	for _, change := range []string{"key", "character", "settings", "canceled"} {
		t.Run(change, func(t *testing.T) {
			a, fake, opts, mainDir := marketFallbackSyncFixture(t)
			key := a.apiKey
			ctx, cancel := context.WithCancel(a.ctx)
			defer cancel()
			fallbackPath := fallbackSyncFileForTest(t, filepath.Join(a.dir, "market-fallback"))
			mainPath := fallbackSyncFileForTest(t, mainDir)
			var fallbackBefore, mainBefore []byte
			fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
				fallbackBefore, _ = os.ReadFile(fallbackPath)
				mainBefore, _ = os.ReadFile(mainPath)
				a.mu.Lock()
				switch change {
				case "key":
					a.apiKey = "other-test-key"
				case "character":
					a.state.CharacterUID = "character-b"
					a.state.ItemOptions.CharacterUID = "character-b"
				case "settings":
					a.state.ItemOptions.revision++
				case "canceled":
					cancel()
				}
				a.mu.Unlock()
				return market.ProgressWriteResult{}, market.ErrRateLimited
			}
			if err := a.processLocalFirstMarketFallback(ctx, key, opts); !errors.Is(err, context.Canceled) {
				t.Fatalf("stale upload published into the new context: %v", err)
			}
			if pending, err := a.readMarketFallbackSyncItem(opts, trackingBSGID); err != nil || pending != nil {
				t.Fatalf("definitively rejected old-profile upload remained pending: %+v %v", pending, err)
			}
			fallbackAfter, _ := os.ReadFile(fallbackPath)
			mainAfter, _ := os.ReadFile(mainPath)
			if len(fake.writes) != 1 || fake.loads != 1 || !bytes.Equal(fallbackBefore, fallbackAfter) || !bytes.Equal(mainBefore, mainAfter) {
				t.Fatalf("definite rejection retried/read back or changed stock/usage: writes=%+v loads=%d", fake.writes, fake.loads)
			}
		})
	}
}

func TestLocalFirstMarketFallbackUncertainCancellationKeepsCapturedIntent(t *testing.T) {
	a, fake, opts, mainDir := marketFallbackSyncFixture(t)
	fallbackPath := fallbackSyncFileForTest(t, filepath.Join(a.dir, "market-fallback"))
	mainPath := fallbackSyncFileForTest(t, mainDir)
	var fallbackBefore, mainBefore []byte
	fake.writeHook = func(stockWrite) (market.ProgressWriteResult, error) {
		fallbackBefore, _ = os.ReadFile(fallbackPath)
		mainBefore, _ = os.ReadFile(mainPath)
		return market.ProgressWriteResult{}, errors.Join(market.ErrProgressWriteUncertain, context.DeadlineExceeded)
	}
	if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, opts); !errors.Is(err, errMarketFallbackSyncPending) {
		t.Fatalf("sent cancellation was treated as definitely unapplied: %v", err)
	}
	pending, err := a.readMarketFallbackSyncItem(opts, trackingBSGID)
	if err != nil || pending == nil || pending.Version != 2 {
		t.Fatalf("uncertain upload lost its captured intent: %+v %v", pending, err)
	}
	fake.writeHook = nil
	if err := a.processLocalFirstMarketFallback(a.ctx, a.apiKey, opts); !errors.Is(err, errMarketFallbackSyncPending) || len(fake.writes) != 1 {
		t.Fatalf("automatic worker replayed uncertain cancellation: %v %+v", err, fake.writes)
	}
	fallbackAfter, _ := os.ReadFile(fallbackPath)
	mainAfter, _ := os.ReadFile(mainPath)
	if !bytes.Equal(fallbackBefore, fallbackAfter) || !bytes.Equal(mainBefore, mainAfter) {
		t.Fatal("uncertain cancellation changed captured stock, quality, or usage")
	}
}

func TestMarketFallbackRetryRejectionDoesNotEraseOriginalUncertainty(t *testing.T) {
	a, fake, opts, _ := pendingFallbackRecoveryFixture(t)
	pending, err := a.readMarketFallbackSyncItem(opts, trackingBSGID)
	if err != nil || pending == nil {
		t.Fatal("pending fixture is missing", err)
	}
	path, _ := a.marketFallbackSyncPath(opts, pending.ItemID)
	before, _ := os.ReadFile(path)
	fake.writeContextHook = func(context.Context, stockWrite) (market.ProgressWriteResult, error) {
		return market.ProgressWriteResult{}, context.DeadlineExceeded
	}
	if _, _, err := a.retrySavedMarketFallback(a.ctx, a.apiKey, opts); !errors.Is(err, errMarketFallbackSyncPending) {
		t.Fatalf("retry cancellation erased original uncertainty: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("definite retry rejection changed the original uncertain intent")
	}
}
