package localprogress

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/tarkovdev"
)

// Merge tiszta művelet: a Market ugyanazon célhoz tartozó tárgykövetelménye
// elsődleges. A dev csak hiányzó cél+tárgy kapcsolatot adhat hozzá.
// Személyes állapotot kizárólag a Market pillanatképei határoznak meg.
func Merge(tracked market.TrackedSnapshot, quests market.QuestSnapshot, hideout market.HideoutSnapshot, dev tarkovdev.Catalog) (Snapshot, error) {
	if tracked.CharUID != quests.CharUID || tracked.CharUID != hideout.CharUID || tracked.GameMode != quests.GameMode || tracked.GameMode != hideout.GameMode || tracked.Revision != quests.Revision || tracked.Revision != hideout.Revision {
		return Snapshot{}, ErrSnapshotMismatch
	}
	if tracked.Items == nil || quests.Quests == nil || hideout.Modules == nil {
		return Snapshot{}, ErrInvalidSnapshot
	}
	questIndex := make(map[string]market.Quest)
	for _, quest := range quests.Quests {
		for _, id := range append([]string{quest.QuestID}, quest.QuestIDAliases...) {
			if !validID(id) {
				return Snapshot{}, ErrInvalidSnapshot
			}
			if previous, exists := questIndex[id]; exists && previous.QuestID != quest.QuestID {
				return Snapshot{}, ErrInvalidSnapshot
			}
			questIndex[id] = quest
		}
	}
	upgradeIndex := make(map[string]market.HideoutUpgrade)
	for _, upgrade := range hideout.Modules {
		if !validUpgradeKey(upgrade.UpgradeKey) || upgrade.Level < 1 {
			return Snapshot{}, ErrInvalidSnapshot
		}
		if _, duplicate := upgradeIndex[upgrade.UpgradeKey]; duplicate {
			return Snapshot{}, ErrInvalidSnapshot
		}
		upgradeIndex[upgrade.UpgradeKey] = upgrade
	}

	out := Snapshot{CharUID: tracked.CharUID, GameMode: tracked.GameMode, Revision: tracked.Revision, FIRInventorySeparate: tracked.FIRInventorySeparate, Items: make([]Item, 0, len(tracked.Items))}
	rows := make(map[string][]int)
	marketLinks := make(map[string]bool)
	for _, item := range tracked.Items {
		local := Item{ItemID: item.ItemID, MarketTracked: true, MarketRequiredQuantity: item.RequiredQuantity, MarketAvailableQuantity: item.AvailableQuantity, Name: item.Name, ShortName: item.ShortName, Slots: item.Slots, Icon: item.Icon, RequiredQuantity: item.RequiredQuantity, SpentQuantity: item.SpentQuantity, AvailableQuantity: item.AvailableQuantity, RemainingQuantity: item.RemainingQuantity, Sources: make([]Source, 0, len(item.Sources))}
		local.MarketRawSources = append([]market.TrackedSource{}, item.Sources...)
		for _, raw := range item.Sources {
			source := Source{TrackedSource: raw, Origin: OriginMarket}
			kind := sourceKind(raw)
			if kind != "" {
				source.Type = kind
			}
			goal := ""
			switch kind {
			case "quest":
				goal = raw.QuestID
				if quest, exists := questIndex[raw.QuestID]; exists {
					goal = quest.QuestID
					closeQuestSource(&source, quest)
				}
			case "hideout":
				goal = raw.UpgradeKey
				if upgrade, exists := upgradeIndex[goal]; exists && (upgrade.Completed || upgrade.Prebuilt) {
					source.Completed, source.RemainingRequiredQuantity = true, 0
				}
			}
			if goal != "" {
				marketLinks[linkKey(kind, goal, item.ItemID)] = true
			}
			local.Sources = append(local.Sources, source)
		}
		rows[item.ItemID] = append(rows[item.ItemID], len(out.Items))
		out.Items = append(out.Items, local)
	}
	if err := validateSnapshot(out); err != nil {
		return Snapshot{}, err
	}

	appendSource := func(itemID string, source Source) {
		indexes := rows[itemID]
		// Egy BSG-ID több változatot is takarhat. A dev nem adja meg, hogy
		// a hiányzó kapcsolat melyik Market-változathoz tartozik.
		if len(indexes) > 1 {
			return
		}
		if len(indexes) == 0 {
			indexes = []int{len(out.Items)}
			rows[itemID] = indexes
			out.Items = append(out.Items, Item{ItemID: itemID, Name: itemID, ShortName: itemID, Sources: []Source{}})
		}
		out.Items[indexes[0]].Sources = append(out.Items[indexes[0]].Sources, source)
	}
	seenTasks := make(map[string]bool)
	for _, task := range dev.Tasks {
		quest, matched := questIndex[task.ID]
		if !matched || seenTasks[quest.QuestID] {
			continue
		}
		seenTasks[quest.QuestID] = true
		objectiveItems := make(map[string]int)
		indexedObjectives := make(map[string]bool)
		for _, objective := range task.Objectives {
			if objective.Optional || !(itemRequirementType(objective.Type) || singleUseFlare(objective)) || objective.Count <= 0 || indexedObjectives[objective.ID] {
				continue
			}
			if !validQuantity(objective.Count) {
				return Snapshot{}, ErrInvalidQuantity
			}
			indexedObjectives[objective.ID] = true
			for _, id := range uniqueIDs(objective.ItemIDs) {
				objectiveItems[id]++
			}
		}
		seenObjectives := make(map[string]bool)
		for _, objective := range task.Objectives {
			if objective.Optional || !(itemRequirementType(objective.Type) || singleUseFlare(objective)) || objective.Count <= 0 {
				continue
			}
			if !validQuantity(objective.Count) {
				return Snapshot{}, ErrInvalidQuantity
			}
			if seenObjectives[objective.ID] {
				continue
			}
			seenObjectives[objective.ID] = true
			ids := uniqueIDs(objective.ItemIDs)
			if len(ids) == 0 {
				continue
			}
			group := ""
			base := market.TrackedSource{Type: "quest", QuestID: quest.QuestID, Amount: objective.Count, RemainingRequiredQuantity: objective.Count, FIR: objective.FoundInRaid}
			if len(ids) > 1 {
				group = "quest:" + quest.QuestID + ":" + objective.ID
				if !markAlternatives(&out, rows, questIndex, quest.QuestID, ids, group, objectiveItems, &base) {
					continue
				}
			}
			for _, id := range ids {
				if marketLinks[linkKey("quest", quest.QuestID, id)] {
					continue
				}
				source := Source{TrackedSource: base, Origin: OriginDev, GroupID: group}
				if group != "" {
					source.Alternatives = len(ids)
					source.GroupAmount = base.Amount
				}
				closeQuestSource(&source, quest)
				// Egy ismert kötelező tárgy mennyisége teljesítésként megjelenhet.
				// Az alternatívából leadott tárgy kilétét a Market pipa nem bizonyítja.
				if source.Completed && source.Alternatives > 1 {
					source.Amount = 0
				}
				appendSource(id, source)
			}
		}
	}
	for _, upgrade := range hideout.Modules {
		station, matched := matchStation(upgrade, dev.Hideout)
		if !matched {
			continue
		}
		for _, level := range station.Levels {
			if level.Level != upgrade.Level {
				continue
			}
			seenRequirements := make(map[string]bool)
			for _, requirement := range level.Requirements {
				if requirement.Count <= 0 || !validID(requirement.ItemID) {
					continue
				}
				if !validQuantity(requirement.Count) {
					return Snapshot{}, ErrInvalidQuantity
				}
				if seenRequirements[requirement.ID] || marketLinks[linkKey("hideout", upgrade.UpgradeKey, requirement.ItemID)] {
					continue
				}
				seenRequirements[requirement.ID] = true
				source := Source{TrackedSource: market.TrackedSource{Type: "hideout", UpgradeKey: upgrade.UpgradeKey, Amount: requirement.Count, RemainingRequiredQuantity: requirement.Count, FIR: requirement.FoundInRaid}, Origin: OriginDev}
				if upgrade.Completed || upgrade.Prebuilt {
					source.Completed, source.RemainingRequiredQuantity = true, 0
				}
				appendSource(requirement.ItemID, source)
			}
		}
	}
	for i := range out.Items {
		if len(out.Items[i].Sources) > 0 {
			if err := recount(&out.Items[i]); err != nil {
				return Snapshot{}, err
			}
		}
	}
	if err := validateSnapshot(out); err != nil {
		return Snapshot{}, err
	}
	return out, nil
}

