package nextitem

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

// Forecast csak megjelenítési becslés. A készletet és az Add-korlátot nem írja.
type Forecast struct {
	Status             string   `json:"status"`
	Category           string   `json:"category,omitempty"`
	PossibleCategories []string `json:"possibleCategories,omitempty"`
	GoalName           string   `json:"goalName,omitempty"`
	Changes            []Change `json:"changes,omitempty"`
	Targets            []Target `json:"targets,omitempty"`
	Reason             string   `json:"reason,omitempty"`
	// FixedStock ugyanannak az aktuális kiosztásnak a fix kategóriaszámlálója,
	// amelyből a Targets készül. A megjelenítés belső adata, nem része a DTO-nak.
	FixedStock *localprogress.RequirementStock `json:"-"`
}

type Change struct {
	GoalKey  string `json:"goalKey"`
	GoalName string `json:"goalName"`
	Category string `json:"category"`
	Delta    int64  `json:"delta"`
}

// Target az aktuális tárgyat elfogadó cél jelenlegi részaránya. A Shared
// készlet más jelöltek darabjait is tartalmazza; nem a virtuális +1 eredménye.
// A teljesített forrás a saját eredeti igényével, x/x arányként marad látható.
type Target struct {
	GoalKey           string `json:"goalKey"`
	GoalName          string `json:"goalName"`
	Kind              string `json:"kind"`
	AvailableQuantity int64  `json:"availableQuantity"`
	RequiredQuantity  int64  `json:"requiredQuantity"`
	AllocationUnknown bool   `json:"allocationUnknown,omitempty"`
	Completed         bool   `json:"completed,omitempty"`
}

type groupCounter struct {
	goal, need string
	available  int64
}

type forecastGraph struct {
	goals    []goal
	stock    []int64
	counters []groupCounter
	unknown  []bool
}

type priorityModel struct {
	future  []bool
	reverse bool
}

const estimatedReason = "Estimated from API progress. Tarkov Market does not expose its complete horizon, stage and tie order; adding one item can also reassign existing stock."

// Calculate a megerősített teljes pillanatkép és ugyanannak a készlet+1
// változatának különbségét számolja. Sem hálózatot, sem helyi mentést nem használ.
func Calculate(snapshot localprogress.Snapshot, itemID string) Forecast {
	unavailable := func(reason string) Forecast { return Forecast{Status: "unavailable", Reason: reason} }
	if !snapshot.MetadataComplete || snapshot.CharUID == "" || snapshot.Revision == "" || snapshot.Items == nil ||
		(snapshot.GameMode != "PVP" && snapshot.GameMode != "PVE" && snapshot.GameMode != "SEASON") {
		return unavailable("Refresh complete character progress before estimating the next item.")
	}
	if snapshot.FIRInventorySeparate {
		return unavailable("Separate Found in Raid inventory is not supported by this estimate.")
	}
	target, matches := -1, 0
	for i, row := range snapshot.Items {
		if row.AvailableQuantity < 0 || row.AvailableQuantity > localprogress.MaxQuantity ||
			row.MarketAvailableQuantity < 0 || row.MarketAvailableQuantity > localprogress.MaxQuantity {
			return unavailable("The confirmed inventory contains an invalid quantity.")
		}
		if row.AvailableQuantity != row.MarketAvailableQuantity {
			return unavailable("Wait for pending stock changes to be confirmed before estimating the next item.")
		}
		if itemID != "" && row.ItemID == itemID {
			target, matches = i, matches+1
		}
	}
	if matches != 1 || !snapshot.Items[target].MarketTracked {
		return unavailable("The next item needs one unambiguous tracked inventory row.")
	}
	graph, ok := buildForecastGraph(snapshot)
	if !ok {
		return unavailable("Quest, hideout or shared requirement data is incomplete or inconsistent.")
	}
	component := connectedGoals(graph, target)
	if len(component) == 0 {
		return Forecast{Status: "uncertain", Targets: displayTargets(snapshot, graph, nil, target, false),
			Reason: "No active requirement can be matched to this item."}
	}
	capped := snapshot.Items[target].MarketAvailableQuantity == localprogress.MaxQuantity
	models, bounded := forecastModels(graph, component)
	afterStock := append([]int64(nil), graph.stock...)
	if !capped {
		afterStock[target]++
	}
	var primary *Forecast
	seenCategories, seenResults := make(map[string]bool), make(map[string]bool)
	var categories []string
	for _, model := range models {
		input := modelGoals(graph, model, component)
		before := allocateGoals(input, graph.stock)
		if !matchesGroupCounters(before, graph.counters) {
			continue
		}
		if capped {
			return Forecast{Status: "unavailable", Targets: displayTargets(snapshot, graph, before, target, true),
				Reason: "The inventory quantity limit has been reached.", FixedStock: currentFixedStock(before, target)}
		}
		after := allocateGoals(input, afterStock)
		result := changesForecast(before, after, target)
		if primary == nil {
			copy := result
			copy.Targets = displayTargets(snapshot, graph, before, target, true)
			copy.FixedStock = currentFixedStock(before, target)
			primary = &copy
		}
		if !seenCategories[result.Category] {
			seenCategories[result.Category] = true
			categories = append(categories, result.Category)
		}
		encoded, _ := json.Marshal(result.Changes)
		seenResults[result.Category+string(encoded)] = true
	}
	if primary == nil {
		if capped {
			return Forecast{Status: "unavailable", Targets: displayTargets(snapshot, graph, nil, target, false),
				Reason: "The inventory quantity limit has been reached."}
		}
		return Forecast{Status: "uncertain", Targets: displayTargets(snapshot, graph, nil, target, false),
			Reason: "The modeled priorities do not reproduce the current API shared progress. Refresh progress before relying on an estimate."}
	}
	primary.Status, primary.Reason = "estimated", estimatedReason
	if primary.Category == "Reallocated" {
		primary.Reason += " The modeled change also moves existing allocations; the listed changes are net goal/category counters."
	}
	if len(categories) > 1 {
		primary.PossibleCategories = categories
		primary.Reason += " Other valid modeled priorities place the next item in different categories."
	} else if len(seenResults) > 1 {
		primary.Reason += " Other valid modeled priorities change the affected goal or allocation."
	}
	if bounded {
		primary.Reason += " This item has a large competing component; alternative-priority coverage is limited."
	}
	return *primary
}

