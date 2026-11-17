package core

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Incident is an unplanned event at a site that hurt the rooms' service, or
// could have (a near miss): a power or cooling failure, a leak, a fire
// alarm, a door left open, a wrong breaker switched.
//
// It follows the ISO 31000 risk process. Each one is rated by its worst
// credible consequence and the likelihood of it happening again (risk
// assessment, 6.4), treated with a chosen option (6.5), re-rated once the
// action is in (residual risk), and kept with a timeline nobody can edit
// (recording and reporting, 6.7). Across incidents, the measures and the
// risk matrix are the monitoring and review (6.6). The fields also cover
// ISO 27001 A.5.25 to A.5.28: assessment, response, learning and evidence.
// Incidents never change a PM task's status.
type Incident struct {
	ID       int    `json:"id"`
	Ref      string `json:"ref"` // INC-2026-003, by year of occurrence; fixed once logged
	SiteID   string `json:"site_id"`
	TaskID   string `json:"task_id,omitempty"` // the system involved, through its PM task, if any
	Kind     string `json:"kind"`              // incident | near_miss
	Category string `json:"category"`          // the risk source, see IncidentCategories
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"` // what happened, in order

	Occurred   string `json:"occurred"`           // 2006-01-02T15:04, local time
	Detected   string `json:"detected,omitempty"` // when someone noticed, if later
	DetectedBy string `json:"detected_by"`        // how it was noticed, see IncidentDetections
	Restored   string `json:"restored,omitempty"` // service back to normal

	// Impact is what actually happened, 0 to 5 on the consequence scale:
	// 0 means no effect, which is what makes it a near miss.
	Impact int `json:"impact"`
	// Consequence (1 to 5) is the worst that could credibly have happened,
	// and Likelihood (1 to 5) the chance of it happening again without
	// further action. Together they are the incident's risk rating.
	Consequence int `json:"consequence"`
	Likelihood  int `json:"likelihood"`

	Response string `json:"response,omitempty"` // how it was contained and recovered
	Owner    string `json:"owner,omitempty"`    // who carries the follow-up
	Due      string `json:"due,omitempty"`      // YYYY-MM-DD: target date for the action
	Status   string `json:"status"`             // open | contained | restored | closed

	Cause     string `json:"cause,omitempty"`     // root cause
	Action    string `json:"action,omitempty"`    // corrective and preventive action
	Lessons   string `json:"lessons,omitempty"`   // what others should know
	Treatment string `json:"treatment,omitempty"` // ISO 31000 6.5.2 option, see IncidentTreatments
	// The rating once the action is in place. Needed to close.
	ResidualConsequence int    `json:"residual_consequence,omitempty"`
	ResidualLikelihood  int    `json:"residual_likelihood,omitempty"`
	ClosedOn            string `json:"closed_on,omitempty"` // YYYY-MM-DD
	ClosedBy            string `json:"closed_by,omitempty"`

	// FindingID is a finding at the same site that warned of it, or that
	// came out of it.
	FindingID int `json:"finding_id,omitempty"`
	// RepeatOf is an earlier incident at the same site that has happened again.
	RepeatOf int `json:"repeat_of,omitempty"`

	// Notes are the timeline: every change and every update, appended and
	// never edited, so the record keeps its history after the activity log
	// has rolled over.
	Notes   []IncidentNote `json:"notes,omitempty"`
	By      string         `json:"by,omitempty"`
	Created time.Time      `json:"created"`
}

// IncidentNote is one entry of an incident's timeline.
type IncidentNote struct {
	At      time.Time `json:"at"`
	By      string    `json:"by"`
	Changes []string  `json:"changes,omitempty"` // fields changed, worded for people
	Text    string    `json:"text,omitempty"`    // an update typed by someone
}

// Incident kinds and statuses.
const (
	IncidentKind     = "incident"
	NearMiss         = "near_miss"
	IncidentOpen     = "open"
	IncidentContain  = "contained"
	IncidentRestored = "restored"
	IncidentClosed   = "closed"
)

