package plus

import "github.com/ggdiam/TarkovPilot/app/plus/internal/localprogress"

func stockItem(snapshot localprogress.Snapshot, id string) (localprogress.Item, bool) {
	var item localprogress.Item
	count := 0
	for _, candidate := range snapshot.Items {
		if candidate.ItemID == id {
			item = candidate
			count++
		}
	}
	return item, count == 1
}
