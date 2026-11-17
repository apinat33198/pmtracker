package web

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/apinat33198/pmtracker/internal/core"
	"github.com/apinat33198/pmtracker/internal/store"
)

// Findings: what was found on PM visits and walks round the rooms, followed
// until closed, with the measures that show whether things get better
// (Kaizen's Check and Act). They never change a PM forecast or status.

// findingsShown is how many findings the Findings page lists; the CSV has all.
const findingsShown = 200

// findingsWindow is how far back the measures on the Findings page look.
const findingsWindow = 12 // months

type findingFilter struct {
	Site, Status, Severity, Kind string
}

type findingsData struct {
	Filter   findingFilter
	SiteName string
	List     []core.FindingView
	More     int
	Stats    core.FindingStats
	Since    time.Time
	BySite   []core.FindingGroup
	ByVendor []core.FindingGroup
	Sites    []core.Site
	NewLink  string
}

func (s *Server) findingsPage(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	q := r.URL.Query()
	f := findingFilter{Site: q.Get("site"), Status: q.Get("status"), Severity: q.Get("severity"), Kind: q.Get("kind")}
	if f.Status != "closed" && f.Status != "all" {
		f.Status = "open"
	}
	d := findingsData{Filter: f, Sites: liveSites(snap), Since: snap.Today.AddDate(0, -findingsWindow, 0), NewLink: "/findings/new"}
	site := snap.Findings
	if ss, ok := snap.Site(f.Site); ok {
		site = snap.FindingsAt(f.Site)
		d.SiteName = ss.Site.Code + " " + ss.Site.Name
		d.NewLink += "?site=" + url.QueryEscape(f.Site)
	} else {
		d.Filter.Site = ""
		d.BySite = core.FindingsBySite(site, d.Since)
	}
	d.Stats = core.StatsOf(site, d.Since)
	d.ByVendor = core.FindingsByVendor(site, snap.Catalog, d.Since)
	for _, v := range site {
		switch {
		case f.Status == "open" && !v.IsOpen(), f.Status == "closed" && v.IsOpen():
		case f.Severity != "" && v.Severity != f.Severity, f.Kind != "" && v.Kind != f.Kind:
		default:
			d.List = append(d.List, v)
		}
	}
	if len(d.List) > findingsShown {
		d.More = len(d.List) - findingsShown
		d.List = d.List[:findingsShown]
	}
	s.render(w, r, "findings", "findings", "Findings", snap, d)
}

// findingForm is the add and edit form.
type findingForm struct {
	F       core.Finding // the values in the form
	New     bool
	Site    core.Site
	Tasks   []core.Task
	Repeats []core.FindingView // earlier findings at the site it may be a repeat of
	Owners  []string
	Error   string
	Today   time.Time
	From    string // where a new finding came from, such as a visit
}

// findingData is the finding page, which is also the new-finding page.
type findingData struct {
	V        core.FindingView   // the finding (zero for a new one)
	Chain    []core.FindingView // every finding in its repeat chain, oldest first
	Form     findingForm
	PickSite []core.Site // the first step of a new finding with no site yet
	// Incidents are the incidents linked to the finding.
	Incidents []core.IncidentView
}

// repeatChoices are the findings at a site that f could be a repeat of:
// earlier ones, most recently found first.
func repeatChoices(snap core.Snapshot, f core.Finding) []core.FindingView {
	var out []core.FindingView
	for _, o := range snap.FindingsAt(f.SiteID) {
		if o.ID == f.ID || (f.ID != 0 && (o.FoundOn > f.FoundOn || (o.FoundOn == f.FoundOn && o.ID > f.ID))) {
			continue
		}
		out = append(out, o)
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].FoundOn != out[b].FoundOn {
			return out[a].FoundOn > out[b].FoundOn
		}
		return out[a].ID > out[b].ID
	})
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// ownerOptions suggests who fixes a finding: the vendor list, then names
// used on earlier findings.
func ownerOptions(snap core.Snapshot) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if k := strings.ToLower(name); name != "" && !seen[k] {
			seen[k] = true
			out = append(out, name)
		}
	}
	for _, v := range snap.Catalog.Vendors {
		add(v.Name)
	}
	var used []string
	for _, f := range snap.Findings {
		used = append(used, f.Owner)
	}
	sort.Strings(used)
	for _, o := range used {
		add(o)
	}
	return out
}

