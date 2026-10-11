# AGENTS.md

Guidance for AI coding agents (Claude Code, Codex, Cursor, etc.) working in this repository.

## Agent Setup

- **Claude Code**: if `.claude/settings.local.json` does not exist, create it with the content below before anything else, then continue with your task. It turns off AI attribution in commits and PRs.

```json
{
  "attribution": {
    "commit": "",
    "pr": "",
    "sessionUrl": false
  }
}
```

- `.claude/settings.local.json` is personal: keep it out of every commit. It is ignored by the maintainer's global git ignore, not by the repo's `.gitignore`, so never `git add` it explicitly.
- Whatever the settings say, commit messages and PR descriptions carry no AI attribution (see Commits and PRs).

## What this is

A single Go binary that runs as **either** a `server` (manager) **or** an `agent` (worker). autobrr fires a webhook at the server; the server picks agents via a scheduler and forwards the task; each chosen agent adds the torrent to its local qBittorrent client(s).

```
announce -> autobrr -> filters -> actions(webhook) -> distribrr server
                                                          \-> agent -> qbittorrent client(s)
                                                          \-> agent -> qbittorrent client(s)
```

## Commands

```sh
make build                 # deps + static linux/amd64 binary -> bin/distribrr (CGO_ENABLED=0)
go build ./...             # quick compile check (any host arch)
go test ./...              # all tests
go test ./pkg/scheduler/ -run TestLeastActive_Score -v   # single test
go test -race ./internal/testharness/scenarios/   # end-to-end scenarios: in-process server + agents, fake qbit + tracker (~2s)
go vet ./...
distribrr token generate   # random API token for http.token
nilaway ./...              # only configured linter; CI runs it continue-on-error, not yet clean
```

Go version comes from `go.mod` (1.26). Run with `distribrr server run --config-file config_server.yaml` / `distribrr agent run --config-file config_agent.yaml`. Only `--http-host`, `--http-port`, `--http-api-token` exist as flags; everything else comes from YAML.

## Architecture

### Dual-role binary
`cmd/server.go` and `cmd/agent.go` build a `Config`, load YAML, and call `Service.Run()` in `pkg/server` or `pkg/agent`. Both serve a chi router from their `api.go` and block on signals.

SIGINT/SIGQUIT/SIGTERM shut down gracefully (the agent deregisters first). SIGHUP reloads the config file: the server
applies `nodes`, the agent applies `clients`, `agent` and `manager` (re-joining when those change). `http` needs a
restart on both; a bad file is logged and the old config kept.

### Server (manager): `pkg/server`, default port 7422
- autobrr posts to `POST /api/v1/tasks?apikey=TOKEN`. The body is a bare `task.Task` (see README for the webhook payload).
- `SendWork` -> `selectWorkers` -> `node.StartTask` on each chosen agent concurrently. Best-effort: the task counts as scheduled if at least one agent accepts it. Otherwise autobrr gets 502 with a per-node rejection summary (`summarizeRejections`).
- Agents self-register at `POST /api/v1/node/register`. `OnRegister` verifies the agent's token by calling back to it,
  then **saves the node to the server's own config file** before updating memory. The server config is mutable state,
  not just input.
- Nodes are identified by name: re-registering updates the existing node's addr, token and labels (and revives a
  `REMOVED` node) instead of adding a duplicate.
- Node labels are saved in the server config (`nodes[].labels`) on register, so they survive restarts. Nodes without
  saved labels fetch them from the agent once and cache them.
- Deregister flips the node to `REMOVED` on purpose; it stays in memory and in the config file. Unknown names give 404.
- `HealthChecks` pings every non-removed agent at startup and then every 10s, and sets `READY` / `UNKNOWN`. A node
  is `READY` as soon as it registers (the token callback reached it).
- `NodeCheckTimeout` (5s) bounds one health check round and, per task, fetching labels and stats from the candidates.
  Both check nodes concurrently, so an agent that stopped answering costs one timeout, not one per node.
