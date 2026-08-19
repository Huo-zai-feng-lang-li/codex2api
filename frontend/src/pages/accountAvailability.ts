import type { AccountRow } from "../types";

export function isModelLimitedAccount(account: Pick<AccountRow, "model_cooldowns">): boolean {
  const now = Date.now();
  return (account.model_cooldowns ?? []).some((cooldown) => {
    if ((cooldown.remaining_seconds ?? 0) > 0) return true;
    const resetAt = Date.parse(cooldown.reset_at);
    return Number.isFinite(resetAt) && resetAt > now;
  });
}

export function isNormalAccount(account: AccountRow): boolean {
  if (typeof account.is_available === "boolean") return account.is_available;

  const status = (account.status || "").toLowerCase();
  if (status !== "active" && status !== "ready") return false;
  if (account.enabled === false || isModelLimitedAccount(account)) return false;

  const reason = (account.cooldown_reason || "").toLowerCase();
  return reason !== "rate_limited" && reason !== "payment_required";
}

export function countNormalAccounts(accounts: AccountRow[]): number {
  return accounts.filter(isNormalAccount).length;
}
