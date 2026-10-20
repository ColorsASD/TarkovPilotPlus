package nextitem

import (
	_ "embed"
	"encoding/json"
	"math/rand"
	"reflect"
	"testing"
)

func engineTestGoal(t *testing.T, goals []goal, key string) goal {
	t.Helper()
	for _, g := range goals {
		if g.key == key {
			return g
		}
	}
	t.Fatalf("hiányzik a kiosztott cél: %s", key)
	return goal{}
}

func TestEnginePreallocatesCompleteGoalsBeforeIncompleteOnes(t *testing.T) {
	input := []goal{
		{key: "small-uncoverable", kind: "quest", needs: []need{{key: "present", rows: []int{1}, amount: 1}, {key: "absent", rows: []int{3}, amount: 1}}},
		{key: "large-coverable", kind: "quest", needs: []need{{key: "shared", rows: []int{0, 1, 2}, amount: 3, shared: true}}},
	}
	result := allocateGoals(input, []int64{1, 1, 1, 0})
	if result[0].key != "large-coverable" || result[0].remaining != 0 || result[0].needs[0].have != 3 {
		t.Fatalf("a fedezhető teljes cél helyett egy korábbi részleges cél foglalta le a készletet: %+v", result)
	}
	small := engineTestGoal(t, result, "small-uncoverable")
	if small.remaining != 2 || small.needs[0].have != 0 {
		t.Fatalf("a végső kiosztás nem az előfoglalás által kiválasztott csoportsorrendet követte: %+v", small)
	}
}

func TestEngineVirtualAdditionCanReallocateAnExistingSharedItem(t *testing.T) {
	goals := []goal{{key: "same-goal", kind: "quest", needs: []need{
		{key: "shared", rows: []int{0, 1}, amount: 1, shared: true},
		{key: "fixed", rows: []int{0}, amount: 1},
	}}}
	before := allocateGoals(goals, []int64{1, 0})[0]
	after := allocateGoals(goals, []int64{1, 1})[0]
	if !reflect.DeepEqual(before.needs[0].allocations, map[int]int64{0: 1}) || before.needs[1].have != 0 || before.remaining != 1 {
		t.Fatalf("hibás alapállapot a sorrendi augmentálóút előtt: %+v", before)
	}
	if !reflect.DeepEqual(after.needs[0].allocations, map[int]int64{1: 1}) ||
		!reflect.DeepEqual(after.needs[1].allocations, map[int]int64{0: 1}) || after.remaining != 0 {
		t.Fatalf("a virtuális +1 nem rendezte át a korábbi Shared tárgyat a fix igényhez: %+v", after)
	}
	if after.needs[0].have-before.needs[0].have != 0 || after.needs[1].have-before.needs[1].have != 1 {
		t.Fatal("az új fizikai tárgy útvonala nem azonos a követelményszámlálók nettó változásával")
	}
}

func TestEngineSecondPassUsesPreviousRemainingAndKeepsStableTies(t *testing.T) {
	input := []goal{
		{key: "small-total", kind: "quest", needs: []need{{rows: []int{0}, amount: 3}}},
		{key: "large-total", kind: "quest", needs: []need{{rows: []int{1}, amount: 4}}},
	}
	result := allocateGoals(input, []int64{1, 3})
	if result[0].key != "large-total" || result[0].remaining != 1 || result[1].key != "small-total" || result[1].remaining != 2 {
		t.Fatalf("a második menet továbbra is az összigény szerint rendezett: %+v", result)
	}
	ties := []goal{
		{key: "future", kind: "module", future: true, needs: []need{{rows: []int{0}, amount: 1}}},
		{key: "quest-deep", kind: "quest", depth: 3, needs: []need{{rows: []int{0}, amount: 1}}},
		{key: "quest-first", kind: "quest", depth: 1, needs: []need{{rows: []int{0}, amount: 1}}},
		{key: "quest-second", kind: "quest", depth: 1, needs: []need{{rows: []int{0}, amount: 1}}},
		{key: "module", kind: "module", depth: 9, needs: []need{{rows: []int{0}, amount: 1}}},
	}
	result = allocateGoals(ties, []int64{0})
	var keys []string
	for _, g := range result {
		keys = append(keys, g.key)
	}
	if !reflect.DeepEqual(keys, []string{"module", "quest-first", "quest-second", "quest-deep", "future"}) {
		t.Fatalf("hibás future/module/depth/stabil sorrend: %v", keys)
	}
}

