package service

import (
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/gbadopt/gbadopt/internal/constants"
	"github.com/gbadopt/gbadopt/internal/repository"
)

func withdrawSetup(t *testing.T) (*ApplicationService, sqlmock.Sqlmock) {
	t.Helper()
	db, mock := newServiceDB(t)
	appRepo := repository.NewAdoptionApplicationRepository(db)
	petRepo := repository.NewPetRepository(db)
	orgRepo := repository.NewOrganizationRepository(db)
	svc := NewApplicationService(db, appRepo, petRepo, orgRepo, newTestLogger())
	return svc, mock
}

func appRow(status string) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows(
		[]string{"id", "user_id", "pet_id", "org_id", "questionnaire", "status", "withdrawn_at", "created_at", "updated_at"},
	).AddRow(1, 10, 20, 30, "{}", status, nil, now, now)
}

func TestWithdrawSuccess(t *testing.T) {
	svc, mock := withdrawSetup(t)

	mock.MatchExpectationsInOrder(true)
	// Read
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "adoption_applications" WHERE "adoption_applications"."id" = $1 ORDER BY "adoption_applications"."id" LIMIT $2`)).
		WithArgs(uint64(1), 1).
		WillReturnRows(appRow(constants.AppStatusSubmitted))
	mock.ExpectBegin()
	// Conditional application move: submitted -> withdrawn
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "adoption_applications" SET "status"=$1,"updated_at"=NOW(),"withdrawn_at"=$2 WHERE id = $3 AND status = $4`)).
		WithArgs(constants.AppStatusWithdrawn, sqlmock.AnyArg(), uint64(1), constants.AppStatusSubmitted).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Pet pending -> available
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "pets" SET "status"=$1 WHERE id = $2 AND status = $3`)).
		WithArgs(constants.PetStatusAvailable, uint64(20), constants.PetStatusPending).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// Reload
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "adoption_applications" WHERE "adoption_applications"."id" = $1 ORDER BY "adoption_applications"."id" LIMIT $2`)).
		WithArgs(uint64(1), 1).
		WillReturnRows(appRow(constants.AppStatusWithdrawn))

	a, err := svc.Withdraw(10, 1)
	if err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	if a.Status != constants.AppStatusWithdrawn {
		t.Errorf("status = %s, want withdrawn", a.Status)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestWithdrawNotOwner(t *testing.T) {
	svc, mock := withdrawSetup(t)
	mock.ExpectQuery(`SELECT \* FROM "adoption_applications"`).
		WithArgs(uint64(1), 1).
		WillReturnRows(appRow(constants.AppStatusSubmitted))
	if _, err := svc.Withdraw(99, 1); err == nil {
		t.Fatal("expected forbidden error for non-owner")
	}
}

func TestWithdrawRejectsNonWithdrawableStatus(t *testing.T) {
	svc, mock := withdrawSetup(t)
	mock.ExpectQuery(`SELECT \* FROM "adoption_applications"`).
		WithArgs(uint64(1), 1).
		WillReturnRows(appRow(constants.AppStatusOfflineInterview))
	_, err := svc.Withdraw(10, 1)
	if err == nil {
		t.Fatal("expected conflict for offline_interview")
	}
	if msg := err.Error(); msg == "" {
		t.Fatal("error should carry a message")
	}
}

func TestWithdrawConcurrentAdvanceKeepsOrgStatus(t *testing.T) {
	svc, mock := withdrawSetup(t)
	// Initial read: still confirmed (withdrawable).
	mock.ExpectQuery(`SELECT \* FROM "adoption_applications"`).
		WithArgs(uint64(1), 1).
		WillReturnRows(appRow(constants.AppStatusConfirmed))
	mock.ExpectBegin()
	// Conditional update matches zero rows because the org moved it to
	// offline_interview in between: withdrawal must fail and roll back.
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "adoption_applications" SET "status"=$1,"updated_at"=NOW(),"withdrawn_at"=$2 WHERE id = $3 AND status = $4`)).
		WithArgs(constants.AppStatusWithdrawn, sqlmock.AnyArg(), uint64(1), constants.AppStatusConfirmed).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	// Re-read returns the org's just-committed status.
	mock.ExpectQuery(`SELECT \* FROM "adoption_applications"`).
		WithArgs(uint64(1), 1).
		WillReturnRows(appRow(constants.AppStatusOfflineInterview))

	_, err := svc.Withdraw(10, 1)
	if err == nil {
		t.Fatal("expected conflict when org advanced concurrently")
	}
	if !errors.Is(err, errAppStatusConflict) {
		// AppError is returned, not the internal sentinel; but it must report
		// the org's current status in its message.
		if msg := err.Error(); !containsAll(msg, "offline_interview") {
			t.Errorf("conflict error should report current status, got %v", err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func containsAll(s, sub string) bool {
	return regexp.MustCompile(regexp.QuoteMeta(sub)).MatchString(s)
}

func TestUpdateStatusLosesToConcurrentWithdraw(t *testing.T) {
	svc, mock := withdrawSetup(t)
	// Org advances confirmed -> offline_interview, but the applicant withdraws
	// in between. Read shows confirmed.
	mock.ExpectQuery(`SELECT \* FROM "adoption_applications"`).
		WithArgs(uint64(1), 1).
		WillReturnRows(appRow(constants.AppStatusConfirmed))
	// Org ownership check.
	orgRows := sqlmock.NewRows([]string{"id", "user_id", "name", "status"}).
		AddRow(30, 50, "暖窝救助站", constants.OrgStatusApproved)
	mock.ExpectQuery(`SELECT \* FROM "organizations"`).
		WithArgs(uint64(50), 1).
		WillReturnRows(orgRows)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "adoption_applications" SET "status"=$1,"updated_at"=NOW() WHERE id = $2 AND status = $3`)).
		WithArgs(constants.AppStatusOfflineInterview, uint64(1), constants.AppStatusConfirmed).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	mock.ExpectQuery(`SELECT \* FROM "adoption_applications"`).
		WithArgs(uint64(1), 1).
		WillReturnRows(appRow(constants.AppStatusWithdrawn))

	_, err := svc.UpdateStatus(50, 1, "org", constants.AppStatusOfflineInterview)
	if err == nil {
		t.Fatal("expected conflict when applicant withdrew concurrently")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}
