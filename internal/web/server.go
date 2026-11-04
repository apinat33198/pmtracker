// Package web serves the tracker's pages, actions, admin area and feeds.
package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/apinat33198/pmtracker/internal/auth"
	"github.com/apinat33198/pmtracker/internal/backup"
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
	Backups        *backup.Manager

	pages      map[string]*template.Template
	secret     []byte
	limiter    *auth.Limiter
	setupToken string
	setupMu    sync.Mutex
	dummyHash  string
	dummyOnce  sync.Once
}

func (s *Server) Handler() (http.Handler, error) {
	if err := s.parse(); err != nil {
		return nil, err
	}
	if err := s.initAuth(); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	files := http.StripPrefix("/static/", http.FileServerFS(static))
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pages link to /static/file?v=<content hash>, so a new release gets a
		// new address and browsers and Cloudflare never serve a stale copy.
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /forecast.ics", s.feed)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /setup", s.setupPage)
	mux.HandleFunc("POST /setup", s.setup)

	v, e, a := config.RoleViewer, config.RoleEditor, config.RoleAdmin
	handle := func(pattern, role string, h http.HandlerFunc) { mux.Handle(pattern, s.need(role, h)) }

	handle("GET /{$}", v, s.overview)
	handle("GET /sites/{id}", v, s.sitePage)
	handle("GET /tasks/{id}", v, s.taskPage)
	handle("GET /inbox", v, s.inbox)
	handle("GET /events", v, s.events)
	handle("GET /jobs", v, s.jobsPage)
	handle("GET /jobs.csv", v, s.jobsCSV)
	handle("GET /export.csv", v, s.exportCSV)
	handle("GET /digest.txt", v, s.digestText)
	handle("GET /account", v, s.accountPage)
	handle("POST /account/password", v, s.changeOwnPassword)
	handle("POST /account/signout-others", v, s.signOutOthers)

	handle("POST /sync", e, s.syncNow)
	handle("POST /inbox/link", e, s.link)
	handle("POST /inbox/ignore", e, s.ignore)
	handle("POST /events/undo", e, s.undo)
	handle("POST /visits/mark", e, s.mark)
	handle("POST /visits/unmark", e, s.unmark)
	handle("POST /visits/manual", e, s.addManual)
	handle("POST /visits/manual/delete", e, s.deleteManual)
	handle("POST /jobs", e, s.addJob)
	handle("POST /jobs/{id}", e, s.editJob)
	handle("POST /jobs/{id}/done", e, s.jobDone)
	handle("POST /jobs/{id}/reopen", e, s.jobReopen)
	handle("POST /jobs/{id}/delete", e, s.deleteJob)

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
	handle("GET /admin/activity.csv", a, s.adminActivityCSV)
	handle("GET /admin/backups", a, s.adminBackups)
	handle("POST /admin/backups", a, s.adminBackupNow)
	handle("GET /admin/backups/file/{name}", a, s.adminBackupFile)
	handle("POST /admin/backups/restore", a, s.adminRestoreBackup)
	handle("POST /admin/backups/upload", a, s.adminRestoreUpload)
	handle("GET /admin/users", a, s.adminUsers)
	handle("POST /admin/users", a, s.adminAddUser)
	handle("POST /admin/users/{name}/role", a, s.adminSetRole)
	handle("POST /admin/users/{name}/password", a, s.adminResetPassword)
	handle("POST /admin/users/{name}/delete", a, s.adminDeleteUser)

	return s.authenticate(mux), nil
}

// --- templates ---

