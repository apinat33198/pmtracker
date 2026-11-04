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
- **Ad-hoc jobs:** one-off work at a site outside the PM cycle, such as
  "Repair raised floor": a date, who does it and a note, open or done. Add
  them from a site's page or the Jobs page; open ones are listed on the
  overview below the PM tables, and jobs due today or late go in the daily
  digest. Jobs are kept apart from PM: they never change a PM date, status,
  the grid or the vendor counts. Editors and admins add and close them;
  everything is in the activity log, the backups and the Jobs CSV.
- **Admin:** add, rename, archive and delete sites; add, edit, pause and
  delete PM tasks; set last PM dates; manage vendors, systems and calendars;
  import sites and tasks from CSV; activity log of who changed what. The
  System field on task forms is a search box: type part of a name or other
  name ("PAC" finds CRAC) and pick it, or choose "Add … as a new system". A
  typed name that matches an existing system or its other names always uses
  that system, so it can't add a near-duplicate column.
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

Anyone can look around the demo. To change things, sign in as `admin`,
`editor` or `viewer`, all with the password `demo`.

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
   and `public_url`, and list your calendars (or add them later under
   **Admin, Calendars and sync**).
4. Check, then run:

```sh
pmtracker -config config.json check   # verifies the key and every calendar, prints match counts
pmtracker -config config.json         # serve on :8080, sync every 15 minutes
```

