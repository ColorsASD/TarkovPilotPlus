package plus

import (
	"strings"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

// A sikeres PATCH-nyugta a saját műveletet igazolja; a későbbi GET már más
// kliens új készletét is mutathatja. A revíziók átlátszatlan azonosítók.
func pendingMarketWriteAcknowledged(entry pendingMarketAdd) bool {
	return entry.Stage == "sent" && entry.WriteError == nil && entry.ACK.OK && entry.ACK.Changed &&
		strings.TrimSpace(entry.ACK.Revision) != "" && entry.BeforeRevision != "" && entry.ACK.Revision != entry.BeforeRevision
}

func pendingMarketChangeConfirmed(entry pendingMarketAdd, remote market.TrackedSnapshot) bool {
	if entry.Stage != "sent" || remote.CharUID != entry.Opts.CharacterUID || !strings.EqualFold(remote.GameMode, entry.Opts.Mode) ||
		remote.Items == nil || remote.Revision == "" || remote.Revision == entry.BeforeRevision {
		return false
	}
	quantity, known := observedPendingStock(entry, remote)
	if !known {
		return false
	}
	if quantity == entry.Target {
		return true
	}
	// Ugyanahhoz a revízióhoz eltérő készlet ellentmondó válasz lenne.
	// Bizonytalan nyugtát sem igazolhat egy más mennyiségű külső módosítás.
	return pendingMarketWriteAcknowledged(entry) && remote.Revision != entry.ACK.Revision
}