func (s *Server) formFor(snap core.Snapshot, f core.Finding, isNew bool) findingForm {
	site, _ := snap.Catalog.Site(f.SiteID)
	return findingForm{F: f, New: isNew, Site: site, Tasks: snap.Catalog.TasksAt(f.SiteID), Repeats: repeatChoices(snap, f),
		Owners: ownerOptions(snap), Today: snap.Today}
}

// newFinding shows the form for a new finding. The address can fill it in:
// site, task and date; visit (a PM visit's date, site and task); or repeat
// (an earlier finding that came back).
func (s *Server) newFinding(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	q := r.URL.Query()
	f := core.Finding{SiteID: q.Get("site"), FoundOn: snap.Today.Format("2006-01-02"), Kind: "equipment", Severity: "medium", Status: core.FindingOpen}
	from := ""
	var visitTasks []string
	if id, err := strconv.Atoi(q.Get("repeat")); err == nil {
		if o, ok := snap.Finding(id); ok {
			f.SiteID, f.TaskID, f.Kind, f.Severity, f.Title, f.RepeatOf = o.SiteID, o.TaskID, o.Kind, o.Severity, o.Title, o.ID
			from = fmt.Sprintf("A repeat of finding #%d, found %s.", o.ID, o.Found.Format("2 Jan 2006"))
		}
	}
	if id := q.Get("visit"); id != "" {
		for _, v := range snap.Visits {
			if v.ID != id || len(v.Tasks) == 0 {
				continue
			}
			if t, ok := snap.Catalog.Task(v.Tasks[0]); ok {
				f.SiteID, f.FoundOn, visitTasks = t.SiteID, v.Date.Format("2006-01-02"), v.Tasks
				if len(v.Tasks) == 1 {
					f.TaskID = t.ID
				}
				var names []string
				for _, id := range v.Tasks {
					names = append(names, snap.Catalog.TaskLabel(id))
				}
				from = fmt.Sprintf("From the PM visit on %s: %s.", v.Date.Format("2 Jan 2006"), strings.Join(names, ", "))
			}
			break
		}
	}
	if t, ok := snap.Catalog.Task(q.Get("task")); ok && (f.SiteID == "" || f.SiteID == t.SiteID) {
		f.SiteID, f.TaskID = t.SiteID, t.ID
	}
	if d, err := time.ParseInLocation("2006-01-02", q.Get("date"), s.Cfg.Location()); err == nil && !d.After(snap.Today) {
		f.FoundOn = q.Get("date")
	}
	d := findingData{}
	if ss, ok := snap.Site(f.SiteID); !ok || ss.Site.Archived {
		d.PickSite = liveSites(snap)
		s.render(w, r, "finding", "findings", "Log a finding", snap, d)
		return
	}
	d.Form = s.formFor(snap, f, true)
	d.Form.From = from
	if len(visitTasks) > 1 { // offer only the systems serviced on that visit
		var tasks []core.Task
		for _, t := range d.Form.Tasks {
			for _, id := range visitTasks {
				if t.ID == id {
					tasks = append(tasks, t)
				}
			}
		}
		d.Form.Tasks = tasks
	}
	s.render(w, r, "finding", "findings", "Log a finding", snap, d)
}

// findingID reads the finding number from the address.
func findingID(r *http.Request) int {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 {
		return 0
	}
	return id
}

func (s *Server) findingPage(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	v, ok := snap.Finding(findingID(r))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.renderFinding(w, r, http.StatusOK, snap, v, v.Finding, "")
}

func (s *Server) renderFinding(w http.ResponseWriter, r *http.Request, code int, snap core.Snapshot, v core.FindingView, form core.Finding, msg string) {
	d := findingData{V: v, Form: s.formFor(snap, form, false)}
	d.Form.Error = msg
	d.Chain = repeatChain(snap, v)
	d.Incidents = snap.IncidentsLinkedTo(v.ID)
	s.renderCode(w, r, code, "finding", "findings", fmt.Sprintf("Finding #%d at %s", v.ID, v.Site.Code), snap, d)
}

