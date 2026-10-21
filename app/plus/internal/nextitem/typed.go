package nextitem

import (
	"strings"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
)

// TypedGroupStock a helyi minőségi készlet ugyanazon globális kiosztásának
// csoportaránya és a kiválasztott fizikai tárgy saját hozzájárulása.
type TypedGroupStock struct {
	FIR               bool
	AvailableQuantity int64
	RequiredQuantity  int64
	AllocatedQuantity int64
	Contributions     map[string]int64
	RowContributions  map[int]int64
}

// TypedStockSummary a készlet minőségi kategóriáinak külön megjelenítési
// kiosztása. A FiR igények előbb kapnak FiR készletet; a Regular kategóriák
// nonFiR készlet után csak a fennmaradó FiR darabokat használhatják.
// Nem módosítja a célprioritás szerinti előrejelzést vagy a gombok állapotát.
type TypedStockSummary struct {
	FixedStock localprogress.RequirementStock
	Groups     map[string]TypedGroupStock
}

// TypedView csak helyi számítás: a készletet és a Market adatait nem írja.
// A Groups kulcsa quest:<kanonikus quest ID>:<requirement ID>.
type TypedView struct {
	Known             bool
	Forecast          Forecast
	FixedStock        localprogress.RequirementStock
	RequiredQuantity  int64
	Groups            map[string]TypedGroupStock
	StockSummary      *TypedStockSummary
	AddFIRAllowed     bool
	AddRegularAllowed bool
}

// CalculateTyped a Market teljes készletét a helyben megadott FiR/nonFiR
// bontással osztja a fix és Shared követelményekhez. A teljes készlet és a két
// ismert minőség különbsége kategorizálatlan, ezért egyetlen célt sem fed le.
// A Market csoportszámláló nem tartalmaz minőségi adatot, ezért nem írhatja
// felül ezt a kiosztást.
// allowFIRForRegular esetén a Regular követelmény előbb nonFiR darabot használ,
// majd szükség esetén FiR darabot is; nonFiR darab FiR igényt sosem fedhet le.
// A minőség nem írja felül a meglévő célprioritást: későbbi FiR cél nem foglal
// előre készletet egy korábban sorra kerülő Regular követelmény elől.
func CalculateTyped(snapshot localprogress.Snapshot, itemID string, allowFIRForRegular bool) TypedView {
	return calculateTyped(snapshot, itemID, allowFIRForRegular, false)
}

// CalculateTypedForDisplay a célkiosztás mellett a Stock sorainak külön
// minőségi összesítését is elkészíti. A csak jogosultságot kérő útvonalaknak
// nincs szükségük erre a további készletkiosztásra.
func CalculateTypedForDisplay(snapshot localprogress.Snapshot, itemID string, allowFIRForRegular bool) TypedView {
	return calculateTyped(snapshot, itemID, allowFIRForRegular, true)
}

func calculateTyped(snapshot localprogress.Snapshot, itemID string, allowFIRForRegular, withStockSummary bool) TypedView {
	target, matches := -1, 0
	for i, row := range snapshot.Items {
		if itemID != "" && row.ItemID == itemID {
			target, matches = i, matches+1
		}
	}
	if matches != 1 {
		return TypedView{Forecast: Forecast{Status: "unavailable", Reason: "Typed allocation needs one unambiguous tracked inventory row."}}
	}
	return calculateTypedRow(snapshot, target, allowFIRForRegular, withStockSummary)
}

// CalculateTypedRow egy fizikai API-rekordhoz számolja a helyi kiosztást.
// Azonos item ID eltérő változatai külön készletet és követelményeket kapnak.
// A közös item ID-ra címezhető API-írás ilyenkor továbbra is le van tiltva.
func CalculateTypedRow(snapshot localprogress.Snapshot, target int, allowFIRForRegular bool) TypedView {
	return calculateTypedRow(snapshot, target, allowFIRForRegular, false)
}

