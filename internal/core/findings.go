package core

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Finding is something found at a site, on a PM visit or a walk round the
// room: a fault or worn part, a housekeeping (5S) or safety problem, or an
// idea for doing it better. Findings are the Check and Act half of PDCA:
// each is followed until it's closed with what was done, and the record
// shows what keeps coming back and what was carried to other sites.
// Findings never change a PM task's status.
type Finding struct {
	ID       int    `json:"id"`
	SiteID   string `json:"site_id"`
	TaskID   string `json:"task_id,omitempty"` // the PM task (system and vendor) it came from, if any
	FoundOn  string `json:"found_on"`          // YYYY-MM-DD
	Kind     string `json:"kind"`              // equipment | 5s | safety | idea
	Severity string `json:"severity"`          // high | medium | low
	Title    string `json:"title"`
	Reading  string `json:"reading,omitempty"` // a measured value, such as "6.2 mΩ, limit 5.5"
	Detail   string `json:"detail,omitempty"`
	Owner    string `json:"owner,omitempty"` // person or vendor fixing it
	Due      string `json:"due,omitempty"`   // YYYY-MM-DD: target date to close it
	Status   string `json:"status"`          // open | doing | closed
	Cause    string `json:"cause,omitempty"` // root cause, from asking why
	Action   string `json:"action,omitempty"`
	ClosedOn string `json:"closed_on,omitempty"` // YYYY-MM-DD
	ClosedBy string `json:"closed_by,omitempty"`
	// RepeatOf is an earlier finding at the same site that came back.
	RepeatOf int `json:"repeat_of,omitempty"`
	// Spread is whether the other sites should be checked for the same
	// thing (yokoten): "" not needed, todo, or done.
	Spread  string    `json:"spread,omitempty"`
	By      string    `json:"by,omitempty"`
	Created time.Time `json:"created"`
}

// Finding statuses.
const (
	FindingOpen   = "open"
	FindingDoing  = "doing"
	FindingClosed = "closed"
)

// Choice is one option of a finding field, with its label.
type Choice struct {
	Key, Label, Hint string
}

// The choices for a finding's fields, in form order.
var (
	FindingKinds = []Choice{
		{"equipment", "Equipment fault or wear", "A part failing, worn or out of limits"},
		{"5s", "Housekeeping (5S)", "Clutter, labels, cabling, blocked airflow"},
		{"safety", "Safety or fire risk", "Combustibles, blocked exits, expired tags"},
		{"idea", "Improvement idea", "A better way to do the PM or run the room"},
	}
	FindingSeverities = []Choice{
		{"high", "High", "could cause an outage or injury"},
		{"medium", "Medium", "fix before the next PM"},
		{"low", "Low", "fix when convenient"},
	}
	FindingStatuses = []Choice{
		{FindingOpen, "Open", ""},
		{FindingDoing, "In progress", ""},
		{FindingClosed, "Closed", ""},
	}
	FindingSpreads = []Choice{
		{"", "Not needed", "Only this site is affected"},
		{"todo", "Check the other sites", "The same thing could be at other sites"},
		{"done", "Other sites checked", "Checked or fixed at the other sites too"},
	}
)

func choiceLabel(list []Choice, key string) string {
	for _, c := range list {
		if c.Key == key {
			return c.Label
		}
	}
	return key
}

func validChoice(list []Choice, key string) bool {
	for _, c := range list {
		if c.Key == key {
			return true
		}
	}
	return false
}

// Limits on a finding's text, so one form can't bloat the data file.
const (
	MaxFindingTitle   = 200
	MaxFindingReading = 200
	MaxFindingOwner   = 100
	MaxFindingText    = 2000
)

// textBlock tidies text that may run over several lines: Windows line
// breaks become plain ones and other control characters are dropped.
func textBlock(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069') {
			return -1
		}
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, strings.ReplaceAll(s, "\r\n", "\n")))
}