func currentFixedStock(goals []goal, target int) *localprogress.RequirementStock {
	stock := &localprogress.RequirementStock{Known: true}
	for _, goal := range goals {
		for _, need := range goal.needs {
			if need.shared {
				continue
			}
			if need.fir {
				stock.FIR += need.allocations[target]
			} else {
				stock.Regular += need.allocations[target]
			}
		}
	}
	return stock
}

func displayTargets(snapshot localprogress.Snapshot, graph forecastGraph, before []goal, target int, allocationKnown bool) []Target {
	type metadata struct {
		target              Target
		completed, canceled bool
		level               int
	}
	quests, modules := make(map[string]*metadata), make(map[string]*metadata)
	var order []*metadata
	for _, quest := range snapshot.MarketQuests {
		status := strings.ToLower(strings.TrimSpace(quest.Status))
		info := &metadata{target: Target{GoalKey: "quest:" + quest.QuestID, GoalName: quest.Name, Kind: "quest"},
			completed: quest.Completed || status == "completed",
			canceled:  status == "failed" || status == "unreachable" || status == "canceled" || status == "cancelled"}
		order = append(order, info)
		for _, id := range append([]string{quest.QuestID}, quest.QuestIDAliases...) {
			quests[id] = info
		}
	}
	for _, module := range snapshot.MarketHideout {
		name := module.Name
		if name != "" && module.Level > 0 {
			name += " " + strconv.Itoa(module.Level)
		}
		info := &metadata{target: Target{GoalKey: "module:" + module.UpgradeKey, GoalName: name, Kind: "module"}, completed: module.Completed || module.Prebuilt, level: module.Level}
		modules[module.UpgradeKey], order = info, append(order, info)
	}
	canonical := func(id string) string {
		if info := quests[id]; info != nil {
			return strings.TrimPrefix(info.target.GoalKey, "quest:")
		}
		return id
	}
	entries := make(map[string]Target)
	for _, entry := range forecastTargets(graph, before, target, allocationKnown) {
		entries[entry.GoalKey] = entry
	}
	row, seen := snapshot.Items[target], make(map[string]bool)
	activeEvidence := make(map[string]bool)
	for i, source := range row.Sources {
		if source.Origin != localprogress.OriginMarket || source.Optional || source.Canceled || (source.QuestID != "" && source.UpgradeKey != "") {
			continue
		}
		info := quests[source.QuestID]
		if source.UpgradeKey != "" {
			info = modules[source.UpgradeKey]
		}
		if info == nil || info.canceled {
			continue
		}
		amount, key, completed := source.Amount, "fixed:"+strconv.Itoa(i), source.Completed || info.completed
		explicit, excludedGroup := false, false
		for _, group := range snapshot.MarketGroups {
			if !matchesForecastGroup(row, source, group, canonical) {
				continue
			}
			explicit = true
			amount, key = group.Amount, "group:"+group.RequirementID
			completed = completed || group.Completed
			if group.Optional || group.Canceled {
				completed = false
				excludedGroup = true
			}
			break
		}
		if !completed && !excludedGroup && (source.Amount > 0 || source.RemainingRequiredQuantity > 0) {
			activeEvidence[info.target.GoalKey] = true
		}
		if !explicit && (source.Alternatives > 1 || strings.EqualFold(source.Type, "questGroup")) {
			continue
		}
		key = info.target.GoalKey + "\x00" + key
		if !completed || amount <= 0 || amount > localprogress.MaxQuantity || seen[key] {
			continue
		}
		seen[key] = true
		entry, exists := entries[info.target.GoalKey]
		if !exists {
			entry = info.target
			entry.Completed = true
			if strings.TrimSpace(entry.GoalName) == "" {
				entry.GoalName = source.Name
			}
			if strings.TrimSpace(entry.GoalName) == "" {
				entry.GoalName = strings.TrimPrefix(strings.TrimPrefix(entry.GoalKey, "quest:"), "module:")
			}
			if info.target.GoalName == "" && info.level > 0 {
				entry.GoalName += " " + strconv.Itoa(info.level)
			}
		}
		entry.AvailableQuantity += amount
		entry.RequiredQuantity += amount
		entries[entry.GoalKey] = entry
	}
	var targets []Target
	for _, info := range order {
		if entry, exists := entries[info.target.GoalKey]; exists {
			entry.Completed = entry.Completed && !activeEvidence[entry.GoalKey]
			if entry.AllocationUnknown {
				entry.AvailableQuantity = 0
			}
			targets = append(targets, entry)
		}
	}
	return targets
}

