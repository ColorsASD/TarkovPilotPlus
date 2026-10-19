package plus

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func addMarketItemForTest(t *testing.T, a *ItemWindowApp, uid string) ItemState {
	t.Helper()
	a.AddTrackedItem(uid)
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
