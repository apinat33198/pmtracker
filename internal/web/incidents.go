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

	"github.com/apinat33198/pmtracker/internal/config"
	"github.com/apinat33198/pmtracker/internal/core"
	"github.com/apinat33198/pmtracker/internal/store"
)

// Incidents: unplanned events and near misses at the sites, rated and
// treated the ISO 31000 way (assess, treat, re-rate, record, review). Like
// findings, they never change a PM forecast or status.

// incidentsShown is how many incidents the Incidents page lists; the CSV has all.
const incidentsShown = 200

// incidentsWindow is how far back the measures on the Incidents page look.
const incidentsWindow = 12 // months

type incidentFilter struct {
	Site, Status, Kind, Category, Risk string
	L, C                               int // a cell of the risk matrix (current rating)
}

type incidentsData struct {
	Filter     incidentFilter
	SiteName   string
	List       []core.IncidentView
	More       int
	Stats      core.IncidentStats
	Since      time.Time
	Matrix     []core.MatrixRow
	BySite     []core.IncidentGroup
	ByCategory []core.IncidentGroup
	Sites      []core.Site
	NewLink    string
	CellLink   string // the matrix cells' link, without l and c
	Filtered   bool
}

func (s *Server) incidentsPage(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	q := r.URL.Query()
	f := incidentFilter{Site: q.Get("site"), Status: q.Get("status"), Kind: q.Get("kind"), Category: q.Get("category"), Risk: q.Get("risk")}
	f.L, _ = strconv.Atoi(q.Get("l"))
	f.C, _ = strconv.Atoi(q.Get("c"))
	if f.L < 1 || f.L > 5 || f.C < 1 || f.C > 5 {
		f.L, f.C = 0, 0
	}
	if f.Status != "closed" && f.Status != "all" {
		f.Status = "open"
	}
	d := incidentsData{Filter: f, Sites: liveSites(snap), Since: snap.Today.AddDate(0, -incidentsWindow, 0), NewLink: "/incidents/new"}
	list := snap.Incidents
	cell := url.Values{"status": {"all"}}
	if ss, ok := snap.Site(f.Site); ok {
		list = snap.IncidentsAt(f.Site)
		d.SiteName = ss.Site.Code + " " + ss.Site.Name
		d.NewLink += "?site=" + url.QueryEscape(f.Site)
		cell.Set("site", f.Site)
	} else {
		d.Filter.Site = ""
		d.BySite = core.IncidentsBySite(list, d.Since)
	}
	d.CellLink = "/incidents?" + cell.Encode()
	d.Stats = core.IncidentStatsOf(list, d.Since)
	d.Matrix = core.RiskMatrix(list, d.Since)
	d.ByCategory = core.IncidentsByCategory(list, d.Since)
	for _, v := range list {
		cur := v.Current()
		switch {
		case f.Status == "open" && !v.IsOpen(), f.Status == "closed" && v.IsOpen():
		case f.Kind != "" && v.Kind != f.Kind, f.Category != "" && v.Category != f.Category:
		case f.Risk != "" && cur.Key() != f.Risk:
		case f.L != 0 && (cur.Likelihood != f.L || cur.Consequence != f.C):
		default:
			d.List = append(d.List, v)
		}
	}
	d.Filtered = d.Filter.Site != "" || f.Kind != "" || f.Category != "" || f.Risk != "" || f.L != 0 || f.Status != "open"
	if len(d.List) > incidentsShown {
		d.More = len(d.List) - incidentsShown
		d.List = d.List[:incidentsShown]
	}
	s.render(w, r, "incidents", "incidents", "Incidents", snap, d)
}

// incidentForm is the add and edit form.
type incidentForm struct {
	I         core.Incident // the values in the form
	New       bool
	Site      core.Site
	Tasks     []core.Task
	Findings  []core.FindingView  // findings at the site it can be linked to
	Repeats   []core.IncidentView // earlier incidents at the site it may be a repeat of
	Owners    []string
	Error     string
	Now       string // the latest time the form accepts, as a datetime-local value
	Today     time.Time
	From      string // where a new incident came from, such as a finding
	CanAccept bool   // may close with the risk still high or extreme (admins)
}

// incidentData is the incident page, which is also the new-incident page.
type incidentData struct {
	V        core.IncidentView
	Chain    []core.IncidentView // every incident in its repeat chain, oldest first
	Finding  core.FindingView    // the linked finding, if HasFinding
	HasFind  bool
	Timeline []timelineEntry
	Form     incidentForm
	PickSite []core.Site // the first step of a new incident with no site yet
}