func matchesForecastGroup(row localprogress.Item, source localprogress.Source, group market.TrackedGroup, canonical func(string) string) bool {
	if source.UpgradeKey != "" || source.QuestID == "" || canonical(source.QuestID) != canonical(group.QuestID) ||
		source.RequirementID != group.RequirementID || source.FIR != group.FIR {
		return false
	}
	for _, id := range group.ItemIDs {
		if id == row.ItemID {
			return true
		}
	}
	return false
}

func forecastTargets(graph forecastGraph, before []goal, target int, allocationKnown bool) []Target {
	allocated, shared := make(map[string]int64), make(map[string]int64)
	for _, goal := range before {
		for _, need := range goal.needs {
			allocated[goal.key+"\x00"+need.key] = need.have
		}
	}
	for _, counter := range graph.counters {
		shared[counter.goal+"\x00"+counter.need] = counter.available
	}
	var targets []Target
	// Keep the API metadata order within each kind, independent of the modeled
	// allocation order. Only needs accepting this physical row enter the ratio.
	for _, kind := range []string{"quest", "module"} {
		for _, goal := range graph.goals {
			if goal.kind != kind {
				continue
			}
			entry := Target{GoalKey: goal.key, GoalName: goal.name, Kind: kind}
			seen := make(map[string]bool)
			for _, need := range goal.needs {
				if need.amount <= 0 || seen[need.key] {
					continue
				}
				accepts := false
				for _, row := range need.rows {
					if row == target {
						accepts = true
						break
					}
				}
				if !accepts {
					continue
				}
				seen[need.key] = true
				entry.RequiredQuantity += need.amount
				key := goal.key + "\x00" + need.key
				if available, known := shared[key]; need.shared && known {
					entry.AvailableQuantity += available
				} else if available, known := allocated[key]; allocationKnown && known {
					entry.AvailableQuantity += available
				} else {
					entry.AllocationUnknown = true
				}
			}
			if entry.RequiredQuantity > 0 {
				if entry.AllocationUnknown {
					entry.AvailableQuantity = 0
				}
				targets = append(targets, entry)
			}
		}
	}
	return targets
}

