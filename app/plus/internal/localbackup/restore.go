// Package localbackup a saját mentési mappák helyi, visszaállítható cseréjét végzi.
package localbackup

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var Directories = []string{"local-progress", "market-fallback", "stock-usage", "handovers", "stock-edits", "market-fallback-sync"}
var ErrRestore = errors.New("The local backup could not be restored safely. Existing files and the recovery journal were kept.")

const workName = ".local-progress-import"

type File struct {
	Path string
	Data []byte
}
type restoreJournal struct {
	Version    int             `json:"version"`
	Phase      string          `json:"phase"`
	ID         string          `json:"id"`
	OldPresent map[string]bool `json:"oldPresent"`
	Checksum   string          `json:"checksum"`
}

func ValidPath(path string) bool {
	parts := strings.Split(path, "/")
	if len(parts) != 2 || strings.ContainsAny(path, "\\:\x00") || !knownDir(parts[0]) || !strings.HasSuffix(parts[1], ".json") {
		return false
	}
	if parts[0] == "market-fallback-sync" && len(parts[1]) == 134 && parts[1][64] == '-' {
		return canonicalHash(parts[1][:64]) && canonicalHash(parts[1][65:129])
	}
	if len(parts[1]) != 69 {
		return false
	}
	return canonicalHash(parts[1][:64])
}

func canonicalHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

// GeneratedTemporaryLimit csak a saját atomikus írók pontos fájlnévmintáit
// ismeri. A tartalom félbemaradhat, ezért nem importáljuk és nem töröljük;
// a hívó regular-file, link- és méretellenőrzést is végez.
func GeneratedTemporaryLimit(dir, name string) (int64, bool) {
	if dir == "stock-usage" && strings.HasSuffix(name, ".json.tmp") && len(name) == 73 && canonicalHash(name[:64]) {
		return 8 << 20, true
	}
	prefix, limit := "", int64(0)
	switch dir {
	case "local-progress", "market-fallback":
		prefix, limit = ".local-progress-", 8<<20
	case "handovers":
		prefix, limit = ".handover-", 256<<10
	case "stock-edits":
		prefix, limit = ".stock-edit-", 8<<10
	case "market-fallback-sync":
		prefix, limit = ".fallback-sync-", 8<<10
	default:
		return 0, false
	}
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".tmp") {
		return 0, false
	}
	number := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".tmp")
	value, err := strconv.ParseUint(number, 10, 32)
	return limit, err == nil && strconv.FormatUint(value, 10) == number
}
func knownDir(dir string) bool {
	for _, name := range Directories {
		if name == dir {
			return true
		}
	}
	return false
}

// CheckPath nem követhet linket vagy junctiont egy mentési fájlig.
func CheckPath(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ErrRestore
	}
	for current := absolute; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return ErrRestore
		}
		if err == nil && info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return ErrRestore
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}
func checkTree(path string) error {
	if CheckPath(path) != nil {
		return ErrRestore
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return ErrRestore
	}
	folder, err := os.Open(path)
	if err != nil {
		return ErrRestore
	}
	entries, err := folder.ReadDir(2049)
	closeErr := folder.Close()
	if err != nil && !errors.Is(err, io.EOF) || closeErr != nil || len(entries) > 2048 {
		return ErrRestore
	}
	for _, entry := range entries {
		dir := filepath.Base(path)
		limit, temporary := GeneratedTemporaryLimit(dir, entry.Name())
		if !ValidPath(dir+"/"+entry.Name()) && !temporary {
			return ErrRestore
		}
		p := filepath.Join(path, entry.Name())
		if CheckPath(p) != nil {
			return ErrRestore
		}
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() || temporary && info.Size() > limit {
			return ErrRestore
		}
	}
	return nil
}

