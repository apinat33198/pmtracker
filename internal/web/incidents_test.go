package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/apinat33198/pmtracker/internal/config"
	"github.com/apinat33198/pmtracker/internal/core"
	"github.com/apinat33198/pmtracker/internal/demo"
)

// incidentValues is a filled-in incident form for BKK1. The test clock is
// 3 Nov 2026, 09:00.
func incidentValues(mod func(url.Values)) url.Values {
	v := url.Values{"site": {"bkk1"}, "kind": {"incident"}, "category": {"power"}, "title": {"UPS 2 on bypass after a module fault"},
		"occurred": {"2026-11-02T16:30"}, "detected_by": {"alarm"}, "impact": {"2"}, "consequence": {"5"}, "likelihood": {"4"},
		"status": {"contained"}, "owner": {"ABC Power"}, "due": {"2026-11-02"}, "response": {"Spare module ordered"}}
	if mod != nil {
		mod(v)
	}
	return v
}

func closeValues(v url.Values, rl, rc string) {
	v.Set("status", "closed")
	v.Set("restored", "2026-11-02T19:00")
	v.Set("cause", "Module capacitor failed")
	v.Set("action", "Module replaced; capacitors added to the PM checklist")
	v.Set("treatment", "reduce")
	v.Set("residual_likelihood", rl)
	v.Set("residual_consequence", rc)
}

