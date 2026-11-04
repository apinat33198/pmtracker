package core

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/apinat33198/pmtracker/internal/config"
)

// Anchor modes for a task's PM cycle.
const (
	AnchorRolling = "rolling" // next due = last done + interval
	AnchorFixed   = "fixed"   // due dates = anchor + n × interval
)

// Site is one location that receives vendor PM visits.
type Site struct {
	ID       string    `json:"id"`   // stable slug; never changes on rename
	Code     string    `json:"code"` // short code used in event titles, e.g. BKK1
	Name     string    `json:"name"`
	Region   string    `json:"region,omitempty"`
	Aliases  []string  `json:"aliases,omitempty"` // other names that appear in titles or locations
	Notes    string    `json:"notes,omitempty"`
	Archived bool      `json:"archived,omitempty"`
	Created  time.Time `json:"created"`
}

// System is a kind of equipment that gets PM: UPS, DC power, CRAC …
type System struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
}

type Vendor struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	Domains []string `json:"domains,omitempty"`
	Contact string   `json:"contact,omitempty"` // contact person
	Phone   string   `json:"phone,omitempty"`
	Email   string   `json:"email,omitempty"`
	Notes   string   `json:"notes,omitempty"`
}

// Task is one recurring PM job: a system at a site, serviced by a vendor.
type Task struct {
	ID             string    `json:"id"`
	SiteID         string    `json:"site_id"`
	System         string    `json:"system"`
	Vendor         string    `json:"vendor"`
	IntervalMonths int       `json:"interval_months,omitempty"` // 0 = default
	WindowDays     int       `json:"window_days,omitempty"`
	LeadDays       int       `json:"lead_days,omitempty"`
	GraceDays      int       `json:"grace_days,omitempty"`
	AnchorMode     string    `json:"anchor_mode,omitempty"`
	AnchorDate     string    `json:"anchor_date,omitempty"` // YYYY-MM-DD
	Notes          string    `json:"notes,omitempty"`
	Paused         bool      `json:"paused,omitempty"`
	Created        time.Time `json:"created"`
}

// Calendar is a Google calendar the tracker reads.
type Calendar struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	PMOnly bool   `json:"pm_only,omitempty"` // every event on it is a PM visit
	SiteID string `json:"site_id,omitempty"` // every event on it belongs to this site
}

// Catalog is everything an admin manages.
type Catalog struct {
	Sites     []Site     `json:"sites"`
	Systems   []System   `json:"systems"`
	Vendors   []Vendor   `json:"vendors"`
	Tasks     []Task     `json:"tasks"`
	Calendars []Calendar `json:"calendars"`
	NextTask  int        `json:"next_task"`
}

// Policy is a task's effective cycle settings after defaults.
type Policy struct {
	IntervalMonths int
	WindowDays     int
	LeadDays       int
	GraceDays      int
	AnchorMode     string
	Anchor         time.Time
}

func (t Task) Policy(d config.Defaults, loc *time.Location) Policy {
	p := Policy{IntervalMonths: t.IntervalMonths, WindowDays: t.WindowDays, LeadDays: t.LeadDays, GraceDays: t.GraceDays, AnchorMode: t.AnchorMode}
	if p.IntervalMonths <= 0 {
		p.IntervalMonths = d.IntervalMonths
	}
	if p.WindowDays <= 0 {
		p.WindowDays = d.WindowDays
	}
	if p.LeadDays <= 0 {
		p.LeadDays = d.LeadDays
	}
	if p.GraceDays <= 0 {
		p.GraceDays = d.GraceDays
	}
	if p.AnchorMode == "" {
		p.AnchorMode = AnchorRolling
	}
	if t.AnchorDate != "" {
		p.Anchor, _ = time.ParseInLocation("2006-01-02", t.AnchorDate, loc)
	}
	return p
}

