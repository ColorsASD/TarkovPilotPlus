package localprogress

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

const maxStoreBytes int64 = 8 << 20
const storeVersion = 5

// Store saját gyűjtött készletet, annak jóváhagyott helyi felhasználását és
// a megerősített Market-változások minőségi jelöléseit menti. Nem fogad
// API-kulcsot és nincs hálózati függősége;
// a Market teljes készlete, követelménye és teljesítése nincs a fájlban.
type Store struct {
	dir       string
	mu        sync.Mutex
	writeFile func(string, []byte) error
	eligible  map[string]map[string]bool
}

type storedProgress struct {
	Version          int                             `json:"version"`
	CharUID          string                          `json:"charUid"`
	GameMode         string                          `json:"gameMode"`
	Stock            map[string]int64                `json:"stock"`
	RegularStock     map[string]int64                `json:"regularStock"`
	FIRStock         map[string]int64                `json:"firStock"`
	MarketKinds      map[string]marketKindAnnotation `json:"marketKinds"`
	LocalUsed        map[string]usedLocalStock       `json:"localUsed"`
	HandoverReceipts map[string][]HandoverEntry      `json:"handoverReceipts"`
	TransferReceipts map[string]HandoverEntry        `json:"transferReceipts"`
}

type usedLocalStock struct {
	Regular int64 `json:"regular"`
	FIR     int64 `json:"fir"`
	Unknown int64 `json:"unknown"`
}

type legacyTypedProgress struct {
	Version      int                             `json:"version"`
	CharUID      string                          `json:"charUid"`
	GameMode     string                          `json:"gameMode"`
	Stock        map[string]int64                `json:"stock"`
	RegularStock map[string]int64                `json:"regularStock"`
	FIRStock     map[string]int64                `json:"firStock"`
	MarketKinds  map[string]marketKindAnnotation `json:"marketKinds"`
}

type marketKindAnnotation struct {
	Regular   int64  `json:"regular"`
	FIR       int64  `json:"fir"`
	StockHash string `json:"stockHash"`
}

// A korábbi saját készlet minősége ismeretlen; a migráció nem talál ki FiR-t.
type legacyStockProgress struct {
	Version  int              `json:"version"`
	CharUID  string           `json:"charUid"`
	GameMode string           `json:"gameMode"`
	Stock    map[string]int64 `json:"stock"`
}

// Csak a privát kísérlet korábbi mentésének ellenőrzött migrációjához kell.
type legacyProgress struct {
	Version   int              `json:"version"`
	CharUID   string           `json:"charUid"`
	GameMode  string           `json:"gameMode"`
	Stock     map[string]int64 `json:"stock"`
	LastSpent map[string]int64 `json:"lastSpent"`
	Snapshot  Snapshot         `json:"snapshot"`
}

func NewStore(dir string) *Store {
	return &Store{dir: dir, writeFile: atomicWrite, eligible: make(map[string]map[string]bool)}
}

// Apply teljes élő merge-et fogad és ad vissza, és megőrzi a korábban gyűjtött
// saját gyűjtött készletrészt akkor is, ha a dev-forrás később eltűnik. A Market
// sorhoz a még felhasználható saját készletet adja; Market készlet nincs a fájlban.
// A cél állapota és a követelmények csak ebben az átmeneti pillanatképben élnek.
func (s *Store) Apply(snapshot Snapshot) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	path, err := s.path(snapshot.CharUID, snapshot.GameMode)
	if err != nil {
		return Snapshot{}, err
	}
	previous, err := readStateWithLegacy(path, snapshot.CharUID, snapshot.GameMode, true)
	if errors.Is(err, os.ErrNotExist) {
		previous = emptyState(snapshot.CharUID, snapshot.GameMode)
	} else if err != nil {
		return Snapshot{}, err
	}
	state := previous
	copy := Clone(snapshot)
	resetAllocation(&copy)
	counts := countRows(copy)
	eligible := make(map[string]bool)
	marketRows := make(map[string]bool)
	for i := range copy.Items {
		item := &copy.Items[i]
		if counts[item.ItemID] != 1 {
			continue
		}
		if item.MarketTracked {
			marketRows[item.ItemID] = true
			annotation, exists := state.MarketKinds[item.ItemID]
			if exists && (item.MarketAvailableQuantity == 0 || annotation.StockHash != marketStockHash(item.ItemID, item.MarketAvailableQuantity)) {
				delete(state.MarketKinds, item.ItemID)
				annotation = marketKindAnnotation{}
			}
			if annotation.Regular > item.MarketAvailableQuantity-annotation.FIR {
				return Snapshot{}, ErrCorruptStore
			}
			item.MarketRegularQuantity, item.MarketFIRQuantity = annotation.Regular, annotation.FIR
		}
		stock, remembered := previous.Stock[item.ItemID]
		if !remembered && !hasDevSource(*item) {
			continue
		}
		used := state.LocalUsed[item.ItemID]
		effective := stock - used.Regular - used.FIR - used.Unknown
		if item.MarketAvailableQuantity > MaxQuantity-effective {
			return Snapshot{}, ErrInvalidQuantity
		}
		state.Stock[item.ItemID] = stock
		for _, source := range item.Sources {
			if source.Origin == OriginDev && active(source) && source.RemainingRequiredQuantity > 0 {
				eligible[item.ItemID] = true
			}
		}
		projectLocalStock(item, state)
		item.AvailableQuantity = item.MarketAvailableQuantity + effective
		if len(item.Sources) > 0 {
			if err := recount(item); err != nil {
				return Snapshot{}, err
			}
		} else {
			item.RemainingQuantity = max(0, item.RequiredQuantity-item.AvailableQuantity)
		}
	}
	// Hiányzó vagy több változatot takaró Market sorhoz nem tartható meg
	// bizonyított minőségi jelölés. A saját helyi készlet ettől megmarad.
	for id := range state.MarketKinds {
		if !marketRows[id] {
			delete(state.MarketKinds, id)
		}
	}
	if err := s.commit(path, state, true); err != nil {
		return Snapshot{}, err
	}
	s.eligible[snapshot.CharUID+"\x00"+snapshot.GameMode] = eligible
	return copy, nil
}

