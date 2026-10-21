package plus

import (
	"sort"
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
	view.QualityKnown = false
	view.FIRQuantity, view.RegularQuantity, view.UncategorizedQuantity = 0, 0, view.AvailableQuantity
	view.AddFIRAllowed, view.AddRegularAllowed, view.RemoveFIRAllowed, view.RemoveRegularAllowed = false, false, false, false
	for i := range view.Requirements {
		view.Requirements[i].AvailableQuantity, view.Requirements[i].AllocationUnknown = 0, true
		view.Requirements[i].FIRQuantity = 0
	}
	for i := range view.AnyGroups {
		view.AnyGroups[i].AvailableQuantity, view.AnyGroups[i].AllocationUnknown = 0, true
		view.AnyGroups[i].OtherStock = nil
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
	applyTypedTrackingResult(view, row, snapshot, nextitem.CalculateTypedForDisplay(snapshot, row.ItemID, true))
}

func applyTypedTrackingRow(view *ItemTrackingState, row localprogress.Item, snapshot localprogress.Snapshot, index int) {
	applyTypedTrackingResult(view, row, snapshot, nextitem.CalculateTypedRowForDisplay(snapshot, index, true))
	view.AddAllowed, view.RemoveAllowed = false, false
	view.AddFIRAllowed, view.AddRegularAllowed, view.RemoveFIRAllowed, view.RemoveRegularAllowed = false, false, false, false
}

func applyTypedTrackingResult(view *ItemTrackingState, row localprogress.Item, snapshot localprogress.Snapshot, typed nextitem.TypedView) {
	view.QualityKnown = typed.Known
	if typed.Known {
		view.RequiredQuantity = typed.RequiredQuantity
		view.RemainingQuantity = max(0, typed.RequiredQuantity-row.AvailableQuantity)
	}
	view.FIRQuantity, view.RegularQuantity = row.FIRQuantity, row.RegularQuantity
	view.UncategorizedQuantity = max(0, row.AvailableQuantity-row.FIRQuantity-row.RegularQuantity)
	view.RemoveFIRAllowed = view.RemoveAllowed && row.FIRQuantity > 0
	view.RemoveRegularAllowed = view.RemoveAllowed && row.RegularQuantity > 0
	view.NextItem = &typed.Forecast
	view.AddFIRAllowed, view.AddRegularAllowed = typed.Known && typed.AddFIRAllowed, typed.Known && typed.AddRegularAllowed
	view.AddAllowed = view.AddFIRAllowed || view.AddRegularAllowed
	summary := typed.StockSummary
	if summary == nil {
		summary = &nextitem.TypedStockSummary{}
	}
	for i := range view.Requirements {
		requirement := &view.Requirements[i]
		requirement.FIRQuantity = 0
		requirement.AvailableQuantity = summary.FixedStock.Regular
		if requirement.Kind == "fir" {
			requirement.AvailableQuantity = summary.FixedStock.FIR
		}
		requirement.AvailableQuantity = min(requirement.RequiredQuantity, requirement.AvailableQuantity)
		requirement.AllocationUnknown = !typed.Known
		if requirement.Kind == "regular" && typed.Known {
			requirement.FIRQuantity = min(requirement.AvailableQuantity, summary.RegularFIRQuantity)
		}
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
		group.OtherStock = nil
		for j := range group.Sources {
			source := &group.Sources[j]
			for k := range source.Items {
				source.Items[k].AvailableQuantity = 0
			}
			allocated, exists := summary.Groups[groupKey(source.GroupID)]
			if !exists {
				group.AllocationUnknown = true
				source.AvailableQuantity = 0
			} else {
				source.AvailableQuantity, source.RequiredQuantity = allocated.AvailableQuantity, allocated.RequiredQuantity
				group.OtherStock = append(group.OtherStock, allocated.OtherStock...)
				applyTypedGroupContributions(source.Items, allocated, snapshot)
			}
			group.AvailableQuantity += source.AvailableQuantity
			group.RequiredQuantity += source.RequiredQuantity
		}
		if len(group.Sources) == 0 {
			group.RequiredQuantity = required
			if allocated, exists := summary.Groups[groupKey(group.GroupID)]; exists {
				group.AvailableQuantity, group.RequiredQuantity = allocated.AvailableQuantity, allocated.RequiredQuantity
				group.OtherStock = append(group.OtherStock, allocated.OtherStock...)
			} else {
				group.AllocationUnknown = true
			}
		}
		if group.AllocationUnknown {
			group.OtherStock = nil
		} else {
			group.OtherStock = mergedTrackingOtherStock(group.OtherStock)
		}
	}
}

func mergedTrackingOtherStock(items []nextitem.StockContribution) []nextitem.StockContribution {
	type identity struct{ id, name, icon string }
	quantities := make(map[identity]nextitem.StockContribution)
	for _, item := range items {
		key := identity{item.ItemID, item.Name, item.Icon}
		entry := quantities[key]
		entry.ItemID, entry.Name, entry.Icon = item.ItemID, item.Name, item.Icon
		entry.FIRQuantity += item.FIRQuantity
		entry.RegularQuantity += item.RegularQuantity
		quantities[key] = entry
	}
	var out []nextitem.StockContribution
	for _, entry := range quantities {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].ItemID != out[j].ItemID {
			return out[i].ItemID < out[j].ItemID
		}
		return out[i].Icon < out[j].Icon
	})
	return out
}

func applyTypedGroupContributions(items []ItemTrackingAnyItem, group nextitem.TypedGroupStock, snapshot localprogress.Snapshot) {
	type identity struct{ id, name string }
	quantities := make(map[identity][]int64)
	for index, row := range snapshot.Items {
		quantity, member := group.RowContributions[index]
		if !member || !row.MarketTracked || row.MarketAvailableQuantity <= 0 {
			continue
		}
		name := strings.TrimSpace(row.Name)
		if name == "" {
			name = row.ItemID
		}
		key := identity{row.ItemID, name}
		quantities[key] = append(quantities[key], quantity)
	}
	// sharedTrackingItems név szerint stabilan rendez, azonos ID és név
	// esetén az eredeti fizikai sorsorrend marad meg. Így az azonos-ID
	// változatok egyenként a saját, akár nulla hozzájárulásukat kapják.
	for i := range items {
		key := identity{items[i].ItemID, items[i].Name}
		items[i].AvailableQuantity = 0
		if values := quantities[key]; len(values) > 0 {
			items[i].AvailableQuantity, quantities[key] = values[0], values[1:]
		}
	}
}