// The choices for an incident's fields, in form order.
var (
	IncidentKinds = []Choice{
		{IncidentKind, "Incident", "It affected the room or its service"},
		{NearMiss, "Near miss", "Nothing was affected, but it could have been"},
	}
	IncidentCategories = []Choice{
		{"power", "Power", "Utility, UPS, DC, generator, distribution"},
		{"cooling", "Cooling", "CRAC, PAC, chiller, airflow"},
		{"fire", "Fire or smoke", "Detection, suppression, alarms"},
		{"water", "Water, humidity or dust", "Leaks, condensate, flooding"},
		{"access", "Physical security", "Doors, access control, CCTV"},
		{"it", "Network or IT equipment", "Links, switches, servers in the room"},
		{"human", "Human error or procedure", "Wrong switch, missed step, no procedure"},
		{"vendor", "Vendor or contractor work", "Damage or trips during someone's work"},
		{"other", "Other", ""},
	}
	IncidentDetections = []Choice{
		{"alarm", "Alarm or monitoring", "BMS, NMS, UPS or CRAC alarm"},
		{"pm", "PM visit or inspection", ""},
		{"staff", "Staff on site", ""},
		{"user", "Users or customers", "Someone affected reported it"},
		{"vendor", "Vendor", ""},
		{"other", "Other", ""},
	}
	IncidentStatuses = []Choice{
		{IncidentOpen, "Open", "Happening, or being dealt with"},
		{IncidentContain, "Contained", "Stopped from getting worse, not back to normal yet"},
		{IncidentRestored, "Service restored", "Back to normal; cause and action to follow"},
		{IncidentClosed, "Closed", "Cause found, action in place, risk re-rated"},
	}
	// IncidentTreatments are the ISO 31000 (6.5.2) risk treatment options.
	IncidentTreatments = []Choice{
		{"reduce", "Reduce", "Lower the likelihood or the consequence: fix, add a control, change the PM"},
		{"remove", "Remove the source", "Replace the equipment or change the design"},
		{"share", "Share", "Through the vendor contract, an SLA or insurance"},
		{"avoid", "Avoid", "Stop the activity that causes it"},
		{"retain", "Retain", "Accept it, by an informed decision"},
	}
)

// Level is one step of a rating scale.
type Level struct {
	N           int
	Label, Hint string
}

// The rating scales, worded for DC and CSR rooms. They are the risk
// criteria ISO 31000 (6.3.4) asks an organisation to set; change them here
// if the company's risk policy words them differently.
var (
	ConsequenceScale = []Level{
		{1, "Insignificant", "No effect on the IT load; redundancy intact"},
		{2, "Minor", "Redundancy lost or degraded; no effect on the IT load"},
		{3, "Moderate", "Part of the IT load affected, such as a rack or a circuit; or a first-aid injury"},
		{4, "Major", "A room or site loses service for under 4 hours; or an injury needing treatment"},
		{5, "Severe", "A site loses service for 4 hours or more; a fire, serious injury or lost data"},
	}
	LikelihoodScale = []Level{
		{1, "Rare", "Not expected; under once in 5 years"},
		{2, "Unlikely", "Once in 2 to 5 years"},
		{3, "Possible", "About once a year"},
		{4, "Likely", "Several times a year"},
		{5, "Almost certain", "Every month or more"},
	}
	// ImpactScale is the consequence scale with "none", for what actually happened.
	ImpactScale = append([]Level{{0, "None", "Nothing was affected (a near miss)"}}, ConsequenceScale...)
)

// RiskBand is a range of likelihood × consequence scores and what it calls for.
type RiskBand struct {
	Key, Label, Hint string
	Min              int // lowest score in the band
}

// RiskBands, lowest first. Scores run 1 to 25.
var RiskBands = []RiskBand{
	{"low", "Low", "Retain and watch through the PM routine.", 1},
	{"medium", "Medium", "Treat by the next PM round, within 90 days.", 5},
	{"high", "High", "Treat with an owner and a plan within 30 days.", 10},
	{"extreme", "Extreme", "Act now; management decides the treatment.", 20},
}

// Risk is a rating: likelihood × consequence, and its band.
type Risk struct {
	Likelihood, Consequence, Score int
	Band                           RiskBand
}

// RiskOf rates a likelihood and a consequence. Zero if either is missing.
func RiskOf(likelihood, consequence int) Risk {
	if likelihood < 1 || likelihood > 5 || consequence < 1 || consequence > 5 {
		return Risk{}
	}
	r := Risk{Likelihood: likelihood, Consequence: consequence, Score: likelihood * consequence}
	for _, b := range RiskBands {
		if r.Score >= b.Min {
			r.Band = b
		}
	}
	return r
}

func (r Risk) Rated() bool   { return r.Score > 0 }
func (r Risk) Key() string   { return r.Band.Key }
func (r Risk) Label() string { return r.Band.Label }

// AtLeastHigh reports whether the rating is high or extreme.
func (r Risk) AtLeastHigh() bool { return r.Score >= RiskBands[2].Min }

// Tone is the colour the rating borrows from PM: low is green, medium
// amber, high and extreme red.
func (r Risk) Tone() Status {
	switch {
	case !r.Rated():
		return StatusNoHistory
	case r.AtLeastHigh():
		return StatusOverdue
	case r.Score >= RiskBands[1].Min:
		return StatusDueSoon
	}
	return StatusOnTrack
}

