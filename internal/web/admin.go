package web

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apinat33198/pmtracker/internal/core"
	"github.com/apinat33198/pmtracker/internal/store"
)

// --- sites ---

type adminSitesData struct {
	Rows        []adminSiteRow
	Archived    []adminSiteRow
	Regions     []string
	TaskSystems []core.System
}

type adminSiteRow struct {
	Site    core.Site
	Tasks   int
	Summary core.SiteSummary
}

func (s *Server) adminSites(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	cat := snap.Catalog
	d := adminSitesData{Regions: cat.Regions(), TaskSystems: cat.Systems}
	sites := append([]core.Site(nil), cat.Sites...)
	core.SortSites(sites)
	for _, site := range sites {
		row := adminSiteRow{Site: site, Tasks: len(cat.TasksAt(site.ID))}
		if ss, ok := snap.Site(site.ID); ok {
			row.Summary = ss
		}
		if site.Archived {
			d.Archived = append(d.Archived, row)
		} else {
			d.Rows = append(d.Rows, row)
		}
	}
	s.render(w, r, "admin_sites", "admin", "Sites", snap, d)
}

func (s *Server) adminCreateSite(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	code, name := strings.TrimSpace(r.FormValue("code")), strings.TrimSpace(r.FormValue("name"))
	if code == "" || name == "" {
		s.redirect(w, r, "/admin/sites", "A new site needs a code and a name.", "error")
		return
	}
	u := userOf(r)
	now := s.Now()
	loc := s.Cfg.Location()
	var site core.Site
	type lastPM struct {
		system string
		date   time.Time
	}
	var lasts []lastPM
	err := s.Store.UpdateCatalog(u.Name, fmt.Sprintf("Added site %s %s", code, name), func(c *core.Catalog) error {
		if _, ok := c.SiteByCode(code); ok {
			return fmt.Errorf("Site code %s is already in use.", code)
		}
		site = core.Site{ID: core.UniqueSlug(code, func(id string) bool { _, ok := c.Site(id); return ok }),
			Code: code, Name: name, Region: strings.TrimSpace(r.FormValue("region")),
			Aliases: core.SplitList(r.FormValue("aliases")), Created: now}
		c.Sites = append(c.Sites, site)
		// One row per system on the form: pick a vendor (or name a new one) to create the task.
		for _, sys := range append([]core.System(nil), c.Systems...) {
			sel := r.FormValue("vendor_" + sys.Key)
			if sel == "" {
				continue
			}
			ven, _, err := resolveVendor(c, sel, r.FormValue("newvendor_"+sys.Key))
			if err != nil {
				return fmt.Errorf("%s: %v", sys.Name, err)
			}
			c.AddTask(core.Task{SiteID: site.ID, System: sys.Key, Vendor: ven, Created: now})
			if v := r.FormValue("last_" + sys.Key); v != "" {
				d, err := core.ParseDate(v, loc)
				if err != nil {
					return fmt.Errorf("Last PM date for %s: %v", sys.Name, err)
				}
				lasts = append(lasts, lastPM{sys.Key, d})
			}
		}
		return nil
	})
	if err != nil {
		s.redirect(w, r, "/admin/sites", err.Error(), "error")
		return
	}
	if err := s.recordLastPM(u.Name, site.ID, func(add func(system string, d time.Time)) {
		for _, l := range lasts {
			add(l.system, l.date)
		}
	}); err != nil {
		s.fail(w, err)
		return
	}
	s.redirect(w, r, "/admin/sites/"+site.ID, fmt.Sprintf("Added %s %s.", code, name), "")
}

// recordLastPM adds "last PM date" visits for a site's tasks by system.
func (s *Server) recordLastPM(by, siteID string, each func(add func(system string, d time.Time))) error {
	cat := s.Store.Catalog()
	var visits []core.ManualVisit
	each(func(system string, d time.Time) {
		for _, t := range cat.TasksAt(siteID) {
			if t.System == system {
				visits = append(visits, core.ManualVisit{Date: d.Format("2006-01-02"), Tasks: []string{t.ID},
					Note: "Last PM date set by admin", By: by, Created: s.Now()})
			}
		}
	})
	if len(visits) == 0 {
		return nil
	}
	return s.Store.AddManual(fmt.Sprintf("Set last PM dates at %s", cat.SiteLabel(siteID)), visits...)
}

type adminSiteData struct {
	Site    core.Site
	Tasks   []adminTaskRow
	Regions []string
	Vendors []siteVendor
}

