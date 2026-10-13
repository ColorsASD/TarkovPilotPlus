package localbackup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testFile = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json"

func oldRestoreFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range Directories {
		if os.Mkdir(filepath.Join(root, dir), 0700) != nil || os.WriteFile(filepath.Join(root, dir, testFile), []byte("old-"+dir), 0600) != nil {
			t.Fatal("fixture")
		}
	}
	if os.WriteFile(filepath.Join(root, "api-key.bin"), []byte("kept-key"), 0600) != nil {
		t.Fatal("key")
	}
	return root
}
func newRestoreFiles() []File {
	out := []File{}
	for _, dir := range Directories {
		out = append(out, File{dir + "/" + testFile, []byte("new-" + dir)})
	}
	return out
}
func assertRestoreFiles(t *testing.T, root, prefix string) {
	t.Helper()
	for _, dir := range Directories {
		data, err := os.ReadFile(filepath.Join(root, dir, testFile))
		if err != nil || string(data) != prefix+"-"+dir {
			t.Fatalf("partial state: %s %q %v", dir, data, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, "api-key.bin"))
	if string(data) != "kept-key" {
		t.Fatal("unrelated key changed")
	}
}
func TestRestoreSixDirectoriesKeepsAutomaticOldBackup(t *testing.T) {
	root := oldRestoreFixture(t)
	if err := Restore(root, newRestoreFiles()); err != nil {
		t.Fatal(err)
	}
	assertRestoreFiles(t, root, "new")
	entries, err := os.ReadDir(filepath.Join(root, ".local-progress-backups"))
	if err != nil || len(entries) != 1 {
		t.Fatal("old backup missing", err)
	}
	old := filepath.Join(root, ".local-progress-backups", entries[0].Name())
	for _, dir := range Directories {
		data, _ := os.ReadFile(filepath.Join(old, dir, testFile))
		if string(data) != "old-"+dir {
			t.Fatal("old backup changed")
		}
	}
	if err := Recover(root); err != nil {
		t.Fatal(err)
	}
	assertRestoreFiles(t, root, "new")
}

func TestRestoreGeneratedTemporaryArtifactsArePreservedInOldBackupAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "rollback"}[fail], func(t *testing.T) {
			root := oldRestoreFixture(t)
			path := filepath.Join(root, "handovers", ".handover-1.tmp")
			if err := os.WriteFile(path, []byte("partial interrupted file"), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			err := restore(root, newRestoreFiles(), func(from, to string) error {
				calls++
				if fail && calls == 2 {
					return errors.New("injected disk failure")
				}
				return os.Rename(from, to)
			})
			if fail {
				if err == nil {
					t.Fatal("failure accepted")
				}
				assertRestoreFiles(t, root, "old")
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertRestoreFiles(t, root, "new")
				entries, e := os.ReadDir(filepath.Join(root, ".local-progress-backups"))
				if e != nil || len(entries) != 1 {
					t.Fatal("old backup missing", e)
				}
				path = filepath.Join(root, ".local-progress-backups", entries[0].Name(), "handovers", ".handover-1.tmp")
			}
			data, e := os.ReadFile(path)
			if e != nil || string(data) != "partial interrupted file" {
				t.Fatal("existing crash artifact was deleted", e)
			}
		})
	}
}