// Text is the rating in words, such as "High (4 × 4 = 16)".
func (r Risk) Text() string {
	if !r.Rated() {
		return "Not rated"
	}
	return fmt.Sprintf("%s (%d × %d = %d)", r.Band.Label, r.Likelihood, r.Consequence, r.Score)
}

func levelLabel(scale []Level, n int) string {
	for _, l := range scale {
		if l.N == n {
			return l.Label
		}
	}
	return strconv.Itoa(n)
}

// Limits on an incident's text and timeline, so one form can't bloat the data file.
const (
	MaxIncidentTitle = 200
	MaxIncidentOwner = 100
	MaxIncidentText  = 4000
	MaxIncidentNotes = 300
	MaxNoteText      = 2000
)

// IncidentTime is the layout of an incident's times, as a datetime-local
// field sends them.
const IncidentTime = "2006-01-02T15:04"

// incidentTime parses an incident time, refusing years far outside working life.
func incidentTime(s string) (time.Time, bool) {
	t, err := time.Parse(IncidentTime, s)
	return t, err == nil && t.Year() >= 2000 && t.Year() <= 2100
}

var refPattern = regexp.MustCompile(`^INC-(\d{4})-(\d{3,})$`)

// NextIncidentRef is the next reference for an incident that occurred in
// year: INC-<year>-<number>, numbered from 001 within the year.
func NextIncidentRef(all []Incident, year int) string {
	high := 0
	for _, in := range all {
		if m := refPattern.FindStringSubmatch(in.Ref); m != nil && m[1] == strconv.Itoa(year) {
			n, _ := strconv.Atoi(m[2])
			high = max(high, n)
		}
	}
	return fmt.Sprintf("INC-%04d-%03d", year, high+1)
}