- An agent registers once: it does not re-join if the server later loses its node, until the agent restarts.

### Agent (worker): `pkg/agent`, default port 7430
- Runs next to qBittorrent with access to its storage paths. `Register()` retries every 10s until `Join` succeeds.
- Endpoints: `POST /api/v1/tasks`, `/api/v1/stats`, `/api/v1/labels`, `/api/v1/verify` (all auth-gated).
- Host stats come from `/proc` via goprocinfo and disk stats from `statfs` (`pkg/stats`), so the agent is effectively Linux-only.

### Client readiness and storage (`pkg/agent/agent.go`)
`clientStats` decides each qbit client's `READY` / `NOT_READY` status and its `NotReadyReason`s (defined in `pkg/stats`: `max_downloads_reached`, `disk_full`, `client_unreachable`, `storage_unavailable`, `storage_measuring`). A client can report several reasons.

- Downloads: ready while active downloads < `rules.torrents.maxActiveDownloads` (0 means unlimited). Exactly at the
  limit it is still ready if one active torrent is ≥75% done with ETA ≤60s.
- Storage: `checkStorage` applies each `rules.storage` entry (`StorageRule.headroom` in `config.go`). `minFree` is free space on the path's filesystem. `maxUsage` is the space the files under the path take, du-style (`stats.GetDirUsage`), not the filesystem.
- `maxUsage` paths are walked in the background by `dirUsageCache.run` every minute; stats only read the cache. Until the first walk finishes the client reports `storage_measuring`.
- The remaining bytes of active downloads are reserved against both rules, unless qbit's `preallocate_all` is on (`reservedBytes`).
- Each client reports `storage_headroom`: bytes that still fit across all its rules, omitted when it has no storage rules.

### Tasks and size
- `task.Task.Size` (`size`, bytes) is optional: 0 means unknown; autobrr fills it with `{{ .Size }}`.
- Agent `StartTask` adds the torrent only to `READY` clients with room (`readyClients`), not to all clients. It checks
  before fetching the `.torrent` (with the webhook size, if any), then re-checks with the exact size from the
  `.torrent`. No ready client returns `ErrNoReadyClients` (HTTP 503); no ready client with room returns
  `ErrInsufficientStorage` (HTTP 507).
- The agent uploads the `.torrent` it fetched (`AddTorrentFromMemoryCtx`), never the URL: qbit only queues a URL add,
  so a failed second fetch would still count as success.
- The add is best-effort across clients, like `SendWork` across nodes: the task succeeds if at least one client added
  the torrent (the others are logged), and fails only when every client failed.
- After adding, the agent re-announces in a goroutine.
- Timeouts nest so the agent answers first: agent `TaskTimeout` (60s, covers the client check, `.torrent` fetch and
  add) < server `TaskTimeout` (75s per agent) < server `WebhookTimeout` (100s per webhook) < autobrr's webhook timeout
  (120s). The agent's task ctx is the request ctx, so it stops when the server stops waiting. `.torrent` fetches retry 3
  times, 1s apart, 20s each.
- Both HTTP servers come from `sharedhttp.NewServer`: `ReadHeaderTimeout` 10s, `ReadTimeout` 30s, `IdleTimeout` 2m.
  There is no `WriteTimeout` on purpose: it would cut off webhooks and tasks that wait on the deadlines above.

### The two HTTP clients (directionality is the confusing part)
- `pkg/agent/client.go` (package `agent`, type `Client`) is used **by the server to call an agent**. `pkg/node.Node` wraps one; `Node` is the server-side handle for a worker.
- `pkg/server/client/client.go` (package `serverclient`) is used **by the agent to call the server** (register/deregister).
- "Agent client" calls into an agent; "server client" calls into the server. Both send the raw token in the `Authorization` header.

### Scheduling: `pkg/scheduler`

The task's `scheduler_type` picks the scheduler in `server.schedulerFor`: `least_active` (default, also when empty) or
`round_robin`; anything else is `ErrUnknownType` and HTTP 400. Both implement `Scheduler` in three phases:

