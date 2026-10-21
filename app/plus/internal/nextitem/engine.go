// Package nextitem a mentett Market-gráf tiszta, helyi készletkiosztását számolja.
// Nem tartalmaz hálózati klienst vagy készletmódosító műveletet.
package nextitem

import "sort"

type need struct {
	key         string
	rows        []int
	amount      int64
	fir, shared bool
	// A helyi minőségi nézet egy fizikai tárgy több készletcsomópontját is
	// elfogadhatja anélkül, hogy a fix követelmény Shared csoporttá válna.
	typed       bool
	allocations map[int]int64
	have        int64
}

type goal struct {
	key, name, kind string
	future          bool
	depth           int
	needs           []need
	remaining       int64
}

// allocateGoals a Market ft/Nn két menetét követi: először a teljes igény,
// majd az első menetben megmaradt igény alapján rendez. A future=false célok
// mindkét menetben előbbiek. A fir nem rendezési kulcs. A szerződésben nem
// szereplő quest-stage és Kappa adatok helyett a holtverseny stabil marad.
// Az inputok, a belső rows szeletek és az allocations mapek is függetlenek.
func allocateGoals(goals []goal, stock []int64) []goal {
	out := engineCloneGoals(goals)
	engineAllocatePass(out, stock, false)
	engineAllocatePass(out, stock, true)
	return out
}

func engineCloneGoals(goals []goal) []goal {
	if goals == nil {
		return nil
	}
	out := append([]goal{}, goals...)
	for i := range out {
		out[i].needs = append([]need(nil), goals[i].needs...)
		for j := range out[i].needs {
			out[i].needs[j].rows = append([]int(nil), goals[i].needs[j].rows...)
			if goals[i].needs[j].allocations != nil {
				out[i].needs[j].allocations = make(map[int]int64, len(goals[i].needs[j].allocations))
				for row, amount := range goals[i].needs[j].allocations {
					out[i].needs[j].allocations[row] = amount
				}
			}
		}
	}
	return out
}

func engineTotalNeeds(g goal) int64 {
	var total int64
	for _, n := range g.needs {
		total += n.amount
	}
	return total
}

func engineAllocatePass(goals []goal, stock []int64, byRemaining bool) {
	sort.SliceStable(goals, func(i, j int) bool {
		a, b := goals[i], goals[j]
		if a.future != b.future {
			return !a.future
		}
		x, y := a.remaining, b.remaining
		if !byRemaining {
			x, y = engineTotalNeeds(a), engineTotalNeeds(b)
		}
		if x != y {
			return x < y
		}
		if a.kind != b.kind {
			return a.kind == "module"
		}
		return a.depth < b.depth
	})

	available := append([]int64(nil), stock...)
	coverable := make([]bool, len(goals))
	for i := range goals {
		allocations := engineAllocateNeeds(goals[i].needs, available)
		full := true
		for j, n := range goals[i].needs {
			if engineAllocationTotal(allocations[j]) < n.amount {
				full = false
				break
			}
		}
		if full {
			coverable[i] = true
			for _, allocation := range allocations {
				for row, amount := range allocation {
					available[row] -= amount
				}
			}
		}
	}

	// Az előfoglalás csak a célok fedezhetőségét dönti el. A tényleges végső
	// kiosztás az eredeti készletből, e négy csoport teljes needsorrendjén fut.
	type position struct{ goal, need int }
	var positions []position
	var ordered []need
	for _, future := range []bool{false, true} {
		for _, full := range []bool{true, false} {
			for i, g := range goals {
				if g.future != future || coverable[i] != full {
					continue
				}
				for j, n := range g.needs {
					positions = append(positions, position{i, j})
					ordered = append(ordered, n)
				}
			}
		}
	}
	allocations := engineAllocateNeeds(ordered, stock)
	for i := range goals {
		goals[i].remaining = 0
	}
	for i, p := range positions {
		n := &goals[p.goal].needs[p.need]
		n.allocations = allocations[i]
		n.have = engineAllocationTotal(allocations[i])
		goals[p.goal].remaining += n.amount - n.have
	}
}