// Clean tidies an incident's text and checks it on its own. Links to the
// site, the PM task, a finding and an earlier incident are checked by
// CheckLinks.
func (in *Incident) Clean() error {
	in.Title, in.Owner, in.ClosedBy = oneLine(in.Title), oneLine(in.Owner), oneLine(in.ClosedBy)
	in.Detail, in.Response = textBlock(in.Detail), textBlock(in.Response)
	in.Cause, in.Action, in.Lessons = textBlock(in.Cause), textBlock(in.Action), textBlock(in.Lessons)
	long := func(s string, max int) bool { return utf8.RuneCountInString(s) > max }
	switch {
	case in.SiteID == "":
		return errors.New("pick the site")
	case !visible(in.Title):
		return errors.New("enter what happened in a line")
	case long(in.Title, MaxIncidentTitle):
		return fmt.Errorf("keep the title under %d characters", MaxIncidentTitle)
	case long(in.Owner, MaxIncidentOwner):
		return fmt.Errorf("keep who follows it up under %d characters", MaxIncidentOwner)
	case long(in.Detail, MaxIncidentText), long(in.Response, MaxIncidentText), long(in.Cause, MaxIncidentText),
		long(in.Action, MaxIncidentText), long(in.Lessons, MaxIncidentText):
		return fmt.Errorf("keep each text box under %d characters", MaxIncidentText)
	case !validChoice(IncidentKinds, in.Kind):
		return errors.New("pick whether it's an incident or a near miss")
	case !validChoice(IncidentCategories, in.Category):
		return errors.New("pick what kind of incident it is")
	case !validChoice(IncidentDetections, in.DetectedBy):
		return errors.New("pick how it was noticed")
	case in.Treatment != "" && !validChoice(IncidentTreatments, in.Treatment):
		return fmt.Errorf("unknown treatment %q", in.Treatment)
	case in.Impact < 0 || in.Impact > 5:
		return errors.New("pick what the actual impact was")
	case in.Consequence < 1 || in.Consequence > 5:
		return errors.New("pick the worst consequence it could credibly have had")
	case in.Likelihood < 1 || in.Likelihood > 5:
		return errors.New("pick how likely it is to happen again")
	case in.Kind == NearMiss && in.Impact != 0:
		return errors.New("a near miss had no impact; make it an incident, or set the impact to none")
	case in.Kind == IncidentKind && in.Impact == 0:
		return errors.New("an incident had some impact; pick it, or make it a near miss")
	case in.Consequence < in.Impact:
		return errors.New("the worst credible consequence can't be less than what actually happened")
	case in.ResidualConsequence < 0 || in.ResidualConsequence > 5 || in.ResidualLikelihood < 0 || in.ResidualLikelihood > 5:
		return errors.New("pick the residual rating from the lists")
	case (in.ResidualConsequence == 0) != (in.ResidualLikelihood == 0):
		return errors.New("rate both the residual likelihood and consequence, or neither")
	case in.RepeatOf < 0 || (in.ID > 0 && in.RepeatOf == in.ID):
		return errors.New("an incident can't be a repeat of itself")
	case in.FindingID < 0:
		return errors.New("pick the finding from the list")
	case len(in.Notes) > MaxIncidentNotes:
		return fmt.Errorf("an incident's timeline holds at most %d entries", MaxIncidentNotes)
	}
	occurred, ok := incidentTime(in.Occurred)
	if !ok {
		return errors.New("enter when it happened, as a date and time (between 2000 and 2100)")
	}
	if in.Detected != "" {
		t, ok := incidentTime(in.Detected)
		switch {
		case !ok:
			return errors.New("enter when it was noticed as a date and time, or leave it empty")
		case t.Before(occurred):
			return errors.New("it can't have been noticed before it happened")
		}
	}
	if in.Restored != "" {
		t, ok := incidentTime(in.Restored)
		switch {
		case !ok:
			return errors.New("enter when service was restored as a date and time, or leave it empty")
		case t.Before(occurred):
			return errors.New("service can't have been restored before it happened")
		}
	}
	day := time.Date(occurred.Year(), occurred.Month(), occurred.Day(), 0, 0, 0, 0, time.UTC)
	if in.Due != "" {
		due, ok := jobDate(in.Due)
		switch {
		case !ok:
			return errors.New("enter the target date as a date (between 2000 and 2100), or leave it empty")
		case due.Before(day):
			return errors.New("the target date can't be before it happened")
		}
	}
	for _, n := range in.Notes {
		if n.At.IsZero() || (len(n.Changes) == 0 && !visible(n.Text)) || utf8.RuneCountInString(n.Text) > MaxNoteText {
			return errors.New("a timeline entry is empty, undated or too long")
		}
	}
	switch in.Status {
	case "", IncidentOpen, IncidentContain:
		if in.Status == "" {
			in.Status = IncidentOpen
		}
		in.ClosedOn, in.ClosedBy = "", ""
	case IncidentRestored:
		in.ClosedOn, in.ClosedBy = "", ""
		if in.Kind == IncidentKind && in.Restored == "" {
			return errors.New("enter when service was restored")
		}
	case IncidentClosed:
		closed, ok := jobDate(in.ClosedOn)
		switch {
		case !ok:
			return errors.New("enter the date it was closed (between 2000 and 2100)")
		case closed.Before(day):
			return errors.New("it can't be closed before it happened")
		case in.Kind == IncidentKind && in.Restored == "":
			return errors.New("enter when service was restored before closing it")
		case !visible(in.Cause):
			return errors.New("enter the root cause before closing it")
		case !visible(in.Action):
			return errors.New("enter the action taken before closing it, or why none is needed")
		case in.Treatment == "":
			return errors.New("pick how the risk is treated before closing it")
		case in.ResidualConsequence == 0:
			return errors.New("re-rate the risk with the action in place (residual likelihood and consequence) before closing it")
		}
	default:
		return fmt.Errorf("unknown incident status %q", in.Status)
	}
	return nil
}

// before reports whether a happened before b: by time, then by number, so
// "is a repeat of" can never go round in a circle.
func (a Incident) before(b Incident) bool {
	if a.Occurred != b.Occurred {
		return a.Occurred < b.Occurred
	}
	return a.ID < b.ID
}

// CheckLinks checks that an incident's site exists, its PM task and finding
// are at that site, and the incident it repeats is an earlier one at the
// same site, and that incidents recorded as repeats of it are still later.
// all is every incident on record; the one with in's ID is replaced by in.
func (in Incident) CheckLinks(cat *Catalog, findings []Finding, all []Incident) error {
	if _, ok := cat.Site(in.SiteID); !ok {
		return errors.New("that site no longer exists")
	}
	if in.TaskID != "" {
		t, ok := cat.Task(in.TaskID)
		if !ok || t.SiteID != in.SiteID {
			return errors.New("pick one of this site's systems, or none")
		}
	}
	if in.FindingID != 0 {
		found := false
		for _, f := range findings {
			if f.ID == in.FindingID {
				if f.SiteID != in.SiteID {
					return fmt.Errorf("finding #%d is at another site", f.ID)
				}
				found = true
			}
		}
		if !found {
			return fmt.Errorf("there's no finding #%d", in.FindingID)
		}
	}
	repeatFound := in.RepeatOf == 0
	for _, o := range all {
		if o.ID == in.ID {
			continue
		}
		if in.RepeatOf != 0 && o.ID == in.RepeatOf {
			switch {
			case o.SiteID != in.SiteID:
				return fmt.Errorf("%s is at another site; a repeat is the same thing happening again at the same site", o.Ref)
			case !o.before(in):
				return fmt.Errorf("%s happened after this one, so this can't be a repeat of it", o.Ref)
			}
			repeatFound = true
		}
		if o.RepeatOf == in.ID && in.ID != 0 && !in.before(o) {
			return fmt.Errorf("%s is recorded as a repeat of this one, so this one can't have happened after it", o.Ref)
		}
	}
	if !repeatFound {
		return fmt.Errorf("there's no incident #%d", in.RepeatOf)
	}
	return nil
}

