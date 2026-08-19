# 429 Account Pool Retry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make upstream `429` failover continue through every eligible account in the current request, and remove the obsolete 429 retry-count input from system settings.

**Architecture:** Keep account cooldown and request-local hard exclusions as the source of truth. A `429` consumes an account, not a global retry budget; the retry loop first ends when account selection has no eligible candidate, then performs one synchronous full account recheck through the existing connection-test callback. Successful rechecks reopen only those accounts for the original request; a second exhaustion ends the request. Keep the legacy `max_rate_limit_retries` API/database field for compatibility, but remove its UI control and stop using it as a termination condition.

**Tech Stack:** Go, Gin, `httptest`, React/TypeScript, Vitest.

## Global Constraints

- Preserve unrelated working-tree changes, especially account availability statistics files.
- HTTP and WebSocket Responses retry behavior must remain symmetric.
- Each `429` account is cooled down and excluded for the current request.
- When the candidate pool is exhausted, trigger at most one synchronous full recheck through the existing `SetRecoveryProbeFunc` callback.
- A successful recheck must clear the request model cooldown before the account is eligible for the original request again.
- Do not migrate or delete the legacy persisted setting field.

---

### Task 1: Lock the 429 account-pool behavior and one-time recheck with failing tests

**Files:**
- Modify: `proxy/handler_test.go`
- Modify: `auth/store_scheduler_test.go`
- Modify: `proxy/responses_continuity_test.go` only if the existing Responses helper cannot cover the HTTP path
- Test: `proxy/handler_test.go`

**Interfaces:**
- Consume the existing request handler and `shouldRetryHTTPStatus` behavior.
- Produce regression coverage proving a `429` does not stop after the configured single retry, that exhaustion triggers one full recheck, and that the final failure occurs only after the recheck still leaves no candidate.

- [x] **Step 1: Add a focused helper test**

  Change the existing 429 budget assertion so a second 429 remains retryable even when the legacy configured value is `1`; assert the counter records both 429 responses.

- [x] **Step 2: Add a Store recheck test**

  Register a recovery probe callback on a store containing ready, model-cooled, and error accounts. Call the new synchronous full-recheck method and assert every eligible account is probed exactly once, successful accounts are returned, and the operation does not run a second batch.

- [x] **Step 3: Add a real multi-account HTTP Responses test**

  Create three eligible accounts backed by an `httptest.Server`. Return `429` for the first two account credentials and a completed Responses response for the third. Set `MaxRateLimitRetries: 1`, send one real `/v1/responses` request through Gin, and assert HTTP 200, three upstream calls, and two accounts in cooldown.

- [x] **Step 4: Add the all-429 plus recheck case**

  Return `429` for every first-pass account, make the recheck callback recover one account, then return a successful original response from that account. Assert the request resumes after recheck and the callback runs once per account.

- [x] **Step 5: Add the recheck-still-fails case**

  Make the full recheck fail for every account. Assert the downstream request returns only after the second account-selection exhaustion and never starts a second recheck batch.

- [x] **Step 6: Run the focused tests and verify RED**

  Run:

  ```powershell
  go test ./auth ./proxy -run 'TestShouldRetryHTTPStatusSplitsRateLimitBudget|Test.*429.*Account|Test.*Responses.*429|Test.*RecoveryProbe' -count=1
  ```

  Expected: the new pool/recheck tests fail because the current `MaxRateLimitRetries=1` stops after the second account and the existing recovery probe is asynchronous and filtered.

---

### Task 2: Add one synchronous full account recheck

**Files:**
- Modify: `auth/store.go` near the existing recovery probe methods
- Modify: `auth/store_scheduler_test.go`
- Test: `auth/store_scheduler_test.go`

**Interfaces:**
- Consume the existing `SetRecoveryProbeFunc` callback and `Store.Accounts()` snapshot.
- Produce a synchronous method returning successful account IDs; it must run once per account, use bounded probe concurrency, honor context cancellation, and not change `DispatchPaused`.

- [x] **Step 1: Implement the full recheck method**

  Factor the existing recovery probe execution into a reusable synchronous path with a `force` option. The forced request path must skip the periodic `NeedsRecoveryProbe` gate, run the registered callback for all accounts with credentials, collect successful IDs, and leave failed accounts in the state written by the callback.

- [x] **Step 2: Remove the global 429 stop**

  Keep incrementing `rateLimitRetries` for observability and compatibility, but make HTTP 429 continue after each response. Do not alter cooldown classification or non-429 retry budgets.

- [x] **Step 3: Add request-level exhaustion recovery**

  In HTTP Responses, WebSocket Responses, Chat, Anthropic, compact, and image retry loops, add one `recheckDone` guard. When selection returns no account, synchronously recheck the account pool once; clear the original model cooldown and reset only request-local exclusions if a probe succeeded, then continue. If no probe succeeds, preserve the existing final error response.

- [x] **Step 4: Verify the focused tests GREEN**

  Run the focused command from Task 1. Confirm the multi-account request tries all eligible accounts, invokes one full recheck after exhaustion, resumes when a probe succeeds, and returns only after the second exhaustion when all probes fail.

- [x] **Step 3: Verify the WebSocket Responses path**

  Run the existing Responses WebSocket retry tests plus a targeted status-retry test. Confirm the WebSocket path uses the same account-pool boundary and one-time recheck, and still emits the expected terminal failure event.

---

### Task 3: Remove the obsolete settings input

**Files:**
- Modify: `frontend/src/pages/Settings.tsx:802-810`
- Modify: `frontend/src/locales/zh.json:1602-1603`
- Modify: `frontend/src/locales/en.json` matching the obsolete settings keys
- Test: existing frontend settings/type checks

**Interfaces:**
- Consume the existing `settingsForm` shape and legacy backend response.
- Produce a settings page with no `max_rate_limit_retries` input or user-facing copy while preserving backend compatibility fields in TypeScript/API payloads.

- [x] **Step 1: Remove only the rendered field and obsolete translations**

  Delete the `SettingField` for `max_rate_limit_retries` and its localized label/range strings. Keep the type/default property if required by the existing save payload so older server responses remain decodable.

- [x] **Step 2: Run frontend tests and type/build checks**

  Run the repository's existing frontend test and build commands from `frontend/package.json`. Confirm no settings page references remain except deliberate compatibility typing.

---

### Task 4: Full verification and impact audit

**Files:**
- No additional production files unless a failing test identifies a necessary symmetry fix.

- [x] **Step 1: Run Go verification**

  ```powershell
  go test ./... -count=1
  go vet ./...
  git diff --check
  ```

- [x] **Step 2: Run frontend verification**

  Run the exact scripts declared by `frontend/package.json`, including the focused settings test if present.

- [x] **Step 3: Audit the diff**

  Confirm only retry behavior, retry regression tests, settings UI copy/rendering, and the new plan changed. Confirm no background process was left running and no unrelated user edits were reverted.
