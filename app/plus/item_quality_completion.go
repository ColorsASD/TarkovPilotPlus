package plus

import (
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"unicode"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
)

// Csak teljesítési jelzőket és a tárgysorok minimális azonosságát mentjük.
// A Revisions csak a korábbi helyi fájlok kompatibilis olvasását szolgálja.
// Az API ETag értéke újra felhasználható, ezért nem teljesítési eseményazonosító.
type qualityCompletionProfile struct {
	Goals     map[string]qualityCompletionGoal `json:"goals"`
	Revisions []string                         `json:"revisions,omitempty"`
}

type qualityCompletionGoal struct {
	Completed bool                    `json:"completed"`
	Items     []qualityCompletionItem `json:"items,omitempty"`
}

type qualityCompletionItem struct {
	ItemID string `json:"itemId"`
	RowKey string `json:"rowKey"`
	Name   string `json:"name,omitempty"`
}

type qualityCompletionObservation struct {
	key       string
	aliases   []string
	completed bool
	items     []qualityCompletionItem
}

func validQualityCompletions(profiles map[string]qualityCompletionProfile) bool {
	for profile, history := range profiles {
		if !validQualityCompletionIdentity(profile, 256) || history.Goals == nil || len(history.Revisions) > 64 {
			return false
		}
		seenRevisions := map[string]bool{}
		for _, revision := range history.Revisions {
			if !validQualityCompletionIdentity(revision, 256) || seenRevisions[revision] {
				return false
			}
			seenRevisions[revision] = true
		}
		for key, goal := range history.Goals {
			kind, identity, ok := strings.Cut(key, ":")
			if !ok || (kind != "quest" && kind != "module") || !validQualityCompletionIdentity(identity, 128) {
				return false
			}
			seenItems := map[string]bool{}
			for _, item := range goal.Items {
				if !validQualityCompletionIdentity(item.ItemID, 128) || len(item.Name) > 4096 || seenItems[qualityCompletionItemKey(item)] {
					return false
				}
				seenItems[qualityCompletionItemKey(item)] = true
				if item.RowKey != "" {
					if len(item.RowKey) != 68 || !strings.HasPrefix(item.RowKey, "row:") {
						return false
					}
					if _, err := hex.DecodeString(item.RowKey[4:]); err != nil {
						return false
					}
				}
			}
		}
	}
	return true
}

