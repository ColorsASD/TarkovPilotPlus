package plus

import (
	"strings"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/nextitem"
)

func unknownQualitySnapshot(snapshot localprogress.Snapshot) localprogress.Snapshot {
	snapshot = localprogress.Clone(snapshot)
	snapshot.MetadataComplete = false
	for i := range snapshot.Items {
		snapshot.Items[i].QualityKnown, snapshot.Items[i].FIRQuantity, snapshot.Items[i].RegularQuantity = true, 0, 0
	}
	return snapshot
}

func markTypedAllocationUnknown(view *ItemTrackingState) {
	view.FIRQuantity, view.RegularQuantity, view.UncategorizedQuantity = 0, 0, view.AvailableQuantity
	view.AddFIRAllowed, view.AddRegularAllowed, view.RemoveFIRAllowed, view.RemoveRegularAllowed = false, false, false, false
	for i := range view.Requirements {
		view.Requirements[i].AvailableQuantity, view.Requirements[i].AllocationUnknown = 0, true
	}
	for i := range view.AnyGroups {
		view.AnyGroups[i].AvailableQuantity, view.AnyGroups[i].AllocationUnknown = 0, true
		for j := range view.AnyGroups[i].Sources {
			source := &view.AnyGroups[i].Sources[j]
			source.AvailableQuantity = 0
			for k := range source.Items {
				source.Items[k].AvailableQuantity = 0
			}
		}
	}
}

func applyTypedTrackingView(view *ItemTrackingState, row localprogress.Item, snapshot localprogress.Snapshot) {
	view.FIRQuantity, view.RegularQuantity = row.FIRQuantity, row.RegularQuantity
	view.UncategorizedQuantity = max(0, row.AvailableQuantity-row.FIRQuantity-row.RegularQuantity)
	view.RemoveFIRAllowed = view.RemoveAllowed && row.FIRQuantity > 0
	view.RemoveRegularAllowed = view.RemoveAllowed && row.RegularQuantity > 0
	typed := nextitem.CalculateTyped(snapshot, row.ItemID, true)
	view.NextItem = &typed.Forecast
	view.AddFIRAllowed, view.AddRegularAllowed = typed.Known && typed.AddFIRAllowed, typed.Known && typed.AddRegularAllowed
	view.AddAllowed = view.AddFIRAllowed || view.AddRegularAllowed
	for i := range view.Requirements {
		requirement := &view.Requirements[i]
		requirement.AvailableQuantity = typed.FixedStock.Regular
		if requirement.Kind == "fir" {
			requirement.AvailableQuantity = typed.FixedStock.FIR
		}
		requirement.AvailableQuantity = min(requirement.RequiredQuantity, requirement.AvailableQuantity)
		requirement.AllocationUnknown = !typed.Known
	}
	aliases := map[string]string{}
	for _, quest := range snapshot.MarketQuests {
		aliases[quest.QuestID] = quest.QuestID
		for _, alias := range quest.QuestIDAliases {
			aliases[alias] = quest.QuestID
		}
	}
	groupKey := func(id string) string {
		parts := strings.SplitN(id, ":", 3)
		if len(parts) == 3 && parts[0] == "quest" && aliases[parts[1]] != "" {
			return "quest:" + aliases[parts[1]] + ":" + parts[2]
		}
		return id
	}
	for i := range view.AnyGroups {
		group := &view.AnyGroups[i]
		required := group.RequiredQuantity
		group.AvailableQuantity, group.RequiredQuantity, group.AllocationUnknown = 0, 0, !typed.Known
		for j := range group.Sources {
			source := &group.Sources[j]
			for k := range source.Items {
				source.Items[k].AvailableQuantity = 0
			}
			allocated, exists := typed.Groups[groupKey(source.GroupID)]
			if !exists {
				group.AllocationUnknown = true
				source.AvailableQuantity = 0
			} else {
				source.AvailableQuantity, source.RequiredQuantity = allocated.AvailableQuantity, allocated.RequiredQuantity
				for k := range source.Items {
					source.Items[k].AvailableQuantity = allocated.Contributions[source.Items[k].ItemID]
				}
			}
			group.AvailableQuantity += source.AvailableQuantity
			group.RequiredQuantity += source.RequiredQuantity
		}
		if len(group.Sources) == 0 {
			group.RequiredQuantity = required
			if allocated, exists := typed.Groups[groupKey(group.GroupID)]; exists {
				group.AvailableQuantity, group.RequiredQuantity = allocated.AvailableQuantity, allocated.RequiredQuantity
			} else {
				group.AllocationUnknown = true
			}
		}
	}
}