// Add a legutóbbi sikeres, élő Apply által engedélyezett helyi készletrészt
// növeli. A hívó újraindítás után először friss Apply-t kér; a fájl önmagában
// nem tartalmaz aktív dev-követelményt vagy aktuális hozzáadási jogosultságot.
func (s *Store) Add(charUID, gameMode, itemID string) (Snapshot, error) {
	return s.AddKind(charUID, gameMode, itemID, "regular")
}

// AddKind a saját helyi teljes készletet és a kiválasztott minőségi részét
// egyszerre növeli. A korábban mentett, ismeretlen készletrész változatlan marad.
func (s *Store) AddKind(charUID, gameMode, itemID, kind string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validID(itemID) {
		return Snapshot{}, ErrInvalidIdentity
	}
	if !validStockKind(kind) {
		return Snapshot{}, ErrInvalidQuantity
	}
	path, err := s.path(charUID, gameMode)
	if err != nil {
		return Snapshot{}, err
	}
	state, err := readState(path, charUID, gameMode)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, ErrItemNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	stock, exists := state.Stock[itemID]
	if !exists || !s.eligible[charUID+"\x00"+gameMode][itemID] {
		return Snapshot{}, ErrItemNotFound
	}
	if stock >= MaxQuantity {
		return Snapshot{}, ErrInvalidQuantity
	}
	state.Stock[itemID] = stock + 1
	if kind == "fir" {
		state.FIRStock[itemID]++
	} else {
		state.RegularStock[itemID]++
	}
	if err := s.commit(path, state); err != nil {
		return Snapshot{}, err
	}
	return stockSnapshot(state), nil
}

// RecordMarketAdd csak a hívó által már megerősített, egy darabos API-növelés
// minőségét menti. Nem növeli a helyi teljes készletet és nem végez API-írást.
// Eltérő kiinduló készletnél a korábbi minőség ismeretlennek számít.
func (s *Store) RecordMarketAdd(charUID, gameMode, itemID, kind string, before, after int64) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validID(itemID) {
		return Snapshot{}, ErrInvalidIdentity
	}
	if !validStockKind(kind) || !validQuantity(before) || !validQuantity(after) || before >= MaxQuantity || after != before+1 {
		return Snapshot{}, ErrInvalidQuantity
	}
	path, err := s.path(charUID, gameMode)
	if err != nil {
		return Snapshot{}, err
	}
	state, err := readState(path, charUID, gameMode)
	if errors.Is(err, os.ErrNotExist) {
		state = emptyState(charUID, gameMode)
	} else if err != nil {
		return Snapshot{}, err
	}
	annotation := state.MarketKinds[itemID]
	if annotation.StockHash != marketStockHash(itemID, before) {
		annotation = marketKindAnnotation{}
	}
	if annotation.Regular > before-annotation.FIR {
		return Snapshot{}, ErrCorruptStore
	}
	if kind == "fir" {
		annotation.FIR++
	} else {
		annotation.Regular++
	}
	annotation.StockHash = marketStockHash(itemID, after)
	state.MarketKinds[itemID] = annotation
	if err := s.commit(path, state); err != nil {
		return Snapshot{}, err
	}
	return stockSnapshot(state), nil
}

func validStockKind(kind string) bool { return kind == "regular" || kind == "fir" }

