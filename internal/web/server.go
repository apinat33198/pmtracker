// Package web serves the tracker's pages, actions, admin area and feeds.
package web

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/csv"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/apinat33198/pmtracker/internal/config"
	"github.com/apinat33198/pmtracker/internal/core"
	"github.com/apinat33198/pmtracker/internal/gcal"
	"github.com/apinat33198/pmtracker/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	Cfg            *config.Config
	Store          *store.Store
	Syncer         *gcal.Syncer
	Now            func() time.Time
	Log            *log.Logger
	Demo           bool
	ServiceAccount string

	pages map[string]*template.Template
}

type ctxKey struct{}

type user struct {
	Name string
	Role string
}

func (u user) can(role string) bool { return config.RoleRank(u.Role) >= config.RoleRank(role) }

func userOf(r *http.Request) user {
	if u, ok := r.Context().Value(ctxKey{}).(user); ok {
		return u
	}
	return user{Name: "anonymous", Role: config.RoleViewer}
}

func (s *Server) Handler() (http.Handler, error) {
	if err := s.parse(); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /forecast.ics", s.feed)

	v, e, a := config.RoleViewer, config.RoleEditor, config.RoleAdmin
	handle := func(pattern, role string, h http.HandlerFunc) { mux.Handle(pattern, s.need(role, h)) }

	handle("GET /{$}", v, s.overview)
	handle("GET /sites/{id}", v, s.sitePage)
	handle("GET /tasks/{id}", v, s.taskPage)
	handle("GET /inbox", v, s.inbox)
	handle("GET /events", v, s.events)
	handle("GET /export.csv", v, s.exportCSV)
	handle("GET /digest.txt", v, s.digestText)

	handle("POST /sync", e, s.syncNow)
	handle("POST /inbox/link", e, s.link)
	handle("POST /inbox/ignore", e, s.ignore)
	handle("POST /events/undo", e, s.undo)
	handle("POST /visits/mark", e, s.mark)
	handle("POST /visits/unmark", e, s.unmark)
	handle("POST /visits/manual", e, s.addManual)
	handle("POST /visits/manual/delete", e, s.deleteManual)

	handle("GET /admin", a, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/admin/sites", http.StatusSeeOther) })
	handle("GET /admin/sites", a, s.adminSites)
	handle("POST /admin/sites", a, s.adminCreateSite)
	handle("GET /admin/sites/{id}", a, s.adminSite)
	handle("POST /admin/sites/{id}", a, s.adminUpdateSite)
	handle("POST /admin/sites/{id}/archive", a, s.adminArchiveSite)
	handle("POST /admin/sites/{id}/delete", a, s.adminDeleteSite)
	handle("POST /admin/sites/{id}/tasks", a, s.adminAddTask)
	handle("GET /admin/tasks/{id}", a, s.adminTask)
	handle("POST /admin/tasks/{id}", a, s.adminUpdateTask)
	handle("POST /admin/tasks/{id}/delete", a, s.adminDeleteTask)
	handle("GET /admin/vendors", a, s.adminVendors)
	handle("POST /admin/vendors", a, s.adminSaveVendor)
	handle("POST /admin/vendors/delete", a, s.adminDeleteVendor)
	handle("GET /admin/vendors/{key}", a, s.adminVendor)
	handle("POST /admin/vendors/{key}/replace", a, s.adminReplaceVendor)
	handle("GET /admin/systems", a, s.adminSystems)
	handle("POST /admin/systems", a, s.adminSaveSystem)
	handle("POST /admin/systems/delete", a, s.adminDeleteSystem)
	handle("GET /admin/calendars", a, s.adminCalendars)
	handle("POST /admin/calendars", a, s.adminSaveCalendar)
	handle("POST /admin/calendars/delete", a, s.adminDeleteCalendar)
	handle("POST /admin/rules/delete", a, s.adminDeleteRule)
	handle("GET /admin/import", a, s.adminImport)
	handle("POST /admin/import", a, s.adminImportPost)
	handle("GET /admin/import/template.csv", a, s.adminImportTemplate)
	handle("GET /admin/activity", a, s.adminActivity)

	return s.authenticate(mux), nil
}

// authenticate resolves the user (basic auth when users are configured,
// otherwise everyone is an admin) and rejects cross-site form posts.
func (s *Server) authenticate(next http.Handler) http.Handler {
	users := s.Cfg.Auth.Users
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		open := r.URL.Path == "/forecast.ics" || r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/static/")
		u := user{Name: "anonymous", Role: config.RoleAdmin}
		if len(users) > 0 && !open {
			name, pass, ok := r.BasicAuth()
			found := false
			if ok {
				for _, cu := range users {
					if subtle.ConstantTimeCompare([]byte(name), []byte(cu.Username)) == 1 &&
						subtle.ConstantTimeCompare([]byte(pass), []byte(cu.Password)) == 1 {
						u, found = user{Name: cu.Username, Role: cu.Role}, true
					}
				}
			}
			if !found {
				w.Header().Set("WWW-Authenticate", `Basic realm="PM Tracker", charset="UTF-8"`)
				http.Error(w, "Sign in to use the PM tracker.", http.StatusUnauthorized)
				return
			}
		}
		if r.Method == http.MethodPost {
			if o := r.Header.Get("Origin"); o != "" {
				if ou, err := url.Parse(o); err != nil || ou.Host != r.Host {
					http.Error(w, "Cross-site form posts are not allowed.", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

func (s *Server) need(role string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := userOf(r); !u.can(role) {
			http.Error(w, fmt.Sprintf("Your account (%s, %s) can't do this. Ask an admin for %s access.", u.Name, u.Role, role), http.StatusForbidden)
			return
		}
		h(w, r)
	})
}

// --- templates ---

func (s *Server) parse() error {
	loc := s.Cfg.Location()
	funcs := template.FuncMap{
		"date":      func(t time.Time) string { return fmtDate(t, "2 Jan 2006") },
		"dateShort": func(t time.Time) string { return fmtDate(t, "Mon 2 Jan") },
		"dm":        func(t time.Time) string { return fmtDate(t, "2 Jan") },
		"dateTime":  func(t time.Time) string { return fmtDate(t.In(loc), "2 Jan 2006, 15:04") },
		"iso":       func(t time.Time) string { return fmtDate(t, "2006-01-02") },
		"ago":       func(t time.Time) string { return ago(t, s.Now()) },
		"rel":       relDays,
		"dueRel":    dueRel,
		"dueShort":  dueShort,
		"offset":    offsetText,
		"fromToday": func(t time.Time) string { return relDays(core.DaysBetween(core.Day(s.Now(), loc), t)) },
		"sinceDays": func(t time.Time) int { return core.DaysBetween(t, core.Day(s.Now(), loc)) },
		"plural":    plural,
		"join":      strings.Join,
		"has":       func(list []string, v string) bool { return slices.Contains(list, v) },
		"ribbon":    func(f core.Forecast) template.HTML { return forecastSVG(f, rowRibbon) },
		"bigRibbon": func(f core.Forecast) template.HTML { return forecastSVG(f, assetRibbon) },
		"axis":      axisSVG,
		"add":       func(a, b int) int { return a + b },
		"taskLabel": func(cat *core.Catalog, id string) string { return cat.TaskLabel(id) },
		"statuses":  func() []core.Status { return core.AllStatuses },
		"count":     func(m map[core.Status]int, s string) int { return m[core.Status(s)] },
		"newVendor": func() string { return newVendorOption },
		"tel": func(s string) string {
			return strings.Map(func(r rune) rune {
				if r == '+' || (r >= '0' && r <= '9') {
					return r
				}
				return -1
			}, s)
		},
		"calLabel": func(cat *core.Catalog, id string) string {
			if c, ok := cat.Calendar(id); ok {
				return c.Label
			}
			return id
		},
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				m[kv[i].(string)] = kv[i+1]
			}
			return m
		},
	}
	base, err := template.New("base").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/parts.html")
	if err != nil {
		return err
	}
	s.pages = map[string]*template.Template{}
	pages, _ := fs.Glob(templateFS, "templates/*.html")
	for _, p := range pages {
		name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/"), ".html")
		if name == "layout" || name == "parts" {
			continue
		}
		t, err := base.Clone()
		if err != nil {
			return err
		}
		if _, err := t.ParseFS(templateFS, p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		s.pages[name] = t
	}
	return nil
}

// page is the data every template gets.
type page struct {
	Title      string
	Nav        string
	Back       string
	Demo       bool
	Msg        string
	MsgKind    string
	User       user
	IsEditor   bool
	IsAdmin    bool
	OpenAccess bool
	Snap       core.Snapshot
	Cat        *core.Catalog
	Cfg        *config.Config
	LastSync   time.Time
	SyncErrors []string
	Data       any
}

func (s *Server) snapshot() core.Snapshot { return core.Build(s.Cfg, s.Store.Inputs(), s.Now()) }

func (s *Server) render(w http.ResponseWriter, r *http.Request, name, nav, title string, snap core.Snapshot, data any) {
	u := userOf(r)
	p := page{Title: title, Nav: nav, Demo: s.Demo, Snap: snap, Cat: snap.Catalog, Cfg: s.Cfg, Data: data, User: u,
		IsEditor: u.can(config.RoleEditor), IsAdmin: u.can(config.RoleAdmin), OpenAccess: len(s.Cfg.Auth.Users) == 0,
		Msg: r.URL.Query().Get("msg"), MsgKind: r.URL.Query().Get("kind")}
	q := r.URL.Query()
	q.Del("msg")
	q.Del("kind")
	p.Back = r.URL.Path
	if enc := q.Encode(); enc != "" {
		p.Back += "?" + enc
	}
	for _, c := range snap.Catalog.Calendars {
		st := s.Store.CalendarState(c.ID)
		if st.LastSyncedAt.After(p.LastSync) {
			p.LastSync = st.LastSyncedAt
		}
		if st.LastError != "" && st.LastErrorAt.After(st.LastSyncedAt) {
			p.SyncErrors = append(p.SyncErrors, fmt.Sprintf("%s: %s", c.Label, st.LastError))
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "missing template "+name, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
		s.Log.Printf("render %s: %v", name, err)
		http.Error(w, "This page failed to render: "+err.Error(), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}

// --- viewer pages ---

type overviewData struct {
	Region, Vendor, System, Status string
	Regions                        []string
	Sites                          []core.SiteSummary
	Systems                        []core.System
	Chips                          []chip
	Upcoming                       []core.Forecast
	Vendors                        []core.VendorSummary
	Total                          int
	Filtered                       bool
	Query                          string
}

type chip struct {
	Status core.Status
	Count  int
	Active bool
	Href   string
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	q := r.URL.Query()
	d := overviewData{Region: q.Get("region"), Vendor: q.Get("vendor"), System: q.Get("system"), Status: q.Get("status"),
		Regions: snap.Catalog.Regions()}
	d.Filtered = d.Region != "" || d.Vendor != "" || d.System != "" || d.Status != ""
	keep := func(f core.Forecast) bool {
		return (d.Region == "" || f.Site.Region == d.Region) && (d.Vendor == "" || f.Task.Vendor == d.Vendor) &&
			(d.System == "" || f.Task.System == d.System)
	}
	counts := map[core.Status]int{}
	for _, f := range snap.Forecasts {
		if keep(f) {
			counts[f.Status]++
			d.Total++
		}
	}
	base := url.Values{}
	for k, v := range map[string]string{"region": d.Region, "vendor": d.Vendor, "system": d.System} {
		if v != "" {
			base.Set(k, v)
		}
	}
	d.Query = base.Encode()
	for _, st := range core.AllStatuses {
		if counts[st] == 0 {
			continue
		}
		qq := url.Values{}
		for k, v := range base {
			qq[k] = v
		}
		if d.Status != string(st) {
			qq.Set("status", string(st))
		}
		href := "/"
		if e := qq.Encode(); e != "" {
			href += "?" + e
		}
		d.Chips = append(d.Chips, chip{Status: st, Count: counts[st], Active: d.Status == string(st), Href: href})
	}
	for _, sys := range snap.Systems {
		if d.System == "" || sys.Key == d.System {
			d.Systems = append(d.Systems, sys)
		}
	}
	for _, ss := range snap.Sites {
		if d.Region != "" && ss.Site.Region != d.Region {
			continue
		}
		cells := map[string][]core.Forecast{}
		match := 0
		for sys, fs := range ss.Cells {
			for _, f := range fs {
				if keep(f) {
					cells[sys] = append(cells[sys], f)
					if d.Status == "" || string(f.Status) == d.Status {
						match++
					}
				}
			}
		}
		if (d.Vendor != "" || d.System != "" || d.Status != "") && match == 0 {
			continue
		}
		ss.Cells = cells
		d.Sites = append(d.Sites, ss)
	}
	for _, f := range snap.Upcoming {
		if keep(f) && (d.Status == "" || string(f.Status) == d.Status) {
			d.Upcoming = append(d.Upcoming, f)
		}
	}
	d.Vendors = snap.Vendors
	s.render(w, r, "overview", "overview", "Overview", snap, d)
}

type siteData struct {
	S       core.SiteSummary
	Paused  []core.Task
	Visits  []core.Visit
	Vendors []siteVendor
	Today   time.Time
}

func (s *Server) sitePage(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	ss, ok := snap.Site(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	d := siteData{S: ss, Today: snap.Today, Vendors: siteVendors(*snap.Catalog, ss.Site.ID)}
	for _, t := range snap.Catalog.TasksAt(ss.Site.ID) {
		if t.Paused {
			d.Paused = append(d.Paused, t)
		}
	}
	ids := map[string]bool{}
	for _, f := range ss.Forecasts {
		ids[f.Task.ID] = true
	}
	for _, v := range snap.Visits {
		for _, t := range v.Tasks {
			if ids[t] {
				d.Visits = append(d.Visits, v)
				break
			}
		}
		if len(d.Visits) >= 40 {
			break
		}
	}
	s.render(w, r, "site", "overview", ss.Site.Code+" "+ss.Site.Name, snap, d)
}

type taskData struct {
	F      core.Forecast
	Vendor core.Vendor
	Today  time.Time
}

func (s *Server) taskPage(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	f, ok := snap.Forecast(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	v, _ := snap.Catalog.Vendor(f.Task.Vendor)
	s.render(w, r, "task", "overview", snap.Catalog.TaskLabel(f.Task.ID), snap, taskData{F: f, Vendor: v, Today: snap.Today})
}

func (s *Server) inbox(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	s.render(w, r, "inbox", "inbox", "Review inbox", snap, nil)
}

type eventsData struct {
	Filter string
	Events []core.EventView
	Counts map[string]int
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	d := eventsData{Filter: r.URL.Query().Get("show"), Counts: map[string]int{}}
	for _, ev := range snap.Events {
		c := ev.Match.Class.String()
		d.Counts[c]++
		if d.Filter == "" || d.Filter == c {
			d.Events = append(d.Events, ev)
		}
	}
	s.render(w, r, "events", "events", "Calendar events", snap, d)
}

func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="pm-status-%s.csv"`, snap.Today.Format("2006-01-02")))
	w.Write([]byte("\ufeff")) // so Excel opens Thai text as UTF-8
	cw := csv.NewWriter(w)
	cw.Write([]string{"site_code", "site_name", "region", "system", "vendor", "status", "last_pm_date", "next_due", "days_left", "booked_date", "interval_months", "anchor_mode", "anchor_date"})
	for _, ss := range snap.Sites {
		for _, f := range ss.Forecasts {
			row := []string{ss.Site.Code, ss.Site.Name, ss.Site.Region, snap.Catalog.SystemName(f.Task.System),
				snap.Catalog.VendorName(f.Task.Vendor), f.Status.Label(), "", "", "", "",
				strconv.Itoa(f.Policy.IntervalMonths), f.Policy.AnchorMode, f.Task.AnchorDate}
			if f.LastDone != nil {
				row[6] = f.LastDone.Date.Format("2006-01-02")
			}
			if f.HasDue {
				row[7], row[8] = f.NextDue.Format("2006-01-02"), strconv.Itoa(f.DaysLeft)
			}
			if f.Booked != nil {
				row[9] = f.Booked.Date.Format("2006-01-02")
			}
			cw.Write(row)
		}
	}
	cw.Flush()
}

func (s *Server) digestText(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	d := core.Digest(s.snapshot(), baseURL(r))
	if d == "" {
		d = "Nothing needs attention today."
	}
	w.Write([]byte(d + "\n"))
}

func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	tok := s.Cfg.Feed.Token
	if tok == "" || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(tok)) != 1 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Write([]byte(core.ForecastICS(s.Cfg, s.snapshot(), s.Now())))
}

// --- editor actions ---

func (s *Server) syncNow(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	var errs []string
	changes := 0
	for _, res := range s.Syncer.SyncAll(ctx) {
		if res.Err != nil {
			errs = append(errs, res.Err.Error())
		}
		changes += res.Changes
	}
	if len(errs) > 0 {
		s.back(w, r, "/", "Sync failed: "+strings.Join(errs, "; "), "error")
		return
	}
	s.back(w, r, "/", fmt.Sprintf("Synced. %d %s.", changes, plural(changes, "change", "changes")), "")
}

func (s *Server) link(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ev, ok := s.Store.Event(r.FormValue("event"))
	if !ok {
		s.back(w, r, "/inbox", "That event is no longer in the calendar mirror.", "error")
		return
	}
	cat := s.Store.Catalog()
	var tasks []string
	for _, id := range append(r.Form["tasks"], r.FormValue("other")) {
		if _, ok := cat.Task(id); ok && !slices.Contains(tasks, id) {
			tasks = append(tasks, id)
		}
	}
	if len(tasks) == 0 {
		s.back(w, r, "/inbox", "Pick at least one PM task to link the visit to.", "error")
		return
	}
	u := userOf(r)
	var labels []string
	for _, t := range tasks {
		labels = append(labels, cat.TaskLabel(t))
	}
	msg := fmt.Sprintf("Linked %q to %s.", ev.Summary, strings.Join(labels, ", "))
	if err := s.Store.Decide(core.Decision{EventKey: ev.Key(), Action: "link", Tasks: tasks, By: u.Name, At: s.Now()}, msg); err != nil {
		s.fail(w, err)
		return
	}
	kind, value := "", ""
	switch r.FormValue("remember") {
	case "series":
		kind, value = "series", ev.RecurringEventID
	case "title":
		kind, value = "title", core.NormalizeTitle(ev.Summary)
	}
	if kind != "" && value != "" {
		rule, err := s.Store.AddRule(core.LearnedRule{Kind: kind, Value: value, Label: ev.Summary, Tasks: tasks, By: u.Name, Created: s.Now()})
		if err != nil {
			s.fail(w, err)
			return
		}
		if kind == "series" {
			msg += fmt.Sprintf(" Rule #%d links the rest of the series.", rule.ID)
		} else {
			msg += fmt.Sprintf(" Rule #%d links every event with this title.", rule.ID)
		}
	}
	s.back(w, r, "/inbox", msg, "")
}

func (s *Server) ignore(w http.ResponseWriter, r *http.Request) {
	ev, ok := s.Store.Event(r.FormValue("event"))
	if !ok {
		s.back(w, r, "/inbox", "That event is no longer in the calendar mirror.", "error")
		return
	}
	u := userOf(r)
	msg := fmt.Sprintf("Marked %q as not a PM visit.", ev.Summary)
	if err := s.Store.Decide(core.Decision{EventKey: ev.Key(), Action: "ignore", By: u.Name, At: s.Now()}, msg); err != nil {
		s.fail(w, err)
		return
	}
	s.back(w, r, "/inbox", msg+" Undo it from the Events page.", "")
}

func (s *Server) undo(w http.ResponseWriter, r *http.Request) {
	ev, _ := s.Store.Event(r.FormValue("event"))
	if err := s.Store.Undecide(r.FormValue("event"), userOf(r).Name, fmt.Sprintf("Removed the decision on %q", ev.Summary)); err != nil {
		s.fail(w, err)
		return
	}
	s.back(w, r, "/events", "Decision removed. The event is matched by the rules again.", "")
}

func (s *Server) mark(w http.ResponseWriter, r *http.Request) {
	st := r.FormValue("status")
	if st != "done" && st != "missed" {
		http.Error(w, "status must be done or missed", http.StatusBadRequest)
		return
	}
	u := userOf(r)
	label := s.visitLabel(r.FormValue("visit"))
	action := "Confirmed " + label + " as done"
	msg := "Visit confirmed as done."
	if st == "missed" {
		action, msg = "Marked "+label+" as missed", "Visit marked as missed. It no longer counts towards the cycle."
	}
	if err := s.Store.Mark(core.VisitMark{VisitID: r.FormValue("visit"), Status: st, By: u.Name, At: s.Now()}, action); err != nil {
		s.fail(w, err)
		return
	}
	s.back(w, r, "/", msg, "")
}

func (s *Server) visitLabel(id string) string {
	snap := s.snapshot()
	for _, v := range snap.Visits {
		if v.ID == id {
			var names []string
			for _, t := range v.Tasks {
				names = append(names, snap.Catalog.TaskLabel(t))
			}
			return fmt.Sprintf("the %s visit on %s", strings.Join(names, ", "), v.Date.Format("2 Jan 2006"))
		}
	}
	return "a visit"
}

func (s *Server) unmark(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("visit")
	if err := s.Store.Unmark(id, userOf(r).Name, "Reset the status of "+s.visitLabel(id)); err != nil {
		s.fail(w, err)
		return
	}
	s.back(w, r, "/", "Visit status reset.", "")
}

// addManual records a past visit for one or more tasks.
func (s *Server) addManual(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	cat := s.Store.Catalog()
	var tasks []string
	for _, id := range r.Form["tasks"] {
		if _, ok := cat.Task(id); ok && !slices.Contains(tasks, id) {
			tasks = append(tasks, id)
		}
	}
	if len(tasks) == 0 {
		s.back(w, r, "/", "Pick the PM task the visit was for.", "error")
		return
	}
	d, err := core.ParseDate(r.FormValue("date"), s.Cfg.Location())
	if err != nil {
		s.back(w, r, "/", "Enter the visit date as a date.", "error")
		return
	}
	if d.After(core.Day(s.Now(), s.Cfg.Location())) {
		s.back(w, r, "/", "Only past visits can be added by hand. Future visits come from the calendar.", "error")
		return
	}
	u := userOf(r)
	var labels []string
	for _, t := range tasks {
		labels = append(labels, cat.TaskLabel(t))
	}
	action := fmt.Sprintf("Recorded a PM visit on %s for %s", d.Format("2 Jan 2006"), strings.Join(labels, ", "))
	if err := s.Store.AddManual(action, core.ManualVisit{Date: d.Format("2006-01-02"), Tasks: tasks,
		Note: strings.TrimSpace(r.FormValue("note")), By: u.Name, Created: s.Now()}); err != nil {
		s.fail(w, err)
		return
	}
	s.back(w, r, "/", fmt.Sprintf("Recorded the visit on %s.", d.Format("2 Jan 2006")), "")
}

func (s *Server) deleteManual(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.FormValue("id"))
	if err := s.Store.DeleteManual(id, userOf(r).Name, "Deleted "+s.visitLabel(fmt.Sprintf("man:%d", id))); err != nil {
		s.fail(w, err)
		return
	}
	s.back(w, r, "/", "Visit deleted.", "")
}

// --- helpers ---

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.Log.Printf("error: %v", err)
	http.Error(w, "Could not save the change: "+err.Error(), http.StatusInternalServerError)
}

// back redirects to the form's "back" path (same-site only) or a fallback.
func (s *Server) back(w http.ResponseWriter, r *http.Request, fallback, msg, kind string) {
	target := r.FormValue("back")
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.HasPrefix(target, "/\\") {
		target = fallback
	}
	s.redirect(w, r, target, msg, kind)
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, target, msg, kind string) {
	u, _ := url.Parse(target)
	q := u.Query()
	q.Del("msg")
	q.Del("kind")
	if msg != "" {
		q.Set("msg", msg)
	}
	if kind != "" {
		q.Set("kind", kind)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func fmtDate(t time.Time, layout string) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(layout)
}

func relDays(n int) string {
	switch {
	case n == 0:
		return "today"
	case n == 1:
		return "tomorrow"
	case n == -1:
		return "yesterday"
	case n > 0:
		return fmt.Sprintf("in %d days", n)
	default:
		return fmt.Sprintf("%d days ago", -n)
	}
}

func dueRel(n int) string {
	switch {
	case n == 0:
		return "due today"
	case n > 0:
		return relDays(n)
	case n == -1:
		return "1 day late"
	default:
		return fmt.Sprintf("%d days late", -n)
	}
}

// dueShort is dueRel for matrix cells.
func dueShort(n int) string {
	switch {
	case n == 0:
		return "today"
	case n > 0:
		return fmt.Sprintf("in %dd", n)
	default:
		return fmt.Sprintf("%dd late", -n)
	}
}

func offsetText(n int) string {
	switch {
	case n == 0:
		return "on the due date"
	case n == 1:
		return "1 day after due"
	case n == -1:
		return "1 day before due"
	case n > 0:
		return fmt.Sprintf("%d days after due", n)
	default:
		return fmt.Sprintf("%d days before due", -n)
	}
}

func ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
