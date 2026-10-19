package localprogress

import (
	"strings"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

// Merge kizárólag azonos Market-revízióból készít gyűjtési nézetet. Az eredeti
// tárgykövetelmény külön megmarad; az Any csoport közös igényét
// csak a kiosztáshoz vetítjük a Market által felsorolt jelöltekre.
func Merge(tracked market.TrackedSnapshot, quests market.QuestSnapshot, hideout market.HideoutSnapshot) (Snapshot, error) {
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
	upgrades := make(map[string]market.HideoutUpgrade)
	for _, upgrade := range hideout.Modules {
		if !validUpgradeKey(upgrade.UpgradeKey) || upgrade.Level < 1 {
			return Snapshot{}, ErrInvalidSnapshot
		}
		if _, exists := upgrades[upgrade.UpgradeKey]; exists {
			return Snapshot{}, ErrInvalidSnapshot
		}
		upgrades[upgrade.UpgradeKey] = upgrade
	}
	out := Snapshot{CharUID: tracked.CharUID, GameMode: tracked.GameMode, Revision: tracked.Revision, FIRInventorySeparate: tracked.FIRInventorySeparate, Items: make([]Item, 0, len(tracked.Items))}
	rows := make(map[string][]int)
	for _, item := range tracked.Items {
		local := Item{ItemID: item.ItemID, MarketTracked: true, MarketRequiredQuantity: item.RequiredQuantity, MarketAvailableQuantity: item.AvailableQuantity, Name: item.Name, ShortName: item.ShortName, Slots: item.Slots, Icon: item.Icon, RequiredQuantity: item.RequiredQuantity, SpentQuantity: item.SpentQuantity, AvailableQuantity: item.AvailableQuantity, RemainingQuantity: item.RemainingQuantity, Sources: make([]Source, 0, len(item.Sources))}
		for _, raw := range item.Sources {
			source := Source{TrackedSource: raw, Origin: OriginMarket}
			if kind := sourceKind(raw); kind != "" {
				source.Type = kind
			}
			if quest, exists := questIndex[raw.QuestID]; exists {
				closeQuestSource(&source, quest)
			}
			if upgrade, exists := upgrades[raw.UpgradeKey]; exists && (upgrade.Completed || upgrade.Prebuilt) {
				source.Completed, source.RemainingRequiredQuantity = true, 0
			}
			local.Sources = append(local.Sources, source)
		}
		rows[item.ItemID] = append(rows[item.ItemID], len(out.Items))
		out.Items = append(out.Items, local)
	}
	seenGroups := make(map[string]bool)
	for _, group := range tracked.Groups {
		key := "quest:" + group.QuestID + ":" + group.RequirementID
		if !validID(group.QuestID) || !validID(group.RequirementID) || seenGroups[key] || !validQuantity(group.Amount) || group.Amount == 0 || !validQuantity(group.RequiredQuantity) || group.RequiredQuantity > group.Amount || !validQuantity(group.AvailableQuantity) || len(group.ItemIDs) == 0 {
			return Snapshot{}, ErrInvalidSnapshot
		}
		seenGroups[key] = true
		members := make(map[string]bool)
		for _, id := range group.ItemIDs {
			if !validID(id) || members[id] {
				return Snapshot{}, ErrInvalidSnapshot
			}
			members[id] = true
		}
		// Több változat közül a Market csoport sem azonosít egy konkrét sort.
		// Csak ezt a jelöltet hagyjuk ki a kiosztásból; a csoport többi
		// egyértelmű jelöltje továbbra is fedezheti a közös követelményt.
		eligible := make([]string, 0, len(group.ItemIDs))
		for _, id := range group.ItemIDs {
			if len(rows[id]) <= 1 {
				eligible = append(eligible, id)
			}
		}
		for _, id := range eligible {
			if len(rows[id]) == 0 {
				rows[id] = []int{len(out.Items)}
				out.Items = append(out.Items, Item{ItemID: id, Name: id, ShortName: id, MarketTracked: true, Sources: []Source{}})
			}
			item := &out.Items[rows[id][0]]
			position := -1
			for i, source := range item.Sources {
				if source.QuestID == group.QuestID && source.RequirementID == group.RequirementID {
					if position != -1 {
						return Snapshot{}, ErrInvalidSnapshot
					}
					position = i
				}
			}
			projected := Source{TrackedSource: market.TrackedSource{Type: "quest", QuestID: group.QuestID, RequirementID: group.RequirementID, Name: group.QuestName, GroupName: group.Name, Amount: group.Amount, RemainingRequiredQuantity: group.RequiredQuantity, FIR: group.FIR, Optional: group.Optional, Completed: group.Completed, Canceled: group.Canceled}, Origin: OriginMarket, GroupID: key, Alternatives: len(eligible), GroupAmount: group.Amount, MarketGroupProgressKnown: true, MarketGroupAvailableQuantity: group.AvailableQuantity}
			if quest, exists := questIndex[group.QuestID]; exists {
				closeQuestSource(&projected, quest)
			}
			if projected.Completed || projected.Canceled || projected.Optional {
				projected.RemainingRequiredQuantity = 0
			}
			if position == -1 {
				item.Sources = append(item.Sources, projected)
			} else {
				item.Sources[position] = projected
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
	case "quest", "questgroup":
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