// IncidentChanges describes, for the timeline, how b differs from a.
func IncidentChanges(a, b Incident, cat *Catalog) []string {
	var out []string
	pick := func(name string, list []Choice, x, y string) {
		if x != y {
			out = append(out, fmt.Sprintf("%s: %s → %s", name, orNone(choiceLabel(list, x)), orNone(choiceLabel(list, y))))
		}
	}
	text := func(name, x, y string) {
		switch {
		case x == y:
		case x == "":
			out = append(out, name+" added")
		case y == "":
			out = append(out, name+" removed")
		default:
			out = append(out, name+" changed")
		}
	}
	value := func(name, x, y string) {
		if x != y {
			out = append(out, fmt.Sprintf("%s: %s → %s", name, orNone(x), orNone(y)))
		}
	}
	level := func(name string, scale []Level, x, y int) {
		if x != y {
			l := func(n int) string {
				if n == 0 && scale[0].N != 0 {
					return "none"
				}
				return fmt.Sprintf("%d %s", n, levelLabel(scale, n))
			}
			out = append(out, fmt.Sprintf("%s: %s → %s", name, l(x), l(y)))
		}
	}
	pick("Status", IncidentStatuses, a.Status, b.Status)
	pick("Kind", IncidentKinds, a.Kind, b.Kind)
	pick("Category", IncidentCategories, a.Category, b.Category)
	value("Title", a.Title, b.Title)
	if a.TaskID != b.TaskID {
		out = append(out, fmt.Sprintf("System: %s → %s", orNone(cat.TaskLabel(a.TaskID)), orNone(cat.TaskLabel(b.TaskID))))
	}
	value("Happened", spaceT(a.Occurred), spaceT(b.Occurred))
	value("Noticed", spaceT(a.Detected), spaceT(b.Detected))
	pick("Noticed by", IncidentDetections, a.DetectedBy, b.DetectedBy)
	value("Service restored", spaceT(a.Restored), spaceT(b.Restored))
	level("Impact", ImpactScale, a.Impact, b.Impact)
	level("Worst credible consequence", ConsequenceScale, a.Consequence, b.Consequence)
	level("Likelihood", LikelihoodScale, a.Likelihood, b.Likelihood)
	if ra, rb := RiskOf(a.Likelihood, a.Consequence), RiskOf(b.Likelihood, b.Consequence); ra.Score != rb.Score {
		out = append(out, fmt.Sprintf("Risk: %s → %s", ra.Text(), rb.Text()))
	}
	value("Owner", a.Owner, b.Owner)
	value("Target date", a.Due, b.Due)
	text("Details", a.Detail, b.Detail)
	text("Response", a.Response, b.Response)
	text("Root cause", a.Cause, b.Cause)
	text("Action", a.Action, b.Action)
	text("Lessons learned", a.Lessons, b.Lessons)
	pick("Treatment", IncidentTreatments, a.Treatment, b.Treatment)
	if ra, rb := RiskOf(a.ResidualLikelihood, a.ResidualConsequence), RiskOf(b.ResidualLikelihood, b.ResidualConsequence); ra != rb {
		out = append(out, fmt.Sprintf("Residual risk: %s → %s", ra.Text(), rb.Text()))
	}
	value("Closed on", a.ClosedOn, b.ClosedOn)
	ref := func(n int, prefix string) string {
		if n == 0 {
			return ""
		}
		return prefix + strconv.Itoa(n)
	}
	value("Finding", ref(a.FindingID, "#"), ref(b.FindingID, "#"))
	value("Repeat of", ref(a.RepeatOf, "#"), ref(b.RepeatOf, "#"))
	return out
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func spaceT(s string) string { return strings.Replace(s, "T", " ", 1) }

// IncidentView is an incident with its site, system and times worked out.
type IncidentView struct {
	Incident
	Site     Site
	Task     Task // zero when no system is involved
	HasTask  bool
	At       time.Time // Occurred, local
	Noticed  time.Time // zero when not recorded
	Back     time.Time // Restored, zero when not recorded
	DueDay   time.Time // zero when there's no target date
	Closed   time.Time // zero while open
	DaysLeft int       // days from today to the target date; negative is late
	// Age is the days from occurring to closed, or to today while open.
	Age  int
	Risk Risk // as assessed: worst credible consequence × likelihood
	// Residual is the rating with the action in place; zero until rated.
	Residual Risk
	// Repeats are later incidents recorded as this one happening again.
	Repeats []int
}

func (v IncidentView) IsOpen() bool          { return v.Status != IncidentClosed }
func (v IncidentView) IsNearMiss() bool      { return v.Kind == NearMiss }
func (v IncidentView) HasDue() bool          { return !v.DueDay.IsZero() }
func (v IncidentView) Late() bool            { return v.IsOpen() && v.HasDue() && v.DaysLeft < 0 }
func (v IncidentView) KindLabel() string     { return choiceLabel(IncidentKinds, v.Kind) }
func (v IncidentView) CategoryLabel() string { return choiceLabel(IncidentCategories, v.Category) }
func (v IncidentView) DetectedLabel() string { return choiceLabel(IncidentDetections, v.DetectedBy) }
func (v IncidentView) TreatmentLabel() string {
	return choiceLabel(IncidentTreatments, v.Treatment)
}
func (v IncidentView) ImpactLabel() string { return levelLabel(ImpactScale, v.Impact) }

// Ongoing reports whether it's still being contained or recovered from
// (open or contained), near misses included: a leak that hurt nothing yet
// still needs stopping.
func (v IncidentView) Ongoing() bool {
	return v.Status == IncidentOpen || v.Status == IncidentContain
}

// Current is the rating that stands now: the residual one once rated,
// otherwise the assessed one.
func (v IncidentView) Current() Risk {
	if v.Residual.Rated() {
		return v.Residual
	}
	return v.Risk
}

// RestoreMinutes is how long service took to come back, or -1 if unknown.
func (v IncidentView) RestoreMinutes() int {
	if v.Back.IsZero() || v.IsNearMiss() {
		return -1
	}
	return int(v.Back.Sub(v.At).Minutes())
}

// NoticeMinutes is how long it took to notice, or -1 if not recorded.
func (v IncidentView) NoticeMinutes() int {
	if v.Noticed.IsZero() {
		return -1
	}
	return int(v.Noticed.Sub(v.At).Minutes())
}

// Tone is the incident's status colour: still happening is red, contained
// amber, restored with follow-up to do blue, closed green; a follow-up
// past its target date is red too.
func (v IncidentView) Tone() Status {
	switch {
	case !v.IsOpen():
		return StatusOnTrack
	case v.Status == IncidentOpen, v.Late():
		return StatusOverdue
	case v.Status == IncidentContain:
		return StatusDueSoon
	}
	return StatusBooked
}

// StatusLabel names the incident's state.
func (v IncidentView) StatusLabel() string {
	if v.Late() && !v.Ongoing() {
		return "Action past target"
	}
	return choiceLabel(IncidentStatuses, v.Status)
}

// Hint says what to do next.
func (v IncidentView) Hint() string {
	switch {
	case !v.IsOpen() && v.Residual.AtLeastHigh():
		return "Closed with the risk still " + strings.ToLower(v.Residual.Label()) + ", accepted by " + orNone(v.ClosedBy) + ". Review it at the next management review."
	case !v.IsOpen():
		return "Closed. If it happens again, log it as a repeat of this one so it shows in the measures."
	case v.Ongoing() && v.Risk.Band.Key == "extreme":
		return "Extreme risk: tell management now; they decide the treatment. Meanwhile contain it and restore service, adding updates to the timeline."
	case v.Status == IncidentOpen:
		return "Contain it first: stop it getting worse, then restore service. Add updates to the timeline as you go."
	case v.Status == IncidentContain:
		return "Contained. Restore service, then record when it came back."
	case v.Late():
		return "The follow-up is past its target date. Chase the owner, or agree a new date."
	case v.Cause == "":
		return "Service is back. Find the root cause: ask why until you reach something you can fix."
	case v.Risk.AtLeastHigh():
		return v.Risk.Band.Hint + " Then re-rate it with the action in place and close it."
	}
	return "Put the action in place, re-rate the risk with it, and close the incident."
}

// buildIncidents works out every incident's times and ratings and orders
// them: open ones first, still happening first, then highest risk, then
// oldest; then closed ones, most recently closed first. Incidents at
// archived or unknown sites are left out, as their PM tasks are.
func buildIncidents(cat *Catalog, list []Incident, today time.Time, loc *time.Location) []IncidentView {
	repeats := map[int][]int{}
	for _, in := range list {
		if in.RepeatOf != 0 {
			repeats[in.RepeatOf] = append(repeats[in.RepeatOf], in.ID)
		}
	}
	at := func(s string) time.Time {
		t, err := time.ParseInLocation(IncidentTime, s, loc)
		if err != nil {
			return time.Time{}
		}
		return t
	}
	var out []IncidentView
	for _, in := range list {
		site, ok := cat.Site(in.SiteID)
		if !ok || site.Archived {
			continue
		}
		v := IncidentView{Incident: in, Site: site, At: at(in.Occurred), Noticed: at(in.Detected), Back: at(in.Restored),
			Risk: RiskOf(in.Likelihood, in.Consequence), Residual: RiskOf(in.ResidualLikelihood, in.ResidualConsequence),
			Repeats: repeats[in.ID]}
		if v.At.IsZero() {
			continue
		}
		if t, ok := cat.Task(in.TaskID); ok && in.TaskID != "" {
			v.Task, v.HasTask = t, true
		}
		if in.Due != "" {
			v.DueDay, _ = time.ParseInLocation("2006-01-02", in.Due, loc)
			v.DaysLeft = DaysBetween(today, v.DueDay)
		}
		end := today
		if in.Status == IncidentClosed {
			v.Closed, _ = time.ParseInLocation("2006-01-02", in.ClosedOn, loc)
			end = v.Closed
		}
		v.Age = max(0, DaysBetween(Day(v.At, loc), end))
		sort.Ints(v.Repeats)
		out = append(out, v)
	}
	sort.SliceStable(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if x.IsOpen() != y.IsOpen() {
			return x.IsOpen()
		}
		if !x.IsOpen() {
			if !x.Closed.Equal(y.Closed) {
				return x.Closed.After(y.Closed)
			}
			return x.At.After(y.At)
		}
		if x.Ongoing() != y.Ongoing() {
			return x.Ongoing()
		}
		if x.Risk.Score != y.Risk.Score {
			return x.Risk.Score > y.Risk.Score
		}
		if !x.At.Equal(y.At) {
			return x.At.Before(y.At)
		}
		return x.ID < y.ID
	})
	return out
}

