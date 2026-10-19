package localprogress

import "sort"

type allocationPosition struct{ item, source int }

type allocationGroup struct {
	id           string
	positions    []allocationPosition
	alternatives int
	quantity     int64
	amount       int64
	groupAmount  int64
	fir          bool
	kind         string
	valid        bool
	remaining    int64
}

// Allocate kizárólag az élő gyűjtési nézethez osztja ki a közös készletet.
// A kötelező követelményeket előbb lefoglalja, majd a maradékból maximálisan
// csökkenti az Any csoportok közös hiányát.
// A nyers követelmény, a készlet, a FiR feltétel és a teljesítés nem változik.
func Allocate(snapshot Snapshot) (Snapshot, error) {
	out := Clone(snapshot)
	resetAllocation(&out)
	if err := validateSnapshot(out); err != nil {
		return Snapshot{}, err
	}
	rows := countRows(out)
	safeRows := make([]bool, len(out.Items))
	for itemIndex, item := range out.Items {
		var rawTotal int64
		for _, source := range item.Sources {
			if active(source) {
				if err := addQuantity(&rawTotal, source.RemainingRequiredQuantity); err != nil {
					return Snapshot{}, err
				}
			}
		}
		safeRows[itemIndex] = rows[item.ItemID] == 1 && (len(item.Sources) == 0 || rawTotal == item.RequiredQuantity)
	}
	groups := make(map[string]*allocationGroup)
	for itemIndex, item := range out.Items {
		for sourceIndex, source := range item.Sources {
			if source.GroupID == "" {
				continue
			}
			group, exists := groups[source.GroupID]
			if !exists {
				group = &allocationGroup{id: source.GroupID, alternatives: source.Alternatives, quantity: source.RemainingRequiredQuantity, amount: source.Amount, groupAmount: source.GroupAmount, fir: source.FIR, kind: sourceKind(source.TrackedSource), valid: source.Alternatives > 1}
				groups[source.GroupID] = group
			}
			group.positions = append(group.positions, allocationPosition{itemIndex, sourceIndex})
			if !safeRows[itemIndex] || source.Alternatives != group.alternatives || source.RemainingRequiredQuantity != group.quantity || source.Amount != group.amount || source.GroupAmount != group.groupAmount || (source.GroupAmount > 0 && source.GroupAmount != source.Amount) || source.FIR != group.fir || sourceKind(source.TrackedSource) != group.kind || group.kind == "" || !active(source) || source.RemainingRequiredQuantity == 0 || source.RemainingRequiredQuantity > source.Amount {
				group.valid = false
			}
		}
	}
	for _, group := range groups {
		seen := make(map[string]bool)
		for _, position := range group.positions {
			id := out.Items[position.item].ItemID
			if seen[id] {
				group.valid = false
			}
			seen[id] = true
		}
		if len(seen) != group.alternatives {
			group.valid = false
		}
	}

	// A hiányos vagy többértelmű csoportot közönséges, kötelező igényként
	// foglaljuk le. Így a bizonytalan darabok nem fednek le más Any csoportot.
	fixed := make([]int64, len(out.Items))
	fixedMarket := make([]int64, len(out.Items))
	fixedUnmet := make([]int64, len(out.Items))
	fixedFIRAvailable := make([]int64, len(out.Items))
	fixedRegularAvailable := make([]int64, len(out.Items))
	remainingRegular := make([]int64, len(out.Items))
	remainingQualifying := make([]int64, len(out.Items))
	groupIDs := make([]string, 0, len(groups))
	for id, group := range groups {
		if group.valid {
			groupIDs = append(groupIDs, id)
		}
	}
	sort.Strings(groupIDs)
	for itemIndex, item := range out.Items {
		var fixedRegular, fixedFIR int64
		if len(item.Sources) == 0 {
			fixed[itemIndex] = item.RequiredQuantity
			fixedRegular = item.RequiredQuantity
			fixedMarket[itemIndex] = item.MarketRequiredQuantity
		}
		for _, source := range item.Sources {
			if !active(source) {
				continue
			}
			if group := groups[source.GroupID]; source.Alternatives > 1 && group != nil && group.valid {
				continue
			}
			if err := addQuantity(&fixed[itemIndex], source.RemainingRequiredQuantity); err != nil {
				return Snapshot{}, err
			}
			if source.FIR {
				if err := addQuantity(&fixedFIR, source.RemainingRequiredQuantity); err != nil {
					return Snapshot{}, err
				}
			} else if err := addQuantity(&fixedRegular, source.RemainingRequiredQuantity); err != nil {
				return Snapshot{}, err
			}
			if source.Origin == OriginMarket {
				if err := addQuantity(&fixedMarket[itemIndex], source.RemainingRequiredQuantity); err != nil {
					return Snapshot{}, err
				}
			}
		}
		// A korábbi, típus nélküli készlet továbbra alkalmas kapacitás.
		// Az új, ismert Regular darab csak nem-FiR követelményt fedezhet.
		regular := item.MarketRegularQuantity
		qualifying := item.AvailableQuantity - regular
		firAssigned := min(qualifying, fixedFIR)
		qualifying -= firAssigned
		regularAssigned := min(regular, fixedRegular)
		regular -= regularAssigned
		qualifyingAssigned := min(qualifying, fixedRegular-regularAssigned)
		qualifying -= qualifyingAssigned
		fixedUnmet[itemIndex] = fixedFIR - firAssigned + fixedRegular - regularAssigned - qualifyingAssigned
		fixedFIRAvailable[itemIndex], fixedRegularAvailable[itemIndex] = firAssigned, regularAssigned+qualifyingAssigned
		remainingRegular[itemIndex], remainingQualifying[itemIndex] = regular, qualifying
	}
	itemIndexes := make([]int, 0, len(out.Items))
	for i, item := range out.Items {
		if rows[item.ItemID] == 1 {
			itemIndexes = append(itemIndexes, i)
		}
	}
	sort.Slice(itemIndexes, func(i, j int) bool { return out.Items[itemIndexes[i]].ItemID < out.Items[itemIndexes[j]].ItemID })
	type buckets struct{ regular, qualifying int }
	itemNodes := make(map[int]buckets, len(itemIndexes))
	groupNodes := make(map[string]int, len(groupIDs))
	for i, itemIndex := range itemIndexes {
		itemNodes[itemIndex] = buckets{regular: i + 1, qualifying: len(itemIndexes) + i + 1}
	}
	for i, groupID := range groupIDs {
		groupNodes[groupID] = 2*len(itemIndexes) + i + 1
	}
	sink := 2*len(itemIndexes) + len(groupIDs) + 1
	flow := newAllocationFlow(sink + 1)
	for _, itemIndex := range itemIndexes {
		flow.add(0, itemNodes[itemIndex].regular, remainingRegular[itemIndex])
	}
	for _, itemIndex := range itemIndexes {
		flow.add(0, itemNodes[itemIndex].qualifying, remainingQualifying[itemIndex])
	}
	type assignment struct {
		position allocationPosition
		node     int
		edge     int
		quantity int64
	}
	assignments := make([]assignment, 0)
	groupEdges := make(map[string]int, len(groupIDs))
	for _, groupID := range groupIDs {
		group := groups[groupID]
		positions := append([]allocationPosition{}, group.positions...)
		sort.Slice(positions, func(i, j int) bool { return out.Items[positions[i].item].ItemID < out.Items[positions[j].item].ItemID })
		for _, position := range positions {
			nodes := itemNodes[position.item]
			if !group.fir {
				edge := flow.add(nodes.regular, groupNodes[groupID], group.quantity)
				assignments = append(assignments, assignment{position: position, node: nodes.regular, edge: edge, quantity: group.quantity})
			}
			edge := flow.add(nodes.qualifying, groupNodes[groupID], group.quantity)
			assignments = append(assignments, assignment{position: position, node: nodes.qualifying, edge: edge, quantity: group.quantity})
		}
		capacity := int64(0)
		if group.fir {
			capacity = group.quantity
		}
		groupEdges[groupID] = flow.add(groupNodes[groupID], sink, capacity)
	}
	// A szigorúbb FiR csoportok előbb kapnak alkalmas készletet. A sima
	// csoportokat ezután nyitjuk meg; a maradék hálózat továbbra is átoszthatja
	// a jelölteket, de a már kiosztott FiR csoportmennyiséget nem csökkenti.
	flow.run(0, sink)
	for _, groupID := range groupIDs {
		if group := groups[groupID]; !group.fir {
			flow.edges[groupNodes[groupID]][groupEdges[groupID]].capacity = group.quantity
		}
	}
	flow.run(0, sink)
	for _, groupID := range groupIDs {
		group := groups[groupID]
		group.remaining = flow.edges[groupNodes[groupID]][groupEdges[groupID]].capacity
		for _, position := range group.positions {
			source := &out.Items[position.item].Sources[position.source]
			source.AllocationKnown = true
			source.GroupRemainingQuantity = group.remaining
		}
	}
	for _, assignment := range assignments {
		position := assignment.position
		source := &out.Items[position.item].Sources[position.source]
		source.AllocatedQuantity += assignment.quantity - flow.edges[assignment.node][assignment.edge].capacity
	}
	for itemIndex := range out.Items {
		item := &out.Items[itemIndex]
		if rows[item.ItemID] == 1 {
			item.CollectionFIRAvailableQuantity = fixedFIRAvailable[itemIndex]
			item.CollectionRegularAvailableQuantity = fixedRegularAvailable[itemIndex]
		}
		var assigned, missing, closedAssigned, marketOpenAssigned, marketMissing int64
		anyKnown, anyOpen, marketOpen := false, false, false
		for _, source := range item.Sources {
			if !source.AllocationKnown {
				continue
			}
			anyKnown = true
			if err := addQuantity(&assigned, source.AllocatedQuantity); err != nil {
				return Snapshot{}, err
			}
			if source.GroupRemainingQuantity == 0 {
				if err := addQuantity(&closedAssigned, source.AllocatedQuantity); err != nil {
					return Snapshot{}, err
				}
				continue
			}
			anyOpen = true
			if source.FIR {
				if err := addQuantity(&item.CollectionFIRAvailableQuantity, source.AllocatedQuantity); err != nil {
					return Snapshot{}, err
				}
			} else if err := addQuantity(&item.CollectionRegularAvailableQuantity, source.AllocatedQuantity); err != nil {
				return Snapshot{}, err
			}
			if err := addQuantity(&missing, source.GroupRemainingQuantity); err != nil {
				return Snapshot{}, err
			}
			if source.Origin == OriginMarket {
				marketOpen = true
				if err := addQuantity(&marketOpenAssigned, source.AllocatedQuantity); err != nil {
					return Snapshot{}, err
				}
				if err := addQuantity(&marketMissing, source.GroupRemainingQuantity); err != nil {
					return Snapshot{}, err
				}
			}
		}
		typedStock := item.MarketRegularQuantity+item.MarketFIRQuantity > 0
		if !safeRows[itemIndex] || (!anyKnown && !typedStock) {
			continue
		}
		item.CollectionCalculated = true
		item.CollectionRequiredQuantity = fixed[itemIndex]
		item.CollectionRemainingQuantity = fixedUnmet[itemIndex]
		if anyOpen {
			if err := addQuantity(&item.CollectionRequiredQuantity, assigned); err != nil {
				return Snapshot{}, err
			}
			if err := addQuantity(&item.CollectionRequiredQuantity, missing); err != nil {
				return Snapshot{}, err
			}
			if err := addQuantity(&item.CollectionRemainingQuantity, missing); err != nil {
				return Snapshot{}, err
			}
		}
		// A Marketből származó nyitott csoportokra külön vetítjük a kvótát.
		// A lefedett Any csoportokra félretett darabok ebben sem használhatók újra.
		quota := fixedMarket[itemIndex]
		if marketOpen {
			// A végső API-kvóta a nyers Market összegnél nem lehet nagyobb.
			// A köztes összeg több nagy csoport esetén is biztosan int64-ben marad.
			quota += marketOpenAssigned + marketMissing + closedAssigned
		}
		item.CollectionMarketRequiredQuantity = min(item.MarketRequiredQuantity, quota)
	}
	return out, nil
}