// timelineEntry is one line of an incident's timeline as shown.
type timelineEntry struct {
	At      time.Time
	By      string
	Changes []string
	Text    string
	First   bool // the incident being logged
}

func incidentTimeline(v core.IncidentView) []timelineEntry {
	out := []timelineEntry{{At: v.Created, By: v.By, First: true}}
	for _, n := range v.Notes {
		out = append(out, timelineEntry{At: n.At, By: n.By, Changes: n.Changes, Text: n.Text})
	}
	return out
}

// incidentRepeatChoices are the incidents at a site that in could be a
// repeat of: earlier ones, most recent first.
func incidentRepeatChoices(snap core.Snapshot, in core.Incident) []core.IncidentView {
	var out []core.IncidentView
	for _, o := range snap.IncidentsAt(in.SiteID) {
		if o.ID == in.ID || (in.ID != 0 && (o.Occurred > in.Occurred || (o.Occurred == in.Occurred && o.ID > in.ID))) {
			continue
		}
		out = append(out, o)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Occurred > out[b].Occurred })
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// incidentOwners suggests who follows an incident up: the vendor list, then
// names used on earlier incidents and findings.
func incidentOwners(snap core.Snapshot) []string {
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
	for _, v := range snap.Incidents {
		used = append(used, v.Owner)
	}
	for _, f := range snap.Findings {
		used = append(used, f.Owner)
	}
	sort.Strings(used)
	for _, o := range used {
		add(o)
	}
	return out
}

func (s *Server) incidentFormFor(r *http.Request, snap core.Snapshot, in core.Incident, isNew bool) incidentForm {
	site, _ := snap.Catalog.Site(in.SiteID)
	f := incidentForm{I: in, New: isNew, Site: site, Tasks: snap.Catalog.TasksAt(in.SiteID), Findings: snap.FindingsAt(in.SiteID),
		Repeats: incidentRepeatChoices(snap, in), Owners: incidentOwners(snap), Today: snap.Today,
		Now: s.Now().In(s.Cfg.Location()).Format(core.IncidentTime), CanAccept: userOf(r).can(config.RoleAdmin)}
	if len(f.Findings) > 60 {
		f.Findings = f.Findings[:60]
	}
	return f
}

// newIncident shows the form for a new incident. The address can fill it
// in: site and task; finding (a finding that warned of it); or repeat (an
// earlier incident that happened again).
func (s *Server) newIncident(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	q := r.URL.Query()
	in := core.Incident{SiteID: q.Get("site"), Kind: core.IncidentKind, DetectedBy: "alarm", Status: core.IncidentOpen,
		Occurred: s.Now().In(s.Cfg.Location()).Format(core.IncidentTime)}
	from := ""
	if id, err := strconv.Atoi(q.Get("repeat")); err == nil {
		if o, ok := snap.Incident(id); ok {
			in.SiteID, in.TaskID, in.Kind, in.Category, in.Title, in.RepeatOf = o.SiteID, o.TaskID, o.Kind, o.Category, o.Title, o.ID
			from = fmt.Sprintf("It happened again: a repeat of %s, %s.", o.Ref, o.At.Format("2 Jan 2006"))
		}
	}
	if id, err := strconv.Atoi(q.Get("finding")); err == nil {
		if fv, ok := snap.Finding(id); ok {
			in.SiteID, in.TaskID, in.FindingID = fv.SiteID, fv.TaskID, fv.ID
			from = fmt.Sprintf("Linked to finding #%d, “%s”.", fv.ID, fv.Title)
		}
	}
	if t, ok := snap.Catalog.Task(q.Get("task")); ok && (in.SiteID == "" || in.SiteID == t.SiteID) {
		in.SiteID, in.TaskID = t.SiteID, t.ID
	}
	d := incidentData{}
	if ss, ok := snap.Site(in.SiteID); !ok || ss.Site.Archived {
		d.PickSite = liveSites(snap)
		s.render(w, r, "incident", "incidents", "Log an incident", snap, d)
		return
	}
	d.Form = s.incidentFormFor(r, snap, in, true)
	d.Form.From = from
	s.render(w, r, "incident", "incidents", "Log an incident", snap, d)
}

func incidentID(r *http.Request) int {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 {
		return 0
	}
	return id
}

func (s *Server) incidentPage(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	v, ok := snap.Incident(incidentID(r))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.renderIncident(w, r, http.StatusOK, snap, v, v.Incident, "")
}