func buildForecastGraph(snapshot localprogress.Snapshot) (forecastGraph, bool) {
	graph := forecastGraph{stock: make([]int64, len(snapshot.Items))}
	quests, aliases := make(map[string]market.Quest), make(map[string]string)
	for _, quest := range snapshot.MarketQuests {
		if quest.QuestID == "" {
			return graph, false
		}
		quests[quest.QuestID] = quest
		for _, id := range append([]string{quest.QuestID}, quest.QuestIDAliases...) {
			if old, exists := aliases[id]; exists && old != quest.QuestID {
				return graph, false
			}
			aliases[id] = quest.QuestID
		}
	}
	canonical := func(id string) string {
		if key := aliases[id]; key != "" {
			return key
		}
		return id
	}
	modules := make(map[string]market.HideoutUpgrade)
	for _, module := range snapshot.MarketHideout {
		modules[module.UpgradeKey] = module
	}
	rows := make(map[string][]int)
	for i, row := range snapshot.Items {
		if row.ItemID == "" {
			return graph, false
		}
		rows[row.ItemID] = append(rows[row.ItemID], i)
		graph.stock[i] = row.MarketAvailableQuantity
	}
	type goalRecord struct {
		goal
		unknown bool
	}
	records, order := make(map[string]*goalRecord), []string{}
	getGoal := func(source market.TrackedSource) (*goalRecord, bool) {
		if source.QuestID != "" && source.UpgradeKey != "" {
			return nil, false
		}
		kind, id, name, future := "quest", canonical(source.QuestID), source.Name, true
		if source.UpgradeKey != "" {
			kind, id = "module", source.UpgradeKey
			module, found := modules[id]
			if !found {
				return nil, false
			}
			if module.Completed || module.Prebuilt {
				return nil, true
			}
			if module.Name != "" {
				name = module.Name
			}
			if module.Level > 0 {
				name += " " + strconv.Itoa(module.Level)
			}
			future = !module.CanComplete
		} else {
			quest, found := quests[id]
			if !found {
				return nil, false
			}
			switch strings.ToLower(quest.Status) {
			case "completed", "failed", "unreachable", "canceled", "cancelled":
				return nil, true
			}
			if quest.Completed {
				return nil, true
			}
			if quest.Name != "" {
				name = quest.Name
			}
			future = !strings.EqualFold(quest.Status, "available")
		}
		if id == "" {
			return nil, false
		}
		key := kind + ":" + id
		if record := records[key]; record != nil {
			return record, true
		}
		if strings.TrimSpace(name) == "" {
			name = id
		}
		record := &goalRecord{goal: goal{key: key, name: name, kind: kind, future: future}, unknown: future}
		records[key], order = record, append(order, key)
		return record, true
	}
	activeSource := func(source localprogress.Source) bool {
		return source.Origin == localprogress.OriginMarket && !source.Completed && !source.Canceled && !source.Optional
	}
	matchesGroup := func(row localprogress.Item, source localprogress.Source, group market.TrackedGroup) bool {
		return activeSource(source) && matchesForecastGroup(row, source, group, canonical)
	}
	for rowIndex, row := range snapshot.Items {
		fixed := make(map[string]int)
		for _, source := range row.Sources {
			if !activeSource(source) {
				continue
			}
			if source.RemainingRequiredQuantity < 0 || source.RemainingRequiredQuantity > localprogress.MaxQuantity {
				return graph, false
			}
			explicit := false
			for _, group := range snapshot.MarketGroups {
				if matchesGroup(row, source, group) {
					explicit = true
					break
				}
			}
			if explicit || source.RemainingRequiredQuantity == 0 {
				continue
			}
			if strings.EqualFold(source.Type, "questGroup") || source.Alternatives > 1 {
				return graph, false // Explicit progress is needed to validate Shared allocations.
			}
			record, valid := getGoal(source.TrackedSource)
			if !valid {
				return graph, false
			}
			if record == nil {
				continue
			}
			key := record.key + ":item:" + strconv.Itoa(rowIndex) + ":" + strconv.FormatBool(source.FIR)
			if index, exists := fixed[key]; exists {
				record.needs[index].amount += source.RemainingRequiredQuantity
			} else {
				fixed[key] = len(record.needs)
				record.needs = append(record.needs, need{key: key, rows: []int{rowIndex}, amount: source.RemainingRequiredQuantity, fir: source.FIR})
			}
		}
	}
	seenGroups := make(map[string]bool)
	for _, group := range snapshot.MarketGroups {
		if group.Completed || group.Canceled || group.Optional || group.RequiredQuantity == 0 {
			continue
		}
		if group.QuestID == "" || group.RequirementID == "" || group.RequiredQuantity < 0 || group.RequiredQuantity > localprogress.MaxQuantity ||
			group.AvailableQuantity < 0 || group.AvailableQuantity > localprogress.MaxQuantity || len(group.ItemIDs) == 0 {
			return graph, false
		}
		record, valid := getGoal(market.TrackedSource{QuestID: group.QuestID, Name: group.QuestName})
		if !valid {
			return graph, false
		}
		if record == nil {
			continue
		}
		key := record.key + ":group:" + group.RequirementID
		if seenGroups[key] {
			return graph, false
		}
		seenGroups[key] = true
		members, seenRows := []int{}, make(map[int]bool)
		for _, id := range group.ItemIDs {
			if len(rows[id]) == 0 {
				return graph, false
			}
			for _, rowIndex := range rows[id] {
				eligible := len(rows[id]) == 1
				if !eligible {
					for _, source := range snapshot.Items[rowIndex].Sources {
						if matchesGroup(snapshot.Items[rowIndex], source, group) {
							eligible = true
							break
						}
					}
				}
				if eligible && !seenRows[rowIndex] {
					seenRows[rowIndex] = true
					members = append(members, rowIndex)
				}
			}
		}
		record.needs = append(record.needs, need{key: key, rows: members, amount: group.RequiredQuantity, fir: group.FIR, shared: true})
		graph.counters = append(graph.counters, groupCounter{goal: record.key, need: key, available: min(group.AvailableQuantity, group.RequiredQuantity)})
	}
	// Original metadata order supplies the baseline's stable tie order. Unknown
	// horizon and the competing reverse order are explicit alternatives below.
	metadataOrder := make(map[string]int)
	for i, module := range snapshot.MarketHideout {
		metadataOrder["module:"+module.UpgradeKey] = i
	}
	for i, quest := range snapshot.MarketQuests {
		metadataOrder["quest:"+quest.QuestID] = len(snapshot.MarketHideout) + i
	}
	sort.SliceStable(order, func(i, j int) bool { return metadataOrder[order[i]] < metadataOrder[order[j]] })
	for _, key := range order {
		record := records[key]
		if len(record.needs) > 0 {
			graph.goals = append(graph.goals, record.goal)
			graph.unknown = append(graph.unknown, record.unknown)
		}
	}
	return graph, true
}

