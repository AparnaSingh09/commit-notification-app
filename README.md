# Commit Notification App

A web app to log in with Bitbucket, subscribe to repositories, and see a feed of new
commits with AI-generated one-line summaries.

> **This README is kept up to date as the app is built.** The "What works today"
> section and the roadmap checklist reflect the current state, not the end goal.

---

## What works today

- ✅ **Bitbucket OAuth login** — click "Login with Bitbucket" on the frontend,
  approve access, and you're logged in. The backend exchanges the OAuth code,
  fetches your Bitbucket profile, stores it (with encrypted tokens) in MongoDB,
  and issues a session cookie.
- ✅ **Session check** — the frontend calls `/api/me` on load to know whether
  you're logged in, and shows your name/avatar if so.
- ✅ **Health/infra plumbing** — Go backend ↔ MongoDB (single-node replica set,
  via Docker) ↔ React/Vite frontend all run and talk to each other locally.
- ✅ **Manage Repos** — once logged in, add a repo by `workspace` + `repo-slug`;
  the backend validates it exists and you have access via the Bitbucket API
  before saving it, and you can list/remove your subscriptions. Capped at
  `maxReposPerUser` (default 10) per user — mainly to protect the
  per-tick poll budget (see below) from being monopolized by one user.
- ✅ **Commit polling** — a background worker checks every actively-subscribed
  repo every `POLL_INTERVAL_SECONDS` (default 60s) for new commits and stores
  them. The first poll of a newly subscribed repo only records a baseline
  (no backfill of old history, per the earlier cost decision) - only commits
  from that point on get stored.
- ✅ **AI summaries, diff-aware (pluggable provider)** — a second background
  worker picks up commits with `summaryStatus: "pending"` and asks an LLM for
  a one-sentence summary of **the commit message plus its diff** (fetched
  once, capped at 20KB - see `bitbucket.FetchDiff`; falls back to
  message-only if the diff is too large or the fetch fails). Toggle this
  off entirely with `summarizeWithDiff: false` in `config.json` to go back
  to message-only summaries (and skip the extra Bitbucket call per commit).
  This means
  summaries reflect what the code actually changed, not just what the commit
  message claims - e.g. a message like "Add print statement" becomes "Add Go
  main function with fmt.Println test print statement" once the diff is
  factored in. `summaryProvider` in `config.json` picks **Claude** (Haiku 4.5) or
  **Groq** (Qwen, open-weight, free tier) — same interface, same retry
  behavior either way (up to 3 attempts before `"failed"`). **Requires the
  matching API key in `.env`** — without it, the backend logs a warning and
  skips this worker; commits just stay `"pending"` until it's set.
