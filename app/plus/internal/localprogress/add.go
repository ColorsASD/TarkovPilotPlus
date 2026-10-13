package localprogress

import "math"

// AddOptions a teljes követelménylista kiosztásával ellenőrzi, hogy egy új,
// ismert típusú darab csökkentené-e a fennmaradó igényt. Az Any csoport hiányát
// egyszer számolja; egy másik tárgy vagy cél számára felszabaduló készlet is
// számít. A bemenet és a tárolt készlet nem változik.
func AddOptions(snapshot Snapshot, itemID string) (regular, fir bool, err error) {
	allocated, err := Allocate(snapshot)
	if err != nil {
		return false, false, err
	}
	index := -1
	for i, item := range allocated.Items {
		if item.ItemID != itemID {
			continue
		}
		if index != -1 {
			return false, false, ErrAmbiguousItem
		}
		index = i
	}
	if index == -1 {
		return false, false, ErrItemNotFound
	}
	item := allocated.Items[index]
	if item.AvailableQuantity >= MaxQuantity || item.MarketAvailableQuantity >= MaxQuantity {
		return false, false, nil
	}
	var rawTotal int64
	for _, source := range item.Sources {
		if active(source) {
			if err := addQuantity(&rawTotal, source.RemainingRequiredQuantity); err != nil {
				return false, false, err
			}
		}
	}
	if len(item.Sources) != 0 && rawTotal != item.RequiredQuantity {
		return false, false, ErrInvalidSnapshot
	}
	before, err := collectionDeficit(allocated)
	if err != nil || before == 0 {
		return false, false, err
	}
	check := func(kind string) (bool, error) {
		candidate := Clone(allocated)
		row := &candidate.Items[index]
		row.AvailableQuantity++
		// A szimuláció csak egy típusos kapacitást ad hozzá. A Market mezők
		// használata nem módosítja a collected/used elszámolást, és nem dönti
		// el, hogy a tényleges mentés melyik forrásba kerül.
		row.MarketAvailableQuantity++
		if kind == "fir" {
			row.MarketFIRQuantity++
		} else {
			row.MarketRegularQuantity++
		}
		after, err := Allocate(candidate)
		if err != nil {
			return false, err
		}
		missing, err := collectionDeficit(after)
		return missing < before, err
	}
	regular, err = check("regular")
	if err != nil {
		return false, false, err
	}
	fir, err = check("fir")
	return regular, fir, err
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