func (s *Server) parse() error {
	loc := s.Cfg.Location()
	funcs := template.FuncMap{
		"date":       func(t time.Time) string { return fmtDate(t, "2 Jan 2006") },
		"dateShort":  func(t time.Time) string { return fmtDate(t, "Mon 2 Jan") },
		"dm":         func(t time.Time) string { return fmtDate(t, "2 Jan") },
		"dateTime":   func(t time.Time) string { return fmtDate(t.In(loc), "2 Jan 2006, 15:04") },
		"iso":        func(t time.Time) string { return fmtDate(t, "2006-01-02") },
		"ago":        func(t time.Time) string { return ago(t, s.Now()) },
		"rel":        relDays,
		"dueRel":     dueRel,
		"dueShort":   dueShort,
		"offset":     offsetText,
		"fromToday":  func(t time.Time) string { return relDays(core.DaysBetween(core.Day(s.Now(), loc), t)) },
		"sinceDays":  func(t time.Time) int { return core.DaysBetween(t, core.Day(s.Now(), loc)) },
		"plural":     plural,
		"join":       strings.Join,
		"has":        func(list []string, v string) bool { return slices.Contains(list, v) },
		"ribbon":     func(f core.Forecast) template.HTML { return forecastSVG(f, rowRibbon) },
		"bigRibbon":  func(f core.Forecast) template.HTML { return forecastSVG(f, assetRibbon) },
		"axis":       axisSVG,
		"add":        func(a, b int) int { return a + b },
		"taskLabel":  func(cat *core.Catalog, id string) string { return cat.TaskLabel(id) },
		"statuses":   func() []core.Status { return core.AllStatuses },
		"count":      func(m map[core.Status]int, s string) int { return m[core.Status(s)] },
		"newVendor":  func() string { return newVendorOption },
		"asset":      assetURL,
		"size":       sizeText,
		"backupKind": backupKind,
		"backupWhen": backupWhen,
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
	Bare       bool // sign-in and setup pages: no navigation
	Msg        string
	MsgKind    string
	User       user
	IsEditor   bool
	IsAdmin    bool
	Snap       core.Snapshot
	Cat        *core.Catalog
	Cfg        *config.Config
	LastSync   time.Time
	SyncErrors []string
	Data       any
}

func (s *Server) snapshot() core.Snapshot { return core.Build(s.Cfg, s.Store.Inputs(), s.Now()) }

func (s *Server) render(w http.ResponseWriter, r *http.Request, name, nav, title string, snap core.Snapshot, data any) {
	s.renderCode(w, r, http.StatusOK, name, nav, title, snap, data)
}

// renderCode renders a page with a status code. A snapshot without a
// catalog renders a bare page (sign-in, setup) with no navigation.
func (s *Server) renderCode(w http.ResponseWriter, r *http.Request, code int, name, nav, title string, snap core.Snapshot, data any) {
	u := userOf(r)
	p := page{Title: title, Nav: nav, Demo: s.Demo, Snap: snap, Cat: snap.Catalog, Cfg: s.Cfg, Data: data, User: u,
		IsEditor: u.can(config.RoleEditor), IsAdmin: u.can(config.RoleAdmin), Bare: snap.Catalog == nil}
	q := r.URL.Query()
	if msg, kind := q.Get("msg"), q.Get("kind"); msg != "" && hmac.Equal([]byte(q.Get("sig")), []byte(s.flashSig(msg, kind))) {
		p.Msg, p.MsgKind = msg, kind
	}
	q.Del("msg")
	q.Del("kind")
	q.Del("sig")
	p.Back = r.URL.Path
	if enc := q.Encode(); enc != "" {
		p.Back += "?" + enc
	}
	var cals []core.Calendar
	if snap.Catalog != nil {
		cals = snap.Catalog.Calendars
	}
	for _, c := range cals {
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
	w.WriteHeader(code)
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
	Jobs                           []core.JobView // open ad-hoc jobs (region filter only)
	MoreJobs                       int
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
	for _, j := range snap.OpenJobs {
		if d.Region == "" || j.Site.Region == d.Region {
			d.Jobs = append(d.Jobs, j)
		}
	}
	if len(d.Jobs) > overviewJobs {
		d.MoreJobs = len(d.Jobs) - overviewJobs
		d.Jobs = d.Jobs[:overviewJobs]
	}
	s.render(w, r, "overview", "overview", "Overview", snap, d)
}

// overviewJobs is how many open ad-hoc jobs the overview lists.
const overviewJobs = 8

type siteData struct {
	S        core.SiteSummary
	Paused   []core.Task
	Visits   []core.Visit
	Vendors  []siteVendor
	Today    time.Time
	OpenJobs []core.JobView
	DoneJobs []core.JobView
	MoreDone int
	Who      []string
}

// siteDoneJobs is how many done ad-hoc jobs a site page lists.
const siteDoneJobs = 10

func (s *Server) sitePage(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	ss, ok := snap.Site(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	d := siteData{S: ss, Today: snap.Today, Vendors: siteVendors(*snap.Catalog, ss.Site.ID), Who: whoOptions(snap)}
	d.OpenJobs, d.DoneJobs = snap.JobsAt(ss.Site.ID)
	if len(d.DoneJobs) > siteDoneJobs {
		d.MoreDone = len(d.DoneJobs) - siteDoneJobs
		d.DoneJobs = d.DoneJobs[:siteDoneJobs]
	}
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
			row := []string{csvCell(ss.Site.Code), csvCell(ss.Site.Name), csvCell(ss.Site.Region), csvCell(snap.Catalog.SystemName(f.Task.System)),
				csvCell(snap.Catalog.VendorName(f.Task.Vendor)), f.Status.Label(), "", "", "", "",
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
	d := core.Digest(s.snapshot(), s.baseURL(r))
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
	w.Header().Set("Cache-Control", "private, no-store")
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
	label, ok := s.visit(r.FormValue("visit"))
	if !ok {
		s.back(w, r, "/", "That visit is no longer on record.", "error")
		return
	}
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
	label, _ := s.visit(id)
	return label
}

// visit describes a visit on record, and reports whether there is one.
func (s *Server) visit(id string) (string, bool) {
	snap := s.snapshot()
	for _, v := range snap.Visits {
		if v.ID == id {
			var names []string
			for _, t := range v.Tasks {
				names = append(names, snap.Catalog.TaskLabel(t))
			}
			return fmt.Sprintf("the %s visit on %s", strings.Join(names, ", "), v.Date.Format("2 Jan 2006")), true
		}
	}
	return "a visit", false
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
	target, ok := localPath(r.FormValue("back"))
	if !ok {
		target = fallback
	}
	s.redirect(w, r, target, msg, kind)
}

// redirect goes to a path on this site with a flash message. The message is
// signed so a crafted link can't put words on the page.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, target, msg, kind string) {
	u, err := url.Parse(target)
	if err != nil {
		u = &url.URL{Path: "/"}
	}
	q := u.Query()
	q.Del("msg")
	q.Del("kind")
	q.Del("sig")
	if msg != "" {
		q.Set("msg", msg)
		if kind != "" {
			q.Set("kind", kind)
		}
		q.Set("sig", s.flashSig(msg, kind))
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func (s *Server) flashSig(msg, kind string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte("flash|" + kind + "|" + msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

// baseURL is the tracker's address for links: public_url when set,
// otherwise the request's host.
func (s *Server) baseURL(r *http.Request) string {
	if s.Cfg.PublicURL != "" {
		return strings.TrimRight(s.Cfg.PublicURL, "/")
	}
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// csvCell stops spreadsheet apps from running a cell as a formula.
func csvCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
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

// assetURL is a static file's address with a hash of its content, so it
// changes whenever the file does.
func assetURL(name string) string {
	b, err := fs.ReadFile(staticFS, "static/"+name)
	if err != nil {
		return "/static/" + name
	}
	sum := sha256.Sum256(b)
	return "/static/" + name + "?v=" + hex.EncodeToString(sum[:4])
}
