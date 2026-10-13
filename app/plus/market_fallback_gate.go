package plus

import "strings"

const marketFallbackItemBlockedMessage = "This item's Market upload is awaiting verification. Its local stock and other items can still be changed."

// A valid függő szándék csak a saját Market és rögzített fallback forrását
// zárolja. Ismeretlen vagy sérült szándék mellett nincs biztonságos item scope.
// A hívó az alkalmazás mu zárolását tartja.
func (a *ItemWindowApp) marketFallbackBlocksItemLocked(id string) bool {
	if a.state.MarketFallbackInvalid {
		return true
	}
	if !a.state.MarketFallbackPending {
		return false
	}
	if !handoverItemID(id) {
		return true
	}
	if len(a.state.MarketFallbackItemIDs) != 0 {
		for _, pendingID := range a.state.MarketFallbackItemIDs {
			if !handoverItemID(pendingID) || pendingID == id {
				return true
			}
		}
		return false
	}
	if !handoverItemID(a.state.MarketFallbackItemID) {
		return true
	}
	return a.state.MarketFallbackItemID == id
}

func (a *ItemWindowApp) marketFallbackAppendAllowedLocked(id string) bool {
	if !handoverItemID(id) || a.state.MarketFallbackInvalid {
		return false
	}
	for _, allowed := range a.state.MarketFallbackAppendAllowedItemIDs {
		if allowed == id {
			return true
		}
	}
	return len(a.state.MarketFallbackItemIDs) == 0 && a.state.MarketFallbackItemID == id && a.state.MarketFallbackAppendAllowed
}

// Az írás előtti ellenőrzés a kanonikus, bounded naplót olvassa; sem
// alkalmazásállapotot, sem készletet, sem a függő szándékot nem módosítja.
func (a *ItemWindowApp) marketFallbackBlocksItem(opts ItemOptions, id string) (bool, error) {
	if !handoverItemID(id) {
		return true, errMarketFallbackSyncInvalid
	}
	pending, err := a.readMarketFallbackSync(opts, id)
	if err != nil {
		return true, err
	}
	return pending != nil && pending.ItemID == id, nil
}

// A diskből származó scope csak az aktuális profilhoz publikálható.
// A hívó mu-t tart; az olvasó itt nem kér alkalmazás- vagy hálózati lockot.
func (a *ItemWindowApp) refreshMarketFallbackPendingLocked(opts ItemOptions) error {
	if a.state.ItemOptions.revision != opts.revision || a.state.CharacterUID != opts.CharacterUID ||
		a.state.ItemOptions.CharacterUID != opts.CharacterUID || !strings.EqualFold(a.state.ItemOptions.Mode, opts.Mode) {
		return nil
	}
	intents, err := a.readMarketFallbackSyncIntents(opts)
	a.state.MarketFallbackPending = len(intents) != 0 || err != nil
	a.state.MarketFallbackInvalid = err != nil
	a.state.MarketFallbackItemID = ""
	a.state.MarketFallbackAppendAllowed = false
	a.state.MarketFallbackItemIDs = []string{}
	a.state.MarketFallbackAppendAllowedItemIDs = []string{}
	if err == nil {
		for _, pending := range intents {
			a.state.MarketFallbackItemIDs = append(a.state.MarketFallbackItemIDs, pending.ItemID)
			if pending.Version == 2 {
				allowed, guardErr := a.marketFallbackIntentAllowsAppend(opts, pending.ItemID)
				if allowed && guardErr == nil {
					a.state.MarketFallbackAppendAllowedItemIDs = append(a.state.MarketFallbackAppendAllowedItemIDs, pending.ItemID)
				}
			}
		}
	}
	if err == nil && len(intents) != 0 {
		pending := intents[0]
		a.state.MarketFallbackItemID = pending.ItemID
		if pending.Version == 2 {
			a.state.MarketFallbackAppendAllowed, _ = a.marketFallbackIntentAllowsAppend(opts, pending.ItemID)
		}
	}
	return err
}
