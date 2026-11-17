# Incident log (ISO 31000) and storage for production

Branch `incidents`, November 2026. Two questions: how to log incidents the
ISO risk-management way, and whether the JSON-file storage holds up in
production.

## 1. Incident log

### What ISO 31000 asks, and where it is in the tracker

ISO 31000:2018 is still the current edition (a revision is at committee-draft
stage). It is guidance, not a certifiable standard: it describes a process,
and the organisation sets its own risk criteria. The tracker follows the
process for every incident:

| ISO 31000 clause | In the tracker |
| --- | --- |
| 6.3.4 Risk criteria | Consequence and likelihood scales (1–5, worded for DC and CSR rooms) and four bands: low 1–4, medium 5–9, high 10–16, extreme 20–25, each saying what it calls for. Shown under *How to rate* on every form. One place in code: `internal/core/incidents.go`. |
| 6.4 Risk assessment | Each incident has a category (the risk source), its actual impact, the **worst credible consequence** and the **likelihood of it happening again**. Their product is the rating and its band. |
| 6.5 Risk treatment | Treatment option (reduce, remove the source, share, avoid, retain: the 6.5.2 options), the action, an owner and a target date, then a **residual** rating with the action in place. Closing needs all of them. |
| 6.5 "retain by informed decision" | Closing with the residual risk still high or extreme accepts that risk, so only an admin can; their name is recorded as who closed it. |
| 6.6 Monitoring and review | Measures, a 5 × 5 risk matrix at the current rating, breakdowns by kind and site, repeats, and the daily digest. |
| 6.7 Recording and reporting | A timeline in each incident: every save records what changed and any update typed in; entries can't be edited or removed. Plus the activity log, backups and a CSV export. |

The same fields cover ISO 27001:2022's incident controls, which the activity
log already serves: A.5.25 assessment and decision (kind, rating), A.5.26
response (status, containment, restore time), A.5.27 learning (root cause,
action, lessons, repeats), A.5.28 collection of evidence (the timeline, who
and when, no edits; only admins delete, and that is logged).

### Choices made

- **Incident or near miss.** A near miss had no impact but could have. It is
  rated by its worst credible consequence like any incident, which is what
  makes reporting near misses worthwhile. *Near misses per incident* is a
  measure: more near misses reported per incident means people speak up
  before something hurts.
- **Actual impact and worst credible consequence are separate.** A utility
  outage the generator covered had a minor impact (redundancy lost) but a
  severe credible consequence. Rating only the impact would rate it low.
- **Statuses follow the response:** open → contained → service restored →
  closed. Time to restore and days to close come from them.
- **Findings and incidents stay separate but linked.** A finding is what a PM
  visit turns up before anything happens; an incident is what happened. An
  incident can link to the finding that warned of it (the demo's CNX1 CRAC
  tripped after three findings about the same alarm).
- **Reference numbers** `INC-<year>-<nnn>`, by the year it happened, fixed
  once logged. Audits and reports quote these.
- **The timeline is stored in the incident**, not only in the activity log,
  because the activity log is kept 90 days and incident records are needed
  for longer.
- **A site with incidents can't be deleted**, only archived, so evidence
  isn't lost by accident.
- **Incidents never change PM statuses or the grid**, as with jobs and
  findings.

### Not in this branch (suggested next)