func (s *Server) renderIncident(w http.ResponseWriter, r *http.Request, code int, snap core.Snapshot, v core.IncidentView, form core.Incident, msg string) {
	d := incidentData{V: v, Form: s.incidentFormFor(r, snap, form, false), Timeline: incidentTimeline(v)}
	d.Form.Error = msg
	d.Chain = incidentChain(snap, v)
	if v.FindingID != 0 {
		d.Finding, d.HasFind = snap.Finding(v.FindingID)
	}
	s.renderCode(w, r, code, "incident", "incidents", fmt.Sprintf("%s at %s", v.Ref, v.Site.Code), snap, d)
}

// incidentChain is every incident linked to v by "repeat of", oldest
// first. Links always point to an earlier incident, so the walk ends.
func incidentChain(snap core.Snapshot, v core.IncidentView) []core.IncidentView {
	root := v
	for root.RepeatOf != 0 {
		o, ok := snap.Incident(root.RepeatOf)
		if !ok {
			break
		}
		root = o
	}
	chain := []core.IncidentView{root}
	for i := 0; i < len(chain); i++ {
		for _, id := range chain[i].Repeats {
			if o, ok := snap.Incident(id); ok {
				chain = append(chain, o)
			}
		}
	}
	sort.SliceStable(chain, func(a, b int) bool {
		if !chain[a].At.Equal(chain[b].At) {
			return chain[a].At.Before(chain[b].At)
		}
		return chain[a].ID < chain[b].ID
	})
	return chain
}

// readIncident copies the form into in, and refuses times in the future.
func (s *Server) readIncident(r *http.Request, in *core.Incident) error {
	num := func(name string) (int, error) {
		v := r.FormValue(name)
		if v == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, errors.New("pick the ratings from the lists")
		}
		return n, nil
	}
	in.TaskID, in.Kind, in.Category = r.FormValue("task"), r.FormValue("kind"), r.FormValue("category")
	in.Title, in.Detail = r.FormValue("title"), r.FormValue("detail")
	in.Occurred, in.Detected, in.DetectedBy, in.Restored = r.FormValue("occurred"), r.FormValue("detected"), r.FormValue("detected_by"), r.FormValue("restored")
	in.Response, in.Owner, in.Due, in.Status = r.FormValue("response"), r.FormValue("owner"), r.FormValue("due"), r.FormValue("status")
	in.Cause, in.Action, in.Lessons, in.Treatment = r.FormValue("cause"), r.FormValue("action"), r.FormValue("lessons"), r.FormValue("treatment")
	in.ClosedOn = r.FormValue("closed_on")
	var err error
	for _, p := range []struct {
		name string
		to   *int
	}{{"impact", &in.Impact}, {"consequence", &in.Consequence}, {"likelihood", &in.Likelihood},
		{"residual_consequence", &in.ResidualConsequence}, {"residual_likelihood", &in.ResidualLikelihood},
		{"finding", &in.FindingID}, {"repeat_of", &in.RepeatOf}} {
		if *p.to, err = num(p.name); err != nil {
			return err
		}
	}
	// A near miss had no impact by definition; don't make people pick "none" twice.
	if in.Kind == core.NearMiss && r.FormValue("impact") == "" {
		in.Impact = 0
	}
	loc, now := s.Cfg.Location(), s.Now()
	// A minute's grace: the form's default time is the minute it was opened.
	limit := now.Add(time.Minute)
	for _, f := range []struct{ name, v string }{{"when it happened", in.Occurred}, {"when it was noticed", in.Detected}, {"when service was restored", in.Restored}} {
		if t, err := time.ParseInLocation(core.IncidentTime, f.v, loc); err == nil && t.After(limit) {
			return fmt.Errorf("%s can't be in the future", f.name)
		}
	}
	today := s.today()
	if in.Status == core.IncidentClosed {
		if in.ClosedOn == "" {
			in.ClosedOn = today.Format("2006-01-02")
		}
		if d, err := time.ParseInLocation("2006-01-02", in.ClosedOn, loc); err == nil && d.After(today) {
			return errors.New("the date it was closed can't be in the future")
		}
	}
	return nil
}

// errAccept is the refusal when someone other than an admin closes an
// incident whose risk stays high or extreme: closing it accepts the risk,
// and ISO 31000 asks for that to be an informed decision by someone with
// the authority to make it.
var errAccept = errors.New("closing it with the residual risk still high or extreme accepts that risk, so an admin has to close it; leave it at service restored and ask one")

func incidentLabel(cat core.Catalog, in core.Incident) string {
	code := in.SiteID
	if site, ok := cat.Site(in.SiteID); ok {
		code = site.Code
	}
	what := ""
	if t, ok := cat.Task(in.TaskID); ok && in.TaskID != "" {
		what = cat.SystemName(t.System) + ", "
	}
	kind := "incident"
	if in.Kind == core.NearMiss {
		kind = "near miss"
	}
	return fmt.Sprintf("%s %s at %s (%s%s risk): “%s”", kind, in.Ref, code, what, strings.ToLower(core.RiskOf(in.Likelihood, in.Consequence).Label()), in.Title)
}

