package database

import "strings"

type usageAttemptPredicates struct {
	Terminal string
	Retry    string
}

func usageAttemptSQLPredicates(alias string) usageAttemptPredicates {
	statusColumn := usageAttemptSQLColumn(alias, "status_code")
	retryColumn := usageAttemptSQLColumn(alias, "is_retry_attempt")
	return usageAttemptPredicates{
		Terminal: statusColumn + " <> 499 AND COALESCE(" + retryColumn + ", false) = false",
		Retry:    statusColumn + " <> 499 AND COALESCE(" + retryColumn + ", false) = true",
	}
}

func usageAttemptSQLColumn(alias, column string) string {
	alias = strings.TrimSuffix(strings.TrimSpace(alias), ".")
	if alias == "" {
		return column
	}
	return alias + "." + column
}

func isTerminalUsageAttempt(statusCode int, isRetryAttempt bool) bool {
	return statusCode != 499 && !isRetryAttempt
}

func shouldApplyAPIKeyQuotaUsage(entry usageLogEntry) bool {
	return entry.APIKeyID > 0 && entry.UserBilled > 0 &&
		isTerminalUsageAttempt(entry.StatusCode, entry.IsRetryAttempt)
}
