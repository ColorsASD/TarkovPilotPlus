package localprogress

// OverlayMarketFallback kizárólag a külön tárolt saját Market-tartalékot
// illeszti az élő pillanatképhez. Az API-készlet és a dev-készlet változatlan;
// a tartalék nincs feltöltve a Marketre, és nincs újra beleszámolva ismétléskor.
func OverlayMarketFallback(snapshot Snapshot, fallback Snapshot) (Snapshot, error) {
	if snapshot.Items == nil {
		return Snapshot{}, ErrInvalidSnapshot
	}
	out := Clone(snapshot)
	resetAllocation(&out)
	// A fő Store.Apply a saját készletet már újravetíthette, miközben egy
	// korábbi fallback-nézet mezői még jelen vannak. Ezek nem nyers források.
	for i := range out.Items {
		clearMarketFallbackItem(&out.Items[i])
		if err := recountMarketFallbackItem(&out.Items[i]); err != nil {
			return Snapshot{}, err
		}
	}
	if err := validateSnapshot(out); err != nil {
		return Snapshot{}, err
	}
	if err := validateSnapshot(fallback); err != nil {
		return Snapshot{}, err
	}
	if snapshot.CharUID != fallback.CharUID || snapshot.GameMode != fallback.GameMode {
		return Snapshot{}, ErrSnapshotMismatch
	}
	counts := countRows(out)
	positions := make(map[string]int, len(out.Items))
	for i := range out.Items {
		positions[out.Items[i].ItemID] = i
	}
	for _, count := range countRows(fallback) {
		if count != 1 {
			return Snapshot{}, ErrAmbiguousItem
		}
	}
	for _, own := range fallback.Items {
		// A közös BSG ID több Market-változat készletét takarhatja. Nem
		// másoljuk ugyanazt a saját darabot az összes változatba.
		if counts[own.ItemID] > 1 {
			continue
		}
		position, exists := positions[own.ItemID]
		if !exists {
			position = len(out.Items)
			out.Items = append(out.Items, Item{ItemID: own.ItemID, Sources: []Source{}})
			positions[own.ItemID] = position
			counts[own.ItemID] = 1
		}
		item := &out.Items[position]
		item.MarketFallbackAvailableQuantity = own.LocalAvailableQuantity
		item.MarketFallbackRegularQuantity = own.LocalRegularQuantity
		item.MarketFallbackFIRQuantity = own.LocalFIRQuantity
		item.MarketFallbackUsedQuantity = own.LocalUsedQuantity
		item.MarketFallbackUsedRegularQuantity = own.LocalUsedRegularQuantity
		item.MarketFallbackUsedFIRQuantity = own.LocalUsedFIRQuantity
	}
	for i := range out.Items {
		item := &out.Items[i]
		if counts[item.ItemID] > 1 {
			continue
		}
		if err := recountMarketFallbackItem(item); err != nil {
			return Snapshot{}, err
		}
	}
	if err := validateSnapshot(out); err != nil {
		return Snapshot{}, err
	}
	return out, nil
}

func recountMarketFallbackItem(item *Item) error {
	if !validQuantity(item.MarketAvailableQuantity) {
		return ErrInvalidQuantity
	}
	available := item.MarketAvailableQuantity
	if err := addQuantity(&available, item.LocalAvailableQuantity); err != nil {
		return err
	}
	if err := addQuantity(&available, item.MarketFallbackAvailableQuantity); err != nil {
		return err
	}
	item.AvailableQuantity = available
	if len(item.Sources) > 0 {
		return recount(item)
	}
	item.RemainingQuantity = max(int64(0), item.RequiredQuantity-available)
	return nil
}

func clearMarketFallbackItem(item *Item) {
	item.MarketFallbackAvailableQuantity = 0
	item.MarketFallbackRegularQuantity = 0
	item.MarketFallbackFIRQuantity = 0
	item.MarketFallbackUsedQuantity = 0
	item.MarketFallbackUsedRegularQuantity = 0
	item.MarketFallbackUsedFIRQuantity = 0
}

func validateMarketFallbackItem(item Item) error {
	for _, quantity := range []int64{item.MarketFallbackAvailableQuantity, item.MarketFallbackRegularQuantity, item.MarketFallbackFIRQuantity,
		item.MarketFallbackUsedQuantity, item.MarketFallbackUsedRegularQuantity, item.MarketFallbackUsedFIRQuantity} {
		if !validQuantity(quantity) {
			return ErrInvalidQuantity
		}
	}
	if item.MarketFallbackRegularQuantity > item.MarketFallbackAvailableQuantity-item.MarketFallbackFIRQuantity ||
		item.MarketFallbackUsedRegularQuantity > item.MarketFallbackUsedQuantity-item.MarketFallbackUsedFIRQuantity ||
		item.MarketFallbackAvailableQuantity > MaxQuantity-item.MarketFallbackUsedQuantity {
		return ErrInvalidQuantity
	}
	return nil
}