// siteVendor is a vendor working at a site and the systems it services there.
type siteVendor struct {
	Vendor  core.Vendor
	Systems []string
}

func siteVendors(c core.Catalog, siteID string) []siteVendor {
	var out []siteVendor
	idx := map[string]int{}
	for _, t := range c.TasksAt(siteID) {
		i, ok := idx[t.Vendor]
		if !ok {
			v, _ := c.Vendor(t.Vendor)
			idx[t.Vendor], i = len(out), len(out)
			out = append(out, siteVendor{Vendor: v})
		}
		out[i].Systems = append(out[i].Systems, c.SystemName(t.System))
	}
	return out
}

type adminTaskRow struct {
	Task     core.Task
	Forecast core.Forecast
	Live     bool
}

func (s *Server) adminSite(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	site, ok := snap.Catalog.Site(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	d := adminSiteData{Site: site, Regions: snap.Catalog.Regions(), Vendors: siteVendors(*snap.Catalog, site.ID)}
	for _, t := range snap.Catalog.TasksAt(site.ID) {
		row := adminTaskRow{Task: t}
		row.Forecast, row.Live = snap.Forecast(t.ID)
		d.Tasks = append(d.Tasks, row)
	}
	s.render(w, r, "admin_site", "admin", "Edit "+site.Code, snap, d)
}

func (s *Server) adminUpdateSite(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	back := "/admin/sites/" + id
	cur, ok := s.Store.Catalog().Site(id)
	if !ok {
		s.redirect(w, r, "/admin/sites", "That site no longer exists.", "error")
		return
	}
	code, name := strings.TrimSpace(r.FormValue("code")), strings.TrimSpace(r.FormValue("name"))
	msg := fmt.Sprintf("Saved %s %s.", code, name)
	if code != cur.Code || name != cur.Name {
		msg = fmt.Sprintf("Renamed %s %s to %s %s.", cur.Code, cur.Name, code, name)
	}
	err := s.Store.UpdateCatalog(userOf(r).Name, strings.TrimSuffix(msg, "."), func(c *core.Catalog) error {
		for i := range c.Sites {
			st := &c.Sites[i]
			if st.ID != id {
				continue
			}
			aliases := core.SplitList(r.FormValue("aliases"))
			// Keep the old code as an alias so events titled the old way still match.
			if code != "" && !strings.EqualFold(code, st.Code) && !containsFold(aliases, st.Code) {
				aliases = append(aliases, st.Code)
			}
			st.Code, st.Name, st.Region = code, name, strings.TrimSpace(r.FormValue("region"))
			st.Aliases, st.Notes = aliases, strings.TrimSpace(r.FormValue("notes"))
			return nil
		}
		return errors.New("That site no longer exists.")
	})
	if err != nil {
		s.redirect(w, r, back, err.Error(), "error")
		return
	}
	if code != cur.Code {
		msg += fmt.Sprintf(" %s stays as an alias so older event titles still match.", cur.Code)
	}
	s.redirect(w, r, back, msg, "")
}

func (s *Server) adminArchiveSite(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cur, ok := s.Store.Catalog().Site(id)
	if !ok {
		s.redirect(w, r, "/admin/sites", "That site no longer exists.", "error")
		return
	}
	action, msg := "Archived "+cur.Code, fmt.Sprintf("Archived %s. It is hidden from the overview and gets no new visits; its history is kept.", cur.Code)
	if cur.Archived {
		action, msg = "Restored "+cur.Code, fmt.Sprintf("Restored %s.", cur.Code)
	}
	err := s.Store.UpdateCatalog(userOf(r).Name, action, func(c *core.Catalog) error {
		for i := range c.Sites {
			if c.Sites[i].ID == id {
				c.Sites[i].Archived = !cur.Archived
			}
		}
		return nil
	})
	if err != nil {
		s.redirect(w, r, "/admin/sites", err.Error(), "error")
		return
	}
	s.redirect(w, r, "/admin/sites/"+id, msg, "")
}

func (s *Server) adminDeleteSite(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cat := s.Store.Catalog()
	site, ok := cat.Site(id)
	if !ok {
		s.redirect(w, r, "/admin/sites", "That site no longer exists.", "error")
		return
	}
	if !strings.EqualFold(strings.TrimSpace(r.FormValue("confirm")), site.Code) {
		s.redirect(w, r, "/admin/sites/"+id, fmt.Sprintf("Type %s to confirm deleting the site.", site.Code), "error")
		return
	}
	n := len(cat.TasksAt(id))
	err := s.Store.UpdateCatalog(userOf(r).Name, fmt.Sprintf("Deleted site %s %s and its %d PM tasks", site.Code, site.Name, n), func(c *core.Catalog) error {
		var sites []core.Site
		for _, x := range c.Sites {
			if x.ID != id {
				sites = append(sites, x)
			}
		}
		var tasks []core.Task
		for _, t := range c.Tasks {
			if t.SiteID != id {
				tasks = append(tasks, t)
			}
		}
		for i := range c.Calendars {
			if c.Calendars[i].SiteID == id {
				c.Calendars[i].SiteID = ""
			}
		}
		c.Sites, c.Tasks = sites, tasks
		return nil
	})
	if err != nil {
		s.redirect(w, r, "/admin/sites/"+id, err.Error(), "error")
		return
	}
	s.redirect(w, r, "/admin/sites", fmt.Sprintf("Deleted %s %s and its %d PM %s.", site.Code, site.Name, n, plural(n, "task", "tasks")), "")
}

// --- tasks ---

// taskFromForm reads the task fields shared by the add and edit forms.
// newVendorOption is the value of the "add a new vendor" choice in vendor dropdowns.
const newVendorOption = "__new__"

// resolveVendor turns a vendor dropdown choice into a vendor key, creating
// the vendor when the "new vendor" option was chosen.
func resolveVendor(c *core.Catalog, sel, newName string) (key string, created bool, err error) {
	if sel == newVendorOption {
		v, created, err := c.EnsureVendor(newName)
		return v.Key, created, err
	}
	if _, ok := c.Vendor(sel); !ok {
		return "", false, errors.New("Choose a vendor from the list, or pick “Add a new vendor” and type its name.")
	}
	return sel, false, nil
}

// vendorChoice names the vendor picked on a form, for messages written before saving.
func vendorChoice(c core.Catalog, sel, newName string) string {
	if sel == newVendorOption {
		return strings.TrimSpace(newName)
	}
	return c.VendorName(sel)
}

func (s *Server) taskFromForm(r *http.Request, c *core.Catalog, t *core.Task) error {
	num := func(field string, lo, hi int) (int, error) {
		v := strings.TrimSpace(r.FormValue(field))
		if v == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			return 0, fmt.Errorf("%s must be a whole number from %d to %d.", strings.ReplaceAll(field, "_", " "), lo, hi)
		}
		return n, nil
	}
	var err error
	if t.IntervalMonths, err = num("interval_months", 1, 60); err != nil {
		return err
	}
	if t.WindowDays, err = num("window_days", 1, 120); err != nil {
		return err
	}
	if t.LeadDays, err = num("lead_days", 1, 180); err != nil {
		return err
	}
	if t.GraceDays, err = num("grace_days", 1, 120); err != nil {
		return err
	}
	if t.IntervalMonths == s.Cfg.Defaults.IntervalMonths {
		t.IntervalMonths = 0 // follow the default
	}
	t.AnchorMode = r.FormValue("anchor_mode")
	if t.AnchorMode == core.AnchorRolling {
		t.AnchorMode = ""
	}
	t.AnchorDate = ""
	if v := strings.TrimSpace(r.FormValue("anchor_date")); v != "" {
		d, err := core.ParseDate(v, s.Cfg.Location())
		if err != nil {
			return err
		}
		t.AnchorDate = d.Format("2006-01-02")
	}
	t.Notes = strings.TrimSpace(r.FormValue("notes"))
	if sys := r.FormValue("system"); sys != "" {
		t.System = sys
	}
	if sel := r.FormValue("vendor"); sel != "" {
		key, _, err := resolveVendor(c, sel, r.FormValue("new_vendor"))
		if err != nil {
			return err
		}
		t.Vendor = key
	}
	return nil
}