func (s *Server) addIncident(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	in := core.Incident{SiteID: r.FormValue("site"), By: u.Name, Created: s.Now()}
	err := s.readIncident(r, &in)
	cat := s.Store.Catalog()
	if site, ok := cat.Site(in.SiteID); !ok || site.Archived {
		s.back(w, r, "/incidents", "Pick the site the incident is at.", "error")
		return
	}
	if in.Status == core.IncidentClosed {
		in.ClosedBy = u.Name
		if err == nil && core.RiskOf(in.ResidualLikelihood, in.ResidualConsequence).AtLeastHigh() && !u.can(config.RoleAdmin) {
			err = errAccept
		}
	}
	if err == nil {
		var saved core.Incident
		saved, err = s.Store.AddIncident(in, func(in core.Incident) string {
			if in.Status == core.IncidentClosed {
				return "Logged and closed " + incidentLabel(cat, in)
			}
			return "Logged " + incidentLabel(cat, in)
		})
		if err == nil {
			s.redirect(w, r, fmt.Sprintf("/incidents/%d", saved.ID), fmt.Sprintf("Logged %s.", saved.Ref), "")
			return
		}
	}
	snap := s.snapshot()
	d := incidentData{Form: s.incidentFormFor(r, snap, in, true)}
	d.Form.Error = "The incident wasn't logged: " + err.Error() + "."
	s.renderCode(w, r, http.StatusUnprocessableEntity, "incident", "incidents", "Log an incident", snap, d)
}

