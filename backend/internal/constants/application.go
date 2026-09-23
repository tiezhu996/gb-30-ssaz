package constants

// ApplicationStatus enumerates the adoption application state machine.
const (
	AppStatusSubmitted        = "submitted"
	AppStatusOrgReview        = "org_review"
	AppStatusCommunicating    = "communicating"
	AppStatusConfirmed        = "confirmed"
	AppStatusOfflineInterview = "offline_interview"
	AppStatusApproved         = "approved"
	AppStatusRejected         = "rejected"
	AppStatusWithdrawn        = "withdrawn"
)

// ValidApplicationStatuses returns all accepted application statuses.
func ValidApplicationStatuses() []string {
	return []string{
		AppStatusSubmitted, AppStatusOrgReview, AppStatusCommunicating,
		AppStatusConfirmed, AppStatusOfflineInterview, AppStatusApproved,
		AppStatusRejected, AppStatusWithdrawn,
	}
}

// IsValidApplicationStatus reports whether a status is known.
func IsValidApplicationStatus(s string) bool {
	for _, v := range ValidApplicationStatuses() {
		if v == s {
			return true
		}
	}
	return false
}

// NextApplicationStatuses returns the allowed forward transitions.
func NextApplicationStatuses(s string) []string {
	switch s {
	case AppStatusSubmitted:
		return []string{AppStatusOrgReview, AppStatusRejected}
	case AppStatusOrgReview:
		return []string{AppStatusCommunicating, AppStatusRejected}
	case AppStatusCommunicating:
		return []string{AppStatusConfirmed, AppStatusRejected}
	case AppStatusConfirmed:
		return []string{AppStatusOfflineInterview}
	case AppStatusOfflineInterview:
		return []string{AppStatusApproved, AppStatusRejected}
	default:
		return nil
	}
}

// WithdrawableApplicationStatuses returns the statuses from which an
// applicant may still withdraw the application: anything before the
// offline interview has started.
func WithdrawableApplicationStatuses() []string {
	return []string{
		AppStatusSubmitted, AppStatusOrgReview, AppStatusCommunicating, AppStatusConfirmed,
	}
}

// IsWithdrawableApplicationStatus reports whether the applicant may withdraw
// an application currently in the given status.
func IsWithdrawableApplicationStatus(s string) bool {
	for _, v := range WithdrawableApplicationStatuses() {
		if v == s {
			return true
		}
	}
	return false
}
