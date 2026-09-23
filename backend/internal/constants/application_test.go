package constants

import "testing"

func TestValidApplicationStatusesIncludesWithdrawn(t *testing.T) {
	if !IsValidApplicationStatus(AppStatusWithdrawn) {
		t.Error("withdrawn must be a valid application status")
	}
}

func TestIsWithdrawableStatus(t *testing.T) {
	withdrawable := []string{
		AppStatusSubmitted, AppStatusOrgReview, AppStatusCommunicating, AppStatusConfirmed,
	}
	for _, s := range withdrawable {
		if !IsWithdrawableStatus(s) {
			t.Errorf("%s should be withdrawable", s)
		}
	}
	notWithdrawable := []string{
		AppStatusOfflineInterview, AppStatusApproved, AppStatusRejected, AppStatusWithdrawn,
	}
	for _, s := range notWithdrawable {
		if IsWithdrawableStatus(s) {
			t.Errorf("%s should not be withdrawable", s)
		}
	}
}

func TestWithdrawnHasNoForwardTransition(t *testing.T) {
	if next := NextApplicationStatuses(AppStatusWithdrawn); next != nil {
		t.Errorf("withdrawn must be terminal, got %v", next)
	}
}

func TestWithdrawnNotReachableByForwardMachine(t *testing.T) {
	for _, s := range ValidApplicationStatuses() {
		for _, next := range NextApplicationStatuses(s) {
			if next == AppStatusWithdrawn {
				t.Errorf("withdrawn must not be reachable by forward transition from %s", s)
			}
		}
	}
}
