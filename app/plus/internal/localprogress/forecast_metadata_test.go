package localprogress

import (
	"reflect"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestMergeRetainsNewGoalMetadataAndCloneOwnsAliases(t *testing.T) {
	tracked, quests, hideout := inputs()
	quests.Quests = []market.Quest{{QuestID: "new-quest-abc", QuestIDAliases: []string{"new-alias-abc"}, Name: "New API quest", Status: "available"}}
	hideout.Modules = []market.HideoutUpgrade{{UpgradeKey: "New station_7", Name: "New station", Level: 7, CanComplete: true}}
	merged, err := Merge(tracked, quests, hideout)
	if err != nil {
		t.Fatal(err)
	}
	if !merged.MetadataComplete || !reflect.DeepEqual(merged.MarketQuests, quests.Quests) || !reflect.DeepEqual(merged.MarketHideout, hideout.Modules) {
		t.Fatalf("new API goals were not retained: %+v", merged)
	}
	quests.Quests[0].QuestIDAliases[0] = "mutated-original"
	hideout.Modules[0].CanComplete = false
	if merged.MarketQuests[0].QuestIDAliases[0] != "new-alias-abc" || !merged.MarketHideout[0].CanComplete {
		t.Fatal("merge aliases original metadata")
	}
	copy := Clone(merged)
	copy.MarketQuests[0].QuestIDAliases[0] = "mutated-clone"
	copy.MarketQuests[0].Status = "completed"
	copy.MarketHideout[0].CanComplete = false
	if merged.MarketQuests[0].QuestIDAliases[0] != "new-alias-abc" || merged.MarketQuests[0].Status != "available" || !merged.MarketHideout[0].CanComplete {
		t.Fatal("clone aliases source metadata")
	}
}