// Restore előtt a hívó a teljes csomag minden sémáját és nyugtáját validálja.
func Restore(root string, files []File) error { return restore(root, files, os.Rename) }
func restore(root string, files []File, rename func(string, string) error) error {
	if root == "" || CheckPath(root) != nil || os.MkdirAll(root, 0700) != nil {
		return ErrRestore
	}
	if err := Recover(root); err != nil {
		return err
	}
	work := filepath.Join(root, workName)
	if os.Mkdir(work, 0700) != nil {
		return ErrRestore
	}
	prepared := false
	defer func() {
		if !prepared {
			_ = os.RemoveAll(work)
		}
	}()
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return ErrRestore
	}
	j := restoreJournal{Version: 1, Phase: "staging", ID: hex.EncodeToString(id), OldPresent: map[string]bool{}}
	for _, dir := range Directories {
		live := filepath.Join(root, dir)
		if err := checkTree(live); err != nil {
			return err
		}
		_, err := os.Lstat(live)
		j.OldPresent[dir] = err == nil
	}
	// A staging marker még a saját másolatok előtt készül. Ebben a fázisban
	// az élő mappákhoz nem nyúlunk, így egy megszakított másolás eldobható.
	if writeJournal(work, j) != nil {
		return ErrRestore
	}
	for _, part := range []string{"new", "old"} {
		if os.Mkdir(filepath.Join(work, part), 0700) != nil {
			return ErrRestore
		}
	}
	for _, dir := range Directories {
		if os.Mkdir(filepath.Join(work, "new", dir), 0700) != nil {
			return ErrRestore
		}
	}
	seen := map[string]bool{}
	for _, file := range files {
		if !ValidPath(file.Path) || seen[file.Path] {
			return ErrRestore
		}
		seen[file.Path] = true
		if err := writeSync(filepath.Join(work, "new", filepath.FromSlash(file.Path)), file.Data); err != nil {
			return ErrRestore
		}
	}
	j.Phase = "prepared"
	if writeJournal(work, j) != nil {
		return ErrRestore
	}
	prepared = true
	for _, dir := range Directories {
		live := filepath.Join(root, dir)
		if j.OldPresent[dir] {
			if rename(live, filepath.Join(work, "old", dir)) != nil {
				return errors.Join(ErrRestore, Recover(root))
			}
		}
		if rename(filepath.Join(work, "new", dir), live) != nil {
			return errors.Join(ErrRestore, Recover(root))
		}
	}
	j.Phase = "committed"
	if writeJournal(work, j) != nil {
		return errors.Join(ErrRestore, Recover(root))
	}
	// A durable committed marker után a csere sikeres. Egy későbbi cleanup
	// hibája nem változtatja vissza hibásan a hívó élő Store-handle-jeit.
	_ = Recover(root)
	return nil
}

// Recover a Store-ok létrehozása előtt hívandó. Staging csak másolatot dob el,
// prepared visszaállítja a régi állapotot, committed archiválja a régi másolatot.
func Recover(root string) error {
	if root == "" || CheckPath(root) != nil {
		return ErrRestore
	}
	work := filepath.Join(root, workName)
	info, err := os.Lstat(work)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || CheckPath(work) != nil {
		return ErrRestore
	}
	journalPath := filepath.Join(work, "journal.json")
	if CheckPath(journalPath) != nil {
		return ErrRestore
	}
	file, err := os.Open(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return removeUnstartedWork(work)
	}
	if err != nil {
		return ErrRestore
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > 4096 || !validJournalJSON(data) {
		return ErrRestore
	}
	var j restoreJournal
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&j) != nil || d.Decode(new(any)) != io.EOF || j.Version != 1 || j.Phase != "staging" && j.Phase != "prepared" && j.Phase != "committed" || len(j.ID) != 24 || len(j.OldPresent) != len(Directories) {
		return ErrRestore
	}
	if j.Checksum != journalChecksum(j) {
		return ErrRestore
	}
	decoded, err := hex.DecodeString(j.ID)
	if err != nil || hex.EncodeToString(decoded) != j.ID {
		return ErrRestore
	}
	for _, dir := range Directories {
		if _, ok := j.OldPresent[dir]; !ok {
			return ErrRestore
		}
	}
	if validateWork(work) != nil {
		return ErrRestore
	}
	if j.Phase == "staging" {
		// Itt az old könyvtár üres kell legyen. Mentett régi adat mellett
		// sem egy elveszett, sem egy hibás fázisjelzés nem indokol törlést.
		entries, err := boundedDirectory(filepath.Join(work, "old"), 1)
		if err != nil && !errors.Is(err, os.ErrNotExist) || len(entries) != 0 {
			return ErrRestore
		}
		return os.RemoveAll(work)
	}
	if j.Phase == "committed" {
		history := filepath.Join(root, ".local-progress-backups")
		if CheckPath(history) != nil || os.MkdirAll(history, 0700) != nil {
			return ErrRestore
		}
		old := filepath.Join(work, "old")
		if _, err := os.Lstat(old); err == nil {
			if os.Rename(old, filepath.Join(history, j.ID)) != nil {
				return ErrRestore
			}
		}
		return os.RemoveAll(work)
	}
	for _, dir := range Directories {
		live, old := filepath.Join(root, dir), filepath.Join(work, "old", dir)
		if checkTree(live) != nil {
			return ErrRestore
		}
		_, oldErr := os.Lstat(old)
		if oldErr == nil {
			if os.RemoveAll(live) != nil || os.Rename(old, live) != nil {
				return ErrRestore
			}
		} else if !errors.Is(oldErr, os.ErrNotExist) {
			return ErrRestore
		} else if !j.OldPresent[dir] {
			if os.RemoveAll(live) != nil {
				return ErrRestore
			}
		}
	}
	return os.RemoveAll(work)
}

