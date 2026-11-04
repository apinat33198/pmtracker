// Package store persists the catalog, mirrored calendar events and people's
// decisions in one JSON file, written atomically on every change.
//
// Volume is small (21 sites, a few hundred visits a year), so this is
// plenty; the methods map onto SQL tables if it ever needs to grow.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/apinat33198/pmtracker/internal/auth"
	"github.com/apinat33198/pmtracker/internal/core"
)

// CalendarState is the sync bookkeeping for one calendar.
type CalendarState struct {
	SyncToken      string    `json:"sync_token"`
	LastSyncedAt   time.Time `json:"last_synced_at"`
	LastFullSyncAt time.Time `json:"last_full_sync_at"`
	LastError      string    `json:"last_error"`
	LastErrorAt    time.Time `json:"last_error_at"`
	LastChanges    int       `json:"last_changes"`
}

// AuditEntry records an action: who, when, from where (for sign-ins), what.
type AuditEntry struct {
	At     time.Time `json:"at"`
	By     string    `json:"by"`
	From   string    `json:"from,omitempty"`
	Action string    `json:"action"`
}

type state struct {
	Version   int                       `json:"version"`
	Catalog   core.Catalog              `json:"catalog"`
	Calendars map[string]*CalendarState `json:"sync"`
	Events    map[string]core.Event     `json:"events"`
	Decisions map[string]core.Decision  `json:"decisions"`
	Rules     []core.LearnedRule        `json:"rules"`
	Marks     map[string]core.VisitMark `json:"marks"`
	Manual    []core.ManualVisit        `json:"manual"`
	Changes   []core.Change             `json:"changes"`
	// Jobs are one-off ad-hoc jobs at sites, apart from the PM tasks.
	Jobs []core.Job `json:"jobs,omitempty"`
	// Audit is where the activity log was kept before it moved to its own
	// files; read once to move old entries over, then always empty.
	Audit  []AuditEntry `json:"audit,omitempty"`
	NextID int          `json:"next_id"`
	Users  []auth.User  `json:"users"`
	// SessionSecret signs session cookies; generated on first use.
	SessionSecret []byte `json:"session_secret,omitempty"`
	// Demo marks a file created by -demo, which -demo may replace.
	Demo bool `json:"demo,omitempty"`
}

type Store struct {
	mu       sync.RWMutex
	path     string
	st       state
	now      func() time.Time
	activity *ActivityLog
	pending  []AuditEntry // activity of the change being saved
	// Logf reports problems writing the activity log (default: log.Printf).
	Logf func(format string, args ...any)
}

// currentVersion is the data file's format version.
const currentVersion = 2