func connectedGoals(graph forecastGraph, target int) []int {
	members := make(map[int][]int)
	for i, goal := range graph.goals {
		seen := make(map[int]bool)
		for _, need := range goal.needs {
			for _, row := range need.rows {
				if !seen[row] && (row == target || graph.stock[row] > 0) {
					members[row] = append(members[row], i)
					seen[row] = true
				}
			}
		}
	}
	queue := append([]int(nil), members[target]...)
	seen := make(map[int]bool)
	for _, index := range queue {
		seen[index] = true
	}
	for head := 0; head < len(queue); head++ {
		for _, need := range graph.goals[queue[head]].needs {
			for _, row := range need.rows {
				for _, related := range members[row] {
					if !seen[related] {
						seen[related] = true
						queue = append(queue, related)
					}
				}
			}
		}
	}
	sort.Ints(queue)
	return queue
}

func forecastModels(graph forecastGraph, component []int) ([]priorityModel, bool) {
	base := make([]bool, len(graph.goals))
	for i := range base {
		base[i] = graph.goals[i].future
	}
	var models []priorityModel
	seen := make(map[string]bool)
	add := func(future []bool, reverse bool) {
		encoded, _ := json.Marshal(future)
		key := string(encoded) + strconv.FormatBool(reverse)
		if !seen[key] {
			seen[key] = true
			models = append(models, priorityModel{future: append([]bool(nil), future...), reverse: reverse})
		}
	}
	add(base, false)
	allQuestsNear := append([]bool(nil), base...)
	for i, goal := range graph.goals {
		if goal.kind == "quest" {
			allQuestsNear[i] = false
		}
	}
	add(allQuestsNear, false)
	unknown := []int{}
	for _, index := range component {
		if graph.unknown[index] {
			unknown = append(unknown, index)
		}
	}
	bounded := len(unknown) > 6
	if !bounded {
		for mask := 0; mask < 1<<len(unknown); mask++ {
			future := append([]bool(nil), base...)
			for bit, index := range unknown {
				future[index] = mask&(1<<bit) == 0
			}
			add(future, false)
			add(future, true)
		}
	} else {
		add(base, true)
		add(allQuestsNear, true)
		allNear := append([]bool(nil), base...)
		for _, index := range unknown {
			allNear[index] = false
		}
		add(allNear, false)
		add(allNear, true)
		for _, index := range unknown[:min(12, len(unknown))] {
			future := append([]bool(nil), base...)
			future[index] = false
			add(future, false)
		}
	}
	return models, bounded
}

