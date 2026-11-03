package core

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Built-in phrase keywords (case-insensitive, title or description).
// "PM" itself is handled by hasPMWord.
var defaultKeywords = []string{
	"preventive maintenance",
	"preventative maintenance",
	"บำรุงรักษา", // maintenance (Thai)
	"ซ่อมบำรุง",  // repair and maintenance (Thai)
}

var (
	pmPrefixRe = regexp.MustCompile(`^\s*\[PM\]\s*(.*)$`)
	// "[PM] BKK1 UPS – ABC Power": the vendor follows the last dash that has
	// spaces on both sides, so codes like "AC-01" stay whole.
	vendorSepRe = regexp.MustCompile(`\s+[–—-]\s+`)
	spaceRe     = regexp.MustCompile(`\s+`)
)

// Matcher classifies calendar events against the catalog.
type Matcher struct {
	cat      *Catalog
	rules    []LearnedRule
	keywords []string
	live     map[string]Task // tasks that can receive visits
}

func NewMatcher(cat *Catalog, rules []LearnedRule, extraKeywords []string) *Matcher {
	kw := append([]string{}, defaultKeywords...)
	for _, k := range extraKeywords {
		if k = strings.TrimSpace(strings.ToLower(k)); k != "" {
			kw = append(kw, k)
		}
	}
	m := &Matcher{cat: cat, rules: rules, keywords: kw, live: map[string]Task{}}
	for _, t := range cat.Tasks {
		if s, ok := cat.Site(t.SiteID); ok && !s.Archived && !t.Paused {
			m.live[t.ID] = t
		}
	}
	return m
}

// NormalizeTitle is the key for learned title rules.
func NormalizeTitle(s string) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(strings.ToLower(s), " "))
}

