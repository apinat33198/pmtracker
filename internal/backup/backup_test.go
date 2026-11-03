package backup

import (
	"bytes"
	"compress/gzip"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apinat33198/pmtracker/internal/core"
	"github.com/apinat33198/pmtracker/internal/store"
)

func setup(t *testing.T) (*Manager, *store.Store, *time.Time) {
	t.Helper()
	loc, _ := time.LoadLocation("Asia/Bangkok")
	now := time.Date(2026, 11, 3, 2, 30, 0, 0, loc)
	st, err := store.Open(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.SessionSecret()
	st.UpdateCatalog("admin", "Set up", func(c *core.Catalog) error {
		c.Sites = []core.Site{{ID: "bkk1", Code: "BKK1", Name: "Bang Na"}}
		return nil
	})
	m := &Manager{Dir: filepath.Join(t.TempDir(), "backups"), KeepDays: 30, Store: st,
		Now: func() time.Time { return now }, Loc: loc, Log: log.New(io.Discard, "", 0)}
	return m, st, &now
}

func TestCreateListRead(t *testing.T) {
	m, _, now := setup(t)
	f, err := m.Create(Daily)
	if err != nil || f.Name != "pmtracker-2026-11-03.json.gz" {
		t.Fatalf("daily = %+v, %v", f, err)
	}
	*now = now.Add(20 * time.Hour)
	if f, _ := m.Create(Manual); f.Name != "pmtracker-2026-11-03-223000-manual.json.gz" {
		t.Errorf("manual = %s", f.Name)
	}
	files, _ := m.List()
	if len(files) != 2 || files[0].Kind != Manual || files[1].Kind != Daily {
		t.Fatalf("list = %+v", files)
	}
	info, _ := os.Stat(filepath.Join(m.Dir, files[0].Name))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode = %v, want 0600", info.Mode().Perm())
	}
	if dir, _ := os.Stat(m.Dir); dir.Mode().Perm() != 0o700 {
		t.Errorf("folder mode = %v, want 0700", dir.Mode().Perm())
	}
	b, err := m.Read(files[1].Name)
	if err != nil || !strings.Contains(string(b), `"BKK1"`) || strings.Contains(string(b), "session_secret") {
		t.Errorf("read = %.80s, %v", b, err)
	}
	// Plain JSON is accepted too (a data file copied by hand).
	if plain, err := Decode(strings.NewReader(`{"version": 2}`)); err != nil || string(plain) != `{"version": 2}` {
		t.Errorf("plain = %s, %v", plain, err)
	}
}

func TestNamesCantEscapeTheFolder(t *testing.T) {
	m, _, _ := setup(t)
	m.Create(Daily)
	os.WriteFile(filepath.Join(filepath.Dir(m.Dir), "secret.json.gz"), []byte("x"), 0o600)
	for _, name := range []string{"../secret.json.gz", "/etc/passwd", "pmtracker-2026-11-03.json.gz/../../secret.json.gz",
		"pmtracker-2026-11-03.json", ".backup-1.tmp", "pmtracker-2026-13-45.json.gz"} {
		if _, _, err := m.Open(name); err == nil {
			t.Errorf("Open(%q) should be refused", name)
		}
	}
	if fh, _, err := m.Open("pmtracker-2026-11-03.json.gz"); err != nil {
		t.Error(err)
	} else {
		fh.Close()
	}
}

func TestPruneKeepsThirtyDays(t *testing.T) {
	m, _, now := setup(t)
	start := *now
	for d := 0; d < 45; d++ {
		*now = start.AddDate(0, 0, d)
		if _, err := m.Create(Daily); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := m.List()
	if len(files) != 30 || files[len(files)-1].Name != "pmtracker-2026-11-18.json.gz" {
		t.Errorf("kept %d, oldest %s", len(files), files[len(files)-1].Name)
	}
	// Even very old copies stay when they're the newest few.
	m2, _, now2 := setup(t)
	m2.Create(Daily)
	*now2 = now2.AddDate(1, 0, 0)
	m2.Prune()
	if files, _ := m2.List(); len(files) != 1 {
		t.Error("the newest backups must never be pruned")
	}
}

func TestCatchUp(t *testing.T) {
	m, _, now := setup(t)
	if !m.due() {
		t.Error("no backups yet: one is due")
	}
	m.Create(Daily)
	*now = now.Add(20 * time.Hour)
	if m.due() {
		t.Error("a backup from earlier today is enough")
	}
	*now = now.Add(30 * time.Hour)
	if !m.due() {
		t.Error("over a day without a backup: one is due")
	}
}

func TestDecodeRefusesZipBombs(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(make([]byte, maxSize+1024))
	zw.Close()
	if buf.Len() > 1<<20 {
		t.Fatalf("test file is %d bytes", buf.Len())
	}
	if _, err := Decode(&buf); err == nil {
		t.Error("a small file that expands past the limit should be refused")
	}
}