func addQuantity(target *int64, quantity int64) error {
	if quantity < 0 || quantity > MaxQuantity-*target {
		return ErrInvalidQuantity
	}
	*target += quantity
	return nil
}

func resetAllocation(snapshot *Snapshot) {
	for i := range snapshot.Items {
		item := &snapshot.Items[i]
		item.CollectionCalculated = false
		item.CollectionRequiredQuantity, item.CollectionRemainingQuantity, item.CollectionMarketRequiredQuantity = 0, 0, 0
		item.CollectionFIRAvailableQuantity, item.CollectionRegularAvailableQuantity = 0, 0
		for j := range item.Sources {
			source := &item.Sources[j]
			source.AllocationKnown = false
			source.AllocatedQuantity, source.GroupRemainingQuantity = 0, 0
		}
	}
}

type allocationEdge struct {
	to, reverse int
	capacity    int64
}

type allocationFlow struct {
	edges [][]allocationEdge
	level []int
	next  []int
}

func newAllocationFlow(nodes int) *allocationFlow {
	return &allocationFlow{edges: make([][]allocationEdge, nodes), level: make([]int, nodes), next: make([]int, nodes)}
}

func (flow *allocationFlow) add(from, to int, capacity int64) int {
	index := len(flow.edges[from])
	flow.edges[from] = append(flow.edges[from], allocationEdge{to: to, reverse: len(flow.edges[to]), capacity: capacity})
	flow.edges[to] = append(flow.edges[to], allocationEdge{to: from, reverse: index})
	return index
}

