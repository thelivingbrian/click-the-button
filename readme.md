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
3. Promotes the winning candidate to a fresh main event with empty results.
4. Creates a new next-event ballot and updates the persisted schedule.

Ties, including zero votes, favor the first listed candidate. The initial ballot
contains explicitly labeled editorial candidates. Up to three visible live community submissions fill subsequent ballots before
editorial fallbacks; administrator-prioritized candidates come first, then older
submissions. A
ballot's choices remain fixed for its entire week. Promoting a community event
archives its original results and starts a fresh main round. Community events
otherwise close after seven days.

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

At `/admin`, administrators can hide/restore community events, prioritize
candidates for the following ballot, remove comments, suspend/restore member
accounts, and pause/resume new submissions. Administrator accounts cannot be
suspended through these controls. Every action rechecks the role and session
expiry in its database transaction. The audit log retains actions and target
IDs, never deleted comment text. Hidden events are excluded from public pages,
result downloads, participation, and promotion. Existing ballot votes are
preserved when a candidate is withdrawn; if all candidates are withdrawn, an
editorial fallback keeps a main event available.

For HTTPS behind a reverse proxy, configure the exact public origin and preserve
the public Host header. Cookie security and POST origin validation use this
configured origin instead of trusting arbitrary forwarded headers. Other hosts
redirect to the canonical origin on GET; mutations with the wrong host or origin
are rejected. Local development uses `http://localhost:8080`; `127.0.0.1` redirects
to it when authentication is enabled so OAuth cookies remain on one host.

Discussion accepts 1–500 characters, escapes submitted text, enforces a 15-second
interval per browser, and retains at most 50 comments per event. Closure removes
the comments from the application database. Database backups may retain older
copies. A public deployment still needs durable hosting, an automated backup/restore
process, monitoring, and an operational retention policy for interaction history.

## Routes and archives

- `/`: current event, discussion, next-event ballot, and community events.
- `/create`: Poll, click contest, tug of war, pulse, 1–10 scale, star rating, and
  heat map formats, plus the community submission form.
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
`history.json`. These local results are accurately labeled synthetic development
activity. To create the snapshot from the retired app, stop its server and run
`python3 scripts/archive-legacy.py` from the repository root. The script checks
port 8080, creates a consistent backup, exports history, records checksums, and
verifies an existing archive without replacing it. Its cutoff is the last
persisted snapshot because the retired app had no atomic final flush.

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