// Open loads path, or starts empty if it does not exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now}
	s.st = state{Version: currentVersion}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &s.st); err != nil {
			return nil, fmt.Errorf("store %s: %w", path, err)
		}
	}
	s.st.normalize()
	s.Logf = log.Printf
	act, err := openActivity(ActivityDir(path), func() time.Time { return s.now() })
	if err != nil {
		return nil, err
	}
	s.activity = act
	// Move entries from data files written before the log had its own files.
	if len(s.st.Audit) > 0 {
		if err := act.Append(s.st.Audit...); err != nil {
			return nil, err
		}
		s.st.Audit = nil
		if err := s.write(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Activity is the activity log.
func (s *Store) Activity() *ActivityLog { return s.activity }

func (st *state) normalize() {
	if st.Calendars == nil {
		st.Calendars = map[string]*CalendarState{}
	}
	if st.Events == nil {
		st.Events = map[string]core.Event{}
	}
	if st.Decisions == nil {
		st.Decisions = map[string]core.Decision{}
	}
	if st.Marks == nil {
		st.Marks = map[string]core.VisitMark{}
	}
	// The next number must be past every one in use, or a new visit, rule
	// or job would share a number with an old one (a hand-edited file or
	// backup), and deleting one would delete the other.
	high := 0
	for _, r := range st.Rules {
		high = max(high, r.ID)
	}
	for _, m := range st.Manual {
		high = max(high, m.ID)
	}
	for _, j := range st.Jobs {
		high = max(high, j.ID)
	}
	if st.NextID <= high {
		st.NextID = high + 1
	}
}

// checkJobs reports the first ad-hoc job in a backup that doesn't check out.
func checkJobs(jobs []core.Job) error {
	seen := map[int]bool{}
	for i := range jobs {
		j := &jobs[i]
		if j.ID < 1 || seen[j.ID] {
			return fmt.Errorf("two ad-hoc jobs share the number %d", j.ID)
		}
		seen[j.ID] = true
		if err := j.Clean(); err != nil {
			return fmt.Errorf("ad-hoc job #%d: %v", j.ID, err)
		}
	}
	return nil
}

// Export returns everything as JSON for a backup, without the key that
// signs sign-in cookies: a leaked backup can't be used to forge sessions.
func (s *Store) Export() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := s.st
	st.SessionSecret = nil
	return json.MarshalIndent(st, "", " ")
}

// ErrNotBackup means the file isn't a PM tracker backup.
var ErrNotBackup = errors.New("this isn't a PM tracker backup")

// Restore replaces everything with a backup made by Export, after checking
// it. Everyone has to sign in again afterwards.
func (s *Store) Restore(b []byte, by, action string) error {
	var st state
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		return fmt.Errorf("%w (%v)", ErrNotBackup, err)
	}
	if st.Version == 0 {
		return ErrNotBackup
	}
	if st.Version > currentVersion {
		return fmt.Errorf("this backup is from a newer version of the tracker (data version %d); update the tracker first", st.Version)
	}
	if err := st.Catalog.Validate(); err != nil {
		return fmt.Errorf("the backup's sites and tasks don't check out: %v", err)
	}
	if err := checkJobs(st.Jobs); err != nil {
		return fmt.Errorf("the backup's ad-hoc jobs don't check out: %v", err)
	}
	st.normalize()
	s.mu.Lock()
	defer s.mu.Unlock()
	// Never take a signing key from a file: whoever made the file would
	// know it and could forge sign-ins.
	st.SessionSecret = s.st.SessionSecret
	// End every session. Otherwise an account deleted or reset after the
	// backup was made would come back with its old sign-ins still valid.
	epochs := map[string]int{}
	for _, u := range s.st.Users {
		epochs[u.Username] = u.Epoch
	}
	for i := range st.Users {
		if e := epochs[st.Users[i].Username]; e > st.Users[i].Epoch {
			st.Users[i].Epoch = e
		}
		st.Users[i].Epoch++
	}
	st.Demo = s.st.Demo
	st.Audit = nil // the activity log isn't part of the data, and a restore can't rewrite it
	prev := s.st
	s.st = st
	s.audit(by, action)
	if err := s.save(); err != nil {
		s.st = prev
		return err
	}
	return nil
}

// SetClock overrides the clock used for audit timestamps.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// save writes the data file, then the activity of the change. If the data
// can't be saved, the change didn't happen and its activity is dropped.
func (s *Store) save() error {
	pending := s.pending
	s.pending = nil
	if err := s.write(); err != nil {
		return err
	}
	if err := s.activity.Append(pending...); err != nil {
		s.Logf("activity log: %v", err)
	}
	return nil
}

