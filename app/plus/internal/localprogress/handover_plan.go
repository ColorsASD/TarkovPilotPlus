package localprogress

// HandoverPlan kizárólag a felhasználó által kiválasztott minőséget használja.
// A helyi készletből a lehető legtöbbet fogyasztja; az ismeretlen régi készlet
// közös kapacitás, ezért a Regular és FiR kérést együtt tervezi meg.
type HandoverPlan struct {
	Local           HandoverEntry
	Fallback        HandoverEntry
	Market          HandoverEntry
	RegularToMarket int64
	FIRToMarket     int64
}

func PlanHandoverItem(item Item, regular, fir int64) (HandoverPlan, error) {
	if err := validateMarketFallbackItem(item); err != nil {
		return HandoverPlan{}, err
	}
	if err := validateHandoverBasePools(item); err != nil {
		return HandoverPlan{}, err
	}
	if item.MarketFallbackAvailableQuantity == 0 {
		return planHandoverTwoPools(item, regular, fir)
	}
	// A dev-készlet ismeretlen része úgy oszlik meg, hogy a két alacsonyabb
	// prioritású forrás együtt még teljesíthesse mindkét kiválasztott típust.
	first := item
	if first.MarketAvailableQuantity > MaxQuantity-item.MarketFallbackAvailableQuantity {
		return HandoverPlan{}, ErrInvalidQuantity
	}
	first.MarketAvailableQuantity += item.MarketFallbackAvailableQuantity
	first.MarketRegularQuantity += item.MarketFallbackRegularQuantity
	first.MarketFIRQuantity += item.MarketFallbackFIRQuantity
	devPlan, err := planHandoverTwoPools(first, regular, fir, item)
	if err != nil {
		return HandoverPlan{}, err
	}
	if devPlan.RegularToMarket+devPlan.FIRToMarket == 0 {
		return HandoverPlan{Local: devPlan.Local, Market: HandoverEntry{ItemID: item.ItemID}}, nil
	}
	second := item
	second.LocalAvailableQuantity = item.MarketFallbackAvailableQuantity
	second.LocalRegularQuantity = item.MarketFallbackRegularQuantity
	second.LocalFIRQuantity = item.MarketFallbackFIRQuantity
	fallbackPlan, err := planHandoverTwoPools(second, devPlan.RegularToMarket, devPlan.FIRToMarket)
	if err != nil {
		return HandoverPlan{}, err
	}
	return HandoverPlan{Local: devPlan.Local, Fallback: fallbackPlan.Local, Market: fallbackPlan.Market,
		RegularToMarket: fallbackPlan.RegularToMarket, FIRToMarket: fallbackPlan.FIRToMarket}, nil
}

func planHandoverTwoPools(item Item, regular, fir int64, priorityFallback ...Item) (HandoverPlan, error) {
	plan := HandoverPlan{Local: HandoverEntry{ItemID: item.ItemID}, Market: HandoverEntry{ItemID: item.ItemID}}
	if !validID(item.ItemID) || !validQuantity(regular) || !validQuantity(fir) || regular > MaxQuantity-fir || regular+fir == 0 {
		return plan, ErrInvalidQuantity
	}
	if err := validateHandoverBasePools(item); err != nil {
		return plan, err
	}
	plan.Local.Regular = min(regular, item.LocalRegularQuantity)
	plan.Local.FIR = min(fir, item.LocalFIRQuantity)
	r, f := regular-plan.Local.Regular, fir-plan.Local.FIR
	unknown := min(item.LocalAvailableQuantity-item.LocalRegularQuantity-item.LocalFIRQuantity, r+f)
	marketUnknown := item.MarketAvailableQuantity - item.MarketRegularQuantity - item.MarketFIRQuantity
	// Az x FiR darabra kiosztott helyi ismeretlen készlet olyan tartományban
	// maradjon, amelyből a Market mindkét megmaradt minőséget fedezni tudja.
	lower := max(int64(0), unknown-r, f-item.MarketFIRQuantity-marketUnknown)
	upper := min(unknown, f, unknown-max(int64(0), r-item.MarketRegularQuantity-marketUnknown))
	if lower > upper || r+f-unknown > item.MarketAvailableQuantity {
		return plan, ErrInvalidQuantity
	}
	localUnknownFIR := upper
	if len(priorityFallback) != 0 {
		fallback := priorityFallback[0]
		fallbackUnknown := fallback.MarketFallbackAvailableQuantity - fallback.MarketFallbackRegularQuantity - fallback.MarketFallbackFIRQuantity
		best := int64(-1)
		// A két min() fordulópontja és a megengedett tartomány szélei
		// elegendők. Nem járjuk végig az akár milliárdos unknown készletet.
		for _, candidate := range []int64{upper, lower, fallback.MarketFallbackRegularQuantity - r + unknown, f - fallback.MarketFallbackFIRQuantity} {
			x := min(upper, max(lower, candidate))
			remainingRegular, remainingFIR := r-unknown+x, f-x
			known := min(remainingRegular, fallback.MarketFallbackRegularQuantity) + min(remainingFIR, fallback.MarketFallbackFIRQuantity)
			consumed := min(remainingRegular+remainingFIR, known+fallbackUnknown)
			if consumed > best || consumed == best && x > localUnknownFIR {
				best, localUnknownFIR = consumed, x
			}
		}
	}
	localUnknownRegular := unknown - localUnknownFIR
	plan.Local.Unknown = unknown
	r, f = r-localUnknownRegular, f-localUnknownFIR
	plan.RegularToMarket, plan.FIRToMarket = r, f
	plan.Market.Regular, plan.Market.FIR = min(r, item.MarketRegularQuantity), min(f, item.MarketFIRQuantity)
	plan.Market.Unknown = r + f - plan.Market.Regular - plan.Market.FIR
	if plan.Market.Unknown > marketUnknown {
		return plan, ErrInvalidQuantity
	}
	return plan, nil
}

func validateHandoverBasePools(item Item) error {
	for _, quantity := range []int64{item.LocalAvailableQuantity, item.LocalRegularQuantity, item.LocalFIRQuantity, item.MarketAvailableQuantity, item.MarketRegularQuantity, item.MarketFIRQuantity} {
		if !validQuantity(quantity) {
			return ErrInvalidQuantity
		}
	}
	if item.LocalRegularQuantity > item.LocalAvailableQuantity-item.LocalFIRQuantity || item.MarketRegularQuantity > item.MarketAvailableQuantity-item.MarketFIRQuantity {
		return ErrInvalidQuantity
	}
	return nil
}