// A lenyomat kizárólag a minőségi jelölés frissességét ellenőrzi. Nem ad
// vissza készletet: a teljes Market mennyiséget mindig az élő API szolgáltatja.
func marketStockHash(itemID string, quantity int64) string {
	hash := sha256.Sum256([]byte(itemID + "\x00" + strconv.FormatInt(quantity, 10)))
	return hex.EncodeToString(hash[:])
}

func emptyState(charUID, gameMode string) storedProgress {
	return storedProgress{Version: storeVersion, CharUID: charUID, GameMode: gameMode,
		Stock: make(map[string]int64), RegularStock: make(map[string]int64), FIRStock: make(map[string]int64), MarketKinds: make(map[string]marketKindAnnotation),
		LocalUsed: make(map[string]usedLocalStock), HandoverReceipts: make(map[string][]HandoverEntry), TransferReceipts: make(map[string]HandoverEntry)}
}

// Load minimális helyi készletpillanatképet ad vissza. Nincs benne Market sor,
// frissességi revízió, követelmény, célállapot vagy completed számláló.
func (s *Store) Load(charUID, gameMode string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(charUID, gameMode)
	if err != nil {
		return Snapshot{}, err
	}
	state, err := readState(path, charUID, gameMode)
	if err != nil {
		return Snapshot{}, err
	}
	return stockSnapshot(state), nil
}

func stockSnapshot(state storedProgress) Snapshot {
	snapshot := Snapshot{CharUID: state.CharUID, GameMode: state.GameMode, Revision: "local", Items: []Item{}}
	set := make(map[string]bool, len(state.Stock)+len(state.MarketKinds))
	for id := range state.Stock {
		set[id] = true
	}
	for id := range state.MarketKinds {
		set[id] = true
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		annotation := state.MarketKinds[id]
		item := Item{ItemID: id, MarketRegularQuantity: annotation.Regular, MarketFIRQuantity: annotation.FIR, Sources: []Source{}}
		projectLocalStock(&item, state)
		item.AvailableQuantity = item.LocalAvailableQuantity
		snapshot.Items = append(snapshot.Items, item)
	}
	return snapshot
}

func countRows(snapshot Snapshot) map[string]int {
	counts := make(map[string]int)
	for _, item := range snapshot.Items {
		counts[item.ItemID]++
	}
	return counts
}

func localOnly(item Item) bool {
	if item.MarketTracked || len(item.Sources) == 0 {
		return false
	}
	for _, source := range item.Sources {
		if source.Origin != OriginDev {
			return false
		}
	}
	return true
}

func hasDevSource(item Item) bool {
	for _, source := range item.Sources {
		if source.Origin == OriginDev {
			return true
		}
	}
	return false
}

func (s *Store) path(charUID, gameMode string) (string, error) {
	if !validID(charUID) || !validMode(gameMode) || s.dir == "" {
		return "", ErrInvalidIdentity
	}
	hash := sha256.Sum256([]byte(charUID + "\x00" + gameMode))
	return filepath.Join(s.dir, hex.EncodeToString(hash[:])+".json"), nil
}

func readState(path, charUID, gameMode string) (storedProgress, error) {
	return readStateWithLegacy(path, charUID, gameMode, false)
}