func (s *Store) write() error {
	b, err := json.MarshalIndent(s.st, "", " ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".pmtracker-*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	// Make the rename itself survive a power cut.
	if d, err := os.Open(filepath.Dir(s.path)); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

func (s *Store) audit(by, action string) {
	if by == "" {
		by = "system"
	}
	s.pending = append(s.pending, AuditEntry{At: s.now(), By: by, Action: action})
}

// Audit returns recent entries, newest first.
func (s *Store) Audit(limit int) []AuditEntry {
	out, err := s.activity.Recent(limit)
	if err != nil {
		s.Logf("activity log: %v", err)
	}
	return out
}

// Catalog returns a copy of the catalog.
func (s *Store) Catalog() core.Catalog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.st.Catalog.Clone()
}

// UpdateCatalog applies fn to a copy, validates it and saves it with an
// audit entry (none when action is empty). References to deleted tasks and
// calendars are cleaned up.
func (s *Store) UpdateCatalog(by, action string, fn func(*core.Catalog) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.st.Catalog.Clone()
	if err := fn(&c); err != nil {
		return err
	}
	c.Tidy()
	if err := c.Validate(); err != nil {
		return err
	}
	s.st.Catalog = c
	s.cleanup()
	if action != "" {
		s.audit(by, action)
	}
	return s.save()
}

// cleanup drops references to tasks and calendars that no longer exist.
func (s *Store) cleanup() {
	live := map[string]bool{}
	for _, t := range s.st.Catalog.Tasks {
		live[t.ID] = true
	}
	keep := func(ids []string) []string {
		var out []string
		for _, id := range ids {
			if live[id] {
				out = append(out, id)
			}
		}
		return out
	}
	for k, d := range s.st.Decisions {
		if d.Action == "link" {
			d.Tasks = keep(d.Tasks)
			if len(d.Tasks) == 0 {
				delete(s.st.Decisions, k)
				continue
			}
			s.st.Decisions[k] = d
		}
	}
	var rules []core.LearnedRule
	for _, r := range s.st.Rules {
		if r.Tasks = keep(r.Tasks); len(r.Tasks) > 0 {
			rules = append(rules, r)
		}
	}
	s.st.Rules = rules
	var manual []core.ManualVisit
	for _, m := range s.st.Manual {
		if m.Tasks = keep(m.Tasks); len(m.Tasks) > 0 {
			manual = append(manual, m)
		} else {
			delete(s.st.Marks, fmt.Sprintf("man:%d", m.ID))
		}
	}
	s.st.Manual = manual

	// A deleted site's ad-hoc jobs go with it, as its PM tasks do.
	var jobs []core.Job
	for _, j := range s.st.Jobs {
		if _, ok := s.st.Catalog.Site(j.SiteID); ok {
			jobs = append(jobs, j)
		}
	}
	s.st.Jobs = jobs

	cals := map[string]bool{}
	for _, c := range s.st.Catalog.Calendars {
		cals[c.ID] = true
	}
	for id := range s.st.Calendars {
		if !cals[id] {
			delete(s.st.Calendars, id)
		}
	}
	for k, e := range s.st.Events {
		if !cals[e.CalendarID] {
			delete(s.st.Events, k)
		}
	}
}

// Inputs returns a copy of everything core.Build needs.
func (s *Store) Inputs() core.Inputs {
	s.mu.RLock()
	defer s.mu.RUnlock()
	in := core.Inputs{
		Catalog:   s.st.Catalog.Clone(),
		Decisions: make(map[string]core.Decision, len(s.st.Decisions)),
		Marks:     make(map[string]core.VisitMark, len(s.st.Marks)),
		Rules:     append([]core.LearnedRule(nil), s.st.Rules...),
		Manual:    append([]core.ManualVisit(nil), s.st.Manual...),
		Changes:   append([]core.Change(nil), s.st.Changes...),
		Jobs:      append([]core.Job(nil), s.st.Jobs...),
	}
	for _, e := range s.st.Events {
		in.Events = append(in.Events, e)
	}
	for k, v := range s.st.Decisions {
		in.Decisions[k] = v
	}
	for k, v := range s.st.Marks {
		in.Marks[k] = v
	}
	return in
}

func (s *Store) Event(key string) (core.Event, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.st.Events[key]
	return e, ok
}

func (s *Store) CalendarState(id string) CalendarState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if c, ok := s.st.Calendars[id]; ok {
		return *c
	}
	return CalendarState{}
}

func (s *Store) calendar(id string) *CalendarState {
	c, ok := s.st.Calendars[id]
	if !ok {
		c = &CalendarState{}
		s.st.Calendars[id] = c
	}
	return c
}