- ✅ **Commit Feed** — once logged in, see commits across all your subscribed
  repos in one feed: AI summary, which repo, author, timestamp, and a link to
  the commit on Bitbucket, newest first, with "Load more" pagination. A
  multi-select repo filter dropdown narrows the view to one or more repos
  (client-side, over whatever's been loaded so far) - defaults to "All"
  every time, deliberately not persisted across reloads/re-logins.
- ✅ **Real UI design pass** — the frontend has moved past the default Vite
  starter styling: a proper app header, card-based sections, status badges for
  pending/failed summaries, a real login screen, and light/dark theme support
  (follows the OS setting). No new dependencies — plain CSS with design tokens
  in `index.css`, components styled in `App.css`.
- ✅ **Logout** — clears the session cookie server-side (`POST /auth/logout`),
  with a "Log out" button in the header. Before this, restarting the backend
  was the only way to end a session.
- ✅ **Fully Dockerized** — `docker-compose.full.yml` runs the entire app
  (Mongo + backend + frontend) in containers with one command; no Go/Node/nvm
  install needed. See [Running it](#running-it-locally) below.

**All six planned build steps are done, plus logout and full Dockerization.**
What's left is further polish — see [Roadmap](#roadmap) and
[Possible Enhancements](#possible-enhancements) for ideas (webhooks instead of
polling, automated tests, etc.) — not core functionality.

---

## Architecture

```mermaid
graph TD
    User((User's browser))

    subgraph Frontend["Frontend — React + Vite (:5173)"]
        FE[App.jsx]
    end

    subgraph Backend["Backend — Go + gin (:8080)"]
        Auth["/auth/login, /auth/callback\n(OAuth + JWT session)"]
        Me["/api/me"]
        Repos["/api/repos\n(add/list/remove subscriptions)"]
        Feed["/api/commits\n(feed, paginated)"]
        Worker["polling worker\n(every POLL_INTERVAL_SECONDS)"]
        Summarizer["summary worker\n(every SUMMARY_INTERVAL_SECONDS)"]
    end

    Mongo[(MongoDB\nusers / repos /\nrepoSubscriptions / commits)]
    Bitbucket[[Bitbucket OAuth + REST API]]
    Claude[[Claude or Groq API\nper SUMMARY_PROVIDER, message-only]]

    User -->|loads page| FE
    FE -->|fetch, credentials: include| Auth
    FE -->|fetch| Me
    FE -->|fetch| Repos
    FE -->|fetch| Feed

    Auth -->|redirect, code exchange| Bitbucket
    Auth --> Mongo
    Me --> Mongo
    Repos --> Mongo
    Repos -->|validate repo access| Bitbucket
    Worker -->|poll for new commits| Bitbucket
    Worker --> Mongo
    Summarizer --> Mongo
    Summarizer -->|summarize commit message| Claude
    Feed --> Mongo
```

### How login works today

```mermaid
sequenceDiagram
    participant B as Browser
    participant FE as Frontend (:5173)
    participant BE as Backend (:8080)
    participant BB as Bitbucket
    participant DB as MongoDB

    B->>FE: Click "Login with Bitbucket"
    FE->>BE: GET /auth/login
    BE->>BE: generate state, set oauth_state cookie
    BE-->>B: 302 redirect to Bitbucket authorize URL
    B->>BB: Approve access
    BB-->>B: 302 redirect to /auth/callback?code=...&state=...
    B->>BE: GET /auth/callback
    BE->>BE: verify state matches cookie
    BE->>BB: exchange code for access + refresh token
    BB-->>BE: tokens
    BE->>BB: GET /2.0/user (with access token)
    BB-->>BE: Bitbucket profile
    BE->>DB: upsert user (tokens encrypted, AES-GCM)
    BE->>BE: issue signed session JWT
    BE-->>B: 302 redirect to frontend, Set-Cookie: session
    B->>FE: loads frontend
    FE->>BE: GET /api/me (cookie sent automatically)
    BE->>DB: look up user by id in JWT
    BE-->>FE: { username, displayName, avatarUrl }
    FE-->>B: shows "Welcome, <name>"
```

### How adding a repo works today

```mermaid
sequenceDiagram
    participant FE as Frontend
    participant BE as Backend
    participant BB as Bitbucket API
    participant DB as MongoDB

    FE->>BE: POST /api/repos { workspace, repoSlug }
    BE->>DB: load user, get valid access token (refresh if expired)
    BE->>BB: GET /2.0/repositories/{workspace}/{repoSlug}
    alt not found or no access
        BB-->>BE: 404
        BE-->>FE: 404 "repo not found or you don't have access"
    else exists and accessible
        BB-->>BE: 200 repo details
        BE->>DB: transaction: upsert shared "repos" doc + insert repoSubscription
        DB-->>BE: ok (or duplicate-key if already subscribed)
        BE-->>FE: 201 created / 409 already subscribed
    end
```

### How commit polling works today

```mermaid
sequenceDiagram
    participant W as Poller (every POLL_INTERVAL_SECONDS)
    participant DB as MongoDB
    participant BB as Bitbucket API

    W->>DB: find repos with >=1 active subscriber
    loop each repo
        W->>DB: get a subscriber's user + valid token (refresh if expired)
        W->>BB: GET commits (newest first)
        alt first ever poll for this repo
            W->>DB: set lastSeenCommitHash = newest (no backfill)
        else has a previous cursor
            W->>BB: page further if needed, collecting commits until\nthe previous cursor commit is found
            loop each new commit
                alt SUMMARIZE_WITH_DIFF=true (default)
                    W->>BB: GET diff for this commit
                    Note over W,BB: diff over the 20KB cap, or the fetch failing,\njust means no diff - not a failed insert
                else SUMMARIZE_WITH_DIFF=false
                    Note over W: skip the diff fetch entirely
                end
                W->>DB: upsert commit (message + diff if captured), summaryStatus="pending"
            end
            W->>DB: advance lastSeenCommitHash to the newest
        end
    end
```

### How AI summaries work today

```mermaid
sequenceDiagram
    participant S as Summary worker (every SUMMARY_INTERVAL_SECONDS)
    participant DB as MongoDB
    participant C as Claude or Groq (per SUMMARY_PROVIDER)

    S->>DB: find commits where summaryStatus = "pending"
    loop each pending commit
        S->>C: commit message + diff (if one was captured and fit the size cap)
        alt success
            C-->>S: one-sentence summary
            S->>DB: set aiSummary, summaryStatus = "done"
        else error
            S->>DB: attempts += 1
            alt attempts >= 3
                S->>DB: summaryStatus = "failed" (stop retrying)
            else
                Note over S,DB: stays "pending" - retried next tick
            end
        end
    end
```

### How the Commit Feed works today

```mermaid
sequenceDiagram
    participant FE as Frontend
    participant BE as Backend
    participant DB as MongoDB

    FE->>BE: GET /api/commits?limit=30&before=<cursor?>
    BE->>DB: find the user's repoSubscriptions -> repoIds
    BE->>DB: commits where repoId in repoIds (and committedAt < before),\nsorted newest-first, joined with repos for workspace/repoSlug
    DB-->>BE: page of commits
    BE-->>FE: { commits: [...], nextCursor }
    FE-->>FE: render feed; "Load more" re-fetches with before = nextCursor
```

---

## Tech stack

| Layer | Choice |
|---|---|
| Backend | Go, [gin](https://github.com/gin-gonic/gin), `golang.org/x/oauth2` |
| Database | MongoDB (`go.mongodb.org/mongo-driver/v2`), run as a single-node replica set (needed for later transactions) |
| Frontend | React (Vite), plain `fetch` — no state library |
| AI summaries | Claude (Haiku 4.5) or Groq (`qwen/qwen3.8-27b`) — pluggable via `config.json`'s `summaryProvider`, commit message + diff (capped, with fallback) |
| Auth | Bitbucket OAuth2 → our own signed JWT session cookie |

---

## Requirements

**Option A (just run the app):** Docker only.

**Option B (development mode):**

| Tool | Version | Why |
|---|---|---|
| Docker | any recent | runs MongoDB |
| Go | 1.26+ | backend |
| Node.js | **≥22.12** in `frontend/` | Vite 8's bundler needs it — use `nvm use` (reads `frontend/.nvmrc`) |

---

## Running it locally

There are two ways to run this, depending on what you're doing.

### Option A — Just run the app (recommended if you're not changing code)

One command, no Go/Node/nvm install needed — everything (Mongo, backend,
frontend) runs in containers:

```bash
cd commit-notification-app
cp backend/.env.example backend/.env   # fill in real values - see Environment variables below
docker compose -f docker-compose.full.yml up -d --build
```

Open `http://localhost:5173` and log in with Bitbucket.

**Stopping:** `docker compose -f docker-compose.full.yml down` (add `-v` only
if you intend to wipe stored data).

### Option B — Development mode (editing backend/frontend code)

Only MongoDB runs in Docker; the backend and frontend run directly on your
machine so code changes take effect immediately (no image rebuild):

```bash
# 1. Database
cd commit-notification-app
docker compose up -d          # starts MongoDB as a single-node replica set

# 2. Backend (new terminal)
cd commit-notification-app/backend
go run ./cmd/server            # http://localhost:8080

# 3. Frontend (new terminal)
cd commit-notification-app/frontend
nvm use                        # picks up Node 22.12.0 from .nvmrc
npm run dev                    # http://localhost:5173
```

Open `http://localhost:5173` and log in with Bitbucket.

**Stopping:** `Ctrl+C` the backend/frontend; `docker compose down` for Mongo
(add `-v` only if you intend to wipe stored data).

**Why two separate compose files, not one?** MongoDB's replica set has to know
its own member address, and that address is different depending on who's
connecting: containers reach Mongo as `mongo` (Docker's internal DNS), but a
backend running directly on your machine needs `localhost`. The two modes
each initialize the replica set with the address that mode needs, so they use
separate containers/volumes rather than one trying to serve both.

---

## Docker in detail

What actually gets built and how the containers fit together, for Option A
(`docker-compose.full.yml`).

```mermaid
graph LR
    Browser((Your browser))

    subgraph Host["Your machine"]
        subgraph Net["Docker network: commit-notification-app_default"]
            FE["frontend container\nnginx :80"]
            BE["backend container\nGo binary :8080"]
            DB[("mongo container\n:27017, not published to host")]
            Init["mongo-init container\n(runs once, then exits)"]
        end
    end

    Browser -->|":5173 → :80"| FE
    Browser -->|":8080"| BE
    BE -->|"mongo:27017"| DB
    Init -.->|"rs.initiate() on first run"| DB
    BE -->|"HTTPS"| BB[[Bitbucket API]]
    BE -->|"HTTPS"| CL[[Claude API]]
```

**`backend/Dockerfile`** — two stages: `golang:1.26-alpine` compiles a static
binary (`CGO_ENABLED=0`), then a bare `alpine:3.20` image just runs that
binary. No Go toolchain, source code, or build cache ships in the final
image — just the compiled server plus CA certificates (needed for the
outbound HTTPS calls to Bitbucket and Claude).

**`frontend/Dockerfile`** — two stages: `node:22-alpine` runs `npm ci` +
`npm run build` to produce the static `dist/` bundle (with `VITE_API_BASE_URL`
baked in as a build arg), then `nginx:alpine` serves that static output.
No Node, npm, or `node_modules` ships in the final image.

**`docker-compose.full.yml`** wires four containers together with an explicit
startup order (`depends_on` + healthchecks), so `up -d --build` alone gets
everything into a working state without a manual wait-and-retry:
1. `mongo` starts, healthcheck waits until it responds to pings.
2. `mongo-init` runs once (`rs.initiate(...)`) and exits successfully.
3. `backend` starts only after `mongo-init` has completed - connects using
   `mongo:27017` (Docker's internal DNS resolves the service name `mongo` to
   that container's address; nothing outside this Docker network can reach
   it, which is why the compose file doesn't publish Mongo's port to the host
   in this mode).
4. `frontend` starts, serving the pre-built static bundle - it doesn't call
   the backend itself, so it isn't blocked on anything; the *browser* is what
   calls the backend, directly, using the `VITE_API_BASE_URL` baked in at
   build time.

**Useful commands:**

```bash
# View logs (add -f to follow)
docker compose -f docker-compose.full.yml logs backend
docker compose -f docker-compose.full.yml logs frontend

# Rebuild after changing code (images aren't rebuilt automatically)
docker compose -f docker-compose.full.yml up -d --build

# Get a shell in a running container (for poking at things)
docker exec -it cna-full-backend sh

# Full teardown, including the Mongo data volume
docker compose -f docker-compose.full.yml down -v
```

**Data persistence:** Mongo's data lives in the named volume
`cna-full-mongo-data`, separate from the dev-mode volume used by
`docker-compose.yml` - the two modes never share data (see "Why two separate
compose files" above). A plain `down` (no `-v`) keeps the volume, so
`up -d --build` again picks up right where you left off.

**`config.json` vs `.env` inside the container:** `config.json` gets copied
into the backend image at build time (it's just a file in the build context,
same as the source code) - changing it requires an image rebuild
(`up -d --build`) to take effect. `.env` is the opposite: it's injected at
container *start* via `env_file`, so editing it only needs a restart
(`up -d`, no `--build`) to pick up the new values.

---

## Configuration

Split deliberately into two files, in `backend/`:

- **`.env`** — secrets and deployment-specific values (never committed; gitignored once this becomes a real git repo).
- **`config.json`** — non-secret application settings (model choice, retry counts, safety limits). Safe to commit — no secrets in it — and easier to discover/review than the same values buried in a gitignored env file. Ships with the repo pre-populated with sensible defaults; missing the file entirely, or missing individual fields in it, just falls back to the same hardcoded defaults (see `config.defaultFileSettings`), so it's never required to exist.

Both are found by searching upward from wherever the process starts (same mechanism, so both are equally robust to being launched from `backend/`, `backend/cmd/server/`, or an IDE run configuration that defaults to the package folder).

### `.env` — secrets & deployment values

See `backend/.env.example` for the full template. Required for OAuth login to work:

| Variable | What it is |
|---|---|
| `BITBUCKET_CLIENT_ID` / `BITBUCKET_CLIENT_SECRET` | From your Bitbucket OAuth consumer (workspace Settings → OAuth consumers) |
| `BITBUCKET_CALLBACK_URL` | Must exactly match the consumer's registered Callback URL (scheme/host/port/path/trailing-slash all matter) |
| `JWT_SECRET` | Random string used to derive the key that encrypts stored Bitbucket tokens at rest. **Not** used to sign session cookies (see note below). |
| `MONGO_URI` / `MONGO_DB` | Defaults work for the local Docker setup |
| `ANTHROPIC_API_KEY` | From the [Anthropic Console](https://console.anthropic.com/). Required when `config.json`'s `summaryProvider` is `"claude"`. |
| `GROQ_API_KEY` | From [console.groq.com](https://console.groq.com/) — free tier, no card required. Required when `summaryProvider` is `"groq"` (the default). |
| `FRONTEND_URL` | Where the browser is sent after OAuth login, and the CORS-allowed origin. Default `http://localhost:5173`. |

**Frontend build-time variable (not in `.env`):** `VITE_API_BASE_URL` — where the
browser should reach the backend. Baked into the JS bundle at build time
(Vite convention), so it's passed as a Docker build arg (see
`docker-compose.full.yml`) rather than read at runtime. Defaults to
`http://localhost:8080` for local `npm run dev`.

`.env` holds real secrets and should never be committed; `.env.example` is the
safe-to-commit template (it no longer lists the settings that moved to
`config.json` below — setting them as env vars does nothing now).

### `config.json` — application settings

| Field | What it is |
|---|---|
| `summaryProvider` | `"claude"` or `"groq"` — picks which LLM generates commit summaries. Default `"groq"` (no billing wall, open-weight, free tier — see [Possible Enhancements](#possible-enhancements)'s Cost section for why). |
| `claudeModel` | Default `"claude-haiku-4-5"` — cheapest tier, plenty for a one-sentence summary. |
| `groqModel` | Default `"qwen/qwen3.8-27b"`. **Careful changing this** — Groq's hosted model catalog changes over time, and reasoning-style models (e.g. `openai/gpt-oss-*`, `qwen/qwen3.6-27b`) don't work well here: they spend their token budget "thinking" before answering and can come back with an empty summary. Check `GET /openai/v1/models` against your key before picking a different one, and sanity-check the actual output, not just that the request succeeds. |
| `summarizeWithDiff` | `true` (default) or `false` — whether the poller fetches each commit's diff for the summarizer to use. `false` = message-only, one fewer Bitbucket API call per commit. Doesn't affect the automatic per-commit fallback to message-only when a diff is too large or fails to fetch - that still happens when this is `true`. |
| `pollIntervalSeconds` | How often the commit-polling worker checks each subscribed repo (default `60`) |
| `summaryIntervalSeconds` | How often the summary worker checks for pending commits (default `15`) |
| `maxCommitsPerRepoPerPoll` | Default `200`. Soft cap per repo per poll (may overshoot by up to one page, currently 50, since the cap is only checked at page boundaries - see `collectNewCommits`). A repo with more new commits than this keeps catching up incrementally over subsequent ticks rather than losing anything. |
| `maxCommitsPerPollTick` | Default `500`. Total commits collected across *all* repos in one poll tick - once hit, remaining repos are simply checked on the next tick instead of piling more work into this one. |
| `maxDiffBytes` | Default `20000`. Per-commit diff size cap - see `summarizeWithDiff`. |
| `maxSummaryAttempts` | Default `3`. Retries for a *retryable* summary failure before giving up (permanent errors - bad request, auth, billing - never retry at all, regardless of this value). |
| `maxSummariesPerTick` | Default `20`. Caps how many LLM calls the summary worker fires in one tick, so a large backlog of pending commits (e.g. after being offline for a while) drains gradually instead of bursting. |
| `feedDefaultLimit` / `feedMaxLimit` | Defaults `30` / `100`. The Commit Feed API's default and maximum page size (`?limit=`). |
| `maxReposPerUser` | Default `10`. Max repos a single user can subscribe to - `AddRepo` returns `409` once hit. |

> **Session behavior:** session cookies are signed with a random secret
> generated fresh every time the backend starts, not with `JWT_SECRET`. That
> means **restarting the backend logs everyone out**, on top of the explicit
> "Log out" button now in the header. Within one backend run, a login stays
> valid normally (24h, survives frontend restarts / page refreshes). This was
> originally a stand-in for not having a logout button at all; now that
> logout exists, keeping restart-forces-relogout on top of it is a judgment
> call worth revisiting for multi-user use — see
> [Possible Enhancements](#possible-enhancements).

---

## Project structure

```
commit-notification-app/
├── docker-compose.yml       — MongoDB only, for development mode (Option B)
├── docker-compose.full.yml — whole app in containers, for just running it (Option A)
├── backend/
│   ├── Dockerfile
│   ├── config.json          — non-secret application settings (safe to commit)
│   ├── cmd/server/          — entrypoint
│   └── internal/
│       ├── auth/            — OAuth flow, JWT session, logout, middleware, token refresh
│       ├── bitbucket/       — Bitbucket API client (user profile, repo lookup)
│       ├── config/          — loads .env (secrets) + config.json (app settings)
│       ├── crypto/          — AES-GCM token encryption
│       ├── db/              — Mongo connection, indexes, models
│       ├── repos/           — Manage Repos handlers (add/list/remove)
│       ├── commits/         — polling worker + Commit Feed API handler
│       ├── claude/          — Claude API client + summary worker (+ Summarizer interface)
│       └── groq/            — Groq API client (alternate summary provider)
└── frontend/
    ├── Dockerfile
    ├── nginx.conf           — serves the built SPA in the container
    └── src/
        ├── api/client.js    — fetch wrapper for the backend
        ├── components/ManageRepos.jsx — add/list/remove repo subscriptions
        ├── components/CommitFeed.jsx  — commit feed with AI summaries
        └── App.jsx          — login button / logged-in profile + repos + feed
```

---

## Roadmap

- [x] **Step 1 — Scaffolding**: backend, frontend, MongoDB all run and talk to each other.
- [x] **Step 2 — Bitbucket OAuth login**: redirect → callback → session cookie, tokens stored encrypted, `/api/me` works.
- [x] **Step 3 — Manage Repos**: add/list/remove a repo subscription by workspace/repo-slug, validated against the Bitbucket API.
- [x] **Step 4 — Commit polling**: background loop detects new commits on subscribed repos.
- [x] **Step 5 — Claude summaries**: one-sentence summary per commit (message-only, Haiku 4.5), stored via a background worker.
- [x] **Step 6 — Commit Feed**: frontend page listing commits across subscriptions with summary, author, timestamp, and a link to Bitbucket.
- [x] **Logout**: `POST /auth/logout` clears the session cookie; header has a "Log out" button.
- [x] **Fully Dockerized**: `docker-compose.full.yml` runs the whole app in containers, one command, no local Go/Node install needed.

**Core app is feature-complete and end-to-end runnable.** Ideas for further work, no particular order — see the [Enhancements](#possible-enhancements) section below for the fuller writeup:

- [ ] Webhooks instead of polling, for lower latency and less API usage (was explicitly deferred in planning).
- [ ] Retryable vs. permanent error distinction in the summary worker (right now a billing/config error burns through retries the same as a transient network blip).
- [ ] Auto-refresh or WebSocket/SSE push for the Commit Feed (right now it only loads on page load / "Load more").
- [ ] Automated tests (unit + integration) — everything so far has been verified manually or via throwaway scratch tests.
- [ ] Structured logging / basic metrics (poll success rate, summary latency, API error rates).
- [ ] Ephemeral session secret means a backend restart force-logs-out everyone — fine for one user, worth reconsidering for multi-user or production use.

Decisions already locked in: MongoDB (not Postgres), polling before webhooks,
message+diff summaries (capped at 20KB, falls back to message-only - revised
from the original message-only-only decision once Groq's free tier removed
the original cost concern), no backfill on new subscriptions, simple
expiring JWT (no server-side revocation).

---

## Possible Enhancements

Not needed for the app to work end-to-end — ideas if you want to keep going.

**Latency / real-time feel**
- Bitbucket webhooks instead of polling: push instead of pull, near-instant instead of up-to-`pollIntervalSeconds` delay, fewer wasted API calls. Deferred at the start specifically because it needs a public callback URL (a tunnel like ngrok for local dev) — worth it now that the app runs somewhere more permanent.
- The Commit Feed only refreshes on page load / "Load more" — a poll-on-interval or a WebSocket/SSE push from the backend would make new commits appear without a manual refresh.
- No "new since your last visit" marker — the feed is a flat chronological list with no read/unread concept, so after a long absence you just see more pages to scroll through rather than a highlighted "here's what's new since you were last here."

**Data retention**
- Commits are **never pruned** — the `commits` collection grows forever. The Commit Feed API is paginated (`feedDefaultLimit`/`feedMaxLimit` in `config.json`) so the UI itself doesn't degrade, but the database will keep growing indefinitely with no cleanup. If this matters to you, options include a Mongo TTL index (auto-expire commits past some age) or a periodic cleanup job - not implemented, since "how long should history be kept" is a product decision, not a technical default worth guessing at.

**Reliability**
- ✅ Done: **retryable vs. permanent error handling** — `internal/llmerr.APIError.Retryable()` classifies provider errors by status code (429/5xx retryable, everything else - bad request, auth, billing - permanent). A permanent error now fails immediately instead of wasting the full retry budget; verified against the three real errors this project actually hit (unscoped key, insufficient credits, bad model ID - all correctly fail fast now).
- ✅ Done: **multi-subscriber token fallback** — the poller tries every subscriber of a shared repo (oldest subscription first, and confirms each token actually works via a real API call, not just that it refreshes) until one succeeds, instead of being pinned to a single arbitrary subscriber. One person losing access no longer stops polling for everyone else subscribed to the same repo.
- ✅ Done: **no more silent commit loss on a large backlog** — this was a real correctness bug, not just a missing safety cap: capping a poll used to still jump the cursor straight to the newest commit, permanently skipping everything between the cap boundary and the true previous cursor (since Bitbucket only pages newest→oldest, restarting from the tip next tick would've just rediscovered the same newest commits forever, never reaching the gap). Fixed with resumable pagination (`Repo.CatchUpResumeURL`/`CatchUpNewest`) - a large backlog is now walked incrementally, possibly across many ticks, with nothing lost or duplicated. Verified with a fake multi-page server forcing a stop-and-resume mid-backlog.
- ✅ Done: **per-tick caps**, both configurable (see [Configuration](#configuration)) - `maxSummariesPerTick` bounds how many LLM calls the summary worker fires per tick, and `maxCommitsPerPollTick` bounds total commits collected across all repos per poll tick. Both exist specifically for the "returning after a long time" scenario: a big backlog now drains gradually across ticks instead of firing an unbounded burst of API calls in one go.
- ✅ Done: **Bitbucket rate-limit handling** — a 429 is now detected explicitly (honoring `Retry-After` when present), and the offending repo is skipped in the poll loop until the backoff window passes (`Repo.RateLimitedUntil`), instead of being treated as a generic error and retried on the very next tick regardless.
- No automated test suite persists in the repo — verification has been done with throwaway tests written, run, and deleted in the same pass (including for everything above). A real, kept test suite would catch regressions before they reach you, rather than only when something's actively being changed.

**Auth / sessions**
- Session cookies are signed with a secret regenerated every backend restart (see the note in Environment variables) — this predates the logout button and made sense when restarting was the only way to log out. Now that a real logout endpoint exists, it's worth deciding whether restart-forces-relogout is still wanted: it means everyone gets logged out simultaneously on every deploy/restart, which matters once this isn't just you testing locally.

**Cost / provider flexibility**
- ✅ Done: summaries can now run on Claude or Groq (open-weight Llama), switchable via `summaryProvider` in `config.json` — see `internal/groq/client.go` and the `claude.Summarizer` interface in `internal/claude/worker.go`.
- Still open: a third option (e.g. local Ollama) if you ever want to run entirely without any hosted API — deliberately not pursued for the Dockerized "anyone can run it" goal (see the earlier discussion on why local model hosting doesn't fit that).

**Ops / observability**
- Structured logging and basic metrics (poll success/failure rate, summary latency, API error counts) would make it much easier to tell what's actually happening in the background workers without tailing raw logs.
- No CI — a GitHub Actions workflow running `go build`/`go vet`/`npm run build` on every push would catch a broken build before you find out by testing manually.
