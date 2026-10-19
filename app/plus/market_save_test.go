package plus

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func addMarketKindForTest(t *testing.T, a *ItemWindowApp, uid, kind string) ItemState {
	t.Helper()
	if kind == "fir" {
		a.AddTrackedItemFIR(uid)
	} else if kind == "regular" {
		a.AddTrackedItem(uid)
	} else {
		t.Fatalf("invalid test item quality %q", kind)
	}
	waitPendingForTest(t, a)
	return a.GetState()
}

func memoryMarketStockForTest(t *testing.T, a *ItemWindowApp, id string) localprogress.Item {
	t.Helper()
	for _, row := range a.localSnapshot.Items {
		if row.ItemID == id {
			return row
		}
	}
	t.Fatalf("Market item %s is absent from the in-memory snapshot", id)
	return localprogress.Item{}
}

func assertNoMarketStockFilesForTest(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Base(path) != "item-settings.json" {
			t.Errorf("Market-only operation created a local stock file: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type marketProgressReadFailureForTest struct {
	*fakeCharacterProgress
	stage string
}

func (f *marketProgressReadFailureForTest) LoadQuests(ctx context.Context, key, uid, language string) (market.QuestSnapshot, error) {
	if f.stage == "quests" {
		return market.QuestSnapshot{}, market.ErrProgressUnavailable
	}
	return f.fakeCharacterProgress.LoadQuests(ctx, key, uid, language)
}

func (f *marketProgressReadFailureForTest) LoadHideout(ctx context.Context, key, uid, language string) (market.HideoutSnapshot, error) {
	if f.stage == "hideout" {
		return market.HideoutSnapshot{}, market.ErrProgressUnavailable
	}
	return f.fakeCharacterProgress.LoadHideout(ctx, key, uid, language)
}

func (a *ItemWindowApp) recordLocalMarketAdd(key string, opts ItemOptions, item market.Item, kind string, before, after int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.localCurrentLocked(key, opts) || after != before+1 || before < 0 || after > localprogress.MaxQuantity || (kind != "fir" && kind != "regular") {
		return
	}
	if a.marketKinds == nil {
		a.marketKinds = make(map[string]marketKindState)
	}
	id := marketKindKey(opts.CharacterUID, opts.Mode, item.BSGID)
	known := a.marketKinds[id]
	if known.stock != before {
		known = marketKindState{}
	}
	known.stock = after
	if kind == "fir" {
		known.fir++
	} else {
		known.regular++
	}
	a.marketKinds[id] = known
}