func (s *Server) adminAddTask(w http.ResponseWriter, r *http.Request) {
	siteID := r.PathValue("id")
	back := "/admin/sites/" + siteID
	u := userOf(r)
	var task core.Task
	cat := s.Store.Catalog()
	err := s.Store.UpdateCatalog(u.Name, fmt.Sprintf("Added a PM task at %s: %s by %s", cat.SiteLabel(siteID),
		cat.SystemName(r.FormValue("system")), vendorChoice(cat, r.FormValue("vendor"), r.FormValue("new_vendor"))), func(c *core.Catalog) error {
		if _, ok := c.Site(siteID); !ok {
			return errors.New("That site no longer exists.")
		}
		t := core.Task{SiteID: siteID, Created: s.Now()}
		if err := s.taskFromForm(r, c, &t); err != nil {
			return err
		}
		task = c.AddTask(t)
		return nil
	})
	if err != nil {
		s.redirect(w, r, back, err.Error(), "error")
		return
	}
	msg := fmt.Sprintf("Added %s.", s.Store.Catalog().TaskLabel(task.ID))
	if v := strings.TrimSpace(r.FormValue("last_pm")); v != "" {
		d, err := core.ParseDate(v, s.Cfg.Location())
		if err == nil && !d.After(core.Day(s.Now(), s.Cfg.Location())) {
			s.Store.AddManual(fmt.Sprintf("Set the last PM date of %s to %s", s.Store.Catalog().TaskLabel(task.ID), d.Format("2 Jan 2006")),
				core.ManualVisit{Date: d.Format("2006-01-02"), Tasks: []string{task.ID}, Note: "Last PM date set by admin", By: u.Name, Created: s.Now()})
			msg += fmt.Sprintf(" Last PM %s.", d.Format("2 Jan 2006"))
		} else {
			msg += " The last PM date wasn't a past date, so it was skipped."
		}
	}
	s.redirect(w, r, back, msg, "")
}