func TestIncidentsFlow(t *testing.T) {
	e := demoServer(t, []config.User{
		{Username: "admin", Password: "pw", Role: config.RoleAdmin},
		{Username: "ed", Password: "pw", Role: config.RoleEditor},
		{Username: "v", Password: "pw", Role: config.RoleViewer},
	})
	before := matrix(t, e)
	snap := e.server.snapshot()
	ups := ""
	for _, tk := range snap.Catalog.TasksAt("bkk1") {
		if tk.System == "ups" {
			ups = tk.ID
		}
	}
	for _, p := range []string{"/incidents", "/incidents?status=all", "/incidents.csv", "/incidents/new"} {
		get(t, e, p)
	}
	if p := get(t, e, "/incidents/new"); !strings.Contains(p, "Pick a site") {
		t.Error("a new incident with no site starts by picking one")
	}
	if p := get(t, e, "/incidents/new?task="+ups); !strings.Contains(p, `<option value="`+ups+`" selected>`) || !strings.Contains(p, `value="2026-11-03T09:00"`) {
		t.Error("the form should be filled in from the task, with now as the time")
	}

	// Viewers can look but not log.
	if code, _, _ := req(t, "POST", e.srv.URL+"/incidents", "v", incidentValues(nil)); code != http.StatusForbidden {
		t.Errorf("viewer logging = %d", code)
	}
	code, _, msg := req(t, "POST", e.srv.URL+"/incidents", "ed", incidentValues(func(v url.Values) { v.Set("task", ups) }))
	if code != http.StatusSeeOther || msg != "Logged INC-2026-001." {
		t.Fatalf("log = %d %q", code, msg)
	}
	ins := e.store.Inputs().Incidents
	if len(ins) != 1 || ins[0].By != "ed" || ins[0].TaskID != ups || ins[0].Status != core.IncidentContain || ins[0].Ref != "INC-2026-001" {
		t.Fatalf("stored = %+v", ins)
	}
	id := strconv.Itoa(ins[0].ID)

	home := get(t, e, "/")
	if !strings.Contains(home, `Incidents <span class="count">1</span>`) {
		t.Error("the menu should count open incidents")
	}
	if got := matrix(t, e); got != before {
		t.Error("an incident must not change the PM grid")
	}
	list := get(t, e, "/incidents")
	if !strings.Contains(list, "UPS 2 on bypass after a module fault") || !strings.Contains(list, `risk-extreme">Extreme</span>`) ||
		!strings.Contains(list, `href="/incidents?status=all&amp;l=4&amp;c=5">1</a>`) {
		t.Error("the incidents page should list it as extreme, and count it in the matrix")
	}
	if cell := get(t, e, "/incidents?status=all&l=4&c=5"); !strings.Contains(cell, "UPS 2 on bypass") {
		t.Error("the matrix cell should list it")
	}
	if cell := get(t, e, "/incidents?status=all&l=1&c=1"); strings.Contains(cell, "UPS 2 on bypass") {
		t.Error("another matrix cell shouldn't")
	}
	for _, p := range []string{"/sites/bkk1", "/tasks/" + ups, "/incidents/" + id} {
		if !strings.Contains(get(t, e, p), "UPS 2 on bypass after a module fault") {
			t.Errorf("%s should show the incident", p)
		}
	}
	if d := get(t, e, "/digest.txt"); !strings.Contains(d, "Incidents needing attention (1)\n• BKK1 INC-2026-001 UPS 2 on bypass after a module fault (contained, extreme risk): action 1 day late (ABC Power)") {
		t.Errorf("digest:\n%s", d)
	}

	// Refused: times in the future, a system elsewhere, a near miss with impact.
	cnxTask := snap.Catalog.TasksAt("cnx1")[0].ID
	for _, c := range []struct {
		form url.Values
		want string
	}{
		{incidentValues(func(v url.Values) { v.Set("occurred", "2026-11-03T09:30") }), "be in the future"},
		{incidentValues(func(v url.Values) { v.Set("task", cnxTask) }), "systems, or none"},
		{incidentValues(func(v url.Values) { v.Set("kind", "near_miss") }), "a near miss had no impact"},
		{incidentValues(func(v url.Values) { v.Set("likelihood", "") }), "how likely it is to happen again"},
		{incidentValues(func(v url.Values) { v.Set("impact", "x") }), "pick the ratings"},
		{incidentValues(func(v url.Values) { v.Set("repeat_of", id); v.Set("site", "cnx1") }), "at another site"},
	} {
		code, body, _ := req(t, "POST", e.srv.URL+"/incidents", "ed", c.form)
		if code != http.StatusUnprocessableEntity || !strings.Contains(body, c.want) {
			t.Errorf("%v = %d, want %q", c.form, code, c.want)
		}
	}
	// A near miss needn't pick "none" for the impact.
	if _, _, msg := req(t, "POST", e.srv.URL+"/incidents", "ed", incidentValues(func(v url.Values) {
		v.Set("kind", "near_miss")
		v.Del("impact")
		v.Set("title", "Water by the PDU")
	})); msg != "Logged INC-2026-002." {
		t.Errorf("near miss = %q", msg)
	}

	// Closing needs the cause, action, treatment and residual rating; the
	// form comes back with what was typed.
	edit := incidentValues(func(v url.Values) { v.Set("task", ups); v.Set("status", "closed"); v.Set("cause", "Capacitor failed") })
	code, body, _ := req(t, "POST", e.srv.URL+"/incidents/"+id, "ed", edit)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "restored before closing") || !strings.Contains(body, "Capacitor failed") {
		t.Errorf("closing with nothing filled in = %d", code)
	}
	// An editor can't close it with the risk still high: that accepts the risk.
	edit = incidentValues(func(v url.Values) { v.Set("task", ups); closeValues(v, "3", "4") })
	code, body, _ = req(t, "POST", e.srv.URL+"/incidents/"+id, "ed", edit)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "an admin has to close it") {
		t.Errorf("editor accepting a high risk = %d", code)
	}
	if e.store.Inputs().Incidents[0].Status != core.IncidentContain {
		t.Error("a refused change must not be saved")
	}
	// With the risk brought down to medium, the editor can close it.
	edit = incidentValues(func(v url.Values) {
		v.Set("task", ups)
		closeValues(v, "2", "3")
		v.Set("note", "Tested on load for 2 hours")
	})
	if _, _, msg := req(t, "POST", e.srv.URL+"/incidents/"+id, "ed", edit); msg != "Saved INC-2026-001." {
		t.Fatalf("close = %q", msg)
	}
	in := e.store.Inputs().Incidents[0]
	if in.Status != core.IncidentClosed || in.ClosedOn != "2026-11-03" || in.ClosedBy != "ed" || len(in.Notes) != 1 || in.Notes[0].Text != "Tested on load for 2 hours" {
		t.Errorf("after closing: %+v", in)
	}
	// Raising the residual rating to high while closed is an admin's call too.
	edit = incidentValues(func(v url.Values) { v.Set("task", ups); closeValues(v, "3", "4") })
	if code, _, _ := req(t, "POST", e.srv.URL+"/incidents/"+id, "ed", edit); code != http.StatusUnprocessableEntity {
		t.Errorf("editor raising the residual risk of a closed incident = %d", code)
	}
	if _, _, msg := req(t, "POST", e.srv.URL+"/incidents/"+id, "admin", edit); msg != "Saved INC-2026-001." {
		t.Errorf("admin accepting = %q", msg)
	}
	if in := e.store.Inputs().Incidents[0]; in.ClosedBy != "admin" {
		t.Errorf("the admin who accepted the risk should be recorded: %q", in.ClosedBy)
	}
	page := get(t, e, "/incidents/"+id)
	for _, w := range []string{"Residual risk", "accepted by admin", "Tested on load for 2 hours", "Status: Contained → Closed", "Residual risk: Medium (2 × 3 = 6) → High (3 × 4 = 12)"} {
		if !strings.Contains(page, w) {
			t.Errorf("the incident page lacks %q", w)
		}
	}

	// Saving the same thing again changes nothing; a note can be added on its own.
	if _, _, msg := req(t, "POST", e.srv.URL+"/incidents/"+id, "admin", edit); msg != "Nothing was changed." {
		t.Errorf("no-op save = %q", msg)
	}
	if _, _, msg := req(t, "POST", e.srv.URL+"/incidents/"+id+"/note", "ed", url.Values{"note": {"Reviewed at the monthly meeting"}}); msg != "Added an update to INC-2026-001." {
		t.Errorf("note = %q", msg)
	}
	if _, _, msg := req(t, "POST", e.srv.URL+"/incidents/"+id+"/note", "ed", url.Values{"note": {" "}}); msg != "Type the update first." {
		t.Errorf("empty note = %q", msg)
	}
	if code, _, _ := req(t, "POST", e.srv.URL+"/incidents/"+id+"/note", "v", url.Values{"note": {"x"}}); code != http.StatusForbidden {
		t.Errorf("viewer note = %d", code)
	}
	if n := len(e.store.Inputs().Incidents[0].Notes); n != 3 {
		t.Errorf("%d timeline entries, want 3", n)
	}

	// It happened again.
	again := get(t, e, "/incidents/new?repeat="+id)
	if !strings.Contains(again, `<option value="`+id+`" selected>`) || !strings.Contains(again, "a repeat of INC-2026-001") {
		t.Error("the repeat form should be linked to the earlier incident")
	}

	// The site can't be deleted while it has incidents.
	if _, _, msg := req(t, "POST", e.srv.URL+"/admin/sites/bkk1/delete", "admin", url.Values{"confirm": {"BKK1"}}); !strings.Contains(msg, "Archive the site instead") {
		t.Errorf("deleting a site with incidents = %q", msg)
	}
	if _, ok := e.store.Catalog().Site("bkk1"); !ok {
		t.Fatal("the site was deleted")
	}

	// CSV: one row each, formulas defused.
	req(t, "POST", e.srv.URL+"/incidents", "ed", incidentValues(func(v url.Values) { v.Set("title", "=HYPERLINK(1)") }))
	csv := get(t, e, "/incidents.csv")
	if strings.Count(csv, "\n") != 4 || !strings.Contains(csv, "INC-2026-001") || !strings.Contains(csv, "'=HYPERLINK(1)") {
		t.Errorf("csv:\n%s", csv)
	}

	// Only admins delete.
	if code, _, _ := req(t, "POST", e.srv.URL+"/incidents/"+id+"/delete", "ed", url.Values{}); code != http.StatusForbidden {
		t.Errorf("editor delete = %d", code)
	}
	if msg := post(t, e, "/incidents/"+id+"/delete", url.Values{}); msg != "Deleted INC-2026-001." {
		t.Errorf("delete = %q", msg)
	}
	if code, _, _ := req(t, "GET", e.srv.URL+"/incidents/"+id, "admin", nil); code != 404 {
		t.Errorf("a deleted incident's page = %d", code)
	}
	if got := matrix(t, e); got != before {
		t.Error("the PM grid should be unchanged after all that")
	}

	var acts []string
	for _, a := range e.store.Audit(30) {
		acts = append(acts, a.By+": "+a.Action)
	}
	log := strings.Join(acts, "\n")
	for _, w := range []string{
		"ed: Logged incident INC-2026-001 at BKK1 (UPS, extreme risk): “UPS 2 on bypass after a module fault”",
		"ed: Logged near miss INC-2026-002",
		"ed: Closed incident INC-2026-001",
		"admin: Updated incident INC-2026-001",
		"ed: Added an update to incident INC-2026-001",
		"admin: Deleted incident INC-2026-001",
	} {
		if !strings.Contains(log, w) {
			t.Errorf("activity lacks %q:\n%s", w, log)
		}
	}
}