// Clean tidies a finding's text and checks it on its own. Links to the
// site, the PM task and an earlier finding are checked by CheckLinks.
func (f *Finding) Clean() error {
	f.Title, f.Reading, f.Owner = oneLine(f.Title), oneLine(f.Reading), oneLine(f.Owner)
	f.Detail, f.Cause, f.Action = textBlock(f.Detail), textBlock(f.Cause), textBlock(f.Action)
	f.ClosedBy = oneLine(f.ClosedBy)
	switch {
	case f.SiteID == "":
		return errors.New("pick the site")
	case !visible(f.Title):
		return errors.New("enter what was found")
	case utf8.RuneCountInString(f.Title) > MaxFindingTitle:
		return fmt.Errorf("keep what was found under %d characters", MaxFindingTitle)
	case utf8.RuneCountInString(f.Reading) > MaxFindingReading:
		return fmt.Errorf("keep the reading under %d characters", MaxFindingReading)
	case utf8.RuneCountInString(f.Owner) > MaxFindingOwner:
		return fmt.Errorf("keep who fixes it under %d characters", MaxFindingOwner)
	case utf8.RuneCountInString(f.Detail) > MaxFindingText:
		return fmt.Errorf("keep the details under %d characters", MaxFindingText)
	case utf8.RuneCountInString(f.Cause) > MaxFindingText:
		return fmt.Errorf("keep the root cause under %d characters", MaxFindingText)
	case utf8.RuneCountInString(f.Action) > MaxFindingText:
		return fmt.Errorf("keep what was done under %d characters", MaxFindingText)
	case !validChoice(FindingKinds, f.Kind):
		return errors.New("pick what kind of finding it is")
	case !validChoice(FindingSeverities, f.Severity):
		return errors.New("pick how serious it is")
	case !validChoice(FindingSpreads, f.Spread):
		return fmt.Errorf("unknown choice %q for the other sites", f.Spread)
	case f.RepeatOf < 0 || (f.ID > 0 && f.RepeatOf == f.ID):
		return errors.New("a finding can't be a repeat of itself")
	}
	found, ok := jobDate(f.FoundOn)
	if !ok {
		return errors.New("enter the date it was found (between 2000 and 2100)")
	}
	if f.Due != "" {
		due, ok := jobDate(f.Due)
		switch {
		case !ok:
			return errors.New("enter the target date as a date (between 2000 and 2100), or leave it empty")
		case due.Before(found):
			return errors.New("the target date can't be before the date it was found")
		}
	}
	switch f.Status {
	case "", FindingOpen, FindingDoing:
		if f.Status == "" {
			f.Status = FindingOpen
		}
		f.ClosedOn, f.ClosedBy = "", ""
	case FindingClosed:
		closed, ok := jobDate(f.ClosedOn)
		switch {
		case !ok:
			return errors.New("enter the date it was closed (between 2000 and 2100)")
		case closed.Before(found):
			return errors.New("it can't be closed before the date it was found")
		case !visible(f.Action):
			return errors.New("enter what was done before closing it")
		}
	default:
		return fmt.Errorf("unknown finding status %q", f.Status)
	}
	return nil
}

// before reports whether a was found before b: by date, then by number, so
// "is a repeat of" can never go round in a circle.
func (a Finding) before(b Finding) bool {
	if a.FoundOn != b.FoundOn {
		return a.FoundOn < b.FoundOn
	}
	return a.ID < b.ID
}

// CheckLinks checks that a finding's site exists, its PM task is at that
// site, and the finding it repeats is an earlier one at the same site, and
// that findings recorded as repeats of it are still later than it. all is
// every finding on record; the one with f's ID is replaced by f.
func (f Finding) CheckLinks(cat *Catalog, all []Finding) error {
	if _, ok := cat.Site(f.SiteID); !ok {
		return errors.New("that site no longer exists")
	}
	if f.TaskID != "" {
		t, ok := cat.Task(f.TaskID)
		if !ok || t.SiteID != f.SiteID {
			return errors.New("pick one of this site's PM tasks, or none")
		}
	}
	for _, o := range all {
		if o.ID == f.ID {
			continue
		}
		if f.RepeatOf != 0 && o.ID == f.RepeatOf {
			switch {
			case o.SiteID != f.SiteID:
				return fmt.Errorf("finding #%d is at another site; a repeat is the same thing coming back at the same site", o.ID)
			case !o.before(f):
				return fmt.Errorf("finding #%d was found after this one, so this can't be a repeat of it", o.ID)
			}
		}
		if o.RepeatOf == f.ID && f.ID != 0 && !f.before(o) {
			return fmt.Errorf("finding #%d is recorded as a repeat of this one, so this one's date can't be after it", o.ID)
		}
	}
	if f.RepeatOf != 0 {
		for _, o := range all {
			if o.ID == f.RepeatOf && o.ID != f.ID {
				return nil
			}
		}
		return fmt.Errorf("there's no finding #%d", f.RepeatOf)
	}
	return nil
}

// FindingView is a finding with its site, PM task and dates worked out.
type FindingView struct {
	Finding
	Site     Site
	Task     Task // zero when the finding isn't tied to a PM task
	HasTask  bool
	Found    time.Time // FoundOn, local midnight
	DueDay   time.Time // zero when there's no target date
	Closed   time.Time // zero while open
	DaysLeft int       // days from today to the target date; negative is late
	// Age is the days from found to closed, or to today while open.
	Age int
	// Repeats are later findings recorded as this one coming back.
	Repeats []int
}

