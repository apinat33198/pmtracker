// Package store persists the catalog, mirrored calendar events and people's
// decisions in one JSON file, written atomically on every change.
//
// Volume is small (21 sites, a few hundred visits a year), so this is
// plenty; the methods map onto SQL tables if it ever needs to grow.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
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

// AuditEntry records an admin or editor action.
type AuditEntry struct {
	At     time.Time `json:"at"`
	By     string    `json:"by"`
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
	Audit     []AuditEntry              `json:"audit"`
	NextID    int                       `json:"next_id"`
	Users     []auth.User               `json:"users"`
	// SessionSecret signs session cookies; generated on first use.
	SessionSecret []byte `json:"session_secret,omitempty"`
	// Demo marks a file created by -demo, which -demo may replace.
	Demo bool `json:"demo,omitempty"`
}

type Store struct {
	mu   sync.RWMutex
	path string
	st   state
	now  func() time.Time
}

const maxAudit = 2000

// Open loads path, or starts empty if it does not exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now}
	s.st = state{Version: 2}
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
	if s.st.Calendars == nil {
		s.st.Calendars = map[string]*CalendarState{}
	}
	if s.st.Events == nil {
		s.st.Events = map[string]core.Event{}
	}
	if s.st.Decisions == nil {
		s.st.Decisions = map[string]core.Decision{}
	}
	if s.st.Marks == nil {
		s.st.Marks = map[string]core.VisitMark{}
	}
	if s.st.NextID == 0 {
		s.st.NextID = 1
	}
	return s, nil
}

// SetClock overrides the clock used for audit timestamps.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func (s *Store) save() error {
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
	s.st.Audit = append(s.st.Audit, AuditEntry{At: s.now(), By: by, Action: action})
	if n := len(s.st.Audit); n > maxAudit {
		s.st.Audit = append([]AuditEntry(nil), s.st.Audit[n-maxAudit:]...)
	}
}

// Audit returns recent entries, newest first.
func (s *Store) Audit(limit int) []AuditEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []AuditEntry
	for i := len(s.st.Audit) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.st.Audit[i])
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

// Audit records an action that isn't otherwise stored (failed sign-in lockouts).
func (s *Store) Log(by, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit(by, action)
	return s.save()
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