1. **Risk register.** Standing risks per site or across sites ("utility power
   failure at BKK1"), rated and reviewed on a schedule, with incidents linked
   to the risk they realised. Incidents then feed the register's likelihood.
   This is the fuller ISO 31000 picture; the incident log is the evidence it
   needs first.
2. **Attachments:** photos, vendor reports, RCA documents (also the top item
   from the 3 Nov review, for visits). Needs file storage; see part 2.
3. **Immediate alert** on Telegram when an extreme incident is logged, not
   only in the daily digest.
4. **Printable incident report** for one incident (PDF from the browser).
5. The company's own risk matrix, if its risk policy differs.

## 2. Is the JSON file good enough for production?

### Measured

Years of production-like data: 21 sites, 69 PM tasks, quarterly visits plus 8
other events per task a year in the shared calendars (828 events a year),
150 findings a year. On 2 vCPUs; a 1-core container is slower.

| History kept | Events | Findings | File | Each save | Each page |
| --- | --- | --- | --- | --- | --- |
| 1 year | 828 | 150 | 0.9 MB | 11 ms | 18 ms |
| 2 years (the sync window) | 1,656 | 300 | 1.7 MB | 22 ms | 49 ms |
| 5 years | 4,140 | 750 | 4.2 MB | 60 ms | 105 ms |
| 10 years | 8,280 | 1,500 | 8.5 MB | 102 ms | 197 ms |

Incidents add little: dozens a year at a few KB each with their timelines.

### Verdict

**Yes, for now.** At 21 sites, a handful of users and one container, the file
is not the bottleneck. Writes are atomic and fsynced, restores are validated,
backups are daily, and the activity log is kept apart for 90 days. In steady
state (a 2-year calendar window) it stays around 2 MB, with saves around 20 ms
and pages around 50 ms.

What isn't production-grade yet, most important first:

1. **Recovery point.** One backup a day, on the same disk by default: a dead
   disk loses up to a day and possibly the backups too. No code needed: set
   `backup.dir` to a mounted NAS share, add a Proxmox Backup Server job for
   the container, and download a copy now and then.
2. **Restore replaces everything.** Restoring yesterday's file to undo a
   mistake also discards today's incidents and findings. Keep restores for
   disasters; fix mistakes by editing.
3. **PM history older than the sync window can vanish.** Incremental syncs
   keep old events, but when Google invalidates the sync token the tracker
   re-reads only the last `sync_lookback_months` (24) and drops the rest. If
   audits need a full three-year ISO 27001 certification cycle of PM history,
   set it to 36 (about half as much again per page).
4. **Cost grows with everything kept.** Every page rebuilds from all data and
   every change rewrites the whole file. Fine up to about 5 MB.
5. **No place for files.** Photos and reports can't sensibly go in JSON.
6. **One process, one file.** No second instance or failover. Fine for this
   use.

Changed in this branch:

- A calendar sync that finds nothing new no longer rewrites the file (it did
  every 15 minutes per calendar: about 1.7 MB × 96 × calendars a day), and
  the same sync error twice is written once. If the tracker restarts before
  the next write, the older sync token still works and the changes since it
  are fetched again.
- Data version 3. An older tracker refuses a backup that has incidents with
  "this backup is from a newer version" instead of dropping them.

### The next step: SQLite

Move when visit reports and photos are attached, when the file passes about
5 MB, or when more history has to be kept, whichever comes first.

- **modernc.org/sqlite**: pure Go, no CGO, so still one static binary, the
  same Dockerfile and the same LXC. It is the first dependency outside the
  standard library; that is the one decision it needs.
- WAL mode; a table per entity. PM visits become rows, so history no longer
  depends on the calendar mirror. Incident timelines become an insert-only
  table. Pages query what they show instead of rebuilding everything.
- Attachments as files named by their SHA-256 under
  `/var/lib/pmtracker/files`, with their details in SQLite, included in
  backups.
- Backups with `VACUUM INTO` nightly, plus Litestream replicating
  continuously to S3-compatible storage (MinIO on the NAS, or Cloudflare R2):
  the recovery point drops from a day to seconds.
- Migration: import `data.json` once on first start; keep the JSON export for
  backups and moving servers.
- Not PostgreSQL: one more service to run, secure and back up, for a
  few-user tool on one container. Revisit only for several instances or
  other systems reading the data.

## Rollback

`main` doesn't know incidents. Going back to `main` after logging incidents
drops them from the data file on its next save, and `main` refuses backups
made by this branch. Keep `data.json.before-update` (made by `install.sh` and
`push.sh`) or a backup when trying the branch on the production container.

## Decisions for you

1. Does the company already have a risk matrix (scales and bands) in its risk
   policy? If so, the tracker should use it word for word.
2. Who may accept a high or extreme residual risk? Now: any admin. It could
   be a separate "risk owner" or manager role.
3. Is one dependency (modernc.org/sqlite) acceptable when it's time to move?
4. How long are incident records kept? Now: until deleted. The policy may set
   a period (often 3 years or more).