func TestFindingLinksToIncident(t *testing.T) {
	e := demoServer(t, nil)
	if msg := post(t, e, "/findings", findingValues(nil)); !strings.HasPrefix(msg, "Logged finding #") {
		t.Fatal(msg)
	}
	fid := strconv.Itoa(e.store.Inputs().Findings[0].ID)
	if p := get(t, e, "/findings/"+fid); !strings.Contains(p, `href="/incidents/new?finding=`+fid+`"`) {
		t.Error("a finding should offer to log an incident from it")
	}
	form := get(t, e, "/incidents/new?finding="+fid)
	if !strings.Contains(form, "Linked to finding #"+fid) || !strings.Contains(form, `<option value="`+fid+`" selected>`) {
		t.Error("the form should be linked to the finding")
	}
	if msg := post(t, e, "/incidents", incidentValues(func(v url.Values) { v.Set("finding", fid) })); msg != "Logged INC-2026-001." {
		t.Fatal(msg)
	}
	if p := get(t, e, "/findings/"+fid); !strings.Contains(p, "Incidents linked to it") || !strings.Contains(p, "INC-2026-001") {
		t.Error("the finding should list the incident")
	}
	// Deleting the finding keeps the incident, without the link.
	post(t, e, "/findings/"+fid+"/delete", url.Values{})
	if ins := e.store.Inputs().Incidents; len(ins) != 1 || ins[0].FindingID != 0 {
		t.Errorf("after deleting the finding: %+v", ins)
	}
}