// CalculateTypedRowForDisplay a fizikai API-sor külön minőségi Stock
// összesítését is tartalmazza, az azonos ID-jú változatok összevonása nélkül.
func CalculateTypedRowForDisplay(snapshot localprogress.Snapshot, target int, allowFIRForRegular bool) TypedView {
	return calculateTypedRow(snapshot, target, allowFIRForRegular, true)
}

func calculateTypedRow(snapshot localprogress.Snapshot, target int, allowFIRForRegular, withStockSummary bool) TypedView {
	unavailable := func(reason string) TypedView {
		return TypedView{Forecast: Forecast{Status: "unavailable", Reason: reason}}
	}
	if !snapshot.MetadataComplete || snapshot.CharUID == "" || snapshot.Revision == "" || snapshot.Items == nil ||
		(snapshot.GameMode != "PVP" && snapshot.GameMode != "PVE" && snapshot.GameMode != "SEASON") {
		return unavailable("Refresh complete character progress before allocating typed stock.")
	}
	if target < 0 || target >= len(snapshot.Items) || !snapshot.Items[target].MarketTracked {
		return unavailable("Typed allocation needs one tracked inventory row.")
	}
	matches := 0
	stock := make([]int64, 2*len(snapshot.Items))
	for i, row := range snapshot.Items {
		if !row.QualityKnown || row.FIRQuantity < 0 || row.FIRQuantity > localprogress.MaxQuantity ||
			row.RegularQuantity < 0 || row.RegularQuantity > localprogress.MaxQuantity ||
			row.AvailableQuantity < 0 || row.AvailableQuantity > localprogress.MaxQuantity ||
			row.MarketAvailableQuantity < 0 || row.MarketAvailableQuantity > localprogress.MaxQuantity ||
			row.FIRQuantity+row.RegularQuantity > row.AvailableQuantity {
			return unavailable("The local Found in Raid and Regular quantities exceed the current inventory.")
		}
		stock[2*i], stock[2*i+1] = row.RegularQuantity, row.FIRQuantity
		if row.ItemID == snapshot.Items[target].ItemID {
			matches++
		}
	}
	graph, ok := buildForecastGraph(snapshot)
	if !ok {
		return unavailable("Quest, hideout or shared requirement data is incomplete or inconsistent.")
	}
	typedGoals := typedGoalInputs(graph.goals, allowFIRForRegular)
	allocated := allocateGoals(typedGoals, stock)
	before := collapseTypedGoals(allocated)
	// Az API Shared előrehaladása nem döntheti el a helyi készlet minőségét.
	graph.counters = nil
	fixed := currentFixedStock(before, target)
	view := TypedView{Known: true, FixedStock: *fixed, RequiredQuantity: typedRequiredQuantity(before, target),
		Groups: typedGroupStocks(before, snapshot.Items, target),
		Forecast: Forecast{Status: "estimated", Reason: "Allocated from locally recorded Found in Raid and Regular inventory.",
			Targets: displayTargets(snapshot, graph, before, target, true), FixedStock: fixed}}
	if withStockSummary {
		summary := collapseTypedGoals(typedSummaryGoals(allocated, stock))
		view.StockSummary = &TypedStockSummary{FixedStock: *currentFixedStock(summary, target),
			Groups: typedGroupStocks(summary, snapshot.Items, target)}
	}
	if matches != 1 || snapshot.Items[target].AvailableQuantity == localprogress.MaxQuantity {
		return view
	}
	covered := typedCovered(before)
	canAdd := func(bucket int) bool {
		afterStock := append([]int64(nil), stock...)
		afterStock[2*target+bucket]++
		return typedCovered(allocateGoals(typedGoals, afterStock)) > covered
	}
	view.AddRegularAllowed, view.AddFIRAllowed = canAdd(0), canAdd(1)
	return view
}