func sourceKind(source market.TrackedSource) string {
	if source.QuestID != "" && source.UpgradeKey != "" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(source.Type)) {
	case "quest":
		if source.UpgradeKey == "" {
			return "quest"
		}
	case "hideout":
		if source.QuestID == "" {
			return "hideout"
		}
	}
	if source.QuestID != "" {
		return "quest"
	}
	if source.UpgradeKey != "" {
		return "hideout"
	}
	return ""
}

func itemRequirementType(kind string) bool {
	switch kind {
	case "giveItem", "handoverItem", "plantItem", "buildWeapon", "mark":
		return true
	}
	// A findItem a leadás keresési párja; a useItem count használati esemény
	// is lehet, ezért ebből nem találunk ki beszerzendő darabszámot.
	return false
}

// A dev ellenőrzött flare küldetései egy egyszer használatos jelzőrakétát
// kérnek. Csak a biztosan azonosított tárgyak egyetlen használata jelent
// egy darab beszerzést; más useItem countból nem következtetünk mennyiségre.
func singleUseFlare(objective tarkovdev.Objective) bool {
	if objective.Type != "useItem" || objective.Count != 1 || len(objective.ItemIDs) == 0 {
		return false
	}
	for _, id := range objective.ItemIDs {
		switch id {
		case "62178c4d4ecf221597654e3d", "62389ba9a63f32501b1b4451", "624c0b3340357b5f566e8766", "62389be94d5d474bf712e709":
		default:
			return false
		}
	}
	return true
}

