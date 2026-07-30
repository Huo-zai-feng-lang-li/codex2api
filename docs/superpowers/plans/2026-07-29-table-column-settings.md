# Table Column Settings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Give request logs and request error details persistent column visibility and drag ordering.

**Architecture:** A shared pure preference module owns migration and ordering invariants; a shared dropdown owns interactions. Each page supplies typed column definitions and renders headers/cells from the same ordered list.

**Tech Stack:** React 19, TypeScript, native HTML drag events, Node test runner, Vite.

---

### Task 1: Preference model

**Files:** create frontend/src/lib/tableColumns.ts, test frontend/src/lib/tableColumns.test.mjs.

- [x] Write failing tests for legacy migration, order normalization, visibility protection and move semantics.
- [x] Run node --test frontend/src/lib/tableColumns.test.mjs and confirm missing-module failure.
- [x] Implement the minimal generic preference helpers.
- [x] Re-run the targeted test and confirm all cases pass.

### Task 2: Shared interaction component

**Files:** create frontend/src/components/ColumnSettingsDropdown.tsx.

- [x] Add a typed dropdown that toggles hideable columns.
- [x] Add native drag ordering plus explicit keyboard-accessible up/down controls.
- [x] Keep locked columns sortable but non-hideable.

### Task 3: Page integration

**Files:** modify frontend/src/pages/Usage.tsx, frontend/src/pages/OperationsErrors.tsx, test frontend/src/pages/OperationsErrors.test.mjs.

- [x] Replace Usage's local visibility-only implementation with shared persisted preferences and dynamic ordered rendering.
- [x] Define OperationsErrors columns, lock its actions column visibility, and dynamically render headers/cells from the same ordered list.
- [x] Add page wiring regression assertions and run targeted tests.

### Task 4: Verification

- [x] Run targeted frontend tests and typecheck; skip production build at the user's request.
- [x] Review the final diff for accessibility, state migration and unrelated changes.
- [x] Update .agent/handoff.md with verified results.
