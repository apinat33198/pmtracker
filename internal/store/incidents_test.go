package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apinat33198/pmtracker/internal/core"
)

func incidentStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { clock = clock.Add(time.Minute); return clock })
	if err := s.UpdateCatalog("t", "", func(c *core.Catalog) error {
		c.Sites = []core.Site{{ID: "bkk1", Code: "BKK1", Name: "Bang Na"}, {ID: "cnx1", Code: "CNX1", Name: "Chiang Mai"}}
		c.Systems = core.DefaultSystems()
		c.Vendors = []core.Vendor{{Key: "abc", Name: "ABC Power"}}
		c.AddTask(core.Task{SiteID: "bkk1", System: "ups", Vendor: "abc"}) // t1
		c.AddTask(core.Task{SiteID: "cnx1", System: "ups", Vendor: "abc"}) // t2
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return s, path
}

func anIncident(site, occurred, title string) core.Incident {
	return core.Incident{SiteID: site, Kind: core.IncidentKind, Category: "power", Title: title, Occurred: occurred,
		DetectedBy: "alarm", Impact: 2, Consequence: 5, Likelihood: 3, By: "nok", Created: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)}
}

func TestIncidents(t *testing.T) {
	s, path := incidentStore(t)
	label := func(in core.Incident) string { return in.Ref + " " + in.Title }

	if _, err := s.AddIncident(anIncident("nope", "2026-11-01T10:00", "x"), label); err == nil {
		t.Error("an unknown site must be refused")
	}
	a, err := s.AddIncident(anIncident("bkk1", "2026-11-01T10:00", "UPS on bypass"), label)
	if err != nil || a.ID == 0 || a.Ref != "INC-2026-001" || a.Status != core.IncidentOpen || len(a.Notes) != 0 {
		t.Fatalf("added %+v, %v", a, err)
	}
	b, _ := s.AddIncident(anIncident("bkk1", "2026-11-03T10:00", "UPS on bypass again"), label)
	old, _ := s.AddIncident(anIncident("cnx1", "2025-12-30T23:00", "Logged late"), label)
	if b.Ref != "INC-2026-002" || old.Ref != "INC-2025-001" {
		t.Errorf("refs %s %s: numbered within the year it happened", b.Ref, old.Ref)
	}

	// An update records what changed, and the note, as one timeline entry.
	up, err := s.UpdateIncident(a.ID, "somchai", "Spare module on its way", func(_, in core.Incident) string { return "Updated " + label(in) },
		func(in *core.Incident) error {
			in.Status, in.Owner, in.Likelihood = core.IncidentContain, "ABC Power", 4
			// fn can't rewrite history or move the incident.
			in.Notes = []core.IncidentNote{{At: time.Now(), By: "x", Text: "forged"}}
			in.Ref, in.SiteID, in.By = "INC-1999-001", "cnx1", "mallory"
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if up.Ref != a.Ref || up.SiteID != "bkk1" || up.By != "nok" || len(up.Notes) != 1 {
		t.Fatalf("after update: %+v", up)
	}
	n := up.Notes[0]
	if n.By != "somchai" || n.Text != "Spare module on its way" || !strings.Contains(strings.Join(n.Changes, "; "), "Status: Open → Contained") ||
		!strings.Contains(strings.Join(n.Changes, "; "), "Risk: High (3 × 5 = 15) → Extreme (4 × 5 = 20)") {
		t.Errorf("timeline entry = %+v", n)
	}
	// Nothing changed and no note: refused, nothing written.
	if _, err := s.UpdateIncident(a.ID, "somchai", "  ", func(_, in core.Incident) string { return "x" }, func(*core.Incident) error { return nil }); !errors.Is(err, ErrNoChange) {
		t.Errorf("empty update = %v", err)
	}
	// A note on its own.
	if up, err = s.UpdateIncident(a.ID, "nok", "On site 14:20", func(_, in core.Incident) string { return "Note on " + label(in) }, func(*core.Incident) error { return nil }); err != nil || len(up.Notes) != 2 || len(up.Notes[1].Changes) != 0 {
		t.Errorf("note = %+v, %v", up.Notes, err)
	}
	// A refused change saves nothing.
	if _, err := s.UpdateIncident(a.ID, "nok", "", func(_, in core.Incident) string { return "x" }, func(in *core.Incident) error {
		in.Status = core.IncidentClosed
		return nil
	}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("closing with nothing filled in = %v", err)
	}
	// A repeat must be later and at the same site.
	if _, err := s.UpdateIncident(a.ID, "nok", "", func(_, in core.Incident) string { return "x" }, func(in *core.Incident) error {
		in.RepeatOf = b.ID
		return nil
	}); err == nil || !strings.Contains(err.Error(), "happened after this one") {
		t.Errorf("repeat of a later one = %v", err)
	}
	if _, err := s.UpdateIncident(b.ID, "nok", "", func(_, in core.Incident) string { return "x" }, func(in *core.Incident) error {
		in.RepeatOf = a.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Saved to disk, timeline and all; the next number is past every one in use.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ins := s2.Inputs().Incidents
	if len(ins) != 3 || len(ins[0].Notes) != 2 || ins[1].RepeatOf != a.ID {
		t.Fatalf("reloaded = %+v", ins)
	}
	if f, _ := s2.AddFinding(core.Finding{SiteID: "bkk1", FoundOn: "2026-11-04", Kind: "5s", Severity: "low", Title: "x"}, func(core.Finding) string { return "" }); f.ID <= old.ID {
		t.Errorf("a finding got number %d, already used by an incident", f.ID)
	}

	// Deleting the first keeps its repeat, without the link.
	if _, err := s.DeleteIncident(a.ID, "admin", func(in core.Incident) string { return "Deleted " + label(in) }); err != nil {
		t.Fatal(err)
	}
	if ins := s.Inputs().Incidents; len(ins) != 2 || ins[0].RepeatOf != 0 {
		t.Errorf("after delete: %+v", ins)
	}
	if _, err := s.DeleteIncident(a.ID, "admin", func(core.Incident) string { return "" }); !errors.Is(err, ErrNoIncident) {
		t.Errorf("delete twice = %v", err)
	}
	var acts []string
	for _, e := range s.Audit(20) {
		acts = append(acts, e.Action)
	}
	for _, w := range []string{"INC-2026-001 UPS on bypass", "Updated INC-2026-001", "Note on INC-2026-001", "Deleted INC-2026-001"} {
		if !strings.Contains(strings.Join(acts, "\n"), w) {
			t.Errorf("activity lacks %q:\n%s", w, strings.Join(acts, "\n"))
		}
	}
}

func TestIncidentsFollowCatalogAndFindings(t *testing.T) {
	s, _ := incidentStore(t)
	f, _ := s.AddFinding(core.Finding{SiteID: "bkk1", FoundOn: "2026-10-01", Kind: "equipment", Severity: "high", Title: "Cells weak"}, func(core.Finding) string { return "" })
	in := anIncident("bkk1", "2026-11-01T10:00", "UPS on bypass")
	in.TaskID, in.FindingID = "t1", f.ID
	if _, err := s.AddIncident(in, func(core.Incident) string { return "" }); err != nil {
		t.Fatal(err)
	}
	other := anIncident("cnx1", "2026-11-01T10:00", "x")
	other.FindingID = f.ID
	if _, err := s.AddIncident(other, func(core.Incident) string { return "" }); err == nil || !strings.Contains(err.Error(), "another site") {
		t.Errorf("a finding at another site = %v", err)
	}
	// Deleting the finding drops the link; deleting the PM task keeps the incident at the site.
	s.DeleteFinding(f.ID, "admin", func(core.Finding) string { return "x" })
	s.UpdateCatalog("t", "x", func(c *core.Catalog) error { c.Tasks = c.Tasks[1:]; return nil })
	if ins := s.Inputs().Incidents; len(ins) != 1 || ins[0].FindingID != 0 || ins[0].TaskID != "" || ins[0].SiteID != "bkk1" {
		t.Errorf("after deleting the finding and task: %+v", ins)
	}
}

func TestRestoreChecksIncidents(t *testing.T) {
	s, _ := incidentStore(t)
	s.AddIncident(anIncident("bkk1", "2026-11-01T10:00", "UPS on bypass"), func(core.Incident) string { return "x" })
	b, _ := s.Export()
	var plain map[string]any
	json.Unmarshal(b, &plain)
	if plain["version"].(float64) != currentVersion {
		t.Errorf("exported version %v", plain["version"])
	}
	good := map[string]any{"id": 50, "ref": "INC-2026-050", "site_id": "bkk1", "kind": "incident", "category": "power", "title": "x",
		"occurred": "2026-11-02T10:00", "detected_by": "alarm", "impact": 1, "consequence": 3, "likelihood": 2, "status": "open"}
	craft := func(mod func(map[string]any)) []byte {
		var m map[string]any
		json.Unmarshal(b, &m)
		x := map[string]any{}
		for k, v := range good {
			x[k] = v
		}
		mod(x)
		m["incidents"] = append(m["incidents"].([]any), x)
		out, _ := json.Marshal(m)
		return out
	}
	for name, data := range map[string][]byte{
		"invalid":        craft(func(x map[string]any) { x["impact"] = 9 }),
		"duplicate":      craft(func(x map[string]any) { x["id"] = 1 }),
		"same reference": craft(func(x map[string]any) { x["ref"] = "INC-2026-001" }),
		"no reference":   craft(func(x map[string]any) { delete(x, "ref") }),
		"closed, no why": craft(func(x map[string]any) { x["status"], x["closed_on"] = "closed", "2026-11-03" }),
		"undated note":   craft(func(x map[string]any) { x["notes"] = []any{map[string]any{"by": "x", "text": "y"}} }),
		"unknown field":  craft(func(x map[string]any) { x["severity"] = "high" }),
	} {
		if err := s.Restore(data, "admin", "restore"); err == nil {
			t.Errorf("%s: restore accepted", name)
		}
	}
	if err := s.Restore(craft(func(x map[string]any) { x["repeat_of"], x["finding_id"] = 77, 88 }), "admin", "restore"); err != nil {
		t.Fatal(err)
	}
	if ins := s.Inputs().Incidents; len(ins) != 2 || ins[1].RepeatOf != 0 || ins[1].FindingID != 0 {
		t.Errorf("restored = %+v", ins)
	}
	// An older tracker reading version 3 says the backup is newer instead of
	// "not a backup"; this one refuses version 4 the same way.
	if err := s.Restore([]byte(fmt.Sprintf(`{"version": %d}`, currentVersion+1)), "admin", "restore"); err == nil || !strings.Contains(err.Error(), "newer version") {
		t.Errorf("newer backup = %v", err)
	}
}

func TestOldDataFileIsUpgraded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	os.WriteFile(path, []byte(`{"version": 2, "catalog": {}}`), 0o600)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Export()
	if !strings.Contains(string(b), `"version": 3`) {
		t.Errorf("export of an upgraded file: %.80s", b)
	}
}
