package plus

import (
	"slices"
	"sort"
	"strings"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
)

// sharedTrackingItems a csoporthoz egyértelműen kapcsolt, megerősített készletet
// sorolja fel. Nem osztja ki újra az API közös előrehaladását a jelöltek között.
func sharedTrackingItems(snapshot localprogress.Snapshot, groupID string) []ItemTrackingAnyItem {
	var items []ItemTrackingAnyItem
	for _, group := range snapshot.MarketGroups {
		if groupID != "quest:"+group.QuestID+":"+group.RequirementID || group.Completed || group.Canceled || group.Optional {
			continue
		}
		for _, row := range snapshot.Items {
			if !row.MarketTracked || row.MarketAvailableQuantity <= 0 || !slices.Contains(group.ItemIDs, row.ItemID) {
				continue
			}
			for _, source := range row.Sources {
				if source.Canceled || source.Completed || source.Optional || !localprogress.MarketGroupSourceMatches(row, source, group) {
					continue
				}
				name := strings.TrimSpace(row.Name)
				if name == "" {
					name = row.ItemID
				}
				items = append(items, ItemTrackingAnyItem{ItemID: row.ItemID, Name: name, AvailableQuantity: row.MarketAvailableQuantity})
				break
			}
		}
		break
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

// Ha a többértelmű készletváltozatok mellett egyetlen címezhető jelölt marad,
// az explicit Market csoportsor akkor is megjeleníthető. Ez nem készletkiosztás.
func singleCandidateTrackingGroup(source localprogress.Source, row localprogress.Item, snapshot localprogress.Snapshot) *localTrackingDisplayGroup {
	if source.Alternatives != 1 || !source.MarketGroupProgressKnown || source.GroupID == "" || source.GroupAmount != source.Amount {
		return nil
	}
	for _, group := range snapshot.MarketGroups {
		id := "quest:" + group.QuestID + ":" + group.RequirementID
		if source.GroupID != id || !localprogress.MarketGroupSourceMatches(row, source, group) ||
			source.Amount != group.Amount || source.MarketGroupAvailableQuantity != group.AvailableQuantity ||
			group.Canceled || group.Optional || source.Canceled || source.Optional || source.Completed != group.Completed ||
			!group.Completed && source.RemainingRequiredQuantity != group.RequiredQuantity {
			continue
		}
		members := append([]string(nil), group.ItemIDs...)
		sort.Strings(members)
		return &localTrackingDisplayGroup{id: id, kind: "quest", questName: group.QuestName, category: group.Name,
			fir: group.FIR, completed: group.Completed, count: source.RemainingRequiredQuantity, amount: group.Amount,
			remaining: max(0, group.RequiredQuantity-group.AvailableQuantity), marketProgressKnown: true, marketAvailable: group.AvailableQuantity,
			candidates: strings.Join(members, "\x00"), valid: true}
	}
	return nil
}
