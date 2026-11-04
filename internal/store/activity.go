package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ActivityLog is the record of who did what. It is kept apart from the data
// file, one append-only file per day (UTC), so entries are only ever added,
// a restore can't rewrite them, and old days are deleted by age.
type ActivityLog struct {
	dir      string
	keepDays int
	now      func() time.Time
	// OnAppend, if set, gets each entry after it's written (a second copy
	// in the server log, outside the tracker's own files).
	OnAppend func(AuditEntry)

	mu       sync.Mutex
	prunedOn string
}

// DefaultActivityDays is how long entries are kept unless configured.
const DefaultActivityDays = 90

var activityFileRe = regexp.MustCompile(`^activity-(\d{4}-\d{2}-\d{2})\.jsonl$`)

// ActivityDir is where the log of the data file at dataPath is kept:
// /var/lib/pmtracker/data.json → /var/lib/pmtracker/data-activity.
func ActivityDir(dataPath string) string {
	return strings.TrimSuffix(dataPath, filepath.Ext(dataPath)) + "-activity"
}

func openActivity(dir string, now func() time.Time) (*ActivityLog, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("activity log: %w", err)
	}
	return &ActivityLog{dir: dir, keepDays: DefaultActivityDays, now: now}, nil
}

// Dir is the folder holding the log files.
func (a *ActivityLog) Dir() string { return a.dir }

// KeepDays is the retention period.
func (a *ActivityLog) KeepDays() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.keepDays
}

// Append writes entries, each to the file of its day, and syncs them.
func (a *ActivityLog) Append(entries ...AuditEntry) error {
	if len(entries) == 0 {
		return nil
	}
	entries = append([]AuditEntry(nil), entries...)
	for i := range entries {
		entries[i].By, entries[i].From, entries[i].Action = flat(entries[i].By), flat(entries[i].From), flat(entries[i].Action)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	byDay := map[string][]AuditEntry{}
	var days []string
	for _, e := range entries {
		d := e.At.UTC().Format("2006-01-02")
		if _, ok := byDay[d]; !ok {
			days = append(days, d)
		}
		byDay[d] = append(byDay[d], e)
	}
	var errs []error
	for _, d := range days {
		errs = append(errs, a.appendDay(d, byDay[d]))
	}
	if today := a.now().UTC().Format("2006-01-02"); a.prunedOn != today {
		if err := a.prune(); err != nil {
			errs = append(errs, err)
		}
		a.prunedOn = today
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if a.OnAppend != nil {
		for _, e := range entries {
			a.OnAppend(e)
		}
	}
	return nil
}

func (a *ActivityLog) appendDay(day string, entries []AuditEntry) error {
	f, err := os.OpenFile(filepath.Join(a.dir, "activity-"+day+".jsonl"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("activity log: %w", err)
	}
	var b strings.Builder
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			f.Close()
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	_, werr := f.WriteString(b.String())
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return fmt.Errorf("activity log: %w", err)
	}
	return nil
}

// flat makes text safe for a one-line log entry: line breaks, other control
// characters and text-direction overrides become spaces. Without this, a
// name with a line break in it would forge extra lines in the server log copy.
func flat(s string) string {
	if strings.IndexFunc(s, unsafeInLog) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unsafeInLog(r) {
			return ' '
		}
		return r
	}, s)
}

func unsafeInLog(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' ||
		(r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069')
}

// SetKeepDays changes the retention period and deletes what's now too old.
func (a *ActivityLog) SetKeepDays(days int) error {
	if days < 1 {
		days = DefaultActivityDays
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keepDays = days
	return a.prune()
}

// prune deletes the files of days older than the retention period. A day
// is kept until it is fully keepDays old.
func (a *ActivityLog) prune() error {
	cutoff := a.now().UTC().AddDate(0, 0, -a.keepDays).Format("2006-01-02")
	days, err := a.days()
	if err != nil {
		return err
	}
	var errs []error
	for _, d := range days {
		if d < cutoff {
			if err := os.Remove(filepath.Join(a.dir, "activity-"+d+".jsonl")); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// days lists the days with a log file, oldest first.
func (a *ActivityLog) days() ([]string, error) {
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if m := activityFileRe.FindStringSubmatch(e.Name()); m != nil && e.Type().IsRegular() {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out, nil
}

func (a *ActivityLog) readDay(day string) ([]AuditEntry, error) {
	f, err := os.Open(filepath.Join(a.dir, "activity-"+day+".jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []AuditEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e AuditEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// Recent returns up to limit entries, newest first.
func (a *ActivityLog) Recent(limit int) ([]AuditEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	days, err := a.days()
	if err != nil {
		return nil, err
	}
	var out []AuditEntry
	for i := len(days) - 1; i >= 0 && len(out) < limit; i-- {
		entries, err := a.readDay(days[i])
		if err != nil {
			return out, err
		}
		for j := len(entries) - 1; j >= 0 && len(out) < limit; j-- {
			out = append(out, entries[j])
		}
	}
	return out, nil
}

// Each calls fn for every entry kept, oldest first.
func (a *ActivityLog) Each(fn func(AuditEntry) error) error {
	a.mu.Lock()
	days, err := a.days()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	for _, d := range days {
		a.mu.Lock()
		entries, err := a.readDay(d)
		a.mu.Unlock()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, e := range entries {
			if err := fn(e); err != nil {
				return err
			}
		}
	}
	return nil
}

// Oldest is the time of the oldest entry kept (zero if none).
func (a *ActivityLog) Oldest() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	days, err := a.days()
	if err != nil {
		return time.Time{}
	}
	for _, d := range days {
		if entries, err := a.readDay(d); err == nil && len(entries) > 0 {
			return entries[0].At
		}
	}
	return time.Time{}
}