func (m *Matcher) liveTasks(ids []string) []string {
	var out []string
	for _, id := range ids {
		if _, ok := m.live[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// Match decides whether ev is a PM visit and which tasks it covers.
func (m *Matcher) Match(ev Event, d *Decision) Match {
	if d != nil {
		switch d.Action {
		case "ignore":
			return Match{Class: MatchNone, Method: "ignored", Reasons: []string{"Marked as not a PM visit in the review inbox"}}
		case "link":
			if ts := m.liveTasks(d.Tasks); len(ts) > 0 {
				return m.full(Match{IsPM: true, Method: "manual", Reasons: []string{"Linked by hand in the review inbox"}}, ts)
			}
		}
	}
	for _, rule := range m.rules {
		hit := (rule.Kind == "series" && ev.RecurringEventID != "" && rule.Value == ev.RecurringEventID) ||
			(rule.Kind == "title" && rule.Value == NormalizeTitle(ev.Summary))
		if !hit {
			continue
		}
		if ts := m.liveTasks(rule.Tasks); len(ts) > 0 {
			kind := "title"
			if rule.Kind == "series" {
				kind = "recurring-series"
			}
			return m.full(Match{IsPM: true, Method: rule.Kind, Reasons: []string{fmt.Sprintf("Remembered %s rule #%d", kind, rule.ID)}}, ts)
		}
	}

	var r Match
	reason := func(f string, a ...any) { r.Reasons = append(r.Reasons, fmt.Sprintf(f, a...)) }
	method := func(s string) {
		if r.Method == "" {
			r.Method = s
		}
	}

	if cal, ok := m.cat.Calendar(ev.CalendarID); ok {
		if cal.PMOnly {
			r.IsPM = true
			method("calendar")
			reason("On the PM-only calendar %q", cal.Label)
		}
		if cal.SiteID != "" {
			if s, ok := m.cat.Site(cal.SiteID); ok && !s.Archived {
				r.Site = s.ID
				reason("Calendar %q belongs to %s", cal.Label, s.Code)
			}
		}
	}

	if g := pmPrefixRe.FindStringSubmatch(ev.Summary); g != nil {
		r.IsPM = true
		method("convention")
		reason("Title follows the [PM] convention")
		if locs := vendorSepRe.FindAllStringIndex(g[1], -1); len(locs) > 0 {
			last := locs[len(locs)-1]
			if v, ok := m.cat.FindVendor(g[1][last[1]:]); ok {
				r.Vendor = v.Key
				reason("Vendor %s from the title", v.Name)
			}
		}
	} else if hasPMWord(ev.Summary) {
		r.IsPM = true
		method("keyword")
		reason(`Keyword "PM" in the title`)
	}
	lowSummary := strings.ToLower(ev.Summary)
	lowText := lowSummary + "\n" + strings.ToLower(ev.Description)
	for _, kw := range m.keywords {
		if strings.Contains(lowText, kw) {
			r.IsPM = true
			method("keyword")
			reason("Keyword %q", kw)
			break
		}
	}

	// Site: calendar mapping, else the title, else the event location, else the description.
	if r.Site == "" {
		for _, src := range []struct{ text, where string }{
			{lowSummary, "title"}, {strings.ToLower(ev.Location), "location"}, {strings.ToLower(ev.Description), "description"},
		} {
			found := m.sitesIn(src.text)
			if len(found) == 1 {
				r.Site = found[0].id
				reason("Site %s from %q in the %s", m.cat.SiteLabel(r.Site), found[0].term, src.where)
				break
			}
			if len(found) > 1 {
				var names []string
				for _, f := range found {
					names = append(names, m.cat.SiteLabel(f.id))
				}
				r.Missing = append(r.Missing, fmt.Sprintf("The %s mentions more than one site: %s", src.where, strings.Join(names, ", ")))
				break
			}
		}
	}

	for _, sys := range m.cat.Systems {
		for _, term := range append([]string{sys.Name}, sys.Aliases...) {
			if containsTerm(lowText, strings.ToLower(term)) {
				r.Systems = append(r.Systems, sys.Key)
				reason("System %s from %q", sys.Name, term)
				break
			}
		}
	}

	if r.Vendor == "" {
		if v, dom, ok := m.vendorByEmail(ev); ok {
			r.Vendor = v.Key
			reason("Email domain %s belongs to %s", dom, v.Name)
		}
	}
	if r.Vendor == "" {
	vendors:
		for _, v := range m.cat.Vendors {
			for _, alias := range append([]string{v.Name}, v.Aliases...) {
				if containsTerm(lowText, strings.ToLower(alias)) {
					r.Vendor = v.Key
					reason("Vendor %s named in the text", v.Name)
					break vendors
				}
			}
		}
	}

	resolved := m.resolve(&r)
	candidate := r.Vendor != "" || (r.Site != "" && len(r.Systems) > 0)
	switch {
	case r.IsPM && len(resolved) > 0:
		return m.full(r, resolved)
	case r.IsPM || candidate:
		r.Class = MatchPartial
		if !r.IsPM {
			r.Missing = append([]string{"No PM keyword or PM calendar"}, r.Missing...)
			if len(r.Candidates) == 0 {
				r.Candidates = resolved
			}
		}
		if r.Site == "" && !hasPrefix(r.Missing, "The ") {
			r.Missing = append(r.Missing, "Site not recognised")
		}
		return r
	}
	r.Class = MatchNone
	return r
}

func hasPrefix(list []string, p string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func (m *Matcher) full(r Match, tasks []string) Match {
	sort.Strings(tasks)
	r.Class, r.Tasks, r.Missing, r.Candidates = MatchFull, tasks, nil, nil
	if len(tasks) > 0 {
		t := m.live[tasks[0]]
		r.Site = t.SiteID
		if r.Vendor == "" {
			r.Vendor = t.Vendor
		}
	}
	return r
}

// resolve narrows the site's tasks by vendor and systems. It returns the
// tasks the event covers, or sets Candidates and Missing when it can't tell.
func (m *Matcher) resolve(r *Match) []string {
	if r.Site == "" {
		return nil
	}
	var at []Task
	for _, t := range m.live {
		if t.SiteID == r.Site {
			at = append(at, t)
		}
	}
	sort.Slice(at, func(i, j int) bool { return at[i].ID < at[j].ID })
	site := m.cat.SiteLabel(r.Site)
	if len(at) == 0 {
		r.Missing = append(r.Missing, fmt.Sprintf("No PM tasks are set up for %s yet", site))
		return nil
	}
	ids := func(ts []Task) []string {
		var out []string
		for _, t := range ts {
			out = append(out, t.ID)
		}
		return out
	}
	filtered := at
	if r.Vendor != "" {
		filtered = filterTasks(filtered, func(t Task) bool { return t.Vendor == r.Vendor })
	}
	if len(r.Systems) > 0 {
		filtered = filterTasks(filtered, func(t Task) bool { return contains(r.Systems, t.System) })
	}
	switch {
	case len(filtered) == 0:
		what := []string{}
		for _, s := range r.Systems {
			what = append(what, m.cat.SystemName(s))
		}
		desc := strings.Join(what, " or ")
		if desc == "" {
			desc = "any system"
		}
		if r.Vendor != "" {
			desc += " by " + m.cat.VendorName(r.Vendor)
		}
		r.Missing = append(r.Missing, fmt.Sprintf("%s has no PM task for %s", site, desc))
		r.Candidates = ids(at)
		return nil
	case r.Vendor == "" && len(r.Systems) == 0:
		if len(at) == 1 {
			r.Reasons = append(r.Reasons, "The site's only PM task")
			return ids(at)
		}
		r.Missing = append(r.Missing, "No system or vendor in the event; several tasks at this site fit")
		r.Candidates = ids(at)
		return nil
	case r.Vendor == "":
		vendors := map[string]bool{}
		for _, t := range filtered {
			vendors[t.Vendor] = true
		}
		if len(vendors) == 1 {
			r.Reasons = append(r.Reasons, fmt.Sprintf("Vendor %s from the site's task", m.cat.VendorName(filtered[0].Vendor)))
			return ids(filtered)
		}
		r.Missing = append(r.Missing, "More than one vendor services this system here; pick one")
		r.Candidates = ids(filtered)
		return nil
	default:
		if len(r.Systems) == 0 && len(filtered) > 1 {
			r.Reasons = append(r.Reasons, fmt.Sprintf("Covers all of %s's tasks at this site", m.cat.VendorName(r.Vendor)))
		}
		return ids(filtered)
	}
}

func filterTasks(ts []Task, keep func(Task) bool) []Task {
	var out []Task
	for _, t := range ts {
		if keep(t) {
			out = append(out, t)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

type siteHit struct{ id, term string }

func (m *Matcher) sitesIn(text string) []siteHit {
	if text == "" {
		return nil
	}
	var out []siteHit
	for _, s := range m.cat.Sites {
		if s.Archived {
			continue
		}
		for _, term := range append([]string{s.Code, s.Name}, s.Aliases...) {
			if containsTerm(text, strings.ToLower(term)) {
				out = append(out, siteHit{s.ID, term})
				break
			}
		}
	}
	return out
}

func (m *Matcher) vendorByEmail(ev Event) (Vendor, string, bool) {
	for _, e := range append([]string{ev.Organizer}, ev.Attendees...) {
		at := strings.LastIndex(e, "@")
		if at < 0 {
			continue
		}
		dom := strings.ToLower(e[at+1:])
		for _, v := range m.cat.Vendors {
			for _, d := range v.Domains {
				d = strings.ToLower(strings.TrimSpace(d))
				if d != "" && (dom == d || strings.HasSuffix(dom, "."+d)) {
					return v, dom, true
				}
			}
		}
	}
	return Vendor{}, "", false
}

// hasPMWord finds "PM" as a standalone uppercase word that is not part of a
// clock time such as "3 PM", "3PM" or "15:30 PM".
func hasPMWord(s string) bool {
	for i := 0; i+2 <= len(s); i++ {
		if s[i:i+2] != "PM" {
			continue
		}
		if i > 0 && isWordByte(s[i-1]) {
			continue
		}
		if i+2 < len(s) && isWordByte(s[i+2]) {
			continue
		}
		prev := strings.TrimRight(s[:i], " \t.")
		if prev != "" && prev[len(prev)-1] >= '0' && prev[len(prev)-1] <= '9' {
			continue
		}
		return true
	}
	return false
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// containsTerm reports whether term occurs in text as a whole word. Terms
// with non-ASCII letters (Thai has no spaces between words) match anywhere.
func containsTerm(text, term string) bool {
	if term == "" {
		return false
	}
	for i := 0; i < len(term); i++ {
		if term[i] >= utf8.RuneSelf {
			return strings.Contains(text, term)
		}
	}
	for start := 0; start < len(text); {
		i := strings.Index(text[start:], term)
		if i < 0 {
			return false
		}
		i += start
		before, _ := utf8.DecodeLastRuneInString(text[:i])
		after, _ := utf8.DecodeRuneInString(text[i+len(term):])
		okBefore := i == 0 || !(unicode.IsLetter(before) || unicode.IsDigit(before))
		okAfter := i+len(term) == len(text) || !(unicode.IsLetter(after) || unicode.IsDigit(after))
		if okBefore && okAfter {
			return true
		}
		start = i + 1
	}
	return false
}
