// Package core holds the tracker's domain logic: the site/task catalog,
// matching calendar events to PM visits, visit status and forecasts.
package core

import "time"

// Event is a calendar event as mirrored from Google Calendar.
type Event struct {
	CalendarID       string    `json:"calendar_id"`
	ID               string    `json:"id"`
	ICalUID          string    `json:"ical_uid,omitempty"`
	RecurringEventID string    `json:"recurring_event_id,omitempty"`
	Status           string    `json:"status"` // confirmed | tentative | cancelled
	Summary          string    `json:"summary"`
	Description      string    `json:"description,omitempty"`
	Location         string    `json:"location,omitempty"`
	Start            time.Time `json:"start"`
	End              time.Time `json:"end"`
	AllDay           bool      `json:"all_day"`
	Organizer        string    `json:"organizer,omitempty"`
	Attendees        []string  `json:"attendees,omitempty"`
	HTMLLink         string    `json:"html_link,omitempty"`
	Updated          time.Time `json:"updated"`
}

func (e Event) Key() string     { return e.CalendarID + "|" + e.ID }
func (e Event) Cancelled() bool { return e.Status == "cancelled" }

// Decision is a person's verdict on an event from the review inbox.
type Decision struct {
	EventKey string    `json:"event_key"`
	Action   string    `json:"action"` // link | ignore
	Tasks    []string  `json:"tasks,omitempty"`
	By       string    `json:"by,omitempty"`
	At       time.Time `json:"at"`
}

// LearnedRule links future events to tasks without asking again.
type LearnedRule struct {
	ID      int       `json:"id"`
	Kind    string    `json:"kind"` // series | title
	Value   string    `json:"value"`
	Label   string    `json:"label"`
	Tasks   []string  `json:"tasks"`
	By      string    `json:"by,omitempty"`
	Created time.Time `json:"created"`
}

// VisitMark records a person confirming or rejecting a past visit.
type VisitMark struct {
	VisitID string    `json:"visit_id"`
	Status  string    `json:"status"` // done | missed
	By      string    `json:"by,omitempty"`
	At      time.Time `json:"at"`
}

// ManualVisit is a past visit typed in by hand (back-fill, or a task's last PM date).
type ManualVisit struct {
	ID      int       `json:"id"`
	Date    string    `json:"date"` // YYYY-MM-DD
	Tasks   []string  `json:"tasks"`
	Note    string    `json:"note,omitempty"`
	By      string    `json:"by,omitempty"`
	Created time.Time `json:"created"`
}

// Change is one logged change to a mirrored event (a reschedule, a cancellation).
type Change struct {
	EventKey string    `json:"event_key"`
	Field    string    `json:"field"`
	Old      string    `json:"old"`
	New      string    `json:"new"`
	At       time.Time `json:"at"`
}

// Inputs is everything Build needs from storage.
type Inputs struct {
	Catalog   Catalog
	Events    []Event
	Decisions map[string]Decision
	Rules     []LearnedRule
	Marks     map[string]VisitMark
	Manual    []ManualVisit
	Changes   []Change
}

// MatchClass says how sure the matcher is that an event is a PM visit.
type MatchClass int

const (
	MatchNone    MatchClass = iota // not a PM visit
	MatchPartial                   // looks like PM, needs a person
	MatchFull                      // PM visit linked to one or more tasks
)

func (c MatchClass) String() string {
	switch c {
	case MatchFull:
		return "full"
	case MatchPartial:
		return "partial"
	}
	return "none"
}

type Match struct {
	Class      MatchClass
	IsPM       bool
	Site       string   // site ID, if recognised
	Systems    []string // system keys mentioned
	Vendor     string   // vendor key, if recognised
	Tasks      []string // linked task IDs (full match)
	Candidates []string // task IDs to suggest in the review inbox
	Method     string
	Reasons    []string
	Missing    []string
}

type VisitStatus string

const (
	VisitScheduled   VisitStatus = "scheduled"
	VisitUnconfirmed VisitStatus = "done_unconfirmed"
	VisitDone        VisitStatus = "done"
	VisitMissed      VisitStatus = "missed"
	VisitCancelled   VisitStatus = "cancelled"
)

func (s VisitStatus) Label() string {
	switch s {
	case VisitScheduled:
		return "Scheduled"
	case VisitUnconfirmed:
		return "Done, unconfirmed"
	case VisitDone:
		return "Done"
	case VisitMissed:
		return "Missed"
	case VisitCancelled:
		return "Cancelled"
	}
	return string(s)
}

// Counts reports whether the visit counts as a completed PM.
func (s VisitStatus) Counts() bool { return s == VisitDone || s == VisitUnconfirmed }

// Visit is one vendor trip, covering one or more tasks.
type Visit struct {
	ID          string
	Date        time.Time // local midnight
	Tasks       []string
	Status      VisitStatus
	Source      string // calendar | manual
	Event       *Event
	Method      string
	Note        string
	Reschedules []Change
	ManualID    int
}

// Status is a task's PM status.
type Status string

const (
	StatusOverdue   Status = "overdue"
	StatusDueSoon   Status = "due_soon"
	StatusOffCycle  Status = "off_cycle"
	StatusNoHistory Status = "no_history"
	StatusBooked    Status = "booked"
	StatusOnTrack   Status = "on_track"
)

// AllStatuses in urgency order.
var AllStatuses = []Status{StatusOverdue, StatusDueSoon, StatusOffCycle, StatusNoHistory, StatusBooked, StatusOnTrack}

func (s Status) Rank() int {
	for i, x := range AllStatuses {
		if x == s {
			return i
		}
	}
	return len(AllStatuses)
}

func (s Status) Label() string {
	switch s {
	case StatusOverdue:
		return "Overdue"
	case StatusDueSoon:
		return "Due soon, not booked"
	case StatusOffCycle:
		return "Off-cycle"
	case StatusNoHistory:
		return "No history"
	case StatusBooked:
		return "Booked"
	case StatusOnTrack:
		return "On track"
	}
	return string(s)
}

// Short is the label for tight spaces such as the site matrix.
func (s Status) Short() string {
	if s == StatusDueSoon {
		return "Due soon"
	}
	return s.Label()
}

func (s Status) Hint() string {
	switch s {
	case StatusOverdue:
		return "Past due and grace period with no PM done. Escalate with the vendor."
	case StatusDueSoon:
		return "Due within the reminder window and nothing is booked. Ask the vendor to schedule."
	case StatusOffCycle:
		return "A visit is booked, but far from the due date. Check the date with the vendor."
	case StatusNoHistory:
		return "No completed PM on record. Set the last PM date, or link a past visit."
	case StatusBooked:
		return "A visit is booked inside the due window."
	case StatusOnTrack:
		return "Nothing to do yet."
	}
	return ""
}
