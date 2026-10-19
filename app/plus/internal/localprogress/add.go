package localprogress

import "math"

// AddAllowed az API-csoportadatból számolt tárgykvótáig enged új darabot.
// Bejelentett API-progressz nélküli csoportnál a teljes követelménylista
// kiosztásával ellenőrzi a hiány csökkenését. A bemenet és a készlet nem változik.
func AddAllowed(snapshot Snapshot, itemID string) (bool, error) {
	allocated, err := Allocate(snapshot)
	if err != nil {
		return false, err
	}
	index := -1
	for i, item := range allocated.Items {
		if item.ItemID != itemID {
			continue
		}
		if index != -1 {
			return false, ErrAmbiguousItem
		}
		index = i
	}
	if index == -1 {
		return false, ErrItemNotFound
	}
	item := allocated.Items[index]
	if item.AvailableQuantity >= MaxQuantity || item.MarketAvailableQuantity >= MaxQuantity {
		return false, nil
	}
	var rawTotal int64
	for _, source := range item.Sources {
		if active(source) {
			if err := addQuantity(&rawTotal, source.RemainingRequiredQuantity); err != nil {
				return false, err
			}
		}
	}
	if len(item.Sources) != 0 && rawTotal != item.RequiredQuantity {
		return false, ErrInvalidSnapshot
	}
	if usesReportedMarketQuota(item) {
		return item.AvailableQuantity < item.CollectionRequiredQuantity, nil
	}
	before, err := collectionDeficit(allocated)
	if err != nil || before == 0 {
		return false, err
	}
	candidate := Clone(allocated)
	row := &candidate.Items[index]
	row.AvailableQuantity++
	after, err := Allocate(candidate)
	if err != nil {
		return false, err
	}
	missing, err := collectionDeficit(after)
	return missing < before, err
}

// A megjelenítés tárgyanként megismétli az Any csoport hiányát. A mentési
// jogosultsághoz a kötelező igények és az egyedi csoportok összes hiánya kell.
func collectionDeficit(snapshot Snapshot) (int64, error) {
	var missing int64
	groups := make(map[string]bool)
	appendMissing := func(quantity int64) error {
		if quantity < 0 || missing > math.MaxInt64-quantity {
			return ErrInvalidQuantity
		}
		missing += quantity
		return nil
	}
	for _, item := range snapshot.Items {
		fixed := int64(0)
		if len(item.Sources) == 0 {
			fixed = item.RequiredQuantity
		}
		var openAssigned int64
		for _, source := range item.Sources {
			if !active(source) {
				continue
			}
			if !source.AllocationKnown {
				if err := addQuantity(&fixed, source.RemainingRequiredQuantity); err != nil {
					return 0, err
				}
				continue
			}
			if !groups[source.GroupID] {
				groups[source.GroupID] = true
				if err := appendMissing(source.GroupRemainingQuantity); err != nil {
					return 0, err
				}
			}
			if source.GroupRemainingQuantity > 0 {
				if err := addQuantity(&openAssigned, source.AllocatedQuantity); err != nil {
					return 0, err
				}
			}
		}
		fixedAvailable := item.CollectionFIRAvailableQuantity + item.CollectionRegularAvailableQuantity - openAssigned
		if fixedAvailable < 0 || fixedAvailable > fixed {
			return 0, ErrInvalidSnapshot
		}
		if err := appendMissing(fixed - fixedAvailable); err != nil {
			return 0, err
		}
	}
	return missing, nil
}
