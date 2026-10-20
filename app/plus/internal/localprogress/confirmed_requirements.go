package localprogress

import (
	"sort"
	"strconv"
	"strings"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

// RequirementStock a megerősített közös készletből a fix követelményekhez
// maradó mennyiség. Known=false esetén a kapcsolódó Market csoportelőrehaladás
// nem fedezhető vagy több, eltérő fix maradékot adó kiosztás is lehetséges.
type RequirementStock struct {
	FIR, Regular int64
	Known        bool
}

type confirmedRequirementGroup struct {
	id     string
	demand int64
	rows   []int
	edge   int
}

// ConfirmedRequirementStock kizárólag a megjelenítéshez foglalja le az API
// által visszaigazolt csoportkészletet. Az eredmény indexei a snapshot.Items
// indexeit követik; a bemenet, az Add-korlát és a gyűjtési kvóta nem változik.
func ConfirmedRequirementStock(snapshot Snapshot) []RequirementStock {
	result := make([]RequirementStock, len(snapshot.Items))
	stock := make([]int64, len(snapshot.Items))
	fixedFIR, fixedRegular := make([]int64, len(stock)), make([]int64, len(stock))
	for i, row := range snapshot.Items {
		result[i].Known = true
		stock[i] = row.MarketAvailableQuantity
		legacy := make(map[string]int64)
		if len(row.Sources) == 0 {
			fixedRegular[i] = row.RequiredQuantity
		}
		for _, source := range row.Sources {
			if !active(source) {
				continue
			}
			explicit := false
			for _, group := range snapshot.MarketGroups {
				if MarketGroupSourceMatches(row, source, group) {
					explicit = true
					break
				}
			}
			if explicit {
				continue
			}
			if source.AllocationKnown && source.Alternatives > 1 && source.GroupID != "" {
				legacy[source.GroupID] = max(legacy[source.GroupID], source.AllocatedQuantity)
				continue
			}
			if source.FIR {
				fixedFIR[i] += source.RemainingRequiredQuantity
			} else {
				fixedRegular[i] += source.RemainingRequiredQuantity
			}
		}
		legacyAllocated := int64(0)
		for _, allocated := range legacy {
			legacyAllocated += allocated
		}
		// A régi kiosztás a fix igények után használta az Any készletet.
		// A pendingből származó rész nem fogyaszthat megerősített fix készletet.
		legacyConfirmed := min(legacyAllocated, max(0, stock[i]-fixedFIR[i]-fixedRegular[i]))
		stock[i] -= legacyConfirmed
	}

	groups := make([]confirmedRequirementGroup, 0, len(snapshot.MarketGroups))
	for _, group := range snapshot.MarketGroups {
		if group.Completed || group.Canceled || group.Optional || group.AvailableQuantity <= 0 || group.RequiredQuantity <= 0 {
			continue
		}
		projected := confirmedRequirementGroup{id: "quest:" + group.QuestID + ":" + group.RequirementID,
			demand: min(group.AvailableQuantity, group.RequiredQuantity)}
		for i, row := range snapshot.Items {
			for _, source := range row.Sources {
				if active(source) && MarketGroupSourceMatches(row, source, group) {
					projected.rows = append(projected.rows, i)
					break
				}
			}
		}
		groups = append(groups, projected)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].id < groups[j].id })
	indexes := make([]int, len(stock))
	keys := make([]string, len(stock))
	for i, row := range snapshot.Items {
		indexes[i], keys[i] = i, confirmedRequirementRowKey(row)
	}
	sort.SliceStable(indexes, func(i, j int) bool { return keys[indexes[i]] < keys[indexes[j]] })
	nodes := make([]int, len(stock))
	for i, index := range indexes {
		nodes[index] = 1 + i
	}
	base, sink := 1+len(stock), 1+len(stock)+len(groups)
	flow := newAllocationFlow(sink + 1)
	rowGroups := make([][]int, len(stock))
	for i := range groups {
		group := &groups[i]
		sort.Slice(group.rows, func(i, j int) bool { return nodes[group.rows[i]] < nodes[group.rows[j]] })
		for _, row := range group.rows {
			flow.add(nodes[row], base+i, group.demand)
			if stock[row] > 0 {
				rowGroups[row] = append(rowGroups[row], i)
			}
		}
		group.edge = flow.add(base+i, sink, group.demand)
	}
	inputEdges := make([][3]int, len(stock))
	inputAmounts := make([][3]int64, len(stock))
	for i := range stock {
		fir := min(stock[i], fixedFIR[i])
		regular := min(stock[i]-fir, fixedRegular[i])
		inputAmounts[i] = [3]int64{stock[i] - fir - regular, regular, fir}
	}
	// A fix célhoz nem szükséges készlet fogy el először. Ha a Market
	// visszaigazolt közös készlete többet igényel, utána a Regular, végül
	// a fix FiR számára félretett darabok nyílnak meg ugyanazon hálózatban.
	for phase := range 3 {
		for _, row := range indexes {
			inputEdges[row][phase] = flow.add(0, nodes[row], inputAmounts[row][phase])
		}
		flow.run(0, sink)
	}
	for i := range stock {
		remaining := int64(0)
		for phase := range 3 {
			remaining += flow.edges[0][inputEdges[i][phase]].capacity
		}
		result[i].FIR = min(remaining, fixedFIR[i])
		result[i].Regular = min(remaining-result[i].FIR, fixedRegular[i])
	}

	// Hiányos API-előrehaladásnál csak a pozitív készleten át összefüggő
	// csoportok sora bizonytalan. Egy nulla készletű jelölt vagy nulla
	// előrehaladású csoport nem kapcsol össze egyébként független készleteket.
	visited := make([]bool, len(groups))
	for i, group := range groups {
		if flow.edges[base+i][group.edge].capacity == 0 || visited[i] {
			continue
		}
		queue := []int{i}
		visited[i] = true
		for len(queue) > 0 {
			next := queue[0]
			queue = queue[1:]
			for _, row := range groups[next].rows {
				if stock[row] == 0 {
					continue
				}
				result[row] = RequirementStock{Known: false}
				for _, related := range rowGroups[row] {
					if !visited[related] {
						visited[related] = true
						queue = append(queue, related)
					}
				}
			}
		}
	}
	for row := range stock {
		if !result[row].Known || stock[row] == 0 || fixedFIR[row]+fixedRegular[row] == 0 || len(rowGroups[row]) == 0 {
			continue
		}
		component := confirmedRequirementComponent(groups, rowGroups, stock, row)
		minimum, minKnown := confirmedSharedUsage(groups, component, stock, row, false)
		maximum, maxKnown := confirmedSharedUsage(groups, component, stock, row, true)
		low := confirmedFixedStock(stock[row]-maximum, fixedFIR[row], fixedRegular[row])
		high := confirmedFixedStock(stock[row]-minimum, fixedFIR[row], fixedRegular[row])
		if !minKnown || !maxKnown || low != high {
			result[row] = RequirementStock{Known: false}
		}
	}
	return result
}