// Incident finds an incident by number.
func (s Snapshot) Incident(id int) (IncidentView, bool) {
	for _, v := range s.Incidents {
		if v.ID == id {
			return v, true
		}
	}
	return IncidentView{}, false
}

// IncidentsAt returns a site's incidents, in the snapshot's order.
func (s Snapshot) IncidentsAt(siteID string) []IncidentView {
	var out []IncidentView
	for _, v := range s.Incidents {
		if v.SiteID == siteID {
			out = append(out, v)
		}
	}
	return out
}

// IncidentsFor returns the incidents involving one PM task's system.
func (s Snapshot) IncidentsFor(taskID string) []IncidentView {
	var out []IncidentView
	for _, v := range s.Incidents {
		if v.HasTask && v.TaskID == taskID {
			out = append(out, v)
		}
	}
	return out
}

// IncidentsLinkedTo returns the incidents linked to a finding.
func (s Snapshot) IncidentsLinkedTo(findingID int) []IncidentView {
	var out []IncidentView
	for _, v := range s.Incidents {
		if v.FindingID == findingID {
			out = append(out, v)
		}
	}
	return out
}

// IncidentStats are the measures for monitoring and review. Open counts are
// as of today; the rest cover incidents that happened (or closed) on or
// after Since.
type IncidentStats struct {
	Open, Ongoing, OpenHigh, Late int // OpenHigh: open and rated high or extreme
	Incidents, NearMisses         int // happened since Since
	Repeats                       int // of those, repeats of an earlier one
	Closed                        int // closed since Since
	MedianClose                   int // days from happening to closed, for those
	Restored                      int // incidents since Since with a restore time
	MedianRestore                 int // minutes to restore service, for those
	AlarmShare                    int // percent of those since Since noticed by an alarm, or -1
}

