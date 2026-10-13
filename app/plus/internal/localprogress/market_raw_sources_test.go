package localprogress

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

func TestMarketRawRequirementsCloneIsIndependentAndNeverSerialized(t *testing.T) {
	snapshot := Snapshot{Items: []Item{{ItemID: "item-a", MarketRawSources: []market.TrackedSource{{QuestID: "RAW-QUEST-ONLY", FIR: true}}}}}
	copy := Clone(snapshot)
	copy.Items[0].MarketRawSources[0].FIR = false
	if !snapshot.Items[0].MarketRawSources[0].FIR {
		t.Fatal("raw requirement clone aliases the confirmed snapshot")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "RAW-QUEST-ONLY") || strings.Contains(string(encoded), "MarketRawSources") {
		t.Fatal("memory-only raw Market requirements were serialized")
	}
	if Clone(Snapshot{Items: []Item{{ItemID: "unknown"}}}).Items[0].MarketRawSources != nil {
		t.Fatal("clone turned unknown raw requirements into confirmed empty sources")
	}
}
