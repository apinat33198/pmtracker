package core

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// ImportColumns are the CSV headers the importer understands.
var ImportColumns = []string{"site_code", "site_name", "region", "system", "vendor", "interval_months", "last_pm_date", "anchor_mode", "anchor_date", "notes"}

// ImportTemplate is a ready-to-fill example.
const ImportTemplate = `site_code,site_name,region,system,vendor,interval_months,last_pm_date,anchor_mode,anchor_date,notes
BKK1,Bang Na,Bangkok,DC power,VoltEdge Power,3,2026-09-14,,,
BKK1,Bang Na,Bangkok,UPS,ABC Power,3,2026-08-30,,,
BKK1,Bang Na,Bangkok,CRAC,CoolAir Services,3,15/09/2569,,,Thai Buddhist year is fine
CNX1,Chiang Mai,North,UPS,Siam UPS Service,6,,fixed,2026-01-15,Contract slots every six months
`

type ImportLine struct {
	Line    int
	OK      bool
	Message string
}

type ImportResult struct {
	Lines                                        []ImportLine
	SitesCreated, SystemsCreated, VendorsCreated int
	TasksCreated, TasksUpdated, VisitsAdded      int
	Errors                                       int
}

// ApplyImport adds sites, systems, vendors and tasks from CSV to cat and
// returns the last-PM visits to record. lastPM reports the latest completed
// PM already on record for a task, so older dates are not added again.
func ApplyImport(cat *Catalog, r io.Reader, lastPM func(taskID string) time.Time, now time.Time, loc *time.Location, by string) (ImportResult, []ManualVisit, error) {
	var res ImportResult
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return res, nil, fmt.Errorf("read the header row: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
		col[strings.ReplaceAll(h, " ", "_")] = i
	}
	for _, need := range []string{"site_code", "system", "vendor"} {
		if _, ok := col[need]; !ok {
			return res, nil, fmt.Errorf("the header row needs a %q column (columns: %s)", need, strings.Join(ImportColumns, ", "))
		}
	}
	today := Day(now, loc)
	var visits []ManualVisit
	added := map[string]bool{}

	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			res.Lines = append(res.Lines, ImportLine{Line: line, Message: err.Error()})
			res.Errors++
			continue
		}
		get := func(name string) string {
			if i, ok := col[name]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		fail := func(f string, a ...any) {
			res.Lines = append(res.Lines, ImportLine{Line: line, Message: fmt.Sprintf(f, a...)})
			res.Errors++
		}
		code, sysName, venName := get("site_code"), get("system"), get("vendor")
		if code == "" && sysName == "" && venName == "" {
			continue // blank line
		}
		if code == "" || sysName == "" || venName == "" {
			fail("site_code, system and vendor are all required")
			continue
		}
		interval := 0
		if s := get("interval_months"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > 60 {
				fail("interval_months must be a whole number from 1 to 60, not %q", s)
				continue
			}
			interval = n
		}
		var last time.Time
		if s := get("last_pm_date"); s != "" {
			d, err := ParseDate(s, loc)
			if err != nil {
				fail("last_pm_date: %v", err)
				continue
			}
			if d.After(today) {
				fail("last_pm_date %s is in the future", d.Format("2 Jan 2006"))
				continue
			}
			last = d
		}
		mode := strings.ToLower(get("anchor_mode"))
		if mode != "" && mode != AnchorRolling && mode != AnchorFixed {
			fail("anchor_mode must be rolling or fixed, not %q", mode)
			continue
		}
		anchor := ""
		if s := get("anchor_date"); s != "" {
			d, err := ParseDate(s, loc)
			if err != nil {
				fail("anchor_date: %v", err)
				continue
			}
			anchor = d.Format("2006-01-02")
		}
		if mode == AnchorFixed && anchor == "" {
			fail("fixed mode needs an anchor_date (the first contract slot)")
			continue
		}

		var msgs []string
		site, ok := cat.SiteByCode(code)
		if !ok {
			name := get("site_name")
			if name == "" {
				name = code
			}
			site = Site{ID: UniqueSlug(code, func(id string) bool { _, ok := cat.Site(id); return ok }),
				Code: code, Name: name, Region: get("region"), Created: now}
			cat.Sites = append(cat.Sites, site)
			res.SitesCreated++
			msgs = append(msgs, "new site "+code)
		}
		sys, ok := cat.FindSystem(sysName)
		if !ok {
			sys = System{Key: UniqueSlug(sysName, func(k string) bool { _, ok := cat.System(k); return ok }), Name: sysName, Aliases: []string{sysName}}
			cat.Systems = append(cat.Systems, sys)
			res.SystemsCreated++
			msgs = append(msgs, "new system "+sysName)
		}
		ven, ok := cat.FindVendor(venName)
		if !ok {
			ven = Vendor{Key: UniqueSlug(venName, func(k string) bool { _, ok := cat.Vendor(k); return ok }), Name: venName}
			cat.Vendors = append(cat.Vendors, ven)
			res.VendorsCreated++
			msgs = append(msgs, "new vendor "+venName)
		}

		var task Task
		idx := -1
		for i, t := range cat.Tasks {
			if t.SiteID == site.ID && t.System == sys.Key && t.Vendor == ven.Key {
				idx, task = i, t
			}
		}
		if idx < 0 {
			task = cat.AddTask(Task{SiteID: site.ID, System: sys.Key, Vendor: ven.Key, IntervalMonths: interval,
				AnchorMode: mode, AnchorDate: anchor, Notes: get("notes"), Created: now})
			res.TasksCreated++
			msgs = append(msgs, "new task")
		} else {
			changed := false
			if interval > 0 && interval != task.IntervalMonths {
				cat.Tasks[idx].IntervalMonths, changed = interval, true
			}
			if mode != "" && mode != task.AnchorMode {
				cat.Tasks[idx].AnchorMode, changed = mode, true
			}
			if anchor != "" && anchor != task.AnchorDate {
				cat.Tasks[idx].AnchorDate, changed = anchor, true
			}
			if changed {
				res.TasksUpdated++
				msgs = append(msgs, "task updated")
			} else {
				msgs = append(msgs, "task unchanged")
			}
		}
		if !last.IsZero() {
			key := task.ID + "|" + last.Format("2006-01-02")
			prev := time.Time{}
			if lastPM != nil {
				prev = lastPM(task.ID)
			}
			if !added[key] && last.After(prev) {
				added[key] = true
				visits = append(visits, ManualVisit{Date: last.Format("2006-01-02"), Tasks: []string{task.ID},
					Note: "Imported last PM date", By: by, Created: now})
				res.VisitsAdded++
				msgs = append(msgs, "last PM "+last.Format("2 Jan 2006"))
			}
		}
		res.Lines = append(res.Lines, ImportLine{Line: line, OK: true,
			Message: fmt.Sprintf("%s %s by %s: %s", site.Code, sys.Name, ven.Name, strings.Join(msgs, ", "))})
	}
	if err := cat.Validate(); err != nil {
		return res, nil, err
	}
	return res, visits, nil
}

// ParseDate accepts 2026-09-14, 14/09/2026 and Thai Buddhist years (14/09/2569).
func ParseDate(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02", "2/1/2006", "2-1-2006", "2.1.2006"} {
		if d, err := time.ParseInLocation(layout, s, loc); err == nil {
			if d.Year() > 2400 { // Buddhist Era
				d = d.AddDate(-543, 0, 0)
			}
			return d, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a date; use 2026-09-14 or 14/09/2026", s)
}