func readStateWithLegacy(path, charUID, gameMode string, allowLegacy bool) (storedProgress, error) {
	file, err := os.Open(path)
	if err != nil {
		return storedProgress{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStoreBytes+1))
	if err != nil {
		return storedProgress{}, err
	}
	if int64(len(data)) > maxStoreBytes {
		return storedProgress{}, ErrStoreTooLarge
	}
	var header struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(data, &header) != nil {
		return storedProgress{}, ErrCorruptStore
	}
	if header.Version == 1 && allowLegacy {
		var legacy legacyProgress
		if err := strictDecode(data, &legacy); err != nil {
			return storedProgress{}, err
		}
		if legacy.CharUID != charUID || legacy.GameMode != gameMode || legacy.Stock == nil || legacy.LastSpent == nil || legacy.Snapshot.CharUID != charUID || legacy.Snapshot.GameMode != gameMode {
			return storedProgress{}, ErrCorruptStore
		}
		if err := validateSnapshot(legacy.Snapshot); err != nil {
			return storedProgress{}, errors.Join(ErrCorruptStore, err)
		}
		for id, stock := range legacy.Stock {
			if !validID(id) || !validQuantity(stock) {
				return storedProgress{}, ErrCorruptStore
			}
		}
		for id, spent := range legacy.LastSpent {
			if !validID(id) || !validQuantity(spent) {
				return storedProgress{}, ErrCorruptStore
			}
		}
		state := emptyState(charUID, gameMode)
		counts := countRows(legacy.Snapshot)
		for _, item := range legacy.Snapshot.Items {
			if !localOnly(item) || counts[item.ItemID] != 1 {
				continue
			}
			stock, exists := legacy.Stock[item.ItemID]
			if !exists || !legacy.Snapshot.FIRInventorySeparate && stock != item.AvailableQuantity {
				return storedProgress{}, ErrCorruptStore
			}
			state.Stock[item.ItemID] = stock
		}
		return state, nil
	}
	if header.Version == 2 {
		var legacy legacyStockProgress
		if err := strictDecode(data, &legacy); err != nil {
			return storedProgress{}, err
		}
		if legacy.CharUID != charUID || legacy.GameMode != gameMode || legacy.Stock == nil {
			return storedProgress{}, ErrCorruptStore
		}
		state := emptyState(charUID, gameMode)
		state.Stock = legacy.Stock
		if err := validateStoredState(state); err != nil {
			return storedProgress{}, err
		}
		return state, nil
	}
	if header.Version == 3 {
		var legacy legacyTypedProgress
		if err := strictDecode(data, &legacy); err != nil {
			return storedProgress{}, err
		}
		if legacy.CharUID != charUID || legacy.GameMode != gameMode {
			return storedProgress{}, ErrCorruptStore
		}
		state := emptyState(charUID, gameMode)
		state.Stock, state.RegularStock, state.FIRStock, state.MarketKinds = legacy.Stock, legacy.RegularStock, legacy.FIRStock, legacy.MarketKinds
		if err := validateStoredState(state); err != nil {
			return storedProgress{}, err
		}
		return state, nil
	}
	if header.Version != 4 && header.Version != storeVersion {
		return storedProgress{}, ErrCorruptStore
	}
	// A jelenlegi sémákat ugyanaz a tiszta, teljes mezőellenőrzés védi,
	// amely az importált mentést is ellenőrzi.
	if _, err := ValidateBackupStock(data); err != nil {
		return storedProgress{}, err
	}
	var state storedProgress
	if err := strictDecode(data, &state); err != nil {
		return storedProgress{}, err
	}
	if state.CharUID != charUID || state.GameMode != gameMode {
		return storedProgress{}, ErrCorruptStore
	}
	if state.Version == 4 {
		state.Version = storeVersion
		state.TransferReceipts = make(map[string]HandoverEntry)
	}
	if err := validateStoredState(state); err != nil {
		return storedProgress{}, err
	}
	return state, nil
}

func validateStoredState(state storedProgress) error {
	if state.Version != storeVersion || !validID(state.CharUID) || !validMode(state.GameMode) || state.Stock == nil ||
		state.RegularStock == nil || state.FIRStock == nil || state.MarketKinds == nil || state.LocalUsed == nil || state.HandoverReceipts == nil || state.TransferReceipts == nil {
		return ErrCorruptStore
	}
	for id, stock := range state.Stock {
		if !validID(id) || !validQuantity(stock) {
			return ErrCorruptStore
		}
		if regular, fir := state.RegularStock[id], state.FIRStock[id]; !validQuantity(regular) || !validQuantity(fir) || regular > stock-fir {
			return ErrCorruptStore
		}
	}
	for _, typed := range []map[string]int64{state.RegularStock, state.FIRStock} {
		for id, quantity := range typed {
			if _, exists := state.Stock[id]; !exists || !validID(id) || !validQuantity(quantity) {
				return ErrCorruptStore
			}
		}
	}
	for id, annotation := range state.MarketKinds {
		if !validID(id) || !validQuantity(annotation.Regular) || !validQuantity(annotation.FIR) || annotation.Regular > MaxQuantity-annotation.FIR {
			return ErrCorruptStore
		}
		decoded, err := hex.DecodeString(annotation.StockHash)
		if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != annotation.StockHash {
			return ErrCorruptStore
		}
	}
	if err := validateHandovers(state); err != nil {
		return err
	}
	return validateTransfers(state)
}

func strictDecode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return ErrCorruptStore
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return ErrCorruptStore
	}
	return nil
}

func (s *Store) commit(path string, state storedProgress, onlyChanged ...bool) error {
	if err := validateStoredState(state); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if int64(len(data)) > maxStoreBytes {
		return ErrStoreTooLarge
	}
	if len(onlyChanged) > 0 && onlyChanged[0] {
		// Az Inspect/publikálás nem ment ismét változatlan készletet. A
		// kanonikus fájlbájtok összevetése a régi séma migrációját is megtartja.
		file, err := os.Open(path)
		if err == nil {
			previous, readErr := io.ReadAll(io.LimitReader(file, maxStoreBytes+1))
			file.Close()
			if readErr == nil && bytes.Equal(previous, data) {
				return nil
			}
		}
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return fmt.Errorf("Helyi előrehaladás mappája: %w", err)
	}
	if err := s.writeFile(path, data); err != nil {
		return fmt.Errorf("Helyi előrehaladás mentése: %w", err)
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".local-progress-*.tmp")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