func closeQuestSource(source *Source, quest market.Quest) {
	status := strings.ToLower(strings.TrimSpace(quest.Status))
	if quest.Completed || status == "completed" {
		source.Completed, source.RemainingRequiredQuantity = true, 0
		return
	}
	switch status {
	case "failed", "unreachable", "canceled", "cancelled":
		source.Canceled, source.RemainingRequiredQuantity = true, 0
	}
}

func linkKey(kind, goal, item string) string { return kind + "\x00" + goal + "\x00" + item }

func uniqueIDs(input []string) []string {
	ids := make([]string, 0, len(input))
	seen := make(map[string]bool)
	for _, id := range input {
		if validID(id) && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// markAlternatives kapcsolja a már meglévő Market sort a dev közös csoportjához.
// A Market mennyisége és FiR feltétele a többi jelöltre is elsődleges.
func markAlternatives(snapshot *Snapshot, rows map[string][]int, quests map[string]market.Quest, questID string, ids []string, group string, objectiveItems map[string]int, base *market.TrackedSource) bool {
	type position struct{ item, source int }
	var positions []position
	var authority *market.TrackedSource
	ambiguousObjective := false
	for _, id := range ids {
		if objectiveItems[id] > 1 {
			ambiguousObjective = true
		}
		if len(rows[id]) > 1 {
			return false
		}
		for _, itemIndex := range rows[id] {
			matchedSources := 0
			for sourceIndex, source := range snapshot.Items[itemIndex].Sources {
				quest, exists := quests[source.QuestID]
				if source.Origin != OriginMarket || sourceKind(source.TrackedSource) != "quest" || !exists || quest.QuestID != questID {
					continue
				}
				matchedSources++
				if matchedSources > 1 {
					return false
				}
				if source.GroupID != "" && source.GroupID != group {
					return false
				}
				if authority != nil && (authority.Amount != source.Amount || authority.RemainingRequiredQuantity != source.RemainingRequiredQuantity || authority.FIR != source.FIR || authority.Completed != source.Completed || authority.Canceled != source.Canceled || authority.Optional != source.Optional) {
					return false
				}
				copy := source.TrackedSource
				authority = &copy
				positions = append(positions, position{itemIndex, sourceIndex})
			}
		}
	}
	// A Market source nem tartalmaz dev objectiveID-t. Ha ugyanaz a jelölt
	// több követelményben szerepel, nem dönthető el, melyikhez tartozik a sor.
	// Tisztán dev csoportok átfedhetnek; az Allocate közös készletet oszt közöttük.
	if authority != nil && ambiguousObjective {
		return false
	}
	if authority != nil {
		base.Amount, base.RemainingRequiredQuantity, base.FIR, base.Completed, base.Canceled, base.Optional = authority.Amount, authority.RemainingRequiredQuantity, authority.FIR, authority.Completed, authority.Canceled, authority.Optional
	}
	for _, position := range positions {
		source := &snapshot.Items[position.item].Sources[position.source]
		source.GroupID, source.Alternatives = group, len(ids)
		source.GroupAmount = base.Amount
	}
	return true
}

func normalized(value string) string {
	var result strings.Builder
	for _, ch := range strings.ToLower(value) {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) {
			result.WriteRune(ch)
		}
	}
	return result.String()
}

func matchStation(upgrade market.HideoutUpgrade, stations []tarkovdev.Station) (tarkovdev.Station, bool) {
	var found tarkovdev.Station
	matches := 0
	for _, station := range stations {
		if upgrade.ModuleID != "" && upgrade.ModuleID == station.ID {
			found = station
			matches++
		}
	}
	if upgrade.ModuleID != "" {
		return found, matches == 1
	}
	// A dokumentált null moduleId esetén csak egyértelmű, pontos név/névkulcs
	// egyezést fogadunk el; hasonlósági keresésből nem találunk ki kapcsolatot.
	key := strings.TrimSuffix(upgrade.UpgradeKey, "_"+strconv.Itoa(upgrade.Level))
	key = strings.TrimSuffix(key, "-"+strconv.Itoa(upgrade.Level))
	name := normalized(upgrade.Name)
	key = normalized(key)
	for _, station := range stations {
		stationName, stationKey := normalized(station.Name), normalized(station.NormalizedName)
		if name != "" && (name == stationName || name == stationKey) || key != "" && (key == stationName || key == stationKey) {
			found = station
			matches++
		}
	}
	return found, matches == 1
}
