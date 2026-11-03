# PM Tracker

Tracks vendor preventive-maintenance (PM) visits across many sites from Google
Calendar. Every site has PM tasks: one per system and vendor, such as
"BKK1 UPS by ABC Power" or "BKK1 CRAC by CoolAir". The tracker records each
task's last PM date, forecasts the next one, and shows every site on one
overview.

- **Overview:** a site × system matrix grouped by region, with each cell
  showing the next PM and its status, plus each site's last PM date. Below it:
  overdue and the next 30 days, and a per-vendor summary. Filter by region,
  system, vendor and status. Export to CSV.
- **Site page:** every task with its last PM, next due date, booking and a
  timeline; recent visits; record a visit by hand.
- **Admin:** add, rename, archive and delete sites; add, edit, pause and
  delete PM tasks; set last PM dates; manage vendors, systems and calendars;
  import sites and tasks from CSV; activity log of who changed what.
- **Vendors:** a vendor list (contact person, phone, email, email domains,
  other names) that feeds every vendor dropdown. Each vendor shows the sites
  and systems it covers and what needs attention. Move all of one vendor's
  tasks to another when a contract changes; visit history stays. Vendor
  dropdowns on the site and task forms have "Add a new vendor…" so you never
  have to leave the form. Site pages list their vendors with tap-to-call
  phone numbers.
- **Read-only towards Google.** A service account reads the calendars you share with it.
- Single Go binary, standard library only, no CGO. Data lives in one JSON file.

## Try it

```sh
go run ./cmd/pmtracker -demo        # http://localhost:8080
```

The demo has 21 sites in six regions. Each site has DC power by VoltEdge, UPS
by ABC Power or Siam UPS (by region), and CRAC by CoolAir, Northern Cooling or
Andaman HVAC. Five sites also have a generator, and BKK2 has a second UPS room
with another vendor. Vendors title their invites differently, for example
`[PM] BKK1 DC – VoltEdge`, `PM UPS SRI1 – ABC Power`,
`บำรุงรักษา UPS เชียงใหม่` and `Preventive maintenance CRAC Khon Kaen`. The
Phuket site keeps its own calendar without site codes in titles.

The review inbox holds what the matcher can't place on its own:

- a recurring VoltEdge invite for Udon Thani whose site is only in the event's location; link the series once and UTH1's history appears,
- `[PM] BKK2 UPS`, where two vendors service UPS at that site,
- `[PM] PTT1 UPS – ABC Power` for a site that doesn't exist yet; add PTT1 in Admin and the event matches,
- a Thai event with no site, and a sales visit that isn't PM.

`-today 2027-01-15` pretends it's another day; `-keep` keeps your demo changes
across restarts.

## Set up your 21 sites

1. Fill in a spreadsheet with one row per PM task and import it under
   **Admin, Import and export**. Download the template there:

   ```
   site_code,site_name,region,system,vendor,interval_months,last_pm_date
   BKK1,Bang Na,Bangkok,DC power,VoltEdge Power,3,2026-09-14
   BKK1,Bang Na,Bangkok,UPS,ABC Power,3,2026-08-30
   BKK1,Bang Na,Bangkok,CRAC,CoolAir Services,3,15/09/2569
   ```

   Missing sites, systems and vendors are created. Dates can be 2026-09-14,
   14/09/2026 or Thai Buddhist years. **Check without saving** shows what
   would happen first.
2. Under **Admin, Vendors**, add each vendor's contact details and email
   domains so their calendar invites match automatically. A vendor's email
   address adds its domain for you (not for gmail.com and similar).
3. Under **Admin, Sites**, add other names a site goes by in event titles or
   locations (Thai names, old codes).

Or add sites one at a time. The add-site form takes the vendor and last PM date
for each system in one step.

## Connect Google Calendar

1. In Google Cloud, create a project, enable the **Google Calendar API**,
   create a **service account** and download its JSON key.
2. Share each PM calendar with the service account's email
   (`…@….iam.gserviceaccount.com`) with **See all event details**.
   Workspace admins may need to allow sharing outside the domain.
3. Copy `config.example.json` to `config.json`, set `google.credentials_file`
   and users, and list your calendars (or add them later under
   **Admin, Calendars and sync**).
4. Check, then run:

```sh
pmtracker -config config.json check   # verifies the key and every calendar, prints match counts
pmtracker -config config.json         # serve on :8080, sync every 15 minutes
```

A calendar can be marked **every event is a PM visit**, or tied to one site,
for a site that keeps its own calendar.

## How an event becomes a PM visit