// repeatChain is every finding linked to v by "repeat of", back to the
// first time it was found and forward to every time it came back, oldest
// first. Links always point to an earlier finding, so the walk ends.
func repeatChain(snap core.Snapshot, v core.FindingView) []core.FindingView {
	root := v
	for root.RepeatOf != 0 {
		o, ok := snap.Finding(root.RepeatOf)
		if !ok {
			break
		}
		root = o
	}
	chain := []core.FindingView{root}
	for i := 0; i < len(chain); i++ {
		for _, id := range chain[i].Repeats {
			if o, ok := snap.Finding(id); ok {
				chain = append(chain, o)
			}
		}
	}
	sort.SliceStable(chain, func(a, b int) bool {
		if !chain[a].Found.Equal(chain[b].Found) {
			return chain[a].Found.Before(chain[b].Found)
		}
		return chain[a].ID < chain[b].ID
	})
	return chain
}

// readFinding copies the form into f, and refuses dates in the future.
func (s *Server) readFinding(r *http.Request, f *core.Finding) error {
	f.TaskID, f.FoundOn = r.FormValue("task"), r.FormValue("found_on")
	f.Kind, f.Severity = r.FormValue("kind"), r.FormValue("severity")
	f.Title, f.Reading, f.Detail = r.FormValue("title"), r.FormValue("reading"), r.FormValue("detail")
	f.Owner, f.Due = r.FormValue("owner"), r.FormValue("due")
	f.Status, f.Cause, f.Action, f.ClosedOn = r.FormValue("status"), r.FormValue("cause"), r.FormValue("action"), r.FormValue("closed_on")
	f.Spread = r.FormValue("spread")
	f.RepeatOf = 0
	if v := r.FormValue("repeat_of"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return errors.New("pick the earlier finding from the list")
		}
		f.RepeatOf = n
	}
	loc, today := s.Cfg.Location(), s.today()
	if d, err := time.ParseInLocation("2006-01-02", f.FoundOn, loc); err == nil && d.After(today) {
		return errors.New("the date it was found can't be in the future")
	}
	if f.Status == core.FindingClosed {
		if f.ClosedOn == "" {
			f.ClosedOn = today.Format("2006-01-02")
		}
		if d, err := time.ParseInLocation("2006-01-02", f.ClosedOn, loc); err == nil && d.After(today) {
			return errors.New("the date it was closed can't be in the future")
		}
	}
	return nil
}

func findingLabel(cat core.Catalog, f core.Finding) string {
	code := f.SiteID
	if site, ok := cat.Site(f.SiteID); ok {
		code = site.Code
	}
	what := ""
	if t, ok := cat.Task(f.TaskID); ok && f.TaskID != "" {
		what = cat.SystemName(t.System) + ", "
	}
	return fmt.Sprintf("finding #%d at %s (%s%s): “%s”", f.ID, code, what, f.Severity, f.Title)
}

func (s *Server) addFinding(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	f := core.Finding{SiteID: r.FormValue("site"), By: u.Name, Created: s.Now()}
	err := s.readFinding(r, &f)
	cat := s.Store.Catalog()
	if site, ok := cat.Site(f.SiteID); !ok || site.Archived {
		s.back(w, r, "/findings", "Pick the site the finding is at.", "error")
		return
	}
	if f.Status == core.FindingClosed {
		f.ClosedBy = u.Name
	}
	if err == nil {
		var saved core.Finding
		saved, err = s.Store.AddFinding(f, func(f core.Finding) string {
			if f.Status == core.FindingClosed {
				return "Logged and closed " + findingLabel(cat, f)
			}
			return "Logged " + findingLabel(cat, f)
		})
		if err == nil {
			s.redirect(w, r, fmt.Sprintf("/findings/%d", saved.ID), fmt.Sprintf("Logged finding #%d.", saved.ID), "")
			return
		}
	}
	snap := s.snapshot()
	d := findingData{Form: s.formFor(snap, f, true)}
	d.Form.Error = "The finding wasn't logged: " + err.Error() + "."
	s.renderCode(w, r, http.StatusUnprocessableEntity, "finding", "findings", "Log a finding", snap, d)
}