func engineAllocationTotal(allocation map[int]int64) int64 {
	var total int64
	for _, amount := range allocation {
		total += amount
	}
	return total
}

// engineNeedRows a JavaScript Set stabil sorrendjét őrzi. A fix need egy
// fizikai sort címez; a shared need az egyelemű anyOf esetén is közös marad.
func engineNeedRows(n need) []int {
	rows := n.rows
	if !n.shared && !n.typed && len(rows) > 1 {
		rows = rows[:1]
	}
	result := make([]int, 0, len(rows))
	seen := make(map[int]bool, len(rows))
	for _, row := range rows {
		if !seen[row] {
			seen[row] = true
			result = append(result, row)
		}
	}
	return result
}

func engineStock(stock []int64, row int) int64 {
	if row < 0 || row >= len(stock) {
		return 0
	}
	return max(0, stock[row])
}

// engineAllocateNeeds az Rb: az Any-halmazoktól diszjunkt fix sorok közvetlen
// készletfoglalást kapnak, a többi igényt csak pozitív készleten át kapcsoljuk.
func engineAllocateNeeds(needs []need, stock []int64) []map[int]int64 {
	result := make([]map[int]int64, len(needs))
	sharedRows := make(map[int]bool)
	for _, n := range needs {
		if n.shared || (n.typed && len(engineNeedRows(n)) > 1) {
			for _, row := range engineNeedRows(n) {
				sharedRows[row] = true
			}
		}
	}
	used := make(map[int]int64)
	var complex []int
	for i, n := range needs {
		result[i] = make(map[int]int64)
		if n.amount <= 0 {
			continue
		}
		rows := engineNeedRows(n)
		if !n.shared && len(rows) == 1 && !sharedRows[rows[0]] {
			row := rows[0]
			amount := min(n.amount, max(0, engineStock(stock, row)-used[row]))
			if amount > 0 {
				result[i][row] = amount
				used[row] += amount
			}
		} else {
			complex = append(complex, i)
		}
	}
	parents := make(map[int]int, len(complex))
	for _, index := range complex {
		parents[index] = index
	}
	var root func(int) int
	root = func(index int) int {
		if parents[index] != index {
			parents[index] = root(parents[index])
		}
		return parents[index]
	}
	owners := make(map[int]int)
	for _, index := range complex {
		for _, row := range engineNeedRows(needs[index]) {
			if engineStock(stock, row) <= 0 {
				continue
			}
			if owner, exists := owners[row]; exists {
				parents[root(index)] = root(owner)
			} else {
				owners[row] = index
			}
		}
	}
	components := make([][]int, 0)
	componentIndex := make(map[int]int)
	for _, index := range complex {
		key := root(index)
		position, exists := componentIndex[key]
		if !exists {
			position = len(components)
			componentIndex[key] = position
			components = append(components, nil)
		}
		components[position] = append(components[position], index)
	}
	for _, component := range components {
		// A komponens a már kiszámított célprioritás sorrendjét tartja.
		// A minőség az elfogadható készletet szűri; egy későbbi FiR igény
		// nem veheti el a korábbi Regular célhoz szükséges FiR darabokat.
		ordered := make([]need, 0, len(component))
		for _, index := range component {
			ordered = append(ordered, needs[index])
		}
		allocations := engineLexicographicFlow(ordered, stock)
		for i, index := range component {
			result[index] = allocations[i]
		}
	}
	return result
}

type engineEdge struct {
	to, reverse       int
	capacity, initial int64
}

// engineLexicographicFlow az eredeti qlt él- és BFS-sorrendjét követi.
// A sinkkapuk egyenként nyílnak: egy korábbi need fedezete nem csökkenhet,
// de a hozzá rendelt tárgyak reziduális utakon átrendezhetők későbbi needhez.
func engineLexicographicFlow(needs []need, stock []int64) []map[int]int64 {
	return engineFlow(needs, stock, false)
}