func (flow *allocationFlow) run(source, sink int) {
	for flow.layers(source, sink) {
		for i := range flow.next {
			flow.next[i] = 0
		}
		for flow.push(source, sink, MaxQuantity) > 0 {
		}
	}
}

func (flow *allocationFlow) layers(source, sink int) bool {
	for i := range flow.level {
		flow.level[i] = -1
	}
	flow.level[source] = 0
	queue := []int{source}
	for head := 0; head < len(queue); head++ {
		node := queue[head]
		for _, edge := range flow.edges[node] {
			if edge.capacity > 0 && flow.level[edge.to] < 0 {
				flow.level[edge.to] = flow.level[node] + 1
				queue = append(queue, edge.to)
			}
		}
	}
	return flow.level[sink] >= 0
}

func (flow *allocationFlow) push(node, sink int, quantity int64) int64 {
	if node == sink {
		return quantity
	}
	for flow.next[node] < len(flow.edges[node]) {
		index := flow.next[node]
		edge := &flow.edges[node][index]
		if edge.capacity > 0 && flow.level[edge.to] == flow.level[node]+1 {
			pushed := flow.push(edge.to, sink, min(quantity, edge.capacity))
			if pushed > 0 {
				edge.capacity -= pushed
				flow.edges[edge.to][edge.reverse].capacity += pushed
				return pushed
			}
		}
		flow.next[node]++
	}
	return 0
}