// ApplySync stores one sync result. On a full sync, events missing from the
// result are dropped. Deletions arrive as status "cancelled", often with no
// other fields, so the previous copy is kept and marked cancelled. Moves of
// the start date are logged as changes.
func (s *Store) ApplySync(calendarID string, full bool, events []core.Event, nextToken string, at time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.st.Catalog.Calendar(calendarID); !ok {
		return 0, nil // removed by an admin while the sync was running
	}
	changed := 0
	seen := map[string]bool{}
	for _, e := range events {
		e.CalendarID = calendarID
		key := e.Key()
		seen[key] = true
		prev, existed := s.st.Events[key]
		if e.Cancelled() && existed {
			if !prev.Cancelled() {
				s.st.Changes = append(s.st.Changes, core.Change{EventKey: key, Field: "status", Old: prev.Status, New: "cancelled", At: at})
				changed++
			}
			prev.Status = "cancelled"
			if !e.Updated.IsZero() {
				prev.Updated = e.Updated
			}
			s.st.Events[key] = prev
			continue
		}
		if e.Cancelled() && e.Start.IsZero() {
			continue
		}
		if existed && !prev.Start.IsZero() && !e.Start.Equal(prev.Start) {
			s.st.Changes = append(s.st.Changes, core.Change{EventKey: key, Field: "start", Old: fmtStart(prev), New: fmtStart(e), At: at})
		}
		if !existed || !sameEvent(prev, e) {
			changed++
		}
		s.st.Events[key] = e
	}
	if full {
		for k, e := range s.st.Events {
			if e.CalendarID == calendarID && !seen[k] {
				delete(s.st.Events, k)
				changed++
			}
		}
	}
	c := s.calendar(calendarID)
	c.SyncToken, c.LastSyncedAt, c.LastError, c.LastChanges = nextToken, at, "", changed
	if full {
		c.LastFullSyncAt = at
	}
	return changed, s.save()
}

func fmtStart(e core.Event) string {
	if e.AllDay {
		return e.Start.Format("2006-01-02")
	}
	return e.Start.Format("2006-01-02 15:04")
}

func sameEvent(a, b core.Event) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

func (s *Store) SetSyncError(calendarID string, err error, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.st.Catalog.Calendar(calendarID); !ok {
		return nil
	}
	c := s.calendar(calendarID)
	c.LastError, c.LastErrorAt = err.Error(), at
	return s.save()
}

func (s *Store) Decide(d core.Decision, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Decisions[d.EventKey] = d
	s.audit(d.By, action)
	return s.save()
}

func (s *Store) Undecide(eventKey, by, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.st.Decisions, eventKey)
	s.audit(by, action)
	return s.save()
}

// AddRule stores a learned rule, replacing one with the same kind and value.
func (s *Store) AddRule(r core.LearnedRule) (core.LearnedRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.st.Rules {
		if x.Kind == r.Kind && x.Value == r.Value {
			r.ID = x.ID
			s.st.Rules[i] = r
			return r, s.save()
		}
	}
	r.ID = s.st.NextID
	s.st.NextID++
	s.st.Rules = append(s.st.Rules, r)
	return r, s.save()
}

func (s *Store) DeleteRule(id int, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.LearnedRule
	for _, r := range s.st.Rules {
		if r.ID != id {
			out = append(out, r)
		}
	}
	s.st.Rules = out
	s.audit(by, fmt.Sprintf("Deleted learned rule #%d", id))
	return s.save()
}

func (s *Store) Mark(m core.VisitMark, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Marks[m.VisitID] = m
	if action != "" {
		s.audit(m.By, action)
	}
	return s.save()
}

func (s *Store) Unmark(visitID, by, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.st.Marks, visitID)
	s.audit(by, action)
	return s.save()
}

// AddManual records past visits typed in by hand.
func (s *Store) AddManual(action string, visits ...core.ManualVisit) error {
	if len(visits) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	by := ""
	for _, v := range visits {
		v.ID = s.st.NextID
		s.st.NextID++
		s.st.Manual = append(s.st.Manual, v)
		by = v.By
	}
	sort.SliceStable(s.st.Manual, func(i, j int) bool { return s.st.Manual[i].Date < s.st.Manual[j].Date })
	if action != "" {
		s.audit(by, action)
	}
	return s.save()
}

func (s *Store) DeleteManual(id int, by, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.ManualVisit
	for _, v := range s.st.Manual {
		if v.ID != id {
			out = append(out, v)
		}
	}
	s.st.Manual = out
	delete(s.st.Marks, fmt.Sprintf("man:%d", id))
	s.audit(by, action)
	return s.save()
}

// MarkAll records several visit marks with one save (bulk confirmation, demo seeding).
func (s *Store) MarkAll(marks []core.VisitMark, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range marks {
		s.st.Marks[m.VisitID] = m
	}
	if action != "" && len(marks) > 0 {
		s.audit(marks[0].By, action)
	}
	return s.save()
}

// ErrNoJob means the ad-hoc job doesn't exist (any more).
var ErrNoJob = errors.New("that job no longer exists")