// Clone deep-copies the catalog so an edit can be validated before saving.
func (c Catalog) Clone() Catalog {
	out := Catalog{NextTask: c.NextTask}
	for _, s := range c.Sites {
		s.Aliases = append([]string(nil), s.Aliases...)
		out.Sites = append(out.Sites, s)
	}
	for _, s := range c.Systems {
		s.Aliases = append([]string(nil), s.Aliases...)
		out.Systems = append(out.Systems, s)
	}
	for _, v := range c.Vendors {
		v.Aliases = append([]string(nil), v.Aliases...)
		v.Domains = append([]string(nil), v.Domains...)
		out.Vendors = append(out.Vendors, v)
	}
	out.Tasks = append(out.Tasks, c.Tasks...)
	out.Calendars = append(out.Calendars, c.Calendars...)
	return out
}

func (c Catalog) Site(id string) (Site, bool) {
	for _, s := range c.Sites {
		if s.ID == id {
			return s, true
		}
	}
	return Site{}, false
}

func (c Catalog) SiteByCode(code string) (Site, bool) {
	code = strings.TrimSpace(code)
	for _, s := range c.Sites {
		if strings.EqualFold(s.Code, code) {
			return s, true
		}
	}
	return Site{}, false
}

func (c Catalog) System(key string) (System, bool) {
	for _, s := range c.Systems {
		if s.Key == key {
			return s, true
		}
	}
	return System{}, false
}

func (c Catalog) Vendor(key string) (Vendor, bool) {
	for _, v := range c.Vendors {
		if v.Key == key {
			return v, true
		}
	}
	return Vendor{}, false
}

func (c Catalog) Task(id string) (Task, bool) {
	for _, t := range c.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

func (c Catalog) Calendar(id string) (Calendar, bool) {
	for _, cal := range c.Calendars {
		if cal.ID == id {
			return cal, true
		}
	}
	return Calendar{}, false
}

func (c Catalog) TasksAt(siteID string) []Task {
	var out []Task
	for _, t := range c.Tasks {
		if t.SiteID == siteID {
			out = append(out, t)
		}
	}
	return out
}

// FindSystem matches a system by key, name or alias (case-insensitive).
func (c Catalog) FindSystem(s string) (System, bool) {
	s = strings.TrimSpace(s)
	for _, sys := range c.Systems {
		for _, n := range append([]string{sys.Key, sys.Name}, sys.Aliases...) {
			if strings.EqualFold(n, s) {
				return sys, true
			}
		}
	}
	return System{}, false
}

// FindVendor matches a vendor by key, name or alias (case-insensitive).
func (c Catalog) FindVendor(s string) (Vendor, bool) {
	s = strings.TrimSpace(s)
	for _, v := range c.Vendors {
		for _, n := range append([]string{v.Key, v.Name}, v.Aliases...) {
			if strings.EqualFold(n, s) {
				return v, true
			}
		}
	}
	return Vendor{}, false
}

func (c Catalog) SiteLabel(id string) string {
	if s, ok := c.Site(id); ok {
		return s.Code + " " + s.Name
	}
	return id
}

func (c Catalog) SystemName(key string) string {
	if s, ok := c.System(key); ok {
		return s.Name
	}
	return key
}

func (c Catalog) VendorName(key string) string {
	if v, ok := c.Vendor(key); ok {
		return v.Name
	}
	return key
}

// TaskLabel is "BKK1 UPS (ABC Power)".
func (c Catalog) TaskLabel(id string) string {
	t, ok := c.Task(id)
	if !ok {
		return id
	}
	code := t.SiteID
	if s, ok := c.Site(t.SiteID); ok {
		code = s.Code
	}
	return fmt.Sprintf("%s %s (%s)", code, c.SystemName(t.System), c.VendorName(t.Vendor))
}

// EnsureVendor returns the vendor called name (by name or other name),
// creating it if there is none. created reports whether it was new.
func (c *Catalog) EnsureVendor(name string) (v Vendor, created bool, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Vendor{}, false, errors.New("Enter the new vendor's name.")
	}
	if v, ok := c.FindVendor(name); ok {
		return v, false, nil
	}
	v = Vendor{Key: UniqueSlug(name, func(k string) bool { _, ok := c.Vendor(k); return ok }), Name: name}
	c.Vendors = append(c.Vendors, v)
	return v, true, nil
}

