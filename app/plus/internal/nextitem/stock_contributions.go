package nextitem

import (
	"sort"
	"strings"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

// typedOtherStock a kiosztott minőségi bucketeket alakítja át megjelenítési
// adattá. Az aktuális item ID minden fizikai változata a saját készlet része.
func typedOtherStock(allocations map[int]int64, items []localprogress.Item, target int) []StockContribution {
	if target < 0 || target >= len(items) {
		return nil
	}
	type identity struct{ id, name, icon string }
	quantities := make(map[identity]StockContribution)
	for bucket, amount := range allocations {
		index := bucket / 2
		if bucket < 0 || index >= len(items) || amount <= 0 {
			continue
		}
		row := items[index]
		if !row.QualityKnown || row.ItemID == items[target].ItemID {
			continue
		}
		name := strings.TrimSpace(row.Name)
		if name == "" {
			name = row.ItemID
		}
		key := identity{row.ItemID, name, market.SafeImageURL(row.Icon)}
		entry := quantities[key]
		entry.ItemID, entry.Name, entry.Icon = key.id, key.name, key.icon
		if bucket%2 == 1 {
			entry.FIRQuantity += amount
		} else {
			entry.RegularQuantity += amount
		}
		quantities[key] = entry
	}
	var out []StockContribution
	for _, entry := range quantities {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].ItemID != out[j].ItemID {
			return out[i].ItemID < out[j].ItemID
		}
		return out[i].Icon < out[j].Icon
	})
	return out
}