func confirmedFixedStock(remaining, fir, regular int64) RequirementStock {
	assigned := min(remaining, fir)
	return RequirementStock{FIR: assigned, Regular: min(remaining-assigned, regular), Known: true}
}

func confirmedRequirementComponent(groups []confirmedRequirementGroup, rowGroups [][]int, stock []int64, row int) []int {
	component := append([]int(nil), rowGroups[row]...)
	seen := make(map[int]bool, len(component))
	for _, group := range component {
		seen[group] = true
	}
	for next := 0; next < len(component); next++ {
		for _, member := range groups[component[next]].rows {
			if stock[member] == 0 {
				continue
			}
			for _, related := range rowGroups[member] {
				if !seen[related] {
					seen[related] = true
					component = append(component, related)
				}
			}
		}
	}
	sort.Ints(component)
	return component
}

// A két kapunyitási sorrend az összes API-csoportot fedező megoldások között
// a saját sor legkisebb, illetve legnagyobb hozzájárulását adja. Az API nem
// közli a Market kliens összes forrásprioritását, ezért eltérő fix maradék
// esetén a megjelenítés nem állíthat egy önkényesen választott darabszámot.
func confirmedSharedUsage(groups []confirmedRequirementGroup, component []int, stock []int64, row int, ownFirst bool) (int64, bool) {
	base, sink := 1+len(stock), 1+len(stock)+len(component)
	flow := newAllocationFlow(sink + 1)
	groupEdges := make([]int, len(component))
	for i, index := range component {
		group := groups[index]
		for _, member := range group.rows {
			flow.add(1+member, base+i, group.demand)
		}
		groupEdges[i] = flow.add(base+i, sink, group.demand)
	}
	ownEdge := -1
	for phase := range 2 {
		for member, available := range stock {
			ownPhase := phase == 0 && ownFirst || phase == 1 && !ownFirst
			if (member == row) != ownPhase {
				continue
			}
			edge := flow.add(0, 1+member, available)
			if member == row {
				ownEdge = edge
			}
		}
		flow.run(0, sink)
	}
	for i, edge := range groupEdges {
		if flow.edges[base+i][edge].capacity != 0 {
			return 0, false
		}
	}
	return stock[row] - flow.edges[0][ownEdge].capacity, true
}

// MarketGroupSourceMatches a forrás és az explicit csoport azonosságát,
// FiR-feltételét és a saját készletsor jelölti tagságát ellenőrzi. Az aktív
// vagy lezárt állapotot a hívó kezeli a megjelenítési feladatának megfelelően.
func MarketGroupSourceMatches(row Item, source Source, group market.TrackedGroup) bool {
	if source.Origin != OriginMarket || sourceKind(source.TrackedSource) != "quest" || source.QuestID != group.QuestID || source.RequirementID != group.RequirementID || source.FIR != group.FIR {
		return false
	}
	for _, id := range group.ItemIDs {
		if id == row.ItemID {
			return true
		}
	}
	return false
}

func confirmedRequirementRowKey(row Item) string {
	sources := make([]string, 0, len(row.Sources))
	for _, source := range row.Sources {
		sources = append(sources, strings.Join([]string{source.QuestID, source.RequirementID, source.UpgradeKey, source.GroupID,
			strconv.FormatBool(source.FIR), strconv.FormatBool(source.Completed), strconv.FormatBool(source.Canceled), strconv.FormatBool(source.Optional)}, "\x00"))
	}
	sort.Strings(sources)
	return strings.Join([]string{row.ItemID, row.Name, row.ShortName, strings.Join(sources, "\x01"), strconv.FormatInt(row.MarketAvailableQuantity, 10)}, "\x02")
}
