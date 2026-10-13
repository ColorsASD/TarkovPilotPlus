package localprogress

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// ValidateBackupStock kizárólag a jelenlegi saját készlet mentési formáját
// ellenőrzi. Nem migrál, nem ír fájlt, nem kér API-adatot, és nem talál ki
// Market-készletet a minőségi jelölésekből.
func ValidateBackupStock(data []byte) (Snapshot, error) {
	if int64(len(data)) > maxStoreBytes {
		return Snapshot{}, ErrStoreTooLarge
	}
	if !utf8.Valid(data) || !backupStockUniqueJSON(data) {
		return Snapshot{}, ErrCorruptStore
	}
	var header struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(data, &header) != nil || header.Version != 4 && header.Version != storeVersion {
		return Snapshot{}, ErrCorruptStore
	}
	keys := []string{"version", "charUid", "gameMode", "stock", "regularStock", "firStock", "marketKinds", "localUsed", "handoverReceipts"}
	if header.Version == storeVersion {
		keys = append(keys, "transferReceipts")
	}
	fields, ok := backupStockObject(data, keys...)
	if !ok {
		return Snapshot{}, ErrCorruptStore
	}
	for _, name := range []string{"stock", "regularStock", "firStock"} {
		if _, ok := backupStockMap(fields[name]); !ok {
			return Snapshot{}, ErrCorruptStore
		}
	}
	for _, shape := range []struct {
		name string
		keys []string
	}{
		{"marketKinds", []string{"regular", "fir", "stockHash"}},
		{"localUsed", []string{"regular", "fir", "unknown"}},
	} {
		entries, ok := backupStockMap(fields[shape.name])
		if !ok {
			return Snapshot{}, ErrCorruptStore
		}
		for _, entry := range entries {
			if _, ok := backupStockObject(entry, shape.keys...); !ok {
				return Snapshot{}, ErrCorruptStore
			}
		}
	}
	receipts, ok := backupStockMap(fields["handoverReceipts"])
	if !ok {
		return Snapshot{}, ErrCorruptStore
	}
	for _, receipt := range receipts {
		var entries []json.RawMessage
		if json.Unmarshal(receipt, &entries) != nil || entries == nil {
			return Snapshot{}, ErrCorruptStore
		}
		for _, entry := range entries {
			if _, ok := backupStockObject(entry, "itemId", "regular", "fir", "unknown"); !ok {
				return Snapshot{}, ErrCorruptStore
			}
		}
	}
	if header.Version == storeVersion {
		transfers, ok := backupStockMap(fields["transferReceipts"])
		if !ok {
			return Snapshot{}, ErrCorruptStore
		}
		for _, entry := range transfers {
			if _, ok := backupStockObject(entry, "itemId", "regular", "fir", "unknown"); !ok {
				return Snapshot{}, ErrCorruptStore
			}
		}
	}
	var state storedProgress
	if err := strictDecode(data, &state); err != nil {
		return Snapshot{}, err
	}
	if state.Version == 4 {
		state.Version = storeVersion
		state.TransferReceipts = make(map[string]HandoverEntry)
	}
	if err := validateStoredState(state); err != nil {
		return Snapshot{}, err
	}
	return stockSnapshot(state), nil
}

// A kanonikus mezőnevek kizárják a Go dekóder által elfogadott kis- és
// nagybetűs aliasokat. A null sem mennyiség, sem hiányzó mentési rész.
func backupStockObject(data []byte, keys ...string) (map[string]json.RawMessage, bool) {
	fields, ok := backupStockMap(data)
	if !ok || len(fields) != len(keys) {
		return nil, false
	}
	for _, key := range keys {
		if _, exists := fields[key]; !exists {
			return nil, false
		}
	}
	return fields, true
}

func backupStockMap(data []byte) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return nil, false
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, false
		}
	}
	return fields, true
}

// Az ismételt kulcsot már a strukturált dekódolás előtt elutasítjuk.
// A nyugták tömbjei megengedettek; a korlátozott mélység a mentési séma felett van.
func backupStockUniqueJSON(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var readValue func(int) bool
	readValue = func(depth int) bool {
		if depth > 16 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return true
		}
		switch delim {
		case '{':
			seen := make(map[string]bool)
			for decoder.More() {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || seen[key] || !readValue(depth+1) {
					return false
				}
				seen[key] = true
			}
			closing, err := decoder.Token()
			return err == nil && closing == json.Delim('}')
		case '[':
			for decoder.More() {
				if !readValue(depth + 1) {
					return false
				}
			}
			closing, err := decoder.Token()
			return err == nil && closing == json.Delim(']')
		default:
			return false
		}
	}
	if !readValue(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}