func (s *Server) editIncident(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	cat := s.Store.Catalog()
	var posted core.Incident
	saved, err := s.Store.UpdateIncident(incidentID(r), u.Name, r.FormValue("note"), func(old, in core.Incident) string {
		switch {
		case old.Status != core.IncidentClosed && in.Status == core.IncidentClosed:
			return "Closed " + incidentLabel(cat, in)
		case old.Status == core.IncidentClosed && in.Status != core.IncidentClosed:
			return "Reopened " + incidentLabel(cat, in)
		}
		return "Updated " + incidentLabel(cat, in)
	}, func(in *core.Incident) error {
		old := *in
		err := s.readIncident(r, in)
		posted = *in
		if err != nil {
			return err
		}
		if in.Status == core.IncidentClosed {
			if old.Status != core.IncidentClosed || in.ClosedBy == "" {
				in.ClosedBy = u.Name
			}
			residual := core.RiskOf(in.ResidualLikelihood, in.ResidualConsequence)
			changed := old.Status != core.IncidentClosed || old.ResidualLikelihood != in.ResidualLikelihood || old.ResidualConsequence != in.ResidualConsequence
			if residual.AtLeastHigh() && changed && !u.can(config.RoleAdmin) {
				return errAccept
			}
			if residual.AtLeastHigh() && changed {
				in.ClosedBy = u.Name // the admin who accepted the risk
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, store.ErrNoIncident):
		s.back(w, r, "/incidents", "That incident no longer exists.", "error")
	case errors.Is(err, store.ErrNoChange):
		s.redirect(w, r, fmt.Sprintf("/incidents/%d", saved.ID), "Nothing was changed.", "")
	case err != nil:
		snap := s.snapshot()
		v, ok := snap.Incident(incidentID(r))
		if !ok {
			s.back(w, r, "/incidents", "That incident no longer exists.", "error")
			return
		}
		posted.ID, posted.SiteID, posted.Ref = v.ID, v.SiteID, v.Ref
		s.renderIncident(w, r, http.StatusUnprocessableEntity, snap, v, posted, "Nothing was changed: "+err.Error()+".")
	default:
		s.redirect(w, r, fmt.Sprintf("/incidents/%d", saved.ID), fmt.Sprintf("Saved %s.", saved.Ref), "")
	}
}

// noteIncident adds an update to the timeline without changing anything else.
func (s *Server) noteIncident(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	cat := s.Store.Catalog()
	saved, err := s.Store.UpdateIncident(incidentID(r), u.Name, r.FormValue("note"), func(_, in core.Incident) string {
		return "Added an update to " + incidentLabel(cat, in)
	}, func(*core.Incident) error { return nil })
	target := fmt.Sprintf("/incidents/%d#timeline", incidentID(r))
	switch {
	case errors.Is(err, store.ErrNoIncident):
		s.back(w, r, "/incidents", "That incident no longer exists.", "error")
	case errors.Is(err, store.ErrNoChange):
		s.redirect(w, r, target, "Type the update first.", "error")
	case err != nil:
		s.redirect(w, r, target, "The update wasn't added: "+err.Error()+".", "error")
	default:
		s.redirect(w, r, target, fmt.Sprintf("Added an update to %s.", saved.Ref), "")
	}
}

func (s *Server) deleteIncident(w http.ResponseWriter, r *http.Request) {
	cat := s.Store.Catalog()
	in, err := s.Store.DeleteIncident(incidentID(r), userOf(r).Name, func(in core.Incident) string { return "Deleted " + incidentLabel(cat, in) })
	switch {
	case errors.Is(err, store.ErrNoIncident):
		s.back(w, r, "/incidents", "That incident no longer exists.", "error")
	case err != nil:
		s.fail(w, err)
	default:
		s.redirect(w, r, "/incidents?site="+url.QueryEscape(in.SiteID), fmt.Sprintf("Deleted %s.", in.Ref), "")
	}
}

func (s *Server) incidentsCSV(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	loc := s.Cfg.Location()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="incidents-%s.csv"`, snap.Today.Format("2006-01-02")))
	w.Write([]byte("\ufeff")) // so Excel opens Thai text as UTF-8
	cw := csv.NewWriter(w)
	cw.Write([]string{"ref", "site_code", "site_name", "region", "system", "vendor", "kind", "category", "title", "details",
		"occurred", "detected", "detected_by", "restored", "minutes_to_restore", "impact", "consequence", "likelihood", "risk_score", "risk_level",
		"response", "owner", "target_date", "status", "root_cause", "action", "lessons_learned", "treatment",
		"residual_likelihood", "residual_consequence", "residual_score", "residual_level", "closed_on", "closed_by", "days_to_close",
		"finding", "repeat_of", "timeline_entries", "logged_by", "logged_at"})
	num := func(n int) string {
		if n <= 0 {
			return ""
		}
		return strconv.Itoa(n)
	}
	for _, v := range snap.Incidents {
		system, vendor := "", ""
		if v.HasTask {
			system, vendor = snap.Catalog.SystemName(v.Task.System), snap.Catalog.VendorName(v.Task.Vendor)
		}
		restore, days, repeat, residual := "", "", "", ""
		if m := v.RestoreMinutes(); m >= 0 {
			restore = strconv.Itoa(m)
		}
		if !v.IsOpen() {
			days = strconv.Itoa(v.Age)
		}
		if o, ok := snap.Incident(v.RepeatOf); ok {
			repeat = o.Ref
		}
		if v.Residual.Rated() {
			residual = v.Residual.Label()
		}
		cw.Write([]string{v.Ref, csvCell(v.Site.Code), csvCell(v.Site.Name), csvCell(v.Site.Region), csvCell(system), csvCell(vendor),
			v.KindLabel(), v.CategoryLabel(), csvCell(v.Title), csvCell(v.Detail),
			spaceT(v.Occurred), spaceT(v.Detected), v.DetectedLabel(), spaceT(v.Restored), restore,
			strconv.Itoa(v.Impact), strconv.Itoa(v.Consequence), strconv.Itoa(v.Likelihood), strconv.Itoa(v.Risk.Score), v.Risk.Label(),
			csvCell(v.Response), csvCell(v.Owner), v.Due, choiceText(core.IncidentStatuses, v.Status), csvCell(v.Cause), csvCell(v.Action), csvCell(v.Lessons),
			choiceText(core.IncidentTreatments, v.Treatment), num(v.ResidualLikelihood), num(v.ResidualConsequence), num(v.Residual.Score), residual,
			v.ClosedOn, csvCell(v.ClosedBy), days, num(v.FindingID), repeat, strconv.Itoa(len(v.Notes)),
			csvCell(v.By), v.Created.In(loc).Format("2006-01-02 15:04")})
	}
	cw.Flush()
}

func spaceT(s string) string { return strings.Replace(s, "T", " ", 1) }

func choiceText(list []core.Choice, key string) string {
	for _, c := range list {
		if c.Key == key {
			return c.Label
		}
	}
	return key
}

// minutesText says how long something took: "35 min", "3h 05m", "2d 4h".
func minutesText(m int) string {
	switch {
	case m < 0:
		return "–"
	case m < 60:
		return fmt.Sprintf("%d min", m)
	case m < 48*60:
		return fmt.Sprintf("%dh %02dm", m/60, m%60)
	}
	return fmt.Sprintf("%dd %dh", m/(24*60), m%(24*60)/60)
}
