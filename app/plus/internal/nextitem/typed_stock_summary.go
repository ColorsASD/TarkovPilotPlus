package nextitem

// typedStockSummaryGoals kizárólag a Stock kategóriasoraihoz osztja ki újra a
// teljes készletet. A FiR igények megelőzik a Regular igényeket; mindkét körben
// a már meghatározott célsorrend marad meg, a fix és Shared igények között is.
// A reziduális cserék egy korábbi igény lefedettségét nem csökkenthetik.
func typedStockSummaryGoals(goals []goal, stock []int64) []goal {
	out := engineCloneGoals(goals)
	type position struct{ goal, need int }
	var positions []position
	var ordered []need
	for _, fir := range []bool{true, false} {
		for i, g := range out {
			for j, n := range g.needs {
				if n.fir == fir {
					positions = append(positions, position{i, j})
					ordered = append(ordered, n)
				}
			}
		}
	}
	allocations := engineAllocateNeeds(ordered, stock)
	// A prioritási körben elért lefedettség rögzített. Ezen belül a teljes
	// alternatívahalmazon nonFiR darabokra cseréljük a szükségtelen FiR-t.
	covered := append([]need(nil), ordered...)
	for i := range covered {
		covered[i].amount = engineAllocationTotal(allocations[i])
	}
	allocations = engineRegularPreferredFlow(covered, stock)
	for i := range out {
		out[i].remaining = 0
	}
	for i, p := range positions {
		n := &out[p.goal].needs[p.need]
		n.allocations, n.have = allocations[i], engineAllocationTotal(allocations[i])
		out[p.goal].remaining += n.amount - n.have
	}
	return out
}

// typedRegularFIRQuantity a kiválasztott fizikai sor fix Regular igényeire
// ténylegesen kiosztott FiR darabokat adja, a Shared hozzájárulások nélkül.
func typedRegularFIRQuantity(goals []goal, target int) int64 {
	var quantity int64
	for _, g := range goals {
		for _, n := range g.needs {
			if !n.fir && !n.shared {
				quantity += n.allocations[2*target+1]
			}
		}
	}
	return quantity
}