// NearMissRatio is near misses per incident, such as "2.5", or "–". More
// near misses reported per incident means people speak up before it hurts.
func (s IncidentStats) NearMissRatio() string {
	if s.Incidents == 0 {
		if s.NearMisses == 0 {
			return "–"
		}
		return strconv.Itoa(s.NearMisses) + " : 0"
	}
	return strconv.FormatFloat(float64(s.NearMisses)/float64(s.Incidents), 'f', 1, 64)
}

// RepeatShare is the percentage that happened again, or -1.
func (s IncidentStats) RepeatShare() int { return percent(s.Repeats, s.Incidents+s.NearMisses) }

func median(xs []int) int {
	if len(xs) == 0 {
		return -1
	}
	sort.Ints(xs)
	n := len(xs)
	if n%2 == 1 {
		return xs[n/2]
	}
	return (xs[n/2-1] + xs[n/2] + 1) / 2
}

// IncidentStatsOf measures a list of incidents.
func IncidentStatsOf(list []IncidentView, since time.Time) IncidentStats {
	var st IncidentStats
	var closeDays, restore []int
	alarm := 0
	for _, v := range list {
		if v.IsOpen() {
			st.Open++
			if v.Ongoing() {
				st.Ongoing++
			}
			if v.Risk.AtLeastHigh() {
				st.OpenHigh++
			}
			if v.Late() {
				st.Late++
			}
		}
		if !v.At.Before(since) {
			if v.IsNearMiss() {
				st.NearMisses++
			} else {
				st.Incidents++
				if m := v.RestoreMinutes(); m >= 0 {
					st.Restored++
					restore = append(restore, m)
				}
			}
			if v.RepeatOf != 0 {
				st.Repeats++
			}
			if v.DetectedBy == "alarm" {
				alarm++
			}
		}
		if !v.IsOpen() && !v.Closed.Before(since) {
			st.Closed++
			closeDays = append(closeDays, v.Age)
		}
	}
	st.MedianClose, st.MedianRestore = median(closeDays), median(restore)
	st.AlarmShare = percent(alarm, st.Incidents+st.NearMisses)
	return st
}