func (f FindingView) IsOpen() bool          { return f.Status != FindingClosed }
func (f FindingView) HasDue() bool          { return !f.DueDay.IsZero() }
func (f FindingView) Late() bool            { return f.IsOpen() && f.HasDue() && f.DaysLeft < 0 }
func (f FindingView) KindLabel() string     { return choiceLabel(FindingKinds, f.Kind) }
func (f FindingView) SeverityLabel() string { return choiceLabel(FindingSeverities, f.Severity) }
func (f FindingView) SpreadLabel() string   { return choiceLabel(FindingSpreads, f.Spread) }

// ClosedOnTime reports whether a closed finding with a target was closed by it.
func (f FindingView) ClosedOnTime() bool {
	return !f.IsOpen() && f.HasDue() && !f.Closed.After(f.DueDay)
}

// Tone is the colour a finding's status borrows from PM: past its target
// is red, in progress is purple, open is blue and closed is green.
func (f FindingView) Tone() Status {
	switch {
	case !f.IsOpen():
		return StatusOnTrack
	case f.Late():
		return StatusOverdue
	case f.Status == FindingDoing:
		return StatusOffCycle
	}
	return StatusBooked
}

// StatusLabel names the finding's state.
func (f FindingView) StatusLabel() string {
	if f.Late() {
		return "Past target"
	}
	return choiceLabel(FindingStatuses, f.Status)
}

// Hint says what to do next about the finding.
func (f FindingView) Hint() string {
	switch {
	case !f.IsOpen() && f.Spread == "todo":
		return "Closed here. Check the other sites for the same thing, then mark them checked."
	case !f.IsOpen():
		return "Closed. If it comes back, log it as a repeat of this one so it shows in the measures."
	case f.Late():
		return "Past its target date. Chase whoever is fixing it, or agree a new date."
	case f.Severity == "high":
		return "High severity: it could cause an outage or injury. Fix it before the next PM."
	case !f.HasDue():
		return "No target date yet. Agree one with whoever is fixing it."
	}
	return "Open until it's fixed and closed with what was done."
}

// SeverityTone colours the severity: high is red, medium amber, low grey.
func (f FindingView) SeverityTone() Status {
	switch f.Severity {
	case "high":
		return StatusOverdue
	case "medium":
		return StatusDueSoon
	}
	return StatusNoHistory
}

func severityRank(s string) int {
	for i, c := range FindingSeverities {
		if c.Key == s {
			return i
		}
	}
	return len(FindingSeverities)
}

// buildFindings works out every finding's dates and orders them: open ones
// first, most serious and then longest open first; then closed ones, most
// recently closed first. Findings at archived or unknown sites are left
// out, as their PM tasks are.
func buildFindings(cat *Catalog, findings []Finding, today time.Time, loc *time.Location) []FindingView {
	repeats := map[int][]int{}
	for _, f := range findings {
		if f.RepeatOf != 0 {
			repeats[f.RepeatOf] = append(repeats[f.RepeatOf], f.ID)
		}
	}
	var out []FindingView
	for _, f := range findings {
		site, ok := cat.Site(f.SiteID)
		if !ok || site.Archived {
			continue
		}
		found, err := time.ParseInLocation("2006-01-02", f.FoundOn, loc)
		if err != nil {
			continue
		}
		v := FindingView{Finding: f, Site: site, Found: found, Repeats: repeats[f.ID]}
		if t, ok := cat.Task(f.TaskID); ok && f.TaskID != "" {
			v.Task, v.HasTask = t, true
		}
		if f.Due != "" {
			v.DueDay, _ = time.ParseInLocation("2006-01-02", f.Due, loc)
			v.DaysLeft = DaysBetween(today, v.DueDay)
		}
		end := today
		if f.Status == FindingClosed {
			v.Closed, _ = time.ParseInLocation("2006-01-02", f.ClosedOn, loc)
			end = v.Closed
		}
		v.Age = max(0, DaysBetween(found, end))
		sort.Ints(v.Repeats)
		out = append(out, v)
	}
	sort.SliceStable(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if x.IsOpen() != y.IsOpen() {
			return x.IsOpen()
		}
		if !x.IsOpen() {
			if !x.Closed.Equal(y.Closed) {
				return x.Closed.After(y.Closed)
			}
			return x.ID > y.ID
		}
		if r, s := severityRank(x.Severity), severityRank(y.Severity); r != s {
			return r < s
		}
		if x.Late() != y.Late() {
			return x.Late()
		}
		if !x.Found.Equal(y.Found) {
			return x.Found.Before(y.Found)
		}
		return x.ID < y.ID
	})
	return out
}

// Finding finds a finding by number.
func (s Snapshot) Finding(id int) (FindingView, bool) {
	for _, f := range s.Findings {
		if f.ID == id {
			return f, true
		}
	}
	return FindingView{}, false
}

