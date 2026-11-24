# Changelog

## Unreleased

## v2.3.0 - 2026-11-24

### Changes

- **Codex client auto-update removed from the maintenance tooling.** The Codex desktop app (Microsoft Store package `OpenAI.Codex`), its CLI and `~/.codex` were uninstalled from this machine, so `update_codex_and_models.py` step 1 (`winget upgrade` / `codex update`) and step 3 (`codex app-server daemon restart` / desktop relaunch) had no target left. The script is replaced by **`update_models.py`**, which keeps only the two steps that actually matter for this gateway: official catalog sync (`POST /api/admin/models/sync`) and upstream pool model refresh.
- **Launcher renamed** `更新Codex和模型.bat` → **`更新模型列表.bat`** (the old wrapper's Chinese full-width brackets also broke `cmd` echo parsing; the new launcher uses ASCII-only banner text).
- **Backward compatibility.** `--skip-update` and `--skip-restart` are still accepted as hidden no-ops, so any existing scheduled task invoking the old flags keeps working. New flags: `--skip-sync`, `--skip-refresh`, `--prune`, `--dry-run`, `--keep-all`.
- **Pool refresh no longer PATCHes unchanged accounts.** A `PATCH /api/admin/accounts/{id}/openai-responses` is only issued when the model list actually gained or lost entries (`--prune` removals included), cutting write traffic against the account table.
- **`新模型接入手册.md` marked obsolete sections.** Everything that depends on the Codex desktop app — the `app-server` dropdown, `model_catalog_json`, `~/.codex/config.toml`, `winget` product id — is explicitly flagged as retired; the registry (`model_registry`) and pool (`credentials.models`) flows remain.

### Verification

- `python -m py_compile update_models.py` and a live smoke run of `更新模型列表.bat` against `127.0.0.1:18080` (`/health` ok, launcher banner clean).
- Full run against the live pool: 37 accounts refreshed, 1 upstream offline (`http://127.0.0.1:3000` unreachable), 8 models added — `gpt-6.2-astra`, `gpt-6.2-sol`, `gpt-6.1-sol`, `gpt-6.1-terra`, `codex-auto-review`, `agnes-3.0-pro`, `agnes-sys1-1.0`, `agnes-2.5-pro-theta`. `/v1/models` went from 45 to 51 entries.
- `go build ./...`, `go vet ./...`, `go test ./... -count=1`（16 个包全过，重启后补跑）。
- `build-and-restart.bat` 重建并热替换：`/health` = ok，PID 15004，`/v1/models` 仍为 51 个（含 gpt-6.2-astra / gpt-6.2-sol）。

## v2.2.9 - 2026-11-05

### Fixes

- **Model mapping no longer corrupts Codex native accounts.** `/v1/chat/completions` and `/v1/messages` previously replaced the model name unconditionally, so a mapped third-party model name (e.g. `free/glm-5.3-flash`) could be forwarded to the official Codex upstream for a native account and rejected. Model replacement is now guarded by `account.IsOpenAIResponsesAPI()`, and the account filter excludes Codex accounts unless the mapped model is in the built-in supported list.
- **Model-level cooldowns are now actually honored.** Cooldown writes used the original requested model name while the scheduler filtered on the mapped name, so model-scoped cooldowns never matched. A new `cooldownModelFor` helper resolves the model actually forwarded upstream, and all cooldown write/clear call sites (responses, chat/completions, messages, WebSocket, recheck) now use it.
- **Account-level quota errors are no longer treated as model-scoped.** `insufficient_quota`, `insufficient quota` and `exceeded your current quota` describe billing-level exhaustion. They now fall back to the account-wide `payment_required` cooldown instead of only cooling down a single model, which previously caused the account to be re-dispatched and fail repeatedly.
- **Exposed model whitelist description corrected.** The settings UI claimed the whitelist limited request validation and that clearing it restored six default models. In practice it only filters the `/v1/models` listing, and an empty value disables filtering entirely. The description now states this accurately.
- **Concurrent model-list corruption fixed.** `supportedModelIDs` appended directly to the cached slice returned by `SupportedModelIDs`. Because `enabledModelIDs` allocates with spare capacity, concurrent requests wrote into the same spare slots, causing a data race and cross-contamination of the model list. The caller now copies the slice first.
- **Exhaustion recheck clears the forwarded-model cooldown.** After the account pool was exhausted and re-probed, only the original model name had its cooldown cleared, leaving the mapped model cooled down. The recheck now also clears the mapped name for responses-api accounts.

### Improvements

- **`SupportedModelIDs` caching.** The model registry lookup ran two SQL queries per request on every chat/completions, messages, responses and `/v1/models` call. Results are now cached for 15 seconds behind a read-write mutex and invalidated after a registry sync.
- **Non-blocking cloudflared probe.** `GetSettings` used to run a synchronous `tasklist` (or `/proc` scan) probe of `cloudflared` whenever its 10-second cache expired, occasionally stalling first paint. The probe now refreshes on a background goroutine and settings reads go through an atomic value.
- **`CODEX_TUNNEL_DOMAIN` now accepts an `http://` prefix** in addition to `https://`.

### Verification

- `go build ./...`, `go vet ./...`, `go test ./... -count=1` (16 packages).
- `npx tsc --noEmit` and `npm run build` for the frontend.
- `build-and-restart.bat` hot swap with `/health` reporting `ok`.
- Two-turn `/v1/responses` continuation: both the initial request and the `previous_response_id` follow-up completed.
- Model mapping smoke test through `/v1/chat/completions` and `/v1/messages` both returned 200 with the mapped model forwarded upstream.
- Service log showed no `409`, no panics and no `continuation_context_unavailable` events.

## v2.2.8 - 2026-08-19

### Improvements

- **Multi-upstream rate limit semantic classifier.** Added `matchGenericUpstreamRateLimit` and `isDailyLimitExceeded` to automatically classify rate limit errors from third-party relays and aggregate gateways (such as OneAPI, NewAPI, etc.).
- **Midnight alignment for Daily Limit.** Daily quota exhausted errors (`daily usage limit exceeded`, `daily_limit_exceeded`, etc.) now dynamically compute and align account cooldowns to the next local midnight (00:00:05), completely eliminating repetitive 5-minute probe loops on exhausted accounts.
- **403 daily limit interception.** Intercepted 403 HTTP status responses carrying daily limit error payloads in `ApplyUpstreamAccountFailure` to ensure proper midnight alignment instead of generic payment required penalties.
- **Weekly & Quota exhaust handling.** Automatically map weekly limit errors to 7-day cooldowns, and balance/quota exhaustion errors to 24-hour payment required sleep state.

## v2.2.7 - 2026-08-02

### Improvements

- **Responses high-concurrency memory governance.** Continuation history now stores per-turn parent-linked deltas instead of duplicating full snapshots. Process-wide request count, request-body bytes, and local-history fallback concurrency are bounded and exposed through `/health`; limits are configurable with `CODEX_RESPONSES_*` environment variables.
- **Third-party continuation integrity.** Non-official Responses relays now receive the bounded local full-history replay on the first continuation attempt instead of trusting a relay-owned `previous_response_id` that may return `200` while silently forgetting context. Set `CODEX_RESPONSES_CONTINUITY_MODE=upstream` only for relays with verified stateful continuation support.
- **Restart-safe Responses continuity.** Parent-linked continuation deltas are persisted in the existing application database and lazily restored into the bounded memory cache after service restarts. Persistence uses short fail-open timeouts, sliding access timestamps, TTL cleanup, and the same entry/byte limits as memory.

## v2.2.6 - 2026-07-16

### Fixes

- **Blank proxy now means direct connection.** Fixed upstream calls that inherited `HTTP_PROXY`/`HTTPS_PROXY` from the process environment even when no proxy was configured in CodexProxy. This covered both normal `/v1/responses` dispatch and the admin "request `/v1/models`" flow for OpenAI Responses API accounts, preventing failures such as `proxyconnect tcp: dial tcp 127.0.0.1:51081`.
- **Small account-pool first-token handling.** A single available account no longer triggers first-token timeout account switching; the service waits for the real upstream result instead. Multi-account pools still use first-token timeout as an internal silent failover signal.
- **WebSocket no-account leak.** Responses WebSocket requests now stop cleanly when the downstream client disconnects or cancels after an upstream attempt, instead of retrying into an empty local pool and emitting `stream disconnected before completion: 无可用账号，请稍后重试`.
- **Responses continuation recovery.** OpenAI Responses continuations now stay bound to the account that created the response. Relays that reject HTTP `previous_response_id`, or lose the owning connection before producing output, retry once with a bounded in-memory history snapshot instead of silently starting a fresh conversation.

### Verification

- Ran `go test ./... -count=1`.
- Rebuilt `codex2api.exe` and restarted the local service.
- Verified `/health` returned `status=ok`.
- Verified the admin `/api/admin/accounts/openai-responses/models` request returned the upstream model list without using the stale environment proxy.
- Verified both JSON and SSE two-turn continuations against the configured relay with `store=false`.

## v2.2.4 - 2026-05-28

### Features

- **Scheduler warm-tier bypass (#176).** Added a scheduler option to skip warm-tier demotion so selected accounts can stay in the healthy scheduling lane.
- **API Reference image previews.** The Try it panel now renders image responses directly when `/v1/images/generations` or `/v1/images/edits` returns `b64_json`, image URLs, or Responses-style image output, while still keeping the raw JSON visible.
- **Long-context billing details (#178).** Usage cost tooltips now expose when long-context pricing is active (`input_tokens > 272,000`) and show the actual input, output, and cache-read unit prices used for the request.
- **Root changelog.** Moved the project changelog to the repository root so release notes and documentation links point to a stable location.

### Fixes

- **WebSocket response continuity (#182).** Fixed WS mode context loss so `previous_response_id` continuation remains connected across `/v1/responses` WebSocket turns.
- **First-token timeout retries (#172).** First-token timeouts now retry by account-pool round and clear transient unavailable markers after the pool has been tried, reducing false "no available account" failures for small pools.
- **Banned-account test recovery.** Successful account tests now recover stale banned/error state instead of leaving the account marked unhealthy after the probe succeeds.
- **Bounded batch account tests.** Batch account tests now keep their execution bounded so duplicate or repeated test requests do not destabilize account state handling.
- **Version popover clipping.** The version popover is rendered through a portal so sidebar overflow no longer clips the latest-version panel.

## v2.2.3 - 2026-05-27

### Features

- **Usage reset radar.** New `/subscriptions` page consolidates the Codex Reset Radar summary, recent RSS events, and a reset-time hook. When a window-close signal is detected the backend clears stale cooldown/usage caches and re-tests every account so the pool reflects the new reset boundary immediately.
- **Streaming batch operations in admin.** Account batch refresh/test/enable/disable/lock/reset now stream per-account progress events (`success/banned/rate_limited/failed`) to the admin UI instead of waiting for the full operation to complete.
- **Compact usage number setting.** Added a system setting to render token counts with K/M units in the usage table for easier reading at scale.
- **Card view for account management.** Desktop accounts page gains a table/card view toggle (up to 5 cards per row on `xl` screens). Choice is persisted in `localStorage`.
- **Status badge error tooltip.** Hovering an `unauthorized` or `error` status badge now surfaces the full upstream error message in a popover, matching the usage log status code tooltip style.
- **Anthropic `speed: fast` forwarding (#170).** Anthropic-style `speed: fast` requests now map to the Codex priority tier upstream so fast clients get fast tokens end-to-end.

### Fixes

- **Version popover always clickable.** The sidebar version badge now opens the popover even when the GitHub latest-version lookup is still pending or blocked; a "checking…" hint is shown until the remote tag arrives.
- **First-token timeout and scheduler races.** Hardened the proxy path so first-token timeouts and concurrent scheduler races no longer collapse into a spurious "no available account" 503.
- **`/responses` WebSocket ingress.** WebSocket clients hitting `ws://host/v1/responses` are now accepted; the prior 404/101 misclassification has been fixed. Setting `CODEX_UPSTREAM_TRANSPORT=ws` no longer reports the connect handshake as an unknown error.
- **Anthropic content preservation + deactivated probe flagging.** Anthropic-shaped responses keep their original content blocks; deactivated accounts are clearly flagged in probe state instead of silently appearing healthy.
- **Wham window classification.** Usage window classification now uses `limit_window_seconds` rather than field position, so free-tier accounts no longer have a 7d window misclassified as 5h.

## v2.2.2 - 2026-05-26

### Features

- **First-run setup and admin auth polish.** Added setup guidance for unconfigured deployments, improved the admin authentication flow, and added a frontend logout path.
- **Runtime status API and page.** Added machine-readable runtime checks for service, database, cache, usage log writer, probes, account pool, image storage, and admin auth.
- **Background media customization.** Added configurable background image/video support, realtime glass opacity/blur controls, and raised MP4 dynamic wallpaper uploads to 40MB while keeping image uploads capped at 20MB.
- **Quick-start configuration options.** Added fast-mode and reasoning-effort snippets for supported client templates in the usage docs.
- **Issue templates.** Added structured Chinese and English GitHub issue templates for bugs, ideas, UI feedback, deployment help, and questions.

### Fixes

- **Request body sizing for wallpapers.** Raised the default request body limit to 48MB so 40MB MP4 background uploads can pass multipart overhead safely.

# Changelog — iteration/may-2026-v2

Dates: 2026-05-13 to 2026-05-20. 17 commits.

## Features

- **Credit quota support (#141).** Added `credit_enabled` and `credit_skip_usage_window` flags to the accounts table. Credit-marked accounts skip usage-window penalties in the scheduler. Managed via `PATCH /api/admin/accounts/:id/credit`.

- **Scheduler mode (#133).** Added `scheduler_mode` system setting with two modes: `round_robin` (default, weighted by dispatch score) and `remaining_quota` (prioritize accounts with lowest usage percent). Configurable from Admin Settings page.

- **5h/7d windowed USD cost display.** Replaced the single total-cost column with a windowed billing view. Each account now shows `billed_5h` and `billed_7d` fields aligned with the account's usage-reset boundaries. This reflects actual spending, not estimated token costs.

- **Image-to-image in Image Studio (#135, #136).** The admin Image Studio now supports image-to-image generation via `POST /api/admin/images/edit-jobs`, accepting reference image URLs or data URIs. Added text-to-image and image-to-image tabs in the frontend.

- **Billing model expansion.** Added pricing for gpt-5.5-pro and gpt-5.4-pro families. Implemented long context (>272K tokens) premium pricing for gpt-5.5, gpt-5.5-pro, gpt-5.4, gpt-5.4-pro with automatic detection. Fixed gpt-4o and gpt-4o-mini cache-read pricing.

## Fixes

- **GPT-5.5 pricing corrected.** Updated standard-tier billing from old values to $5.00/M input / $30.00/M output (priority: $12.50/M / $75.00/M), matching current official pricing.

- **SSE stream isolation.** Prevented SSE response mixing when retrying across accounts, using `c.Writer.Written()` as the retry guard instead of a package-level flag.

- **Usage logging for image errors.** Added usage-log emits for read-error paths in image generation, ensuring billing records are not silently dropped on stream failures.

- **Model mapping initialization.** Restored `modelMapping` init that was accidentally removed during the scheduler_mode refactor.

- **Credit field Scan order.** Fixed PostgreSQL `Scan` argument ordering for credit fields that was causing silent zero-values.

- **Round 2 review fixes.** Addressed Haiku review findings including api.ts syntax cleanup, billing test corrections, and several CRITICAL/HIGH issues from automated review.

## Security

- **SQLite default binds to localhost.** SQLite compose files (`docker-compose.sqlite.yml`, `docker-compose.sqlite.local.yml`) now bind ports to `127.0.0.1` by default. Previously they bound to `0.0.0.0`, exposing the service on all interfaces. Standard (PostgreSQL) compose files retain the `0.0.0.0` default.

- **BIND_HOST env var.** Added `BIND_HOST` environment variable support to control the HTTP listen address across all deployment modes. Documented in `.env.example`, `.env.sqlite.example`, and `CONFIGURATION.md`.

## Breaking Changes

- **SQLite compose port binding.** SQLite deployments upgrading from a previous version that relied on external access via the default compose configuration must now explicitly set `BIND_HOST=0.0.0.0` in `.env` or override the port binding in the compose file. All other behavior remains backwards-compatible.