// typedSummaryGoals ugyanazt a teljes készletet egyszer osztja ki a Stock
// kategóriáihoz. A célok már kiszámított sorrendje kategórián belül stabil;
// előbb a fix és Shared FiR, majd a fix és Shared Regular igények következnek.
// A flow egy darabot csak egyszer használhat fel, alternatív tárgyak között is.
func typedSummaryGoals(goals []goal, stock []int64) []goal {
	out := engineCloneGoals(goals)
	type position struct{ goal, need int }
	var positions []position
	var ordered []need
	for _, fir := range []bool{true, false} {
		for _, shared := range []bool{false, true} {
			for i, g := range out {
				for j, n := range g.needs {
					if n.fir == fir && n.shared == shared {
						positions = append(positions, position{i, j})
						ordered = append(ordered, n)
					}
				}
			}
		}
	}
	allocations := engineAllocateNeeds(ordered, stock)
	for i, p := range positions {
		n := &out[p.goal].needs[p.need]
		n.allocations, n.have = allocations[i], engineAllocationTotal(allocations[i])
	}
	return out
}

func typedGoalInputs(goals []goal, allowFIRForRegular bool) []goal {
	out := engineCloneGoals(goals)
	for i := range out {
		for j := range out[i].needs {
			n := &out[i].needs[j]
			rows := engineNeedRows(*n)
			n.rows, n.typed = nil, true
			for _, row := range rows {
				if n.fir {
					n.rows = append(n.rows, 2*row+1)
				} else {
					n.rows = append(n.rows, 2*row)
				}
			}
			// A nonFiR darabok a teljes jelölthalmazban megelőzik a FiR
			// darabokat, így az alternatív tárgy FiR készlete is megmaradhat.
			if !n.fir && allowFIRForRegular {
				for _, row := range rows {
					n.rows = append(n.rows, 2*row+1)
				}
			}
		}
	}
	return out
}

func collapseTypedGoals(goals []goal) []goal {
	out := engineCloneGoals(goals)
	for i := range out {
		for j := range out[i].needs {
			n := &out[i].needs[j]
			allocations := make(map[int]int64, len(n.allocations))
			for bucket, quantity := range n.allocations {
				allocations[bucket/2] += quantity
			}
			n.allocations = allocations
			rows, seen := []int{}, make(map[int]bool)
			for _, bucket := range n.rows {
				row := bucket / 2
				if !seen[row] {
					rows, seen[row] = append(rows, row), true
				}
			}
			n.rows, n.typed = rows, false
		}
	}
	return out
}

func typedCovered(goals []goal) int64 {
	var total int64
	for _, g := range goals {
		for _, n := range g.needs {
			total += n.have
		}
	}
	return total
}

// typedRequiredQuantity a fizikai sor teljes fix igénye és a más jelöltek
// kiosztásával még nem fedezett Shared igény. A saját kiosztott darabokat nem
// vonja le még egyszer; a kategorizálatlan készlet nem fedez követelményt.
func typedRequiredQuantity(goals []goal, target int) int64 {
	var required int64
	for _, g := range goals {
		for _, n := range g.needs {
			for _, row := range n.rows {
				if row != target {
					continue
				}
				quantity := n.amount
				if n.shared {
					quantity -= n.have - n.allocations[target]
				}
				required += min(localprogress.MaxQuantity-required, max(0, quantity))
				break
			}
		}
	}
	return required
}

func typedGroupStocks(goals []goal, items []localprogress.Item, target int) map[string]TypedGroupStock {
	groups := make(map[string]TypedGroupStock)
	for _, g := range goals {
		for _, n := range g.needs {
			if !n.shared {
				continue
			}
			for _, row := range n.rows {
				if row == target {
					contributions := make(map[string]int64, len(n.allocations))
					rowContributions := make(map[int]int64, len(n.rows))
					for _, index := range n.rows {
						rowContributions[index] = n.allocations[index]
					}
					for index, quantity := range n.allocations {
						if index >= 0 && index < len(items) && quantity > 0 {
							contributions[items[index].ItemID] += quantity
						}
					}
					key := g.key + ":" + strings.TrimPrefix(n.key, g.key+":group:")
					groups[key] = TypedGroupStock{FIR: n.fir, AvailableQuantity: n.have,
						RequiredQuantity: n.amount, AllocatedQuantity: n.allocations[target], Contributions: contributions, RowContributions: rowContributions}
					break
				}
			}
		}
	}
	return groups
}
