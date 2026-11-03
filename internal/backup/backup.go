// Package backup keeps dated, compressed copies of the tracker's data:
// one a day on a schedule, plus copies made by hand and before a restore.
package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/apinat33198/pmtracker/internal/notify"
	"github.com/apinat33198/pmtracker/internal/store"
)

// Kinds of backup, in the file name.
const (
	Daily         = "daily"
	Manual        = "manual"
	BeforeRestore = "before-restore"
)

// Names: pmtracker-2026-11-03.json.gz (daily),
// pmtracker-2026-11-03-221530-manual.json.gz and …-before-restore.json.gz.
var nameRe = regexp.MustCompile(`^pmtracker-(\d{4}-\d{2}-\d{2})(?:-(\d{6})-(manual|before-restore))?\.json\.gz$`)

// keepAtLeast newest backups are never pruned, however old.
const keepAtLeast = 3

// maxSize bounds what Read decompresses, so a small "zip bomb" can't use
// up a small container's memory. A data file is a few MB at most.
const maxSize = 64 << 20

type File struct {
	Name string
	Kind string
	At   time.Time // local date (and time for manual and pre-restore copies)
	Size int64
}

type Manager struct {
	Dir      string
	KeepDays int
	Store    *store.Store
	Now      func() time.Time
	Loc      *time.Location
	Log      *log.Logger

	mu      sync.Mutex
	lastErr error
	lastAt  time.Time
}

// Status reports the last scheduled or manual run.
func (m *Manager) Status() (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastAt, m.lastErr
}

// Create writes a backup of the current data and prunes old ones.
func (m *Manager) Create(kind string) (File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.create(kind)
	m.lastAt, m.lastErr = m.Now(), err
	if err == nil {
		if perr := m.prune(); perr != nil {
			m.Log.Printf("backup: pruning old copies: %v", perr)
		}
	}
	return f, err
}

func (m *Manager) create(kind string) (File, error) {
	data, err := m.Store.Export()
	if err != nil {
		return File{}, err
	}
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return File{}, err
	}
	now := m.Now().In(m.Loc)
	name := "pmtracker-" + now.Format("2006-01-02") + ".json.gz"
	if kind != Daily {
		name = "pmtracker-" + now.Format("2006-01-02-150405") + "-" + kind + ".json.gz"
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Name = strings.TrimSuffix(name, ".gz")
	zw.ModTime = now
	if _, err := zw.Write(data); err != nil {
		return File{}, err
	}
	if err := zw.Close(); err != nil {
		return File{}, err
	}
	tmp, err := os.CreateTemp(m.Dir, ".backup-*.tmp") // 0600
	if err != nil {
		return File{}, err
	}
	_, werr := tmp.Write(buf.Bytes())
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(tmp.Name())
		return File{}, err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(m.Dir, name)); err != nil {
		os.Remove(tmp.Name())
		return File{}, err
	}
	return File{Name: name, Kind: kind, At: now, Size: int64(buf.Len())}, nil
}

// List returns the backups, newest first.
func (m *Manager) List() ([]File, error) {
	entries, err := os.ReadDir(m.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []File
	for _, e := range entries {
		f, ok := m.parse(e.Name())
		if !ok || !e.Type().IsRegular() {
			continue
		}
		if info, err := e.Info(); err == nil {
			f.Size = info.Size()
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

func (m *Manager) parse(name string) (File, bool) {
	p := nameRe.FindStringSubmatch(name)
	if p == nil {
		return File{}, false
	}
	f := File{Name: name, Kind: Daily}
	at, err := time.ParseInLocation("2006-01-02", p[1], m.Loc)
	if err != nil {
		return File{}, false
	}
	if p[2] != "" {
		t, err := time.ParseInLocation("2006-01-02 150405", p[1]+" "+p[2], m.Loc)
		if err != nil {
			return File{}, false
		}
		at, f.Kind = t, p[3]
	}
	f.At = at
	return f, true
}

// Open returns a backup file by name. Only names that List would show are
// accepted, so a name can't point outside the backup folder.
func (m *Manager) Open(name string) (*os.File, File, error) {
	f, ok := m.parse(name)
	if !ok {
		return nil, File{}, os.ErrNotExist
	}
	fh, err := os.Open(filepath.Join(m.Dir, name))
	if err != nil {
		return nil, File{}, err
	}
	if info, err := fh.Stat(); err == nil {
		f.Size = info.Size()
	}
	return fh, f, nil
}

// Read returns a backup's data, uncompressed.
func (m *Manager) Read(name string) ([]byte, error) {
	fh, _, err := m.Open(name)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	return Decode(fh)
}

// Decode reads a backup: gzip as written by Create, or plain JSON (a data
// file copied by hand).
func Decode(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", store.ErrNotBackup, err)
		}
		if b, err = io.ReadAll(io.LimitReader(zr, maxSize+1)); err != nil {
			return nil, fmt.Errorf("%w: %v", store.ErrNotBackup, err)
		}
	}
	if len(b) > maxSize {
		return nil, errors.New("the backup is too large")
	}
	return b, nil
}

// prune deletes backups older than KeepDays, always keeping the newest few.
func (m *Manager) prune() error {
	files, err := m.List()
	if err != nil {
		return err
	}
	cutoff := m.Now().In(m.Loc).AddDate(0, 0, -m.KeepDays)
	var errs []error
	for i, f := range files {
		if i < keepAtLeast || !f.At.Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(m.Dir, f.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// due reports whether the daily copy is missing: no backup in the last day.
func (m *Manager) due() bool {
	files, err := m.List()
	if err != nil || len(files) == 0 {
		return true
	}
	return m.Now().Sub(files[0].At) > 24*time.Hour
}

// Run makes a daily backup at hh:mm local time until ctx ends. A copy
// missed while the tracker was off is made at start.
func (m *Manager) Run(ctx context.Context, hhmm string) {
	daily := func(context.Context) {
		f, err := m.Create(Daily)
		if err != nil {
			m.Log.Printf("backup: %v", err)
			return
		}
		m.Log.Printf("backup: saved %s (%d KB)", f.Name, (f.Size+1023)/1024)
	}
	if m.due() {
		daily(ctx)
	}
	if err := notify.Daily(ctx, hhmm, m.Loc, daily); err != nil {
		m.Log.Printf("backup: %v", err)
	}
}

// Prune deletes backups older than KeepDays, keeping the newest few.
func (m *Manager) Prune() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prune()
}
