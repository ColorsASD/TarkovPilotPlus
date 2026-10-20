package plus

import "github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"

// populateVariantTrackingView az egyes API-rekordok megjelenítését tölti ki.
// A közös ID miatt tiltott írást és a készletkiosztást nem módosítja.
func populateVariantTrackingView(view *ItemTrackingState, row localprogress.Item, snapshot localprogress.Snapshot, confirmed localprogress.RequirementStock) {
	view.Storage = "market"
	view.MarketAvailableQuantity = row.MarketAvailableQuantity
	view.MarketRequiredQuantity = row.MarketRequiredQuantity
	view.MarketTracked = row.MarketTracked
	view.AddAllowed, view.RemoveAllowed = false, false
	view.Requirements = []ItemTrackingRequirement{}
	view.AnyGroups = []ItemTrackingAnyGroup{}
	view.CompletedSources = []ItemTrackingCompletedSource{}
	var firRequired, regularRequired int64
	seenGroups := make(map[string]bool)
	for _, source := range row.Sources {
		if source.Canceled {
			continue
		}
		shared := false
		for _, group := range snapshot.MarketGroups {
			if !localprogress.MarketGroupSourceMatches(row, source, group) {
				continue
			}
			shared = true
			id := "quest:" + group.QuestID + ":" + group.RequirementID
			if seenGroups[id] || group.Canceled {
				continue
			}
			if source.Completed || group.Completed {
				seenGroups[id] = true
				view.CompletedSources = append(view.CompletedSources, ItemTrackingCompletedSource{
					Type: completedSourceType(source.TrackedSource), Quantity: group.Amount, Optional: source.Optional || group.Optional,
				})
				continue
			}
			if source.Optional || group.Optional || group.RequiredQuantity <= 0 {
				continue
			}
			seenGroups[id] = true
			view.AnyGroups = append(view.AnyGroups, ItemTrackingAnyGroup{
				GroupID: id, FIR: group.FIR, RequiredQuantity: group.RequiredQuantity,
				AvailableQuantity: min(group.AvailableQuantity, group.RequiredQuantity),
				Sources: []ItemTrackingAnySource{{GroupID: id, QuestName: group.QuestName, Category: group.Name,
					RequiredQuantity: group.RequiredQuantity, AvailableQuantity: min(group.AvailableQuantity, group.RequiredQuantity),
					Items: sharedTrackingItems(snapshot, id)}},
			})
		}
		if shared {
			continue
		}
		if source.Completed {
			view.CompletedSources = append(view.CompletedSources, ItemTrackingCompletedSource{
				Type: completedSourceType(source.TrackedSource), Quantity: source.Amount, Optional: source.Optional,
			})
			continue
		}
		if source.Optional {
			continue
		}
		if source.FIR {
			firRequired += source.RemainingRequiredQuantity
		} else {
			regularRequired += source.RemainingRequiredQuantity
		}
	}
	// Minden változat saját készlete vesz részt a közös, megerősített
	// kiosztásban; a másik azonos ID-jű sor készletét nem vesszük át.
	if firRequired > 0 {
		view.Requirements = append(view.Requirements, ItemTrackingRequirement{Kind: "fir", RequiredQuantity: firRequired,
			AvailableQuantity: min(firRequired, confirmed.FIR), AllocationUnknown: !confirmed.Known})
	}
	if regularRequired > 0 {
		view.Requirements = append(view.Requirements, ItemTrackingRequirement{Kind: "regular", RequiredQuantity: regularRequired,
			AvailableQuantity: min(regularRequired, confirmed.Regular), AllocationUnknown: !confirmed.Known})
	}
	view.Requirement = "unknown"
	switch {
	case firRequired > 0 && regularRequired > 0:
		view.Requirement = "mixed"
	case firRequired > 0:
		view.Requirement = "fir"
	case regularRequired > 0:
		view.Requirement = "regular"
	}
}
