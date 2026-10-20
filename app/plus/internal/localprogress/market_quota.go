package localprogress

import "sort"

func marketQuotaSource(source Source) bool {
	if source.AllocationKnown {
		return true
	}
	// Egyetlen egyértelmű jelölt mellett nincs helyi Any-kiosztás. Az
	// explicit Market-projekció kvótája ettől még használhatja a másik,
	// saját forrással igazolt változat megerősített hozzájárulását.
	return source.MarketGroupProgressKnown && source.Alternatives == 1 &&
		source.GroupAmount == source.Amount && sourceKind(source.TrackedSource) == "quest" &&
		source.GroupID == "quest:"+source.QuestID+":"+source.RequirementID
}

func usesReportedMarketQuota(item Item) bool {
	known := false
	for _, source := range item.Sources {
		if !active(source) || !marketQuotaSource(source) {
			continue
		}
		if !source.MarketGroupProgressKnown {
			return false
		}
		known = true
	}
	return known
}

// A saját tárgy céljából csak a többi jelölt hozzájárulását vonjuk le.
// A saját készlet a Stock számlálójában szerepel, ezért itt nem csökkentheti
// még egyszer a célt. Más tárgy fixed igényére nem végzünk keresztfélretételt.
func reportedMarketQuota(snapshot Snapshot, itemIndex int) (int64, error) {
	item := snapshot.Items[itemIndex]
	groups := make(map[string]Source)
	var required int64
	for _, source := range item.Sources {
		if !active(source) {
			continue
		}
		if marketQuotaSource(source) && source.MarketGroupProgressKnown {
			if _, exists := groups[source.GroupID]; exists {
				continue
			}
			groups[source.GroupID] = source
		}
		if err := addQuantity(&required, source.RemainingRequiredQuantity); err != nil {
			return 0, err
		}
	}
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows := countRows(snapshot)
	// A confirmed és pending kapacitás külön utat kap. Az API reported
	// számlálója csak a confirmed rész felső korlátja; a még nem mentett
	// darabok a gyűjtési korlátot már most csökkentik.
	base := 1 + 2*len(snapshot.Items)
	sink := base + 2*len(ids)
	flow := newAllocationFlow(sink + 1)
	confirmedGroup, totalGroup := make(map[string]int), make(map[string]int)
	remainingEdges := make(map[string]int)
	for i, id := range ids {
		source := groups[id]
		confirmedGroup[id], totalGroup[id] = base+2*i, base+2*i+1
		flow.add(confirmedGroup[id], totalGroup[id], min(source.RemainingRequiredQuantity, source.MarketGroupAvailableQuantity))
		remainingEdges[id] = flow.add(totalGroup[id], sink, source.RemainingRequiredQuantity)
	}
	for i, other := range snapshot.Items {
		if i == itemIndex {
			continue
		}
		confirmed, pending := min(other.AvailableQuantity, other.MarketAvailableQuantity), max(0, other.AvailableQuantity-other.MarketAvailableQuantity)
		confirmedNode, pendingNode := 1+2*i, 2+2*i
		flow.add(0, confirmedNode, confirmed)
		if rows[other.ItemID] != 1 {
			// A változat saját API-készlete csak az eredeti forrásával és az
			// explicit csoporttagsággal igazolt közös célból vonható le.
			// Helyi hozzáadást nem számolunk, és a sor nem kap kiosztást.
			for _, id := range confirmedVariantGroups(snapshot, other, groups) {
				flow.add(confirmedNode, confirmedGroup[id], groups[id].RemainingRequiredQuantity)
			}
			continue
		}
		flow.add(0, pendingNode, pending)
		seen := make(map[string]bool)
		for _, source := range other.Sources {
			group, exists := groups[source.GroupID]
			if !exists || seen[source.GroupID] || !active(source) || !source.AllocationKnown {
				continue
			}
			seen[source.GroupID] = true
			flow.add(confirmedNode, confirmedGroup[source.GroupID], group.RemainingRequiredQuantity)
			flow.add(pendingNode, totalGroup[source.GroupID], group.RemainingRequiredQuantity)
		}
	}
	flow.run(0, sink)
	for _, id := range ids {
		group := groups[id]
		uncovered := flow.edges[totalGroup[id]][remainingEdges[id]].capacity
		required -= group.RemainingRequiredQuantity - uncovered
	}
	return required, nil
}

func confirmedVariantGroups(snapshot Snapshot, item Item, groups map[string]Source) []string {
	ids := make([]string, 0)
	seen := make(map[string]bool)
	for _, source := range item.Sources {
		if !active(source) || source.Origin != OriginMarket || sourceKind(source.TrackedSource) != "quest" || source.GroupID != "" {
			continue
		}
		id := "quest:" + source.QuestID + ":" + source.RequirementID
		target, exists := groups[id]
		if !exists || seen[id] || source.QuestID != target.QuestID || source.RequirementID != target.RequirementID || source.FIR != target.FIR {
			continue
		}
		for _, group := range snapshot.MarketGroups {
			if group.QuestID != source.QuestID || group.RequirementID != source.RequirementID || group.FIR != source.FIR || group.Optional || group.Completed || group.Canceled {
				continue
			}
			// Az eredeti csoport és a célra vetített API-adat ugyanabból a
			// követelményből származzon; név vagy puszta itemId nem elég.
			if group.Amount != target.Amount || group.RequiredQuantity != target.RemainingRequiredQuantity || group.AvailableQuantity != target.MarketGroupAvailableQuantity {
				continue
			}
			for _, member := range group.ItemIDs {
				if member == item.ItemID {
					ids = append(ids, id)
					seen[id] = true
					break
				}
			}
			if seen[id] {
				break
			}
		}
	}
	sort.Strings(ids)
	return ids
}