func modelGoals(graph forecastGraph, model priorityModel, component []int) []goal {
	goals := append([]goal(nil), graph.goals...)
	for i := range goals {
		goals[i].future = model.future[i]
		goals[i].depth = 0
		if model.future[i] {
			goals[i].depth = 1_000_000
		} else if graph.unknown[i] && goals[i].kind == "quest" {
			goals[i].depth = 1
		}
	}
	if model.reverse {
		for i, j := 0, len(component)-1; i < j; i, j = i+1, j-1 {
			left, right := component[i], component[j]
			goals[left], goals[right] = goals[right], goals[left]
		}
	}
	return goals
}

func matchesGroupCounters(goals []goal, counters []groupCounter) bool {
	counts := make(map[string]int64)
	for _, goal := range goals {
		for _, need := range goal.needs {
			if need.shared {
				counts[goal.key+"\x00"+need.key] = need.have
			}
		}
	}
	for _, counter := range counters {
		if counts[counter.goal+"\x00"+counter.need] != counter.available {
			return false
		}
	}
	return true
}

func needCategory(need need) string {
	if need.shared {
		if need.fir {
			return "Shared (FiR)"
		}
		return "Shared"
	}
	if need.fir {
		return "Found in Raid"
	}
	return "Regular"
}

func changesForecast(before, after []goal, target int) Forecast {
	changes := make(map[string]Change)
	accumulate := func(goals []goal, sign int64) {
		for _, goal := range goals {
			for _, need := range goal.needs {
				category := needCategory(need)
				key := goal.key + "\x00" + category
				change := changes[key]
				change.GoalKey, change.GoalName, change.Category = goal.key, goal.name, category
				change.Delta += sign * need.have
				changes[key] = change
			}
		}
	}
	accumulate(before, -1)
	accumulate(after, 1)
	result := Forecast{Category: "Stock only"}
	positive, positiveTotal, negative := []Change{}, int64(0), false
	for _, change := range changes {
		if change.Delta == 0 {
			continue
		}
		result.Changes = append(result.Changes, change)
		if change.Delta > 0 {
			positive = append(positive, change)
			positiveTotal += change.Delta
		} else {
			negative = true
		}
	}
	sort.Slice(result.Changes, func(i, j int) bool {
		if result.Changes[i].GoalKey != result.Changes[j].GoalKey {
			return result.Changes[i].GoalKey < result.Changes[j].GoalKey
		}
		return result.Changes[i].Category < result.Changes[j].Category
	})
	if negative || len(positive) > 1 || positiveTotal > 1 || allocationsMoved(before, after, target) {
		result.Category = "Reallocated"
	} else if len(positive) == 1 {
		result.Category, result.GoalName = positive[0].Category, positive[0].GoalName
	}
	return result
}

func allocationsMoved(before, after []goal, target int) bool {
	type allocationKey struct {
		goal, need string
		row        int
	}
	deltas := make(map[allocationKey]int64)
	for _, goal := range before {
		for _, need := range goal.needs {
			for row, amount := range need.allocations {
				deltas[allocationKey{goal.key, need.key, row}] -= amount
			}
		}
	}
	for _, goal := range after {
		for _, need := range goal.needs {
			for row, amount := range need.allocations {
				deltas[allocationKey{goal.key, need.key, row}] += amount
			}
		}
	}
	var targetPositive int64
	for key, delta := range deltas {
		if delta != 0 && (key.row != target || delta < 0) {
			return true
		}
		if key.row == target && delta > 0 {
			targetPositive += delta
		}
	}
	return targetPositive > 1
}