func validateWork(work string) error {
	entries, err := boundedDirectory(work, 256)
	if err != nil {
		return ErrRestore
	}
	for _, entry := range entries {
		path := filepath.Join(work, entry.Name())
		if CheckPath(path) != nil {
			return ErrRestore
		}
		if entry.Name() == "journal.json" || strings.HasPrefix(entry.Name(), ".journal-") {
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				return ErrRestore
			}
			continue
		}
		if entry.Name() != "new" && entry.Name() != "old" || !entry.IsDir() {
			return ErrRestore
		}
		children, err := boundedDirectory(path, len(Directories))
		if err != nil {
			return ErrRestore
		}
		for _, child := range children {
			if !knownDir(child.Name()) || checkTree(filepath.Join(path, child.Name())) != nil {
				return ErrRestore
			}
		}
	}
	return nil
}

// Napló nélkül csak a még el nem kezdett import üres mappája vagy egy
// félbemaradt napló-tempfájl takarítható. Ismeretlen másolatot megőrzünk.
func removeUnstartedWork(work string) error {
	entries, err := boundedDirectory(work, 256)
	if err != nil {
		return ErrRestore
	}
	for _, entry := range entries {
		path := filepath.Join(work, entry.Name())
		info, err := os.Lstat(path)
		if !strings.HasPrefix(entry.Name(), ".journal-") || CheckPath(path) != nil || err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
			return ErrRestore
		}
	}
	return os.RemoveAll(work)
}

func boundedDirectory(path string, limit int) ([]os.DirEntry, error) {
	folder, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	entries, readErr := folder.ReadDir(limit + 1)
	closeErr := folder.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil || len(entries) > limit {
		return nil, ErrRestore
	}
	return entries, nil
}
func writeJournal(work string, j restoreJournal) error {
	j.Checksum = journalChecksum(j)
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(work, ".journal-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(work, "journal.json"))
}

func journalChecksum(j restoreJournal) string {
	j.Checksum = ""
	data, _ := json.Marshal(j)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
func validJournalJSON(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return false
		}
		seen[name] = true
		switch name {
		case "oldPresent":
			start, err := d.Token()
			if err != nil || start != json.Delim('{') {
				return false
			}
			dirs := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				dir, ok := key.(string)
				if err != nil || !ok || !knownDir(dir) || dirs[dir] {
					return false
				}
				dirs[dir] = true
				value, err := d.Token()
				if _, ok := value.(bool); err != nil || !ok {
					return false
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') || len(dirs) != len(Directories) {
				return false
			}
		case "version":
			value, err := d.Token()
			if err != nil || value != float64(1) {
				return false
			}
		case "phase", "id", "checksum":
			value, err := d.Token()
			if _, ok := value.(string); err != nil || !ok {
				return false
			}
		default:
			return false
		}
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || len(seen) != 5 {
		return false
	}
	_, err = d.Token()
	return err == io.EOF
}
func writeSync(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}