// FindingsAt returns a site's findings, in the snapshot's order.
func (s Snapshot) FindingsAt(siteID string) []FindingView {
	var out []FindingView
	for _, f := range s.Findings {
		if f.SiteID == siteID {
			out = append(out, f)
		}
	}
	return out
}

// FindingsFor returns the findings from one PM task, in the snapshot's order.
func (s Snapshot) FindingsFor(taskID string) []FindingView {
	var out []FindingView
	for _, f := range s.Findings {
		if f.HasTask && f.TaskID == taskID {
			out = append(out, f)
		}
	}
	return out
}

// FindingStats are the measures that show whether things are getting
// better. Open counts are as of today; the rest cover findings found (or
// closed) on or after Since.
type FindingStats struct {
	Open, OpenHigh, Doing, Late int
	SpreadTodo                  int // findings whose other sites still need checking
	Found                       int // found since Since
	Repeats                     int // of those, repeats of an earlier finding
	Closed                      int // closed since Since
	MedianClose                 int // days from found to closed, for those
	WithTarget, OnTime          int // closed since Since with a target date, and by it
}

// RepeatShare is the percentage of findings that were repeats, or -1.
func (s FindingStats) RepeatShare() int { return percent(s.Repeats, s.Found) }

// OnTimeShare is the percentage closed by their target date, or -1.
func (s FindingStats) OnTimeShare() int { return percent(s.OnTime, s.WithTarget) }

func percent(n, of int) int {
	if of == 0 {
		return -1
	}
	return (n*100 + of/2) / of
}

// StatsOf measures a list of findings.
func StatsOf(list []FindingView, since time.Time) FindingStats {
	var st FindingStats
	var days []int
	for _, f := range list {
		if f.IsOpen() {
			st.Open++
			if f.Severity == "high" {
				st.OpenHigh++
			}
			if f.Status == FindingDoing {
				st.Doing++
			}
			if f.Late() {
				st.Late++
			}
		}
		if f.Spread == "todo" {
			st.SpreadTodo++
		}
		if !f.Found.Before(since) {
			st.Found++
			if f.RepeatOf != 0 {
				st.Repeats++
			}
		}
		if !f.IsOpen() && !f.Closed.Before(since) {
			st.Closed++
			days = append(days, f.Age)
			if f.HasDue() {
				st.WithTarget++
				if f.ClosedOnTime() {
					st.OnTime++
				}
			}
		}
	}
	st.MedianClose = -1
	if n := len(days); n > 0 {
		sort.Ints(days)
		st.MedianClose = days[n/2]
		if n%2 == 0 {
			st.MedianClose = (days[n/2-1] + days[n/2] + 1) / 2
		}
	}
	return st
}

// FindingGroup is one row of a findings breakdown: a site or a vendor.
type FindingGroup struct {
	Key, Code, Name string
	Stats           FindingStats
}

// FindingsBySite measures each site with an open finding or one found
// since since: most open high-severity findings first, then most open.
func FindingsBySite(list []FindingView, since time.Time) []FindingGroup {
	return groupFindings(list, since, func(f FindingView) (string, string, string, bool) {
		return f.Site.ID, f.Site.Code, f.Site.Name, true
	})
}

// FindingsByVendor measures findings from PM tasks by the task's vendor.
func FindingsByVendor(list []FindingView, cat *Catalog, since time.Time) []FindingGroup {
	return groupFindings(list, since, func(f FindingView) (string, string, string, bool) {
		if !f.HasTask {
			return "", "", "", false
		}
		return f.Task.Vendor, "", cat.VendorName(f.Task.Vendor), true
	})
}

func groupFindings(list []FindingView, since time.Time, key func(FindingView) (string, string, string, bool)) []FindingGroup {
	var order []string
	groups := map[string][]FindingView{}
	names := map[string][2]string{}
	for _, f := range list {
		k, code, name, ok := key(f)
		if !ok {
			continue
		}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
			names[k] = [2]string{code, name}
		}
		groups[k] = append(groups[k], f)
	}
	var out []FindingGroup
	for _, k := range order {
		st := StatsOf(groups[k], since)
		if st.Open == 0 && st.Found == 0 && st.Closed == 0 {
			continue
		}
		out = append(out, FindingGroup{Key: k, Code: names[k][0], Name: names[k][1], Stats: st})
	}
	sort.SliceStable(out, func(a, b int) bool {
		x, y := out[a].Stats, out[b].Stats
		if x.OpenHigh != y.OpenHigh {
			return x.OpenHigh > y.OpenHigh
		}
		if x.Open != y.Open {
			return x.Open > y.Open
		}
		if x.Found != y.Found {
			return x.Found > y.Found
		}
		if out[a].Code != out[b].Code {
			return out[a].Code < out[b].Code
		}
		return out[a].Name < out[b].Name
	})
	return out
}
