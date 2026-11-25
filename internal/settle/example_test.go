package settle

import "fmt"

// ExamplePeriodStatus shows the one way a caller gets a PeriodStatus: by
// converting the text read from periods.status. The database's CHECK
// constraint (internal/store/migrations/0001_periods.sql) is what keeps
// that column to the three constants, so the constants are the only
// values a caller should ever construct or compare against. The zero
// value, the empty string, is not one of them: a status left unset is a
// bug to surface, never a period to treat as open.
//
// Only a closed period may be anchored; an open one is still metering,
// and an anchored one is already on chain.
func ExamplePeriodStatus() {
	// As scanned from `SELECT status FROM periods`, plus the zero value.
	for _, raw := range []string{"open", "closed", "anchored", ""} {
		switch status := PeriodStatus(raw); status {
		case PeriodStatusOpen:
			fmt.Printf("%q: still metering, not ready to anchor\n", status)
		case PeriodStatusClosed:
			fmt.Printf("%q: ready to anchor\n", status)
		case PeriodStatusAnchored:
			fmt.Printf("%q: already on chain, nothing to do\n", status)
		default:
			fmt.Printf("%q: not a legal period status\n", status)
		}
	}
	// Output:
	// "open": still metering, not ready to anchor
	// "closed": ready to anchor
	// "anchored": already on chain, nothing to do
	// "": not a legal period status
}

// ExampleSettlementStatus shows the one way a caller gets a SettlementStatus:
// by converting the text read from Record.Status or settlements.status. The
// database's CHECK constraint (internal/store/migrations/0006_settlements.sql)
// keeps that column to the three constants, so the constants are the only
// values a caller should ever construct or compare against. The zero value,
// the empty string, is not one of them: a status left unset is a bug to
// surface, never a settlement to treat as in-flight or resolved.
//
// A 'submitting' row blocks Submit and SubmitWithRetry with
// ErrSettlementInFlight until ReconcileSubmitting resolves it against the
// chain; 'confirmed' and 'failed' are terminal for Submit's idempotency key.
func ExampleSettlementStatus() {
	// As read from Record.Status / `SELECT status FROM settlements`, plus the zero value.
	for _, raw := range []string{"submitting", "confirmed", "failed", ""} {
		switch status := SettlementStatus(raw); status {
		case SettlementStatusSubmitting:
			fmt.Printf("%q: in-flight, reconcile before retrying\n", status)
		case SettlementStatusConfirmed:
			fmt.Printf("%q: settled on chain, retry is a safe no-op\n", status)
		case SettlementStatusFailed:
			fmt.Printf("%q: previous attempt failed, do not retry blindly\n", status)
		default:
			fmt.Printf("%q: not a legal settlement status\n", status)
		}
	}
	// Output:
	// "submitting": in-flight, reconcile before retrying
	// "confirmed": settled on chain, retry is a safe no-op
	// "failed": previous attempt failed, do not retry blindly
	// "": not a legal settlement status
}