func TestEngineFIRDoesNotOverrideGoalOrder(t *testing.T) {
	for _, firstFIR := range []bool{false, true} {
		goals := []goal{
			{key: "first", kind: "quest", needs: []need{{rows: []int{0}, amount: 1, fir: firstFIR}}},
			{key: "second", kind: "quest", needs: []need{{rows: []int{0}, amount: 1, fir: !firstFIR}}},
		}
		result := allocateGoals(goals, []int64{1})
		if result[0].key != "first" || result[0].needs[0].have != 1 || result[1].needs[0].have != 0 {
			t.Fatalf("a FiR jelző felülírta a célok sorrendjét: %+v", result)
		}
	}
}

func TestEngineFixedFastPathSharesRepeatedStockOnce(t *testing.T) {
	needs := []need{
		{key: "first-fixed", rows: []int{0}, amount: 4},
		{key: "second-fixed", rows: []int{0}, amount: 4},
		{key: "shared", rows: []int{1, 2, 1}, amount: 3, shared: true},
		{key: "overlapping-fixed", rows: []int{1}, amount: 2},
	}
	allocations := engineAllocateNeeds(needs, []int64{6, 2, 2})
	expected := []map[int]int64{{0: 4}, {0: 2}, {1: 1, 2: 2}, {1: 1}}
	if !reflect.DeepEqual(allocations, expected) {
		t.Fatalf("a diszjunkt fix vagy átfedő Shared kiosztás hibás: got=%v want=%v", allocations, expected)
	}
}

func TestEngineLexicographicFlowPreservesNeedAndCandidateOrder(t *testing.T) {
	for _, rows := range [][]int{{0, 1}, {1, 0}} {
		needs := []need{{key: "first", rows: rows, amount: 1, shared: true}, {key: "second", rows: rows, amount: 1, shared: true}}
		result := engineLexicographicFlow(needs, []int64{1, 1})
		if !reflect.DeepEqual(result[0], map[int]int64{rows[0]: 1}) || !reflect.DeepEqual(result[1], map[int]int64{rows[1]: 1}) {
			t.Fatalf("a qlt élsorrendjét felülírta egy tetszőleges maximális folyam: rows=%v result=%v", rows, result)
		}
	}
}

func TestEngineInputAndOutputNestedStorageAreIndependent(t *testing.T) {
	fixture := func() []goal {
		return []goal{{key: "goal", name: "Quest name", kind: "quest", remaining: 91, needs: []need{
			{key: "shared", rows: []int{0, 1}, amount: 2, shared: true, have: 72, allocations: map[int]int64{0: 71}},
		}}}
	}
	input, before := fixture(), fixture()
	stock := []int64{1, 2}
	result := allocateGoals(input, stock)
	if !reflect.DeepEqual(input, before) || !reflect.DeepEqual(stock, []int64{1, 2}) {
		t.Fatal("a kiosztó átírta a bemeneti gráfot vagy készletet")
	}
	result[0].key = "changed"
	result[0].needs[0].key = "changed-need"
	result[0].needs[0].rows[0] = 99
	result[0].needs[0].allocations[0] = 88
	if !reflect.DeepEqual(input, before) {
		t.Fatal("a kimeneti cél, need, rows vagy allocations tárolója aliasolja a bemenetet")
	}
}

func TestEngineConservesEveryPhysicalStockRowAcrossOverlappingGoals(t *testing.T) {
	random := rand.New(rand.NewSource(15213))
	for sample := 0; sample < 100; sample++ {
		stock := make([]int64, 6)
		for i := range stock {
			stock[i] = int64(random.Intn(5))
		}
		goals := make([]goal, 7)
		for i := range goals {
			goals[i] = goal{kind: "quest", future: random.Intn(3) == 0, depth: random.Intn(3)}
			for j := 0; j < 3; j++ {
				n := need{rows: []int{random.Intn(len(stock))}, amount: int64(random.Intn(5)), fir: random.Intn(2) == 0, shared: random.Intn(2) == 0}
				if n.shared {
					n.rows = append(n.rows, random.Intn(len(stock)), random.Intn(len(stock)))
				}
				goals[i].needs = append(goals[i].needs, n)
			}
		}
		result := allocateGoals(goals, stock)
		used := make([]int64, len(stock))
		for _, g := range result {
			var remaining int64
			for _, n := range g.needs {
				var have int64
				for row, amount := range n.allocations {
					if amount <= 0 || !engineTestContains(n.rows, row) {
						t.Fatalf("nem jogosult fizikai sorból kiosztott készlet: sample=%d need=%+v", sample, n)
					}
					used[row] += amount
					have += amount
				}
				if have != n.have || have > n.amount {
					t.Fatalf("a need készlete eltér a tényleges kiosztástól vagy meghaladja az igényt: %+v", n)
				}
				remaining += n.amount - have
			}
			if g.remaining != remaining {
				t.Fatalf("a cél megmaradt igénye nem a needsorok összegéből készült: %+v", g)
			}
		}
		for row, amount := range used {
			if amount > stock[row] {
				t.Fatalf("egy készletdarab többször került kiosztásra: sample=%d row=%d used=%d stock=%d", sample, row, amount, stock[row])
			}
		}
	}
}

