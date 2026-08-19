# WSS Empty Body Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use inline execution with TDD checkpoints.

**Goal:** Treat the upstream WebSocket handshake error `400 empty_request_body` as unsupported WSS and reuse the existing HTTPS fallback.

**Architecture:** Extend the existing `isOpenAIResponsesWebSocketUnsupported` classifier only for the structured `empty_request_body` marker. The existing per-endpoint fallback cache and HTTPS body conversion remain unchanged; all other 400 responses keep their current terminal behavior.

**Tech Stack:** Go, `net/http`, existing `gjson`/transport-cache tests, Windows batch deployment.

## Global Constraints

- Do not change account concurrency, retry budgets, request-body conversion, or unrelated 400 handling.
- Preserve existing user changes in the dirty worktree.
- Verify with the targeted test, `go test ./... -count=1`, `go vet ./...`, build output, and live `/health`.

### Task 1: Add Regression Coverage

**Files:**
- Modify: `proxy/responses_transport_cache_test.go`

- [x] Add a table case asserting JSON `code=empty_request_body` is classified as unsupported WSS.
- [x] Run the targeted test and confirm it fails before production code changes.

### Task 2: Minimal Classifier Change

**Files:**
- Modify: `proxy/responses_ws.go`

- [x] Parse the 400 body with the existing JSON helper and return true only when the error code is `empty_request_body`.
- [x] Keep existing blank-body and explicit websocket marker behavior unchanged.
- [x] Run the targeted transport-cache tests.

### Task 3: Verification and Deployment

**Files:**
- No additional source changes.

- [x] Run `go test ./... -count=1`.
- [x] Run `go vet ./...` and `git diff --check`.
- [x] Execute `build-and-restart.bat`.
- [x] Verify the new process hash/path, `/health`, and absence of orphan processes.