func TestGeneratedTemporaryNamesAndPerItemIntentPathsArePreciselyBounded(t *testing.T) {
	for _, test := range []struct {
		dir, name string
		valid     bool
	}{
		{"local-progress", ".local-progress-0.tmp", true}, {"market-fallback", ".local-progress-4294967295.tmp", true},
		{"handovers", ".handover-10.tmp", true}, {"stock-edits", ".stock-edit-23.tmp", true}, {"market-fallback-sync", ".fallback-sync-8.tmp", true},
		{"stock-usage", strings.Repeat("a", 64) + ".json.tmp", true}, {"local-progress", ".local-progress-00.tmp", false},
		{"handovers", ".handover-4294967296.tmp", false}, {"handovers", ".stock-edit-1.tmp", false}, {"unknown", ".local-progress-1.tmp", false},
	} {
		_, valid := GeneratedTemporaryLimit(test.dir, test.name)
		if valid != test.valid {
			t.Fatal("temporary name accepted/rejected incorrectly", test)
		}
		if ValidPath(test.dir + "/" + test.name) {
			t.Fatal("temporary artifact can be imported as stock")
		}
	}
	name := strings.Repeat("a", 64) + "-" + strings.Repeat("b", 64) + ".json"
	if !ValidPath("market-fallback-sync/"+name) || ValidPath("local-progress/"+name) || ValidPath("market-fallback-sync/"+strings.ToUpper(name)) {
		t.Fatal("per-item intent filename rules differ")
	}
}
func TestRestoreFailureAtEveryRenameRollsBackAllSixDirectories(t *testing.T) {
	for fail := 1; fail <= 12; fail++ {
		t.Run(string(rune('a'+fail)), func(t *testing.T) {
			root := oldRestoreFixture(t)
			calls := 0
			err := restore(root, newRestoreFiles(), func(from, to string) error {
				calls++
				if calls == fail {
					return errors.New("disk failed")
				}
				return os.Rename(from, to)
			})
			if err == nil {
				t.Fatal("failure accepted")
			}
			assertRestoreFiles(t, root, "old")
			if err := Recover(root); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestRestoreCrashAtEveryRenameRecoversAfterRestart(t *testing.T) {
	for stop := 1; stop <= 12; stop++ {
		t.Run(string(rune('a'+stop)), func(t *testing.T) {
			root := oldRestoreFixture(t)
			calls := 0
			func() {
				defer func() { _ = recover() }()
				_ = restore(root, newRestoreFiles(), func(from, to string) error {
					err := os.Rename(from, to)
					calls++
					if calls == stop {
						panic("crash")
					}
					return err
				})
			}()
			if err := Recover(root); err != nil {
				t.Fatal(err)
			}
			assertRestoreFiles(t, root, "old")
			if err := Recover(root); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestRestoreMissingOldDirectoriesAndInvalidPathsDoNotEscape(t *testing.T) {
	root := t.TempDir()
	if err := Restore(root, newRestoreFiles()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../api-key.bin", "local-progress/../api-key.bin", "local-progress\\" + testFile, "C:/" + testFile, "other/" + testFile, "local-progress/" + strings.ToUpper(testFile)} {
		if ValidPath(path) {
			t.Fatal("unsafe path accepted", path)
		}
		if err := Restore(root, []File{{Path: path, Data: []byte("bad")}}); err == nil {
			t.Fatal("unsafe restore accepted")
		}
		for _, dir := range Directories {
			data, _ := os.ReadFile(filepath.Join(root, dir, testFile))
			if string(data) != "new-"+dir {
				t.Fatal("unsafe path changed data")
			}
		}
	}
}
func preparedJournalFixture(t *testing.T) (string, string, []byte) {
	t.Helper()
	root := oldRestoreFixture(t)
	work := filepath.Join(root, workName)
	_ = os.Mkdir(work, 0700)
	_ = os.Mkdir(filepath.Join(work, "old"), 0700)
	_ = os.Mkdir(filepath.Join(work, "new"), 0700)
	j := restoreJournal{Version: 1, Phase: "prepared", ID: strings.Repeat("a", 24), OldPresent: map[string]bool{}}
	for _, dir := range Directories {
		j.OldPresent[dir] = true
	}
	if writeJournal(work, j) != nil {
		t.Fatal("journal")
	}
	data, _ := os.ReadFile(filepath.Join(work, "journal.json"))
	return root, work, data
}
func TestRestoreCorruptJournalNeverDeletesLiveData(t *testing.T) {
	for _, edit := range []func([]byte) []byte{
		func(data []byte) []byte { return append(data, []byte("{}")...) },
		func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
		},
		func(data []byte) []byte { return bytes.Replace(data, []byte(`"phase"`), []byte(`"Phase"`), 1) },
		func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"local-progress":true`), []byte(`"local-progress":null`), 1)
		},
		func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"local-progress":true`), []byte(`"local-progress":false`), 1)
		},
		func(data []byte) []byte { return bytes.Repeat([]byte(" "), 4097) },
	} {
		root, work, data := preparedJournalFixture(t)
		bad := edit(data)
		_ = os.WriteFile(filepath.Join(work, "journal.json"), bad, 0600)
		if Recover(root) == nil {
			t.Fatal("corrupt journal accepted")
		}
		assertRestoreFiles(t, root, "old")
		after, _ := os.ReadFile(filepath.Join(work, "journal.json"))
		if !bytes.Equal(after, bad) {
			t.Fatal("corrupt journal overwritten")
		}
	}
}
func TestRestorePreparedRollbackIsRepeatableAndCommittedCleanupKeepsNew(t *testing.T) {
	root, work, data := preparedJournalFixture(t)
	var j restoreJournal
	_ = json.Unmarshal(data, &j)
	for _, dir := range Directories {
		_ = os.Rename(filepath.Join(root, dir), filepath.Join(work, "old", dir))
		_ = os.Mkdir(filepath.Join(root, dir), 0700)
		_ = os.WriteFile(filepath.Join(root, dir, testFile), []byte("new-"+dir), 0600)
	}
	j.Phase = "committed"
	if writeJournal(work, j) != nil {
		t.Fatal("marker")
	}
	if Recover(root) != nil {
		t.Fatal("cleanup")
	}
	assertRestoreFiles(t, root, "new")
}
func TestRestoreSymlinkIsRejectedWithoutFollowingIt(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "local-progress")); err != nil {
		t.Skip("symlink unavailable")
	}
	if Restore(root, newRestoreFiles()) == nil {
		t.Fatal("linked directory accepted")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("outside modified")
	}
}

func TestRestoreMissingJournalPreservesMovedOldData(t *testing.T) {
	root, work, _ := preparedJournalFixture(t)
	dir := Directories[0]
	if err := os.Rename(filepath.Join(root, dir), filepath.Join(work, "old", dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(work, "journal.json")); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if Recover(root) == nil {
			t.Fatal("missing journal accepted after live directory was moved")
		}
		data, err := os.ReadFile(filepath.Join(work, "old", dir, testFile))
		if err != nil || string(data) != "old-"+dir {
			t.Fatal("only surviving old data was deleted", err)
		}
		if _, err := os.Lstat(filepath.Join(root, dir)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unverified recovery changed the live directory")
		}
	}
}

func TestRestoreStagingCrashOnlyDiscardsIncompleteNewCopies(t *testing.T) {
	root, work, data := preparedJournalFixture(t)
	var j restoreJournal
	if err := json.Unmarshal(data, &j); err != nil {
		t.Fatal(err)
	}
	j.Phase = "staging"
	if err := writeJournal(work, j); err != nil {
		t.Fatal(err)
	}
	dir := Directories[0]
	if err := os.Mkdir(filepath.Join(work, "new", dir), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "new", dir, testFile), []byte("partial-copy"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Recover(root); err != nil {
		t.Fatal(err)
	}
	assertRestoreFiles(t, root, "old")
	if _, err := os.Lstat(work); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("staging files were not cleaned")
	}
}

func TestRestoreStagingMarkerCannotDeleteSavedOldData(t *testing.T) {
	root, work, data := preparedJournalFixture(t)
	var j restoreJournal
	_ = json.Unmarshal(data, &j)
	j.Phase = "staging"
	if err := writeJournal(work, j); err != nil {
		t.Fatal(err)
	}
	dir := Directories[0]
	if err := os.Rename(filepath.Join(root, dir), filepath.Join(work, "old", dir)); err != nil {
		t.Fatal(err)
	}
	if Recover(root) == nil {
		t.Fatal("staging accepted after old data was moved")
	}
	data, err := os.ReadFile(filepath.Join(work, "old", dir, testFile))
	if err != nil || string(data) != "old-"+dir {
		t.Fatal("old data lost", err)
	}
}

func TestRestoreUnstartedEmptyWorkIsSafeToClean(t *testing.T) {
	for _, temporary := range []bool{false, true} {
		root := oldRestoreFixture(t)
		work := filepath.Join(root, workName)
		if err := os.Mkdir(work, 0700); err != nil {
			t.Fatal(err)
		}
		if temporary {
			if err := os.WriteFile(filepath.Join(work, ".journal-interrupted"), []byte("partial marker"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := Recover(root); err != nil {
			t.Fatal(err)
		}
		assertRestoreFiles(t, root, "old")
		if _, err := os.Lstat(work); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unstarted work was not cleaned")
		}
	}
}