func engineTestContains(rows []int, target int) bool {
	for _, row := range rows {
		if row == target {
			return true
		}
	}
	return false
}

// A rögzített differenciális esetek a mentett, eredeti ft/Rb/qlt JavaScriptből
// készültek. Futtatásukhoz nincs szükség Node-ra, hálózatra vagy ignored fájlra.
func TestEngineMatchesOriginalMarketTwoPassFixtureCorpus(t *testing.T) {
	type fixtureNeed struct {
		Key         string        `json:"key"`
		Rows        []int         `json:"rows"`
		Amount      int64         `json:"amount"`
		FIR         bool          `json:"fir"`
		Shared      bool          `json:"shared"`
		Allocations map[int]int64 `json:"allocations"`
		Have        int64         `json:"have"`
	}
	type fixtureGoal struct {
		Key, Kind string
		Future    bool
		Depth     int
		Needs     []fixtureNeed
		Remaining int64
	}
	var corpus struct {
		Cases []struct {
			Stock    []int64
			Goals    []fixtureGoal
			Expected []fixtureGoal
		}
	}
	if err := json.Unmarshal([]byte(originalEngineFixtureCorpus), &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Cases) != 400 {
		t.Fatalf("a differenciális mintahalmaz váratlanul megváltozott: %d", len(corpus.Cases))
	}
	for sample, fixture := range corpus.Cases {
		input := make([]goal, len(fixture.Goals))
		for i, g := range fixture.Goals {
			input[i] = goal{key: g.Key, kind: g.Kind, future: g.Future, depth: g.Depth}
			seenFixed := make(map[struct {
				row int
				fir bool
			}]bool)
			for _, n := range g.Needs {
				if !n.Shared {
					identity := struct {
						row int
						fir bool
					}{n.Rows[0], n.FIR}
					if seenFixed[identity] {
						t.Fatalf("az Nn összevonást okozó fix uid+FiR duplikátum került a goldenbe: sample=%d goal=%s", sample, g.Key)
					}
					seenFixed[identity] = true
				}
				input[i].needs = append(input[i].needs, need{key: n.Key, rows: n.Rows, amount: n.Amount, fir: n.FIR, shared: n.Shared})
			}
		}
		stockBefore := append([]int64(nil), fixture.Stock...)
		got := allocateGoals(input, fixture.Stock)
		if len(got) != len(fixture.Expected) || !reflect.DeepEqual(stockBefore, fixture.Stock) {
			t.Fatalf("az engine megváltoztatta a célszámot vagy a készletet: sample=%d", sample)
		}
		used := make([]int64, len(fixture.Stock))
		for i, expected := range fixture.Expected {
			actual := got[i]
			if actual.key != expected.Key || actual.remaining != expected.Remaining || len(actual.needs) != len(expected.Needs) {
				t.Fatalf("az eredeti kétmenetes célrangsor eltér: sample=%d position=%d got=%+v want=%+v", sample, i, actual, expected)
			}
			for j, n := range expected.Needs {
				if actual.needs[j].key != n.Key || actual.needs[j].have != n.Have || !reflect.DeepEqual(actual.needs[j].allocations, n.Allocations) {
					t.Fatalf("az eredeti augmentálóutas kiosztás eltér: sample=%d goal=%s need=%s got=%+v want=%+v", sample, expected.Key, n.Key, actual.needs[j], n)
				}
				for row, amount := range actual.needs[j].allocations {
					used[row] += amount
				}
			}
		}
		for row, amount := range used {
			if amount > stockBefore[row] {
				t.Fatalf("a golden gráf több készletet osztott ki a megerősítettnél: sample=%d row=%d used=%d stock=%d", sample, row, amount, stockBefore[row])
			}
		}
	}
}

//go:embed testdata/engine-golden.json
var originalEngineFixtureCorpus string
