package service

import (
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/gbadopt/gbadopt/internal/constants"
	"github.com/gbadopt/gbadopt/internal/repository"
	"github.com/gbadopt/gbadopt/internal/util"
)

func appRows(status string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "user_id", "pet_id", "org_id", "questionnaire", "status", "withdrawn_at", "created_at", "updated_at"}).
		AddRow(1, 7, 3, 2, `{"q":1}`, status, nil, time.Now(), time.Now())
}

func withdrawnRows() *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows([]string{"id", "user_id", "pet_id", "org_id", "questionnaire", "status", "withdrawn_at", "created_at", "updated_at"}).
		AddRow(1, 7, 3, 2, `{"q":1}`, constants.AppStatusWithdrawn, now, time.Now(), now)
}

func petRows(status string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "org_id", "name", "species", "status"}).
		AddRow(3, 2, "旺财", "dog", status)
}

func TestApplicationServiceWithdrawSuccess(t *testing.T) {
	db, mock := newServiceDB(t)
	appRepo := repository.NewAdoptionApplicationRepository(db)
	petRepo := repository.NewPetRepository(db)
	orgRepo := repository.NewOrganizationRepository(db)
	svc := NewApplicationService(db, appRepo, petRepo, orgRepo, newTestLogger())

	mock.MatchExpectationsInOrder(true)
	// Pre-check: load application.
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "adoption_applications" WHERE "adoption_applications"."id" = $1 ORDER BY "adoption_applications"."id" LIMIT $2`)).
		WithArgs(uint(1), 1).
		WillReturnRows(appRows(constants.AppStatusOrgReview))
	mock.ExpectBegin()
	// SELECT ... FOR UPDATE inside the transaction.
	mock.ExpectQuery(`SELECT \* FROM "adoption_applications" .*FOR UPDATE`).
		WillReturnRows(appRows(constants.AppStatusOrgReview))
	// Conditional status flip to withdrawn.
	mock.ExpectExec(`UPDATE "adoption_applications" SET .*"status"=\$\d+`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Pet restore.
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "pets" WHERE "pets"."id" = $1 ORDER BY "pets"."id" LIMIT $2`)).
		WillReturnRows(petRows(constants.PetStatusPending))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "pets" SET`)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// Reload for response.
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "adoption_applications" WHERE "adoption_applications"."id" = $1 ORDER BY "adoption_applications"."id" LIMIT $2`)).
		WillReturnRows(withdrawnRows())

	a, err := svc.Withdraw(7, 1)
	if err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	if a.Status != constants.AppStatusWithdrawn {
		t.Errorf("status = %s, want withdrawn", a.Status)
	}
	if a.WithdrawnAt == nil || a.Questionnaire != `{"q":1}` {
		t.Errorf("withdrawn_at/questionnaire not preserved: %+v", a)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestApplicationServiceWithdrawPastInterviewFails(t *testing.T) {
	db, mock := newServiceDB(t)
	appRepo := repository.NewAdoptionApplicationRepository(db)
	petRepo := repository.NewPetRepository(db)
	orgRepo := repository.NewOrganizationRepository(db)
	svc := NewApplicationService(db, appRepo, petRepo, orgRepo, newTestLogger())

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "adoption_applications" WHERE "adoption_applications"."id" = $1 ORDER BY "adoption_applications"."id" LIMIT $2`)).
		WillReturnRows(appRows(constants.AppStatusOfflineInterview))

	_, err := svc.Withdraw(7, 1)
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.HTTPStatus != 409 {
		t.Fatalf("expected 409 conflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestApplicationServiceWithdrawNotOwner(t *testing.T) {
	db, mock := newServiceDB(t)
	appRepo := repository.NewAdoptionApplicationRepository(db)
	petRepo := repository.NewPetRepository(db)
	orgRepo := repository.NewOrganizationRepository(db)
	svc := NewApplicationService(db, appRepo, petRepo, orgRepo, newTestLogger())

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "adoption_applications" WHERE "adoption_applications"."id" = $1 ORDER BY "adoption_applications"."id" LIMIT $2`)).
		WillReturnRows(appRows(constants.AppStatusSubmitted))

	_, err := svc.Withdraw(99, 1)
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.HTTPStatus != 403 {
		t.Fatalf("expected 403 forbidden, got %v", err)
	}
}

func TestApplicationServiceWithdrawConcurrentOrgPush(t *testing.T) {
	db, mock := newServiceDB(t)
	appRepo := repository.NewAdoptionApplicationRepository(db)
	petRepo := repository.NewPetRepository(db)
	orgRepo := repository.NewOrganizationRepository(db)
	svc := NewApplicationService(db, appRepo, petRepo, orgRepo, newTestLogger())

	mock.MatchExpectationsInOrder(true)
	// Pre-check still sees a withdrawable status.
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "adoption_applications" WHERE "adoption_applications"."id" = $1 ORDER BY "adoption_applications"."id" LIMIT $2`)).
		WillReturnRows(appRows(constants.AppStatusConfirmed))
	mock.ExpectBegin()
	// Under the row lock the org has already pushed it to offline_interview.
	mock.ExpectQuery(`SELECT \* FROM "adoption_applications" .*FOR UPDATE`).
		WillReturnRows(appRows(constants.AppStatusOfflineInterview))
	mock.ExpectRollback()
	// Conflict error reloads the row to report the org's fresh status.
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "adoption_applications" WHERE "adoption_applications"."id" = $1 ORDER BY "adoption_applications"."id" LIMIT $2`)).
		WillReturnRows(appRows(constants.AppStatusOfflineInterview))

	_, err := svc.Withdraw(7, 1)
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.HTTPStatus != 409 {
		t.Fatalf("expected 409 conflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestWithdrawableStatuses(t *testing.T) {
	for _, s := range []string{
		constants.AppStatusSubmitted, constants.AppStatusOrgReview,
		constants.AppStatusCommunicating, constants.AppStatusConfirmed,
	} {
		if !constants.IsWithdrawableApplicationStatus(s) {
			t.Errorf("%s should be withdrawable", s)
		}
	}
	for _, s := range []string{
		constants.AppStatusOfflineInterview, constants.AppStatusApproved,
		constants.AppStatusRejected, constants.AppStatusWithdrawn,
	} {
		if constants.IsWithdrawableApplicationStatus(s) {
			t.Errorf("%s should not be withdrawable", s)
		}
	}
}