// Tidy puts names on one line: line breaks, tabs and repeated spaces in
// codes, names, other names and contact fields become single spaces, and
// empty other names are dropped. Notes keep their line breaks.
func (c *Catalog) Tidy() {
	list := func(xs []string) []string {
		var out []string
		for _, x := range xs {
			if x = oneLine(x); x != "" {
				out = append(out, x)
			}
		}
		return out
	}
	for i := range c.Sites {
		s := &c.Sites[i]
		s.Code, s.Name, s.Region, s.Aliases = oneLine(s.Code), oneLine(s.Name), oneLine(s.Region), list(s.Aliases)
	}
	for i := range c.Systems {
		s := &c.Systems[i]
		s.Name, s.Aliases = oneLine(s.Name), list(s.Aliases)
	}
	for i := range c.Vendors {
		v := &c.Vendors[i]
		v.Name, v.Contact, v.Phone, v.Email = oneLine(v.Name), oneLine(v.Contact), oneLine(v.Phone), oneLine(v.Email)
		v.Aliases, v.Domains = list(v.Aliases), list(v.Domains)
	}
	for i := range c.Calendars {
		c.Calendars[i].Label = oneLine(c.Calendars[i].Label)
	}
}

// MaxSystemName bounds a system's name: it's a column heading on the overview.
const MaxSystemName = 80

// EnsureSystem finds a system by key, name or other name, or adds one with
// that name. Matching other names means typing "PAC" picks CRAC rather than
// adding a second, near-duplicate column.
func (c *Catalog) EnsureSystem(name string) (s System, created bool, err error) {
	name = oneLine(name)
	if name == "" {
		return System{}, false, errors.New("Enter the new system's name.")
	}
	if len([]rune(name)) > MaxSystemName {
		return System{}, false, fmt.Errorf("Keep the system's name under %d characters.", MaxSystemName)
	}
	if s, ok := c.FindSystem(name); ok {
		return s, false, nil
	}
	s = System{Key: UniqueSlug(name, func(k string) bool { _, ok := c.System(k); return ok }), Name: name}
	c.Systems = append(c.Systems, s)
	return s, true, nil
}

// AddTask assigns an ID and appends the task.
func (c *Catalog) AddTask(t Task) Task {
	c.NextTask++
	t.ID = fmt.Sprintf("t%d", c.NextTask)
	c.Tasks = append(c.Tasks, t)
	return t
}

// Slug turns a name into a stable lowercase key.
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// UniqueSlug returns Slug(s), suffixed -2, -3 … if taken.
func UniqueSlug(s string, taken func(string) bool) string {
	base := Slug(s)
	if base == "" {
		base = "item"
	}
	if !taken(base) {
		return base
	}
	for i := 2; ; i++ {
		if k := fmt.Sprintf("%s-%d", base, i); !taken(k) {
			return k
		}
	}
}

// SplitList parses a comma- or newline-separated field from a form.
func SplitList(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == ';' }) {
		p = strings.TrimSpace(p)
		if p != "" && !seen[strings.ToLower(p)] {
			seen[strings.ToLower(p)] = true
			out = append(out, p)
		}
	}
	return out
}