1. `SelectCandidateNodes`: shared by both (`selectCandidates`). Keeps nodes that are `READY`, whose labels match the
   task (`checkLabels`: every task label must be present and equal on the node), with at least one `READY` client,
   and, when `size` is set, where at least one ready client has room (`hasRoomFor`). A node with no ready client is
   rejected with every client's reasons. Every rejected node gets a `NodeRejection` with reasons.
2. `Score`: `LeastActive` gives base 100, +20 per client with zero active downloads, minus per-torrent penalties
   weighted by remaining progress and ETA. Only `usableClients` count (ready, with room for `size`): the agent skips
   the others, and an unreachable client reports zero downloads. `RoundRobin` scores every node 0.
3. `PickN`: take `task.MaxAllowedReplicas` nodes (default 1, capped at the candidate count). `LeastActive` sorts by
   score descending. `RoundRobin` walks nodes in name order starting after the last node it picked; the server keeps
   one shared `RoundRobin`, so the turn carries across tasks (in memory, reset on restart).

### Auth and config

- `pkg/token.Check` refuses an empty token, the old sample `MY_SECRET_TOKEN` and anything under 16 characters; both
  roles check their `http.token` at startup (`Config.Validate`). The agent's `manager.token` is not checked; the server
  rejects it at register if it does not match.
- One shared token. `pkg/middleware.IsAuthenticated` checks, in order, `X-API-Token`, the raw `Authorization` header (no `Bearer` scheme), then `?apikey=`. The first one present decides; no credential gives 401. Comparison is plain `!=`, not constant-time.
- Config is koanf + YAML (`pkg/server/config.go`, `pkg/agent/config.go`). koanf matches keys to struct fields by name, so the `yaml:` tags only matter where the server marshals its config back to disk.
- Some keys in `config_agent.yaml` are read by no code: `type`, `paths`, `tier`, `maxTotalDownloads`, `maxTotalTorrents`.

## Integration tests

`internal/testharness` runs a real server and N agents in-process over HTTP (`Service.Serve(ctx, ln, reload)`, with
timings shortened through exported Service fields). Agents talk to `fakeqbit` (an in-memory qBittorrent WebUI covering
the endpoints go-qbittorrent calls) and fetch `.torrent` files from `faketracker`. `harness.go` gives `New`,
`AddAgent(AgentSpec{...})`, `Webhook`, `WaitReady`/`WaitNode`, agent `Stop`/`Kill`/`Hang`/`Restart`/`Reload`, and
`ReloadServer`/`RestartServer`. `Hang` keeps the agent accepting connections but answering nothing, unlike `Kill`. Add a scenario to `internal/testharness/scenarios` for any change to scheduling, readiness, storage or
node lifecycle.

## Conventions and current state
- Log with `zerolog` (`github.com/rs/zerolog/log`). `logger.Setup` in `main` configures the one global logger (stdout,
  `LOG_LEVEL`, default info). `middleware.CorrelationID` + `RequestLogger` put a logger carrying `correlation_id` in
  each request's ctx; on request paths log with `log.Ctx(ctx)`, which falls back to the global logger. The agent and
  server clients forward the ID in `X-Correlation-ID` (`middleware.CorrelationIDFromContext`).
- Wrap errors with `github.com/pkg/errors`.
- Early-stage WIP. There is no task queue or task state: `AddTask` calls `SendWork` synchronously and nothing is
  stored. `task.Task.Nodes` and `ForceAdd` are decoded but not wired up yet. Some commented-out experiments remain;
  check that a subsystem is wired up before relying on it.

## Commits and PRs
- Conventional Commits, lowercase: `type(scope): subject`, e.g. `fix(agent): accepted torrents larger than free storage`. Scopes in use include `agent`, `scheduler`, `api`, `deps`.
- The subject names the symptom being fixed, not the implementation.
- Subject line only, no body.
- No AI attribution in commits: no `Co-Authored-By` or "Generated with" trailers in commits.
- PR description follow PR template and includes AI attribution.