type adminTaskData struct {
	Task     core.Task
	Site     core.Site
	Forecast core.Forecast
	Live     bool
	Today    time.Time
}

func (s *Server) adminTask(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	t, ok := snap.Catalog.Task(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	site, _ := snap.Catalog.Site(t.SiteID)
	d := adminTaskData{Task: t, Site: site, Today: snap.Today}
	d.Forecast, d.Live = snap.Forecast(t.ID)
	s.render(w, r, "admin_task", "admin", "Edit "+snap.Catalog.TaskLabel(t.ID), snap, d)
}

func (s *Server) adminUpdateTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	back := "/admin/tasks/" + id
	label := s.Store.Catalog().TaskLabel(id)
	err := s.Store.UpdateCatalog(userOf(r).Name, "Edited the PM task "+label, func(c *core.Catalog) error {
		for i := range c.Tasks {
			if c.Tasks[i].ID == id {
				t := c.Tasks[i]
				if err := s.taskFromForm(r, c, &t); err != nil {
					return err
				}
				t.Paused = r.FormValue("paused") == "on"
				c.Tasks[i] = t
				return nil
			}
		}
		return errors.New("That task no longer exists.")
	})
	if err != nil {
		s.redirect(w, r, back, err.Error(), "error")
		return
	}
	s.redirect(w, r, back, "Saved "+s.Store.Catalog().TaskLabel(id)+".", "")
}

func (s *Server) adminDeleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cat := s.Store.Catalog()
	t, ok := cat.Task(id)
	if !ok {
		s.redirect(w, r, "/admin/sites", "That task no longer exists.", "error")
		return
	}
	label := cat.TaskLabel(id)
	if r.FormValue("confirm") != "on" {
		s.redirect(w, r, "/admin/tasks/"+id, "Tick the box to confirm deleting the task.", "error")
		return
	}
	err := s.Store.UpdateCatalog(userOf(r).Name, "Deleted the PM task "+label, func(c *core.Catalog) error {
		var out []core.Task
		for _, x := range c.Tasks {
			if x.ID != id {
				out = append(out, x)
			}
		}
		c.Tasks = out
		return nil
	})
	if err != nil {
		s.redirect(w, r, "/admin/tasks/"+id, err.Error(), "error")
		return
	}
	s.redirect(w, r, "/admin/sites/"+t.SiteID, "Deleted "+label+". Its calendar events stay in the mirror and can be linked again.", "")
}

// --- vendors and systems ---

type usage map[string]int

func taskUsage(c core.Catalog, key func(core.Task) string) usage {
	u := usage{}
	for _, t := range c.Tasks {
		u[key(t)]++
	}
	return u
}

type vendorRow struct {
	Vendor   core.Vendor
	Tasks    int
	Sites    []core.Site
	Services []string // "UPS at 10 sites"
	Counts   map[core.Status]int
}

