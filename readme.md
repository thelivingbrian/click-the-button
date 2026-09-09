# Click the Button

Weekly interactive events, built with Go and SQLite. The homepage contains one
main event, its temporary discussion, the ballot for next week's main event, and
community submissions. Seven fixed interaction formats are available at `/create`.

## Run locally

Use the Go toolchain in `go.mod`:

```sh
cp -n server/.env.example server/.env
go test -race ./...
cd server
go build -o server .
./server
```

Open http://localhost:8080. Start from `server/` so templates, assets, and data
resolve correctly. `HOST` defaults to `127.0.0.1`; `PORT` defaults to `8080`.
The interface uses system fonts and local assets, with no runtime CDN dependency.
Voting and discussion work with ordinary forms when JavaScript is disabled.
JavaScript adds inline feedback and refreshes visible results every three seconds;
refresh failures are displayed and retried. Draft comments retain focus and text
while results update. Archived pages have no live requests.

## Events and the weekly lifecycle

The first main event asks **Which Millennium Problem will be solved next?** It
excludes the Poincaré conjecture and Navier–Stokes. The latter follows the
[September 8, 2026 solution announcement](https://openai.com/index/navier-stokes-solution/),
which is linked beside the poll. This is not a claim that Clay awarded a prize.
All new event totals start at zero.

The first week begins when the event system is initialized in `data/station.db`.
Its deadline is persisted, displayed in UTC, and advances in seven-day intervals.
A server timer checks deadlines every second. Page refreshes and vote acceptance
also check the deadline, so an old open tab cannot vote after closure.

At the boundary, one SQLite transaction:

1. Freezes the outgoing main event and next-event ballot, including accepted vote
   history and a checksummed JSON export.
2. Deletes their discussion; comments never enter result exports.
3. Promotes the winning community candidate in place, preserving its results,
  creator, and discussion, then extends its deadline through the featured week.
4. Creates a new next-event ballot and updates the persisted schedule.

Ties favor the first listed nominated candidate. Ballots begin empty; an
administrator nominates eligible, live community events directly from the
administration page or event detail page. A candidate must close after the
current main event. Ballot choices remain fixed for their week, except an
administrator may withdraw a nomination. If no candidate remains eligible at
the boundary, the current main event continues for another week. Community
events otherwise close after seven days.

On restart, an overdue round is closed at its stored deadline. One fresh round
opens within the current interval on the original weekly schedule; the app does
not fabricate missed rounds for weeks it was offline. Repeating rotation does
not create duplicate rounds.

## Participation and accounts

Anyone with a browser session can interact with open main and community events
and post discussion. The Poll format accepts one ballot per browser session;
clearing cookies permits another vote. The other formats support repeated
interactions, with a shared token-bucket cooldown of eight initial interactions
and four replenished per second. Each event displays its specific rules.

Only an authenticated account can submit community events or vote on the next
main event. These checks run on the server and in the store, not only in the UI.
Next-event votes are unique per account across browser sessions. Submission
validation checks formats, title and option lengths, duplicate options, and a
limit of three submissions per account per day.

Google sign-in uses the server-side authorization-code flow with PKCE, a
browser-bound single-use state token, a nonce, and validated Google ID tokens.
The app checks Google's signature, issuer, audience, expiry, and verified email.
Only Google's stable subject identifier establishes account identity. OAuth
access tokens and refresh tokens are not persisted.

Set `PUBLIC_BASE_URL`, `GOOGLE_CLIENT_FILE`, and optionally
`BOOTSTRAP_ADMIN_EMAIL` in `server/.env`. The client JSON must authorize the exact
`PUBLIC_BASE_URL/auth/google/callback` URI. Alternatively, supply
`GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET` through the environment. Credentials
under `server/secrets/`, `.env`, and `.env.production` are ignored by Git. Without
credentials the account page explains that sign-in is not configured.

Authentication rotates the browser session identifier while preserving prior
votes, retry identifiers, and the discussion cooldown. Account sessions expire
after 30 days; sign-out removes their authorization without resetting browser
ballots. Sign out everywhere revokes every current session for that account.
Google identity email is private to the account page and administrators; public
comments use an optional display name, never the email address.

The configured bootstrap email can claim the initial admin role only after a
verified Google login from an identity for which Google is authoritative (Gmail
or Workspace). This is recorded once in `bootstrap_admin`. The administrator
role then follows the stable account ID, even if its Google email changes.
Remove `BOOTSTRAP_ADMIN_EMAIL` after that first login. Other signups remain
members; there is no public role-assignment endpoint or test-login bypass.

At `/admin`, administrators can hide/restore community events, nominate eligible
candidates into the current ballot, remove comments, suspend/restore member
accounts, and pause/resume new submissions. Administrator accounts cannot be
suspended through these controls. Every action rechecks the role and session
expiry in its database transaction. The audit log retains actions and target
IDs, never deleted comment text. Hidden events are excluded from public pages,
result downloads, participation, and promotion. Existing ballot votes are
preserved when a candidate is withdrawn; if no eligible candidate remains, the
current main event is extended for another week.

Administrators also see nomination controls on eligible community event detail
pages, trash icons beside comments, community event cards and event headings,
and withdrawal controls on the next-event ballot. Ordinary forms work
without JavaScript; with JavaScript, the icon asks for confirmation before
submitting. The main event has no delete control. Comment deletion removes its
row. Event deletion closes the event, removes its discussion and public access,
and preserves its recorded results internally. Suggestion deletion withdraws
that candidate from the current ballot, retaining existing vote counts and
preventing further votes or promotion. Withdrawn candidates disappear from the
active ballot, and the remaining candidates are renumbered visually without
changing their stored voting indices. Live totals show votes for the remaining
candidates; historical counts remain intact in the sealed ballot export. If every
suggestion is withdrawn, the current main event continues for another week.
Ballot exports record which candidates were withdrawn. Deletion authorization is checked again in the
database transaction.

For HTTPS behind a reverse proxy, configure the exact public origin and preserve
the public Host header. Cookie security and POST origin validation use this
configured origin instead of trusting arbitrary forwarded headers. Other hosts
redirect to the canonical origin on GET; mutations with the wrong host or origin
are rejected. Local development uses `http://localhost:8080`; `127.0.0.1` redirects
to it when authentication is enabled so OAuth cookies remain on one host.

Discussion accepts 1–500 characters, escapes submitted text, enforces a 15-second
interval per browser, and retains at most 50 comments per event. Closure removes
the comments from the application database. Database backups may retain older
copies. Each comment row stores an increasing ID, event ID, browser-session ID,
display name at posting, text, and posting timestamp in `station.db`. The display
name is copied, so later profile edits do not rewrite existing comments. Oldest
comments beyond the 50-row limit are deleted, and deleted IDs are never reused.
The moderation audit stores actor and target IDs without comment text. Deletion
is a database operation, not a guarantee of secure erasure from SQLite pages,
WAL files, or backups. Current production backups have no automatic expiry; set
an operational retention policy when choosing how long old copies should remain.

## Routes and archives

- `/`: current event, discussion, next-event ballot, and community events.
- `/create`: Poll, click contest, tug of war, pulse, 1–10 scale, and star rating
  formats, plus the community submission form.
- `/poll/{id}`: an individual event and its current or frozen results.
- `/archive`: closed event results.
- `/archive/{id}/export`: immutable JSON with a SHA-256 ETag.
- `/legacy/`: frozen Cats vs. Dogs totals as a chart, without interactive controls.
- `/account`: Google sign-in, display name, account role, and sign-out.
- `/admin`: administrator-only moderation and candidate priority controls.

The old `/studio` URL shows Create. Manual archive/rematch POST routes reject all
requests, so a guest cannot close the main event or bypass account restrictions.
Old live click, stream, and metric endpoints return 410 Gone.

The upgrade preserves existing prototype rows and immutable archives in
`station.db`, but hides old demo events from the homepage and public archive list.
New databases do not seed them. Their existing direct URLs and exports continue
to work. The original `data/clicks.db` is never opened by the new event system.

Cats vs. Dogs reads `data/legacy/manifest.json`; downloads serve the frozen
`history.json`. Each archive identifies its production or synthetic provenance.
To create a local snapshot from the retired app, stop its server and run
`python3 scripts/archive-legacy.py` from the repository root. The script checks
port 8080, creates a consistent backup, exports history, records checksums, and
verifies an existing archive without replacing it. Its cutoff is the last
persisted snapshot because the retired app had no atomic final flush.
For production, supply `--production --port 14010 --source /path/to/clicks.db
--target /path/to/legacy --source-revision <legacy-commit>` after stopping the
legacy process. Keep the original database and an off-host copy of the archive.

## Production releases

In GitHub **Actions → Release production → Run workflow**, select **main**.
The workflow tests the selected commit, builds a Linux amd64 binary, and publishes
a `production-*` release with the commit ID and SHA-256 checksum. The droplet
checks GitHub every two minutes, creates a verified SQLite backup, switches the
release directory, restarts the service, and checks the running revision and
legacy archive. Actions succeeds only after the public `/healthz` reports that
commit. Source changes must be merged into `main` before release.

No production credentials or SSH keys are stored in GitHub. Releases are public
and contain only the binary, templates, assets, schema, and commit ID. Downloads
use HTTPS and are checked against the release manifest. Repository maintainers
who can publish releases can deploy production; protect that access accordingly.
This uses GitHub-hosted build machines and an outbound pull from the droplet,
so the existing SSH firewall allowlist remains usable.

The app runs as `click-the-button` under `click-the-button.service`, listening on
`127.0.0.1:14010` behind nginx. Code lives under `/srv/click-the-button/releases/`
and `current` selects the release. Persistent data lives in
`/srv/click-the-button/shared/data/`. Environment and Google credentials live
under `/etc/click-the-button/`; the initial administrator uses the configured
verified bootstrap email. Remove that setting after the first admin login.

The root-owned `deploy/release.py` is installed at
`/usr/local/lib/click-the-button/release.py`. The application user can only use
sudo for `systemctl restart click-the-button.service`. Unit templates are in
`deploy/`; deployer and unit changes require installing those files through SSH.
The release timer starts `click-the-button-release.service`. Inspect deployment
errors using `journalctl -u click-the-button-release.service`; app logs use
`journalctl -u click-the-button.service`.

Every deployment and the daily 04:00 UTC backup timer create a consistent SQLite
snapshot, frozen legacy files, and checksums under `/srv/click-the-button/backups/`.
Backups are retained until an operator removes them. Monitor disk usage and copy
backups off the server; the timers alone do not protect against droplet loss.

If health fails, the deployer switches to the previous binary and restarts it.
It records the failed revision to avoid repeated retries. It never restores a
database automatically because that could discard newly accepted votes. Keep
schema changes compatible with the preceding release. If manual data recovery
is needed, stop the app and release timer, preserve the current database and WAL,
restore a verified backup, select compatible code, and restart. Publish a new
release to recover forward, or explicitly retry a bundle with
`sudo -u click-the-button python3 /usr/local/lib/click-the-button/release.py
--bundle /path/to/release.tar.gz --revision <full-commit>`.

## Persistence and validation

SQLite WAL and serialized write transactions order votes, deduplication,
closure, and promotion. Frozen result rows are protected by database triggers.
Back up `station.db` with SQLite's backup API while running, or stop and checkpoint
before copying it. Include `data/legacy/`. Runtime databases, `.env`, and the
server binary are ignored by Git.

`go test -race ./...` covers existing interaction mechanics, durable archives,
weekly winner selection, repeat rotation, restart/catch-up behavior, per-account
next-event voting across sessions, forged account rejection, community
validation/nomination, discussion bounds and deletion, escaped comments, native
forms, and retired route restrictions. Authentication tests use signed test
identities to verify invalid signatures, issuer/audience/expiry/nonce checks,
PKCE exchange, callback replay, administrator bootstrap, session rotation,
expiration, logout, suspensions, and moderation permissions. This remains a single-process deployment;
interaction history and archive exports need operational sizing before large
public traffic.