// MatrixCell is one cell of the 5 × 5 risk matrix.
type MatrixCell struct {
	Risk  Risk
	Count int
}

// MatrixRow is one likelihood row of the risk matrix.
type MatrixRow struct {
	Likelihood Level
	Cells      []MatrixCell // consequence 1 to 5
}

// RiskMatrix counts incidents by their current rating (residual once
// rated), likelihood 5 at the top, consequence 1 on the left. Only open
// incidents and those that happened since since are counted.
func RiskMatrix(list []IncidentView, since time.Time) []MatrixRow {
	var counts [6][6]int
	for _, v := range list {
		if !v.IsOpen() && v.At.Before(since) {
			continue
		}
		if r := v.Current(); r.Rated() {
			counts[r.Likelihood][r.Consequence]++
		}
	}
	var rows []MatrixRow
	for l := 5; l >= 1; l-- {
		row := MatrixRow{Likelihood: LikelihoodScale[l-1]}
		for c := 1; c <= 5; c++ {
			row.Cells = append(row.Cells, MatrixCell{Risk: RiskOf(l, c), Count: counts[l][c]})
		}
		rows = append(rows, row)
	}
	return rows
}

// IncidentGroup is one row of an incident breakdown: a site or a category.
type IncidentGroup struct {
	Key, Code, Name string
	Stats           IncidentStats
}

// IncidentsBySite measures each site with an open incident or one since since.
func IncidentsBySite(list []IncidentView, since time.Time) []IncidentGroup {
	return groupIncidents(list, since, func(v IncidentView) (string, string, string) { return v.Site.ID, v.Site.Code, v.Site.Name })
}

// IncidentsByCategory measures each kind of incident.
func IncidentsByCategory(list []IncidentView, since time.Time) []IncidentGroup {
	return groupIncidents(list, since, func(v IncidentView) (string, string, string) { return v.Category, "", v.CategoryLabel() })
}

func groupIncidents(list []IncidentView, since time.Time, key func(IncidentView) (string, string, string)) []IncidentGroup {
	var order []string
	groups := map[string][]IncidentView{}
	names := map[string][2]string{}
	for _, v := range list {
		k, code, name := key(v)
		if _, seen := groups[k]; !seen {
			order = append(order, k)
			names[k] = [2]string{code, name}
		}
		groups[k] = append(groups[k], v)
	}
	var out []IncidentGroup
	for _, k := range order {
		st := IncidentStatsOf(groups[k], since)
		if st.Open == 0 && st.Incidents == 0 && st.NearMisses == 0 && st.Closed == 0 {
			continue
		}
		out = append(out, IncidentGroup{Key: k, Code: names[k][0], Name: names[k][1], Stats: st})
	}
	sort.SliceStable(out, func(a, b int) bool {
		x, y := out[a].Stats, out[b].Stats
		if x.OpenHigh != y.OpenHigh {
			return x.OpenHigh > y.OpenHigh
		}
		if x.Open != y.Open {
			return x.Open > y.Open
		}
		if n, m := x.Incidents+x.NearMisses, y.Incidents+y.NearMisses; n != m {
			return n > m
		}
		if out[a].Code != out[b].Code {
			return out[a].Code < out[b].Code
		}
		return out[a].Name < out[b].Name
	})
	return out
}

// NoteText tidies an update typed for an incident's timeline.
func NoteText(s string) string { return textBlock(s) }

// Tone is the band's colour, as Risk.Tone gives it.
func (b RiskBand) Tone() Status { return RiskOf(1, max(1, min(b.Min, 5))).Tone() }