// Validate checks references and uniqueness. Errors read as sentences for the admin UI.
func (c *Catalog) Validate() error {
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }

	ids, codes := map[string]bool{}, map[string]string{}
	for _, s := range c.Sites {
		if s.ID == "" || strings.TrimSpace(s.Code) == "" || strings.TrimSpace(s.Name) == "" {
			add("Every site needs a code and a name.")
			continue
		}
		if ids[s.ID] {
			add("Two sites share the ID %q.", s.ID)
		}
		ids[s.ID] = true
		k := strings.ToLower(s.Code)
		if other, ok := codes[k]; ok {
			add("Site code %s is used by both %s and %s.", s.Code, other, s.Name)
		}
		codes[k] = s.Name
	}
	sys := map[string]bool{}
	for _, s := range c.Systems {
		if s.Key == "" || strings.TrimSpace(s.Name) == "" {
			add("Every system needs a name.")
			continue
		}
		if sys[s.Key] {
			add("Two systems share the key %q.", s.Key)
		}
		sys[s.Key] = true
	}
	ven := map[string]bool{}
	for _, v := range c.Vendors {
		if v.Key == "" || strings.TrimSpace(v.Name) == "" {
			add("Every vendor needs a name.")
			continue
		}
		if ven[v.Key] {
			add("Two vendors share the key %q.", v.Key)
		}
		ven[v.Key] = true
	}
	tids, combo := map[string]bool{}, map[string]bool{}
	for _, t := range c.Tasks {
		if tids[t.ID] {
			add("Two tasks share the ID %q.", t.ID)
		}
		tids[t.ID] = true
		if !ids[t.SiteID] {
			add("Task %s points at a site that doesn't exist.", t.ID)
		}
		if !sys[t.System] {
			add("Task %s points at a system that doesn't exist.", t.ID)
		}
		if !ven[t.Vendor] {
			add("Task %s points at a vendor that doesn't exist.", t.ID)
		}
		k := t.SiteID + "|" + t.System + "|" + t.Vendor
		if combo[k] {
			add("%s already has a task for %s by %s.", c.SiteLabel(t.SiteID), c.SystemName(t.System), c.VendorName(t.Vendor))
		}
		combo[k] = true
		if t.IntervalMonths < 0 || t.IntervalMonths > 60 {
			add("Task %s: the interval must be 1 to 60 months.", c.TaskLabel(t.ID))
		}
		if t.AnchorMode != "" && t.AnchorMode != AnchorRolling && t.AnchorMode != AnchorFixed {
			add("Task %s: unknown cycle mode %q.", c.TaskLabel(t.ID), t.AnchorMode)
		}
		if t.AnchorDate != "" {
			if _, err := time.Parse("2006-01-02", t.AnchorDate); err != nil {
				add("Task %s: the anchor date must be a date.", c.TaskLabel(t.ID))
			}
		}
		if t.AnchorMode == AnchorFixed && t.AnchorDate == "" {
			add("Task %s: fixed contract slots need a first slot date.", c.TaskLabel(t.ID))
		}
	}
	cals := map[string]bool{}
	for _, cal := range c.Calendars {
		if strings.TrimSpace(cal.ID) == "" {
			add("Every calendar needs a calendar ID.")
			continue
		}
		if cals[cal.ID] {
			add("Calendar %s is listed twice.", cal.ID)
		}
		cals[cal.ID] = true
		if cal.SiteID != "" && !ids[cal.SiteID] {
			add("Calendar %s points at a site that doesn't exist.", cal.Label)
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, " "))
	}
	return nil
}

// SortSites orders by region, then code.
func SortSites(sites []Site) {
	sort.SliceStable(sites, func(i, j int) bool {
		if sites[i].Region != sites[j].Region {
			return sites[i].Region < sites[j].Region
		}
		return strings.ToLower(sites[i].Code) < strings.ToLower(sites[j].Code)
	})
}

// Regions lists distinct site regions in order.
func (c Catalog) Regions() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range c.Sites {
		if s.Region != "" && !seen[s.Region] {
			seen[s.Region] = true
			out = append(out, s.Region)
		}
	}
	sort.Strings(out)
	return out
}

// DefaultSystems seeds a new data file.
func DefaultSystems() []System {
	return []System{
		{Key: "dc", Name: "DC power", Aliases: []string{"DC", "Rectifier", "DC system", "Battery"}},
		{Key: "ups", Name: "UPS", Aliases: []string{"UPS"}},
		{Key: "crac", Name: "CRAC", Aliases: []string{"CRAC", "PAC", "Precision AC", "Precision air"}},
		{Key: "genset", Name: "Generator", Aliases: []string{"Genset", "Generator", "GEN"}},
		{Key: "fire", Name: "Fire suppression", Aliases: []string{"FM200", "Fire suppression", "Fire alarm"}},
	}
}