func TestMinutesText(t *testing.T) {
	for m, want := range map[int]string{-1: "–", 0: "0 min", 47: "47 min", 79: "1h 19m", 2879: "47h 59m", 2880: "2d 0h", 3000: "2d 2h"} {
		if got := minutesText(m); got != want {
			t.Errorf("minutesText(%d) = %q, want %q", m, got, want)
		}
	}
}

// The demo's incidents are valid and every page shows them.
func TestDemoIncidents(t *testing.T) {
	e := demoServer(t, nil)
	logged := len(e.store.Audit(100))
	if err := demo.SeedFindings(e.store.Catalog(), e.server.Now(), e.server.Cfg.Location(), func(f core.Finding) (core.Finding, error) {
		return e.store.AddFinding(f, func(core.Finding) string { return "" })
	}); err != nil {
		t.Fatal(err)
	}
	err := demo.SeedIncidents(e.store.Catalog(), e.store.Inputs().Findings, e.server.Now(), e.server.Cfg.Location(), func(in core.Incident) (core.Incident, error) {
		return e.store.AddIncident(in, func(core.Incident) string { return "" })
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := e.server.snapshot()
	if len(snap.Incidents) != 9 || snap.OpenIncidents != 4 {
		t.Errorf("demo has %d incidents, %d open", len(snap.Incidents), snap.OpenIncidents)
	}
	paths := []string{"/incidents", "/incidents?status=all", "/incidents?status=closed&risk=high", "/incidents?kind=near_miss&status=all",
		"/incidents?category=power&status=all", "/incidents.csv", "/digest.txt"}
	cnx := 0
	for _, v := range snap.Incidents {
		paths = append(paths, "/incidents/"+strconv.Itoa(v.ID), "/incidents?site="+v.SiteID+"&status=all", "/sites/"+v.SiteID, "/incidents/new?repeat="+strconv.Itoa(v.ID))
		if v.HasTask {
			paths = append(paths, "/tasks/"+v.Task.ID)
		}
		if v.FindingID != 0 {
			cnx = v.FindingID
		}
	}
	for _, p := range paths {
		get(t, e, p)
	}
	// The CNX1 trip is linked to the finding that kept warning of it.
	if cnx == 0 || !strings.Contains(get(t, e, "/findings/"+strconv.Itoa(cnx)), "CRAC 1 tripped on high pressure") {
		t.Error("the CNX1 incident should be linked to its finding")
	}
	if len(e.store.Audit(100)) != logged {
		t.Error("seeding the demo shouldn't fill the activity log")
	}
}