The first start prints a setup link for creating the first admin (see
[Sign-in and users](#sign-in-and-users)).

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

## Sign-in and users

Every page needs a sign-in, and the admin pages need an admin.

| Role | Can |
| --- | --- |
| viewer | see everything, export CSV |
| editor | also work the review inbox, confirm or mark visits missed, record visits by hand, sync now |
| admin | also manage sites, tasks, vendors, systems, calendars, users, import, see the activity log |

**First run.** With no accounts, the server prints a one-time link to its log:

```
No accounts yet. Create the first admin at https://pm.example.com/setup?token=…
```

Open it, pick a username and password, and you're signed in as the first admin.
Every other page redirects to `/setup` until then, and the link stops working
once that account exists. On a server: `journalctl -u pmtracker | grep setup`,
or `docker logs pmtracker`.

**Accounts** live in the data file and are managed under **Admin, Users**: add
someone with a role and a starting password, change roles, set a new password
(signs them out everywhere), or delete them. The last admin can't be demoted
or deleted, and nobody can delete their own account. Each person can change
their password and sign out other devices under **Your account** (their name,
top right).

**How it's protected**

- Passwords are stored as PBKDF2-SHA256 hashes (600,000 iterations, random salt);
  new ones need at least 10 characters.
- Sign-in sets an HttpOnly, SameSite=Lax session cookie signed with a key kept
  in the data file (Secure over HTTPS). Sessions last `auth.session_hours`
  (default 12), or 30 days with **Keep me signed in**. Changing a password,
  an admin setting a new one, or **Sign out everywhere else** ends the
  account's other sessions; deleting an account ends all of them.
- Five wrong passwords for a username from one address block it for 15
  minutes; 20 from one address block that address (IPv6 is counted per /64);
  30 for one username from any mix of addresses block that username. Sign-ins,
  wrong passwords and blocks of real accounts are recorded under **Admin,
  Activity**.
- With an `https://` `public_url`, the tracker won't take a password over plain
  HTTP from another machine (someone opening `http://<LAN address>:8080`): the
  sign-in page points to the HTTPS address instead.
- Form posts from other sites are refused (`Origin` and `Sec-Fetch-Site`
  checks), pages can't be framed, and redirects after sign-in only go to
  pages on this site. Messages shown after an action are signed, so a link
  can't put text on the page.
- Requests are capped at 1 MB (6 MB for the CSV import, 64 KB for sign-in).
- With an `https://` `public_url`, cookies are always Secure and browsers are
  told to use HTTPS only (HSTS).

**Behind a proxy** (nginx, Caddy, cloudflared, Docker): set `public_url` to
the address people use, and list the proxy in `trusted_proxies` so the
lockout counts the real client and not the proxy. The default trusts only
loopback, which covers a proxy on the same host. Under Docker the proxy
usually connects from the bridge network, so add it, for example
`"trusted_proxies": ["127.0.0.1", "::1", "172.16.0.0/12"]`. If you leave it
out there, every visitor shares the proxy's address and one person guessing
passwords can block sign-in for everyone.

**Options** in `auth`:

- `anonymous_access`: `"none"` (default) or `"viewer"` to let people who
  haven't signed in view the pages; changes still need a sign-in. The demo uses
  `"viewer"`. Keep `"none"` on anything reachable from the internet.
- `session_hours`: how long a sign-in lasts without "keep me signed in".
- `users`: optional break-glass accounts defined in the config file. They
  can't be edited from the web, so they still work if the data file's accounts
  are lost. Put a hash in `password`, not the password itself:

  ```sh
  pmtracker hash-password        # type the password; prints pbkdf2-sha256$600000$…
  ```

  Plain-text passwords still work but are logged and flagged on the Users page.

Scripts can use HTTP basic auth with any account, for example
`curl -u reports:… https://pm.example.com/export.csv`. The calendar feed
(`/forecast.ics?token=…`) uses its own token: anyone with the link can read
the forecast, so set `feed.token` to a random value (`openssl rand -hex 24`)
or leave it empty to turn the feed off. The server won't start with the
example value or a token shorter than 24 characters.

**Locked out?** Add a user to `auth.users` in the config with a hashed
password and restart, sign in as them, and set a new password for your
account under Admin, Users.

## Activity log

**Admin, Activity** records who did what, when, and for sign-ins from which
address:
- sign-ins, sign-outs, wrong passwords for real accounts, and lockouts
- account changes
- every change to sites, tasks, vendors, systems and calendars
- inbox decisions and visit confirmations
- imports, backups, restores and downloads

- **Kept for 90 days** (`"activity": {"keep_days": 90}`): today and the 90 days
  before it, then deleted. ISO 27001 (control A.8.15, Logging) leaves the period
  to your own policy; set it to whatever yours says.
- **Stored on its own**, in `data-activity/` next to the data file
  (`/var/lib/pmtracker/data-activity`), one append-only file per day,
  readable by the service only. Nothing in the web interface can edit or delete
  entries, and restoring a backup doesn't change them (the restore itself is
  recorded).
- **A second copy** of every entry goes to the server log:
  `journalctl -u pmtracker | grep activity:`. journald keeps it by size, so make
  sure it has room for 90 days, or forward it to a central log server.
- **Download all (CSV)** on the Activity page exports every entry kept, for
  audits. Downloads are recorded too.
- Times come from the server's clock, which in a Proxmox container is the
  host's, so keep NTP running on the host.
- Root on the container can still delete the files. For logs nobody on the
  tracker's server can change, forward the server log elsewhere, and keep
  Proxmox backups of the container.

## Backups

Everything (sites, tasks, vendors, visits, inbox decisions, users, activity) is
copied every day at 02:30 into `backups/` next to the data file
(`/var/lib/pmtracker/backups` on a server), compressed, readable by the
service only, and kept for 30 days. A copy missed while the tracker was off is
made when it starts.

Under **Admin, Backups** an admin can make a copy now, download one, restore
one (type `restore` to confirm; the current data is saved as a
"before restore" copy first), or restore an uploaded file, which is also how to
move the tracker to a new server.

Backups don't contain the key that signs sign-in cookies, but they do contain
password hashes, so keep downloaded copies private and only restore files you
made. Downloads are recorded under Admin, Activity. A restore never takes a
signing key from the file, and signs everyone out, so sign-ins from before
the restore (including ones of accounts deleted since the backup) stop working.

```json
"backup": {"time": "02:30", "keep_days": 30, "dir": ""}
```

`time` can be `"off"`. To keep copies off the server, download one now and then,
or set `dir` to a mounted share. In a Proxmox container, add it as a mount
point, then let the service write there with `systemctl edit pmtracker`:

```ini
[Service]
ReadWritePaths=/mnt/pm-backups
```

## Deploy

Proxmox LXC or any Linux host: `deploy/pmtracker.service` has the install
steps in its header. Build with a Go release that still gets security fixes
(1.26 or 1.27 as of November 2026; 1.24 and 1.25 are out of support):

```sh
go version   # go1.26.x or go1.27.x
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o pmtracker ./cmd/pmtracker
```

**Proxmox container (LXC) with Cloudflare Tunnel**

The container: unprivileged Debian 12 or 13, 1 core, 512 MB, 4 GB disk, with
**Options, Features, Nesting** turned on. The service's systemd sandboxing
needs it; without it the service fails with `status=226/NAMESPACE`. The
clock comes from the Proxmox host, so keep NTP running there. Proxmox backups
of the container include the data file.

**The quick way, in the container** (as root):

```sh
apt install -y git
git clone https://github.com/apinat33198/pmtracker && cd pmtracker
deploy/install.sh pm.example.com
```

It installs a current Go if needed (checksum-verified), writes `config.json`,
builds, installs the service, sets up the Cloudflare Tunnel (you approve one
login link in a browser) and prints the link for creating the first admin.
Put `service-account.json` in that folder first if you have it; without it
the tracker runs and syncs calendars once the key is added. To update later:
`git pull && deploy/install.sh pm.example.com`.

From your Mac instead: `deploy/install.sh root@<container IP> pm.example.com`
does the same over SSH (the Mac's key has to be on the container).

The same by hand:

1. In `config.json`: `"listen": "127.0.0.1:8080"` so nothing reaches the
   tracker except through the tunnel, `"public_url": "https://pm.example.com"`,
   `"data_file": "/var/lib/pmtracker/data.json"`. Leave `trusted_proxies` at
   its default: cloudflared in the same container connects from loopback, and
   the lockout uses the visitor's address from Cloudflare.
2. Build on your Mac (`GOOS=linux GOARCH=amd64`, see above), copy the binary,
   config, key and `deploy/pmtracker.service` to the container, and follow the
   install steps at the top of the unit file. Copy the setup link:
   `journalctl -u pmtracker | grep setup`.
3. Create the tunnel. In the Zero Trust dashboard: Networks, Tunnels, create a
   cloudflared tunnel, run its install command in the container, and add a
   public hostname `pm.example.com` → `HTTP` `127.0.0.1:8080` (not
   `localhost`, which can resolve to IPv6 first). Or from the command line
   with `deploy/cloudflared.yml`.
4. Open the setup link on `https://pm.example.com/setup?token=…` and create
   the first admin.
5. Recommended: put a Cloudflare Access application on `pm.example.com`
   (allow your company email domain, one-time PIN or Google login). That adds a
   second check in front of the tracker's own sign-in. If you use the forecast
   feed, add a second Access application for the path `forecast.ics` with a
   **Bypass** policy, since Google Calendar fetches it without signing in.

Updates from your Mac: `deploy/push.sh root@pm-tracker` runs the tests,
builds, copies the binary, keeps a copy of the data file as
`data.json.before-update`, and restarts the service.

If cloudflared already runs on **another machine or container**, the tracker has to listen
on its own address instead: `"listen": "0.0.0.0:8080"`, list the cloudflared
container's IP in `trusted_proxies`, point the tunnel at
`http://<tracker container IP>:8080`, and use the Proxmox firewall on the
tracker container to allow port 8080 from the cloudflared container only, so
nobody on the LAN can bypass Cloudflare Access.

**Before going live**

- HTTPS in front (Caddy, nginx or Cloudflare Tunnel), `public_url` set to the
  `https://` address, the proxy listed in `trusted_proxies` if it isn't on the
  same host.
- `feed.token` random or empty; Telegram bot token and service-account key
  readable only by the service (`chmod 640`, owner root, group pmtracker).
- First admin created from the setup link, then one account per person
  under Admin, Users. No shared accounts; config-file users only as break-glass,
  with hashed passwords.
- Back up the data file. It holds password hashes and the session key, so keep
  backups private. If it leaks, rotate: stop the service, delete
  `session_secret` from the file (everyone signs in again), and reset passwords.

Docker: `docker build -t pmtracker .`, then mount your config at `/config`
and a data volume at `/data`.

The data file holds everything managed in the admin pages. Back it up with the
LXC, or with Litestream-style file sync; writes are atomic (temp file + rename).

## Commands

```
pmtracker [-config file | -demo] [serve|check|sync|digest]
pmtracker hash-password            read a password, print its hash for auth.users
  -addr     listen address (overrides config)
  -data     data file (overrides config)
  -today    pretend today is YYYY-MM-DD
  -keep     demo: keep changes across restarts
```

## Code map

```
cmd/pmtracker      flags, wiring, first-run seeding, digest scheduler
internal/auth      password hashing, signed session cookies, failed sign-in limiter
internal/config    JSON config: server, Google, defaults, auth options, roles
internal/core      catalog (sites, systems, vendors, tasks, calendars), matcher,
                   forecaster, snapshot with site and vendor roll-ups, ad-hoc
                   jobs, CSV import, digest, ICS feed
internal/gcal      service-account client, events.list paging, syncer with 410 recovery
internal/store     JSON-file store: catalog, mirrored events, decisions, rules,
                   marks, hand-recorded visits, ad-hoc jobs, change log, accounts and the
                   session key; the activity log in its own daily files
internal/backup    daily backups, pruning, restore input
internal/web       pages, sign-in, setup, accounts, admin handlers, role checks,
                   templates, timeline SVG, CSS
internal/demo      21-site demo catalog and fake calendar
internal/notify    Telegram sender, daily scheduler
```

`go test ./...` covers the matcher across sites (ambiguous vendors, site from
location, Thai names, paused tasks, archived sites), forecasting and month
clamps, site and vendor roll-ups, CSV import, the store's clean-up when tasks
or calendars are deleted, sync against a fake Google API, every page
rendering, roles, the admin lifecycle (create, rename, add/edit/delete
task, archive, delete), and sign-in: sessions, tampered cookies, cross-site
posts, open redirects, lockout, first-run setup, user management and the
last-admin guard.

## Limits and next steps

- Data files from the first single-site POC aren't migrated; start fresh.
- Sign-in is username and password; Google sign-in (OIDC) or two-factor
  would be the next step for exposure to the internet.
- No service report or photo uploads per visit yet.
- Polling only; Google push notifications would need a public HTTPS webhook.