// engineRegularPreferredFlow már rögzített need-lefedésekhez keres kiosztást.
// Előbb a teljes nonFiR készletet használja, majd csak a szükséges FiR darabokat.
// Kizárólag a típusos, célonkénti megjelenítés hívja; a prioritási engine nem.
func engineRegularPreferredFlow(needs []need, stock []int64) []map[int]int64 {
	return engineFlow(needs, stock, true)
}

func engineFlow(needs []need, stock []int64, preferRegular bool) []map[int]int64 {
	graph := make([][]*engineEdge, 2) // source=0, sink=1
	addNode := func() int {
		graph = append(graph, nil)
		return len(graph) - 1
	}
	addEdge := func(from, to int, capacity int64) *engineEdge {
		forward := &engineEdge{to: to, reverse: len(graph[to]), capacity: capacity, initial: capacity}
		backward := &engineEdge{to: from, reverse: len(graph[from])}
		graph[from] = append(graph[from], forward)
		graph[to] = append(graph[to], backward)
		return forward
	}
	itemNodes := make(map[int]int)
	itemRows := []int{}
	seenRows := make(map[int]bool)
	typed := false
	for _, n := range needs {
		typed = typed || n.typed
		for _, row := range engineNeedRows(n) {
			if !seenRows[row] && engineStock(stock, row) > 0 {
				itemRows, seenRows[row] = append(itemRows, row), true
			}
		}
	}
	if typed {
		// A célkapuk prioritási sorrendben nyílnak. Az adott Regular cél
		// előbb nonFiR készletet használ, majd a hozzá jogosult FiR darabokat.
		sort.SliceStable(itemRows, func(i, j int) bool { return itemRows[i]%2 < itemRows[j]%2 })
	}
	var firSources []*engineEdge
	for _, row := range itemRows {
		itemNodes[row] = addNode()
		source := addEdge(0, itemNodes[row], engineStock(stock, row))
		if preferRegular && row%2 == 1 {
			source.capacity = 0
			firSources = append(firSources, source)
		}
	}
	type itemEdge struct {
		row  int
		edge *engineEdge
	}
	items := make([][]itemEdge, len(needs))
	sinks := make([]*engineEdge, len(needs))
	for i, n := range needs {
		node := addNode()
		for _, row := range engineNeedRows(n) {
			if itemNode, exists := itemNodes[row]; exists {
				items[i] = append(items[i], itemEdge{row, addEdge(itemNode, node, max(0, n.amount))})
			}
		}
		sinks[i] = addEdge(node, 1, 0)
	}
	augment := func() bool {
		parents := make([]*engineEdge, len(graph))
		from := make([]int, len(graph))
		visited := make([]bool, len(graph))
		visited[0] = true
		queue := []int{0}
		for next := 0; next < len(queue) && !visited[1]; next++ {
			node := queue[next]
			for _, edge := range graph[node] {
				if edge.capacity > 0 && !visited[edge.to] {
					visited[edge.to] = true
					parents[edge.to], from[edge.to] = edge, node
					queue = append(queue, edge.to)
				}
			}
		}
		if !visited[1] {
			return false
		}
		amount := int64(^uint64(0) >> 1)
		for node := 1; node != 0; node = from[node] {
			amount = min(amount, parents[node].capacity)
		}
		for node := 1; node != 0; node = from[node] {
			edge := parents[node]
			edge.capacity -= amount
			graph[node][edge.reverse].capacity += amount
		}
		return true
	}
	if preferRegular {
		for i, n := range needs {
			sinks[i].capacity, sinks[i].initial = max(0, n.amount), max(0, n.amount)
		}
		for augment() {
		}
		for _, source := range firSources {
			source.capacity = source.initial
		}
		for augment() {
		}
	} else {
		for i, n := range needs {
			sinks[i].capacity, sinks[i].initial = max(0, n.amount), max(0, n.amount)
			for augment() {
			}
		}
	}
	result := make([]map[int]int64, len(needs))
	for i := range needs {
		result[i] = make(map[int]int64)
		for _, item := range items[i] {
			if amount := item.edge.initial - item.edge.capacity; amount > 0 {
				result[i][item.row] = amount
			}
		}
	}
	return result
}
