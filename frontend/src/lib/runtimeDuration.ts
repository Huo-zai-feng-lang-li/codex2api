export function advanceRuntimeDuration(durationMs: number, snapshotAtMs: number, nowMs: number): number {
  return Math.max(0, durationMs) + Math.max(0, nowMs - snapshotAtMs)
}