func (s *Server) adminVendors(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	cat := snap.Catalog
	counts := map[string]map[core.Status]int{}
	for _, f := range snap.Forecasts {
		if counts[f.Task.Vendor] == nil {
			counts[f.Task.Vendor] = map[core.Status]int{}
		}
		counts[f.Task.Vendor][f.Status]++
	}
	var rows []vendorRow
	for _, v := range cat.Vendors {
		row := vendorRow{Vendor: v, Counts: counts[v.Key]}
		siteSeen := map[string]bool{}
		bySystem := map[string]int{}
		var order []string
		for _, t := range cat.Tasks {
			if t.Vendor != v.Key {
				continue
			}
			row.Tasks++
			if site, ok := cat.Site(t.SiteID); ok && !siteSeen[site.ID] {
				siteSeen[site.ID] = true
				row.Sites = append(row.Sites, site)
			}
			if bySystem[t.System] == 0 {
				order = append(order, t.System)
			}
			bySystem[t.System]++
		}
		core.SortSites(row.Sites)
		for _, k := range order {
			n := bySystem[k]
			row.Services = append(row.Services, fmt.Sprintf("%s at %d %s", cat.SystemName(k), n, plural(n, "site", "sites")))
		}
		rows = append(rows, row)
	}
	s.render(w, r, "admin_vendors", "admin", "Vendors", snap, rows)
}

type adminVendorData struct {
	Vendor core.Vendor
	Tasks  []adminTaskRow
	Others []core.Vendor
}