First match wins:

1. A decision from the review inbox, or a learned rule (same recurring series, same title).
2. A PM signal: a PM-only calendar, `[PM]` at the start of the title, `PM` as
   a word (not `3 PM`), "preventive maintenance", `บำรุงรักษา`, `ซ่อมบำรุง`, or
   your own `keywords`.
3. The site: from the calendar's site, else its code or other names in the
   title, then the location, then the description.
4. Systems from their names in the title (UPS, CRAC, PAC, DC, Genset …), and
   the vendor from `– Vendor` at the end of a `[PM]` title, the organizer's or
   an attendee's email domain, or the vendor's name in the text.
5. The site's tasks are narrowed by vendor and systems. If one vendor does
   that system at that site, the vendor can be left out of the title. If two
   vendors do it (BKK2 UPS), the event goes to the inbox with both as choices.

A vendor's visit can cover several systems at once (`[PM] BKK1 DC, UPS – VoltEdge`
when VoltEdge does both); a visit recorded by hand can cover several tasks.

Keep system names specific under **Admin, Systems**. A short name like "DC"
also matches titles that mean "data centre".

## Status rules

| Status | Rule (defaults; each task can override) |
| --- | --- |
| Overdue | past next due + 7 days grace, nothing booked |
| Due soon, not booked | next due within 30 days, nothing booked |
| Off-cycle | a visit is booked more than 21 days from next due |
| Booked | a visit is booked within ±21 days of next due |
| On track | none of the above |
| No history | no completed visit and no first due date |

- Every 3 months by default; each task can be monthly, six-monthly, yearly.
- **From the last visit** (default): next due = last PM + interval.
- **Fixed contract slots:** due dates = first slot + n × interval, computed
  from the first slot every time so month ends don't drift.
- Month arithmetic clamps: 30 Nov + 3 months = 28 Feb.
- Past calendar visits count as done until someone marks them missed.
  Cancelled events don't count.

## Users and roles

`auth.users` in the config sets HTTP basic-auth users:

| Role | Can |
| --- | --- |
| viewer | see everything, export CSV |
| editor | also work the review inbox, confirm or mark visits missed, record visits by hand, sync now |
| admin | also manage sites, tasks, vendors, systems, calendars, import, see the activity log |

With no users configured everyone is an admin and the page says so. That's
only for use behind Cloudflare Access, Tailscale or a VPN. Passwords are
plain text in the config for this version, so keep the file `chmod 640`.

## Deploy

Proxmox LXC or any Linux host: `deploy/pmtracker.service` has the install
steps in its header. Prebuilt static binaries are in `dist/`, or build one:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o pmtracker ./cmd/pmtracker
```

Docker: `docker build -t pmtracker .`, then mount your config at `/config`
and a data volume at `/data`.

The data file holds everything managed in the admin pages. Back it up with the
LXC, or with Litestream-style file sync; writes are atomic (temp file + rename).

## Commands

```
pmtracker [-config file | -demo] [serve|check|sync|digest]
  -addr     listen address (overrides config)
  -data     data file (overrides config)
  -today    pretend today is YYYY-MM-DD
  -keep     demo: keep changes across restarts
```

## Code map

```
cmd/pmtracker      flags, wiring, first-run seeding, digest scheduler
internal/config    JSON config: server, Google, defaults, users and roles
internal/core      catalog (sites, systems, vendors, tasks, calendars), matcher,
                   forecaster, snapshot with site and vendor roll-ups, CSV import,
                   digest, ICS feed
internal/gcal      service-account client, events.list paging, syncer with 410 recovery
internal/store     JSON-file store: catalog, mirrored events, decisions, rules,
                   marks, hand-recorded visits, change log, activity log
internal/web       pages, admin handlers, role checks, templates, timeline SVG, CSS
internal/demo      21-site demo catalog and fake calendar
internal/notify    Telegram sender, daily scheduler
```

`go test ./...` covers the matcher across sites (ambiguous vendors, site from
location, Thai names, paused tasks, archived sites), forecasting and month
clamps, site and vendor roll-ups, CSV import, the store's clean-up when tasks
or calendars are deleted, sync against a fake Google API, every page
rendering, roles, and the admin lifecycle (create, rename, add/edit/delete
task, archive, delete).

## Limits and next steps

- Data files from the first single-site POC aren't migrated; start fresh.
- Basic auth with plain-text passwords; Google sign-in or Cloudflare Access
  headers would be the next step.
- No service report or photo uploads per visit yet.
- Polling only; Google push notifications would need a public HTTPS webhook.