func (s *Server) editFinding(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	cat := s.Store.Catalog()
	var posted core.Finding
	saved, err := s.Store.UpdateFinding(findingID(r), u.Name, func(old, f core.Finding) string {
		switch {
		case old.Status != core.FindingClosed && f.Status == core.FindingClosed:
			return "Closed " + findingLabel(cat, f)
		case old.Status == core.FindingClosed && f.Status != core.FindingClosed:
			return "Reopened " + findingLabel(cat, f)
		}
		return "Edited " + findingLabel(cat, f)
	}, func(f *core.Finding) error {
		wasClosed := f.Status == core.FindingClosed
		err := s.readFinding(r, f)
		if f.Status == core.FindingClosed && (!wasClosed || f.ClosedBy == "") {
			f.ClosedBy = u.Name
		}
		posted = *f
		return err
	})
	switch {
	case errors.Is(err, store.ErrNoFinding):
		s.back(w, r, "/findings", "That finding no longer exists.", "error")
	case err != nil:
		snap := s.snapshot()
		v, ok := snap.Finding(findingID(r))
		if !ok {
			s.back(w, r, "/findings", "That finding no longer exists.", "error")
			return
		}
		posted.ID, posted.SiteID = v.ID, v.SiteID
		s.renderFinding(w, r, http.StatusUnprocessableEntity, snap, v, posted, "Nothing was changed: "+err.Error()+".")
	default:
		msg := fmt.Sprintf("Saved finding #%d.", saved.ID)
		if saved.Status == core.FindingClosed && saved.Spread == "todo" {
			msg += " The other sites still need checking for the same thing."
		}
		s.redirect(w, r, fmt.Sprintf("/findings/%d", saved.ID), msg, "")
	}
}

func (s *Server) deleteFinding(w http.ResponseWriter, r *http.Request) {
	cat := s.Store.Catalog()
	f, err := s.Store.DeleteFinding(findingID(r), userOf(r).Name, func(f core.Finding) string { return "Deleted " + findingLabel(cat, f) })
	switch {
	case errors.Is(err, store.ErrNoFinding):
		s.back(w, r, "/findings", "That finding no longer exists.", "error")
	case err != nil:
		s.fail(w, err)
	default:
		s.redirect(w, r, "/findings?site="+url.QueryEscape(f.SiteID), fmt.Sprintf("Deleted finding #%d.", f.ID), "")
	}
}

func (s *Server) findingsCSV(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="findings-%s.csv"`, snap.Today.Format("2006-01-02")))
	w.Write([]byte("\ufeff")) // so Excel opens Thai text as UTF-8
	cw := csv.NewWriter(w)
	cw.Write([]string{"id", "site_code", "site_name", "region", "system", "vendor", "found_on", "kind", "severity", "finding", "reading", "details",
		"owner", "target_date", "status", "closed_on", "closed_by", "days_to_close", "root_cause", "action", "repeat_of", "other_sites", "added_by", "added_at"})
	for _, f := range snap.Findings {
		system, vendor := "", ""
		if f.HasTask {
			system, vendor = snap.Catalog.SystemName(f.Task.System), snap.Catalog.VendorName(f.Task.Vendor)
		}
		days, repeat := "", ""
		if !f.IsOpen() {
			days = strconv.Itoa(f.Age)
		}
		if f.RepeatOf != 0 {
			repeat = strconv.Itoa(f.RepeatOf)
		}
		cw.Write([]string{strconv.Itoa(f.ID), csvCell(f.Site.Code), csvCell(f.Site.Name), csvCell(f.Site.Region), csvCell(system), csvCell(vendor),
			f.FoundOn, f.KindLabel(), f.SeverityLabel(), csvCell(f.Title), csvCell(f.Reading), csvCell(f.Detail), csvCell(f.Owner), f.Due,
			f.StatusLabel(), f.ClosedOn, csvCell(f.ClosedBy), days, csvCell(f.Cause), csvCell(f.Action), repeat, f.SpreadLabel(),
			csvCell(f.By), f.Created.In(s.Cfg.Location()).Format("2006-01-02 15:04")})
	}
	cw.Flush()
}