func (s *Server) adminVendor(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	v, ok := snap.Catalog.Vendor(r.PathValue("key"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	d := adminVendorData{Vendor: v}
	for _, t := range snap.Catalog.Tasks {
		if t.Vendor == v.Key {
			row := adminTaskRow{Task: t}
			row.Forecast, row.Live = snap.Forecast(t.ID)
			d.Tasks = append(d.Tasks, row)
		}
	}
	sortTaskRows(*snap.Catalog, d.Tasks)
	for _, o := range snap.Catalog.Vendors {
		if o.Key != v.Key {
			d.Others = append(d.Others, o)
		}
	}
	s.render(w, r, "admin_vendor", "admin", v.Name, snap, d)
}

func sortTaskRows(c core.Catalog, rows []adminTaskRow) {
	key := func(t core.Task) string {
		site, _ := c.Site(t.SiteID)
		return site.Region + "|" + strings.ToLower(site.Code) + "|" + c.SystemName(t.System)
	}
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && key(rows[j].Task) < key(rows[j-1].Task); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

// adminSaveVendor adds a vendor, or edits one when the form carries its key.
func (s *Server) adminSaveVendor(w http.ResponseWriter, r *http.Request) {
	key, name := r.FormValue("key"), strings.TrimSpace(r.FormValue("name"))
	fallback := "/admin/vendors"
	if key != "" {
		fallback = "/admin/vendors/" + key
	}
	if name == "" {
		s.back(w, r, fallback, "A vendor needs a name.", "error")
		return
	}
	v := core.Vendor{Name: name, Aliases: core.SplitList(r.FormValue("aliases")),
		Contact: strings.TrimSpace(r.FormValue("contact")), Phone: strings.TrimSpace(r.FormValue("phone")),
		Email: strings.TrimSpace(r.FormValue("email")), Notes: strings.TrimSpace(r.FormValue("notes"))}
	for _, d := range core.SplitList(r.FormValue("domains")) {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@"))
		if at := strings.LastIndex(d, "@"); at >= 0 {
			d = d[at+1:]
		}
		v.Domains = append(v.Domains, d)
	}
	// A vendor's own email address implies its domain.
	if at := strings.LastIndex(v.Email, "@"); at >= 0 && v.Email[at+1:] != "" {
		dom := strings.ToLower(v.Email[at+1:])
		if !containsFold(v.Domains, dom) && !freeMailDomains[dom] {
			v.Domains = append(v.Domains, dom)
		}
	}
	action := "Added vendor " + name
	if key != "" {
		action = "Edited vendor " + name
	}
	var saved core.Vendor
	err := s.Store.UpdateCatalog(userOf(r).Name, action, func(c *core.Catalog) error {
		for _, x := range c.Vendors {
			if x.Key != key && strings.EqualFold(x.Name, name) {
				return fmt.Errorf("There is already a vendor called %s.", name)
			}
		}
		if key == "" {
			v.Key = core.UniqueSlug(name, func(k string) bool { _, ok := c.Vendor(k); return ok })
			c.Vendors = append(c.Vendors, v)
			saved = v
			return nil
		}
		for i := range c.Vendors {
			if c.Vendors[i].Key == key {
				v.Key = key
				c.Vendors[i] = v
				saved = v
				return nil
			}
		}
		return errors.New("That vendor no longer exists.")
	})
	if err != nil {
		s.back(w, r, fallback, err.Error(), "error")
		return
	}
	msg := action + "."
	if key == "" {
		msg += " It's now in the vendor list on every site's forms."
	}
	s.back(w, r, "/admin/vendors/"+saved.Key, msg, "")
}

// freeMailDomains are never added as a vendor domain automatically: they
// would match unrelated invites.
var freeMailDomains = map[string]bool{"gmail.com": true, "hotmail.com": true, "outlook.com": true, "yahoo.com": true, "live.com": true, "icloud.com": true}

// adminReplaceVendor moves every task of one vendor to another (a contract
// change), keeping each task's visit history.
func (s *Server) adminReplaceVendor(w http.ResponseWriter, r *http.Request) {
	from := r.PathValue("key")
	back := "/admin/vendors/" + from
	cat := s.Store.Catalog()
	fromV, ok := cat.Vendor(from)
	if !ok {
		s.redirect(w, r, "/admin/vendors", "That vendor no longer exists.", "error")
		return
	}
	toName := vendorChoice(cat, r.FormValue("to"), r.FormValue("new_vendor"))
	if r.FormValue("to") == from {
		s.redirect(w, r, back, "Pick a different vendor to move the tasks to.", "error")
		return
	}
	deleteAfter := r.FormValue("delete_after") == "on"
	moved := 0
	action := fmt.Sprintf("Moved the PM tasks of %s to %s", fromV.Name, toName)
	if deleteAfter {
		action += fmt.Sprintf(" and deleted %s", fromV.Name)
	}
	var toKey string
	err := s.Store.UpdateCatalog(userOf(r).Name, action, func(c *core.Catalog) error {
		key, _, err := resolveVendor(c, r.FormValue("to"), r.FormValue("new_vendor"))
		if err != nil {
			return err
		}
		if key == from {
			return errors.New("Pick a different vendor to move the tasks to.")
		}
		toKey = key
		var clashes []string
		for _, t := range c.Tasks {
			if t.Vendor != from {
				continue
			}
			for _, o := range c.Tasks {
				if o.Vendor == key && o.SiteID == t.SiteID && o.System == t.System {
					clashes = append(clashes, c.TaskLabel(o.ID))
				}
			}
		}
		if len(clashes) > 0 {
			return fmt.Errorf("%s already has these tasks, so moving would duplicate them: %s. Delete or change those first.",
				c.VendorName(key), strings.Join(clashes, ", "))
		}
		for i := range c.Tasks {
			if c.Tasks[i].Vendor == from {
				c.Tasks[i].Vendor = key
				moved++
			}
		}
		if deleteAfter {
			var out []core.Vendor
			for _, v := range c.Vendors {
				if v.Key != from {
					out = append(out, v)
				}
			}
			c.Vendors = out
		}
		return nil
	})
	if err != nil {
		s.redirect(w, r, back, err.Error(), "error")
		return
	}
	msg := fmt.Sprintf("Moved %d PM %s from %s to %s. Their visit history stays with them.", moved, plural(moved, "task", "tasks"), fromV.Name, toName)
	if deleteAfter {
		msg += fmt.Sprintf(" %s was deleted.", fromV.Name)
		s.redirect(w, r, "/admin/vendors/"+toKey, msg, "")
		return
	}
	s.redirect(w, r, back, msg, "")
}

func (s *Server) adminDeleteVendor(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	cat := s.Store.Catalog()
	if n := taskUsage(cat, func(t core.Task) string { return t.Vendor })[key]; n > 0 {
		s.redirect(w, r, "/admin/vendors/"+key, fmt.Sprintf("%s still has %d PM %s. Move them to another vendor first.", cat.VendorName(key), n, plural(n, "task", "tasks")), "error")
		return
	}
	name := cat.VendorName(key)
	err := s.Store.UpdateCatalog(userOf(r).Name, "Deleted vendor "+name, func(c *core.Catalog) error {
		var out []core.Vendor
		for _, v := range c.Vendors {
			if v.Key != key {
				out = append(out, v)
			}
		}
		c.Vendors = out
		return nil
	})
	if err != nil {
		s.redirect(w, r, "/admin/vendors", err.Error(), "error")
		return
	}
	s.redirect(w, r, "/admin/vendors", "Deleted vendor "+name+".", "")
}

type adminSystemsData struct {
	Systems []core.System
	Used    usage
}

func (s *Server) adminSystems(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	s.render(w, r, "admin_systems", "admin", "Systems", snap,
		adminSystemsData{Systems: snap.Catalog.Systems, Used: taskUsage(*snap.Catalog, func(t core.Task) string { return t.System })})
}

func (s *Server) adminSaveSystem(w http.ResponseWriter, r *http.Request) {
	key, name := r.FormValue("key"), strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.redirect(w, r, "/admin/systems", "A system needs a name.", "error")
		return
	}
	sys := core.System{Name: name, Aliases: core.SplitList(r.FormValue("aliases"))}
	action := "Added system " + name
	if key != "" {
		action = "Edited system " + name
	}
	err := s.Store.UpdateCatalog(userOf(r).Name, action, func(c *core.Catalog) error {
		for _, x := range c.Systems {
			if x.Key != key && strings.EqualFold(x.Name, name) {
				return fmt.Errorf("There is already a system called %s.", name)
			}
		}
		if key == "" {
			sys.Key = core.UniqueSlug(name, func(k string) bool { _, ok := c.System(k); return ok })
			c.Systems = append(c.Systems, sys)
			return nil
		}
		for i := range c.Systems {
			if c.Systems[i].Key == key {
				sys.Key = key
				c.Systems[i] = sys
				return nil
			}
		}
		return errors.New("That system no longer exists.")
	})
	if err != nil {
		s.redirect(w, r, "/admin/systems", err.Error(), "error")
		return
	}
	s.redirect(w, r, "/admin/systems", action+".", "")
}

func (s *Server) adminDeleteSystem(w http.ResponseWriter, r *http.Request) {
	key := r.FormValue("key")
	cat := s.Store.Catalog()
	if n := taskUsage(cat, func(t core.Task) string { return t.System })[key]; n > 0 {
		s.redirect(w, r, "/admin/systems", fmt.Sprintf("%s is used by %d PM %s. Delete them first.", cat.SystemName(key), n, plural(n, "task", "tasks")), "error")
		return
	}
	name := cat.SystemName(key)
	err := s.Store.UpdateCatalog(userOf(r).Name, "Deleted system "+name, func(c *core.Catalog) error {
		var out []core.System
		for _, x := range c.Systems {
			if x.Key != key {
				out = append(out, x)
			}
		}
		c.Systems = out
		return nil
	})
	if err != nil {
		s.redirect(w, r, "/admin/systems", err.Error(), "error")
		return
	}
	s.redirect(w, r, "/admin/systems", "Deleted system "+name+".", "")
}

// --- calendars, rules, feed, digest ---

type adminCalendarsData struct {
	Rows           []calRow
	Rules          []core.LearnedRule
	FeedURL        string
	Digest         string
	ServiceAccount string
	Telegram       bool
}

type calRow struct {
	Cal   core.Calendar
	State store.CalendarState
}

func (s *Server) adminCalendars(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	d := adminCalendarsData{ServiceAccount: s.ServiceAccount, Rules: s.Store.Inputs().Rules,
		Telegram: s.Cfg.Digest.TelegramBotToken != "" && s.Cfg.Digest.TelegramChatID != ""}
	for _, c := range snap.Catalog.Calendars {
		d.Rows = append(d.Rows, calRow{Cal: c, State: s.Store.CalendarState(c.ID)})
	}
	if s.Cfg.Feed.Token != "" {
		d.FeedURL = baseURL(r) + "/forecast.ics?token=" + s.Cfg.Feed.Token
	}
	d.Digest = core.Digest(snap, baseURL(r))
	s.render(w, r, "admin_calendars", "admin", "Calendars and sync", snap, d)
}

func (s *Server) adminSaveCalendar(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.FormValue("id"))
	if id == "" {
		s.redirect(w, r, "/admin/calendars", "Enter the calendar ID from Google Calendar's Integrate calendar section.", "error")
		return
	}
	cal := core.Calendar{ID: id, Label: strings.TrimSpace(r.FormValue("label")), PMOnly: r.FormValue("pm_only") == "on", SiteID: r.FormValue("site")}
	if cal.Label == "" {
		cal.Label = id
	}
	editing := r.FormValue("existing") == "1"
	action := "Added calendar " + cal.Label
	if editing {
		action = "Edited calendar " + cal.Label
	}
	err := s.Store.UpdateCatalog(userOf(r).Name, action, func(c *core.Catalog) error {
		for i := range c.Calendars {
			if c.Calendars[i].ID == id {
				if !editing {
					return fmt.Errorf("Calendar %s is already tracked.", id)
				}
				c.Calendars[i] = cal
				return nil
			}
		}
		c.Calendars = append(c.Calendars, cal)
		return nil
	})
	if err != nil {
		s.redirect(w, r, "/admin/calendars", err.Error(), "error")
		return
	}
	msg := action + "."
	if !editing {
		msg += " It will be read on the next sync."
	}
	s.redirect(w, r, "/admin/calendars", msg, "")
}

func (s *Server) adminDeleteCalendar(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	cat := s.Store.Catalog()
	cal, _ := cat.Calendar(id)
	err := s.Store.UpdateCatalog(userOf(r).Name, "Stopped tracking calendar "+cal.Label, func(c *core.Catalog) error {
		var out []core.Calendar
		for _, x := range c.Calendars {
			if x.ID != id {
				out = append(out, x)
			}
		}
		c.Calendars = out
		return nil
	})
	if err != nil {
		s.redirect(w, r, "/admin/calendars", err.Error(), "error")
		return
	}
	s.redirect(w, r, "/admin/calendars", "Stopped tracking "+cal.Label+". Its mirrored events were removed; visits typed in by hand are kept.", "")
}

func (s *Server) adminDeleteRule(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.FormValue("id"))
	if err := s.Store.DeleteRule(id, userOf(r).Name); err != nil {
		s.fail(w, err)
		return
	}
	s.redirect(w, r, "/admin/calendars", fmt.Sprintf("Rule #%d deleted. Its events are matched by the other rules again.", id), "")
}