// AddJob checks and saves a new ad-hoc job, and returns it with its ID.
func (s *Store) AddJob(j core.Job, action string) (core.Job, error) {
	if err := j.Clean(); err != nil {
		return core.Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.st.Catalog.Site(j.SiteID); !ok {
		return core.Job{}, errors.New("that site no longer exists")
	}
	j.ID = s.st.NextID
	s.st.NextID++
	s.st.Jobs = append(s.st.Jobs, j)
	if action != "" {
		s.audit(j.By, action)
	}
	if err := s.save(); err != nil {
		s.st.Jobs = s.st.Jobs[:len(s.st.Jobs)-1]
		return core.Job{}, err
	}
	return j, nil
}

// UpdateJob applies fn to a copy of a job, checks it and saves it. The job
// stays at its site. The action is formatted with the job as it is after fn.
func (s *Store) UpdateJob(id int, by string, action func(core.Job) string, fn func(*core.Job) error) (core.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, old := range s.st.Jobs {
		if old.ID != id {
			continue
		}
		j := old
		if err := fn(&j); err != nil {
			return old, err
		}
		j.ID, j.SiteID, j.By, j.Created = old.ID, old.SiteID, old.By, old.Created
		if err := j.Clean(); err != nil {
			return old, err
		}
		s.st.Jobs[i] = j
		s.audit(by, action(j))
		if err := s.save(); err != nil {
			s.st.Jobs[i] = old
			return old, err
		}
		return j, nil
	}
	return core.Job{}, ErrNoJob
}

// DeleteJob removes a job and returns what it was.
func (s *Store) DeleteJob(id int, by string, action func(core.Job) string) (core.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, j := range s.st.Jobs {
		if j.ID != id {
			continue
		}
		prev := s.st.Jobs
		s.st.Jobs = append(append([]core.Job(nil), prev[:i]...), prev[i+1:]...)
		s.audit(by, action(j))
		if err := s.save(); err != nil {
			s.st.Jobs = prev
			return j, err
		}
		return j, nil
	}
	return core.Job{}, ErrNoJob
}

// Users returns the accounts stored in the data file.
func (s *Store) Users() []auth.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]auth.User(nil), s.st.Users...)
}

func (s *Store) User(username string) (auth.User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.st.Users {
		if u.Username == username {
			return u, true
		}
	}
	return auth.User{}, false
}

// SaveUser adds or replaces an account.
func (s *Store) SaveUser(u auth.User, by, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	replaced := false
	for i := range s.st.Users {
		if s.st.Users[i].Username == u.Username {
			s.st.Users[i], replaced = u, true
		}
	}
	if !replaced {
		s.st.Users = append(s.st.Users, u)
	}
	if action != "" {
		s.audit(by, action)
	}
	return s.save()
}

func (s *Store) DeleteUser(username, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []auth.User
	for _, u := range s.st.Users {
		if u.Username != username {
			out = append(out, u)
		}
	}
	s.st.Users = out
	s.audit(by, "Deleted the account "+username)
	return s.save()
}

// RecordLogin notes a successful sign-in.
func (s *Store) RecordLogin(username string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Users {
		if s.st.Users[i].Username == username {
			s.st.Users[i].LastLogin = at
			s.save()
		}
	}
}

// Log records an action that doesn't change the data (sign-ins,
// downloads, lockouts).
func (s *Store) Log(by, action string) error {
	return s.LogFrom(by, "", action)
}

// LogFrom records an action with the address it came from.
func (s *Store) LogFrom(by, from, action string) error {
	if by == "" {
		by = "system"
	}
	return s.activity.Append(AuditEntry{At: s.now(), By: by, From: from, Action: action})
}

// SessionSecret returns the key that signs session cookies, creating it once.
func (s *Store) SessionSecret() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.st.SessionSecret) < 32 {
		s.st.SessionSecret = auth.NewSecret()
		if err := s.save(); err != nil {
			return nil, err
		}
	}
	return append([]byte(nil), s.st.SessionSecret...), nil
}

// MarkDemo flags the file as demo data that -demo may replace.
func (s *Store) MarkDemo() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Demo = true
	return s.save()
}

// IsDemo reports whether the file was created by -demo.
func (s *Store) IsDemo() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.st.Demo
}