func validQualityCompletionIdentity(value string, limit int) bool {
	if value == "" || len(value) > limit || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func reconcileQualityCompletions(data *qualityData, profile string, snapshot localprogress.Snapshot, keys []string) error {
	if !snapshot.MetadataComplete {
		return nil
	}
	history := data.Completions[profile]
	observations, err := qualityCompletionObservations(snapshot, keys)
	if err != nil {
		return err
	}
	if history.Goals == nil {
		history.Goals = map[string]qualityCompletionGoal{}
	}
	affected := map[string]qualityCompletionItem{}
	for _, current := range observations {
		previous, known := history.Goals[current.key]
		if !known {
			for _, alias := range current.aliases {
				if previous, known = history.Goals[alias]; known {
					break
				}
			}
		}
		items := mergeQualityCompletionItems(previous.Items, current.items)
		if known && !previous.Completed && current.completed {
			for _, item := range items {
				affected[qualityCompletionItemKey(item)] = item
			}
		}
		history.Goals[current.key] = qualityCompletionGoal{Completed: current.completed, Items: items}
		for _, alias := range current.aliases {
			if alias != current.key {
				delete(history.Goals, alias)
			}
		}
	}
	if len(affected) != 0 {
		resetQualityForCompletedGoals(data, profile, snapshot, keys, affected)
	}
	history.Revisions = nil
	if data.Completions == nil {
		data.Completions = map[string]qualityCompletionProfile{}
	}
	data.Completions[profile] = history
	return nil
}

func qualityCompletionObservations(snapshot localprogress.Snapshot, keys []string) ([]qualityCompletionObservation, error) {
	var out []qualityCompletionObservation
	questOwners, modules := map[string]int{}, map[string]int{}
	invalid := errors.New("The Market completion identities are inconsistent. Local classifications were preserved.")
	for _, quest := range snapshot.MarketQuests {
		if quest.QuestID == "" {
			return nil, invalid
		}
		status := strings.ToLower(strings.TrimSpace(quest.Status))
		completed := quest.Completed || status == "completed"
		if !completed && (status == "failed" || status == "unreachable" || status == "canceled" || status == "cancelled") {
			continue
		}
		index := len(out)
		observation := qualityCompletionObservation{key: "quest:" + quest.QuestID, completed: completed}
		for _, id := range append([]string{quest.QuestID}, quest.QuestIDAliases...) {
			if id == "" {
				return nil, invalid
			}
			if owner, exists := questOwners[id]; exists && owner != index {
				return nil, invalid
			}
			questOwners[id] = index
			observation.aliases = append(observation.aliases, "quest:"+id)
		}
		out = append(out, observation)
	}
	for _, module := range snapshot.MarketHideout {
		if module.UpgradeKey == "" {
			return nil, invalid
		}
		if _, duplicate := modules[module.UpgradeKey]; duplicate {
			return nil, invalid
		}
		modules[module.UpgradeKey] = len(out)
		out = append(out, qualityCompletionObservation{key: "module:" + module.UpgradeKey, completed: module.Completed || module.Prebuilt})
	}
	for index, row := range snapshot.Items {
		ref := qualityCompletionItem{ItemID: row.ItemID, RowKey: keys[index], Name: strings.ToLower(strings.TrimSpace(row.Name))}
		seen := map[int]bool{}
		for _, source := range row.Sources {
			if source.Origin != localprogress.OriginMarket || (source.QuestID != "" && source.UpgradeKey != "") {
				continue
			}
			owner, found := questOwners[source.QuestID]
			if source.UpgradeKey != "" {
				owner, found = modules[source.UpgradeKey]
			}
			if found && !seen[owner] {
				out[owner].items = append(out[owner].items, ref)
				seen[owner] = true
			}
		}
	}
	// A Shared kapcsolat a tárgysor forráslistája nélkül is érvényes. Minden
	// jelölt teljes készlete érintett, akkor is, ha a célhoz nulla darabot adott.
	for _, group := range snapshot.MarketGroups {
		owner, found := questOwners[group.QuestID]
		if !found {
			continue
		}
		for _, itemID := range group.ItemIDs {
			matched := false
			for index, row := range snapshot.Items {
				if row.ItemID == itemID {
					out[owner].items = append(out[owner].items, qualityCompletionItem{ItemID: itemID,
						RowKey: keys[index], Name: strings.ToLower(strings.TrimSpace(row.Name))})
					matched = true
				}
			}
			if !matched {
				out[owner].items = append(out[owner].items, qualityCompletionItem{ItemID: itemID})
			}
		}
	}
	return out, nil
}

func qualityCompletionItemKey(item qualityCompletionItem) string {
	if item.RowKey != "" {
		return item.RowKey
	}
	return "item:" + item.ItemID
}

func mergeQualityCompletionItems(previous, current []qualityCompletionItem) []qualityCompletionItem {
	items := make(map[string]qualityCompletionItem, len(previous)+len(current))
	for _, list := range [][]qualityCompletionItem{previous, current} {
		for _, item := range list {
			items[qualityCompletionItemKey(item)] = item
		}
	}
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]qualityCompletionItem, 0, len(keys))
	for _, key := range keys {
		out = append(out, items[key])
	}
	return out
}

func resetQualityForCompletedGoals(data *qualityData, profile string, snapshot localprogress.Snapshot, keys []string, affected map[string]qualityCompletionItem) {
	rows := qualityRows(data, profile)
	affectedIDs := map[string]bool{}
	for key, item := range affected {
		affectedIDs[item.ItemID] = true
		if old, exists := rows[key]; exists {
			old.FIR, old.Regular = 0, 0
			rows[key] = old
		}
		if old, exists := rows[item.ItemID]; exists {
			old.FIR, old.Regular = 0, 0
			rows[item.ItemID] = old
		}
	}
	counts := map[string]int{}
	for _, row := range snapshot.Items {
		counts[row.ItemID]++
	}
	for index, row := range snapshot.Items {
		if !affectedIDs[row.ItemID] {
			continue
		}
		_, exact := affected[keys[index]]
		_, wholeItem := affected["item:"+row.ItemID]
		match := exact || wholeItem || counts[row.ItemID] == 1
		if !match {
			for _, item := range affected {
				if item.ItemID == row.ItemID && item.Name == strings.ToLower(strings.TrimSpace(row.Name)) {
					match = true
					break
				}
			}
		}
		if match {
			rows[keys[index]] = qualityRecord{Total: row.MarketAvailableQuantity}
		}
	}
	for id, op := range data.Pending {
		if op.Profile == profile && affectedIDs[op.Item] {
			op.Invalidated = true
			data.Pending[id] = op
		}
	}
}