// --- import ---

type adminImportData struct {
	Text    string
	Result  *core.ImportResult
	Applied bool
	Error   string
	Columns []string
}

func (s *Server) adminImport(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "admin_import", "admin", "Import", s.snapshot(), adminImportData{Columns: core.ImportColumns})
}

func (s *Server) adminImportTemplate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="pm-import-template.csv"`)
	w.Write([]byte(core.ImportTemplate))
}

func (s *Server) adminImportPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(4 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		s.redirect(w, r, "/admin/import", "The upload could not be read: "+err.Error(), "error")
		return
	}
	text := r.FormValue("csv")
	if f, _, err := r.FormFile("file"); err == nil {
		b, _ := io.ReadAll(io.LimitReader(f, 4<<20))
		f.Close()
		if len(bytes.TrimSpace(b)) > 0 {
			text = string(b)
		}
	}
	d := adminImportData{Text: text, Columns: core.ImportColumns}
	if strings.TrimSpace(text) == "" {
		d.Error = "Paste CSV rows or choose a file first."
		s.render(w, r, "admin_import", "admin", "Import", s.snapshot(), d)
		return
	}
	snap := s.snapshot()
	lastPM := func(id string) time.Time {
		if f, ok := snap.Forecast(id); ok && f.LastDone != nil {
			return f.LastDone.Date
		}
		return time.Time{}
	}
	u := userOf(r)
	apply := r.FormValue("mode") == "apply"
	cat := s.Store.Catalog()
	res, visits, err := core.ApplyImport(&cat, strings.NewReader(text), lastPM, s.Now(), s.Cfg.Location(), u.Name)
	d.Result = &res
	if err != nil {
		d.Error = err.Error()
	}
	if apply && err == nil {
		summary := fmt.Sprintf("Imported CSV: %d sites, %d vendors, %d systems, %d tasks added, %d tasks updated, %d last PM dates",
			res.SitesCreated, res.VendorsCreated, res.SystemsCreated, res.TasksCreated, res.TasksUpdated, res.VisitsAdded)
		err = s.Store.UpdateCatalog(u.Name, summary, func(c *core.Catalog) error {
			*c = cat
			return nil
		})
		if err == nil {
			err = s.Store.AddManual("", visits...)
		}
		if err != nil {
			d.Error = err.Error()
		} else {
			d.Applied = true
		}
	}
	s.render(w, r, "admin_import", "admin", "Import", s.snapshot(), d)
}

// --- activity ---

func (s *Server) adminActivity(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "admin_activity", "admin", "Activity", s.snapshot(), s.Store.Audit(300))
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
