//go:build integration

package service

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/gbadopt/gbadopt/internal/constants"
	"github.com/gbadopt/gbadopt/internal/model"
	"github.com/gbadopt/gbadopt/internal/repository"
	"github.com/gbadopt/gbadopt/internal/util"
)

// Real-PostgreSQL integration tests for the withdrawal flow.
//
// Run with:
//
//	DB_HOST=127.0.0.1 DB_PORT=55432 DB_USER=gbadopt_user DB_PASSWORD=gbadopt_pwd \
//	DB_NAME=gbadopt_db go test -tags=integration ./internal/service/ -run TestIntegration -v

func integrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "host=" + getenv("DB_HOST", "127.0.0.1") +
		" port=" + getenv("DB_PORT", "55432") +
		" user=" + getenv("DB_USER", "gbadopt_user") +
		" password=" + getenv("DB_PASSWORD", "gbadopt_pwd") +
		" dbname=" + getenv("DB_NAME", "gbadopt_db") + " sslmode=disable TimeZone=Asia/Shanghai"
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.AdoptionApplication{}, &model.Pet{}, &model.Organization{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func newSvc(db *gorm.DB) (*ApplicationService, *repository.AdoptionApplicationRepository, *repository.PetRepository) {
	appRepo := repository.NewAdoptionApplicationRepository(db)
	petRepo := repository.NewPetRepository(db)
	orgRepo := repository.NewOrganizationRepository(db)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewApplicationService(db, appRepo, petRepo, orgRepo, logger), appRepo, petRepo
}

func resetTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`TRUNCATE TABLE adoption_applications, pets, organizations RESTART IDENTITY CASCADE`).Error; err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func seedAppAndPet(t *testing.T, db *gorm.DB, appStatus, petStatus string) (appID, petID uint) {
	t.Helper()
	org := &model.Organization{UserID: 2, Name: "it-org", CertType: "registered", Status: constants.OrgStatusApproved}
	if err := db.Create(org).Error; err != nil {
		t.Fatalf("create org: %v", err)
	}
	pet := &model.Pet{OrgID: org.ID, Name: "it-dog", Species: "dog", Status: petStatus, ImageURLs: "[]"}
	if err := db.Create(pet).Error; err != nil {
		t.Fatalf("create pet: %v", err)
	}
	app := &model.AdoptionApplication{
		UserID: 7, PetID: pet.ID, OrgID: org.ID,
		Questionnaire: `{"q":"keep-me"}`, Status: appStatus,
	}
	if err := db.Create(app).Error; err != nil {
		t.Fatalf("create app: %v", err)
	}
	return app.ID, pet.ID
}

func TestIntegrationWithdrawHappyPath(t *testing.T) {
	db := integrationDB(t)
	resetTables(t, db)
	svc, appRepo, petRepo := newSvc(db)
	appID, petID := seedAppAndPet(t, db, constants.AppStatusCommunicating, constants.PetStatusPending)

	a, err := svc.Withdraw(7, appID)
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if a.Status != constants.AppStatusWithdrawn || a.WithdrawnAt == nil {
		t.Fatalf("unexpected app after withdraw: %+v", a)
	}
	if a.Questionnaire != `{"q":"keep-me"}` {
		t.Fatalf("questionnaire not retained: %s", a.Questionnaire)
	}

	stored, err := appRepo.FindByID(appID)
	if err != nil {
		t.Fatalf("reload app: %v", err)
	}
	if stored.Status != constants.AppStatusWithdrawn || stored.WithdrawnAt == nil {
		t.Fatalf("withdrawn state not persisted: %+v", stored)
	}
	if time.Since(*stored.WithdrawnAt) > time.Minute {
		t.Fatalf("withdrawn_at not recent: %v", *stored.WithdrawnAt)
	}
	pet, err := petRepo.FindByID(petID)
	if err != nil {
		t.Fatalf("reload pet: %v", err)
	}
	if pet.Status != constants.PetStatusAvailable {
		t.Fatalf("pet status = %s, want available", pet.Status)
	}
}

func TestIntegrationWithdrawPastInterviewKeepsOrgStatus(t *testing.T) {
	db := integrationDB(t)
	resetTables(t, db)
	svc, appRepo, petRepo := newSvc(db)
	appID, petID := seedAppAndPet(t, db, constants.AppStatusOfflineInterview, constants.PetStatusPending)

	if _, err := svc.Withdraw(7, appID); err == nil {
		t.Fatal("withdraw past offline_interview must fail")
	} else if ae, ok := err.(*util.AppError); !ok || ae.HTTPStatus != 409 {
		t.Fatalf("want 409, got %v", err)
	}
	a, _ := appRepo.FindByID(appID)
	if a.Status != constants.AppStatusOfflineInterview || a.WithdrawnAt != nil {
		t.Fatalf("org status must be preserved, got %+v", a)
	}
	pet, _ := petRepo.FindByID(petID)
	if pet.Status != constants.PetStatusPending {
		t.Fatalf("pet must stay pending, got %s", pet.Status)
	}

	// Approved flow is unaffected by withdrawal.
	if _, err := svc.UpdateStatus(2, appID, "org", constants.AppStatusApproved); err != nil {
		t.Fatalf("approve after failed withdraw: %v", err)
	}
	pet, _ = petRepo.FindByID(petID)
	if pet.Status != constants.PetStatusAdopted {
		t.Fatalf("pet status = %s, want adopted", pet.Status)
	}
}

func TestIntegrationWithdrawNotOwner(t *testing.T) {
	db := integrationDB(t)
	resetTables(t, db)
	svc, appRepo, petRepo := newSvc(db)
	appID, petID := seedAppAndPet(t, db, constants.AppStatusSubmitted, constants.PetStatusPending)

	if _, err := svc.Withdraw(8, appID); err == nil {
		t.Fatal("non-owner withdraw must fail")
	} else if ae, ok := err.(*util.AppError); !ok || ae.HTTPStatus != 403 {
		t.Fatalf("want 403, got %v", err)
	}
	a, _ := appRepo.FindByID(appID)
	if a.Status != constants.AppStatusSubmitted || a.WithdrawnAt != nil {
		t.Fatalf("state changed by non-owner: %+v", a)
	}
	pet, _ := petRepo.FindByID(petID)
	if pet.Status != constants.PetStatusPending {
		t.Fatalf("pet changed by non-owner withdraw: %s", pet.Status)
	}
}

func TestIntegrationWithdrawnAppCannotBePushed(t *testing.T) {
	db := integrationDB(t)
	resetTables(t, db)
	svc, _, petRepo := newSvc(db)
	appID, petID := seedAppAndPet(t, db, constants.AppStatusSubmitted, constants.PetStatusPending)

	if _, err := svc.Withdraw(7, appID); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	// Org tries to push the same application afterwards: must 409 and the pet
	// must stay available.
	if _, err := svc.UpdateStatus(2, appID, "org", constants.AppStatusOrgReview); err == nil {
		t.Fatal("push on withdrawn application must fail")
	} else if ae, ok := err.(*util.AppError); !ok || ae.HTTPStatus != 409 {
		t.Fatalf("want 409, got %v", err)
	}
	pet, _ := petRepo.FindByID(petID)
	if pet.Status != constants.PetStatusAvailable {
		t.Fatalf("pet status = %s, want available", pet.Status)
	}
}

func TestIntegrationConcurrentWithdrawVsOrgPush(t *testing.T) {
	db := integrationDB(t)
	resetTables(t, db)
	svc, appRepo, petRepo := newSvc(db)
	appID, petID := seedAppAndPet(t, db, constants.AppStatusConfirmed, constants.PetStatusPending)

	// Separate connection that parks itself inside a transaction holding the
	// application row lock, simulating an org push in flight.
	blocker, err := db.DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	conn, err := blocker.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		t.Fatalf("begin blocker: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		`UPDATE adoption_applications SET status = 'offline_interview', updated_at = NOW() WHERE id = $1`, appID); err != nil {
		t.Fatalf("blocker update: %v", err)
	}

	// Withdrawal must block until the blocker commits, then fail because the
	// application is already at offline_interview.
	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, e := svc.Withdraw(7, appID)
		done <- result{e}
	}()
	select {
	case <-done:
		t.Fatal("withdraw returned while blocker holds the row lock")
	case <-time.After(500 * time.Millisecond):
	}

	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatalf("commit blocker: %v", err)
	}
	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("withdraw must fail after concurrent org push")
		} else if ae, ok := r.err.(*util.AppError); !ok || ae.HTTPStatus != 409 {
			t.Fatalf("want 409, got %v", r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("withdraw never returned after blocker release")
	}

	a, _ := appRepo.FindByID(appID)
	if a.Status != constants.AppStatusOfflineInterview || a.WithdrawnAt != nil {
		t.Fatalf("org status must win and be retained, got %+v", a)
	}
	pet, _ := petRepo.FindByID(petID)
	if pet.Status != constants.PetStatusPending {
		t.Fatalf("pet must stay pending on failed withdraw, got %s", pet.Status)
	}
}

func TestIntegrationRepeatApplicationBlockAfterWithdraw(t *testing.T) {
	db := integrationDB(t)
	resetTables(t, db)
	svc, _, petRepo := newSvc(db)
	appID, petID := seedAppAndPet(t, db, constants.AppStatusSubmitted, constants.PetStatusPending)

	if _, err := svc.Withdraw(7, appID); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	pet, _ := petRepo.FindByID(petID)
	if pet.Status != constants.PetStatusAvailable {
		t.Fatalf("pet should be available again, got %s", pet.Status)
	}
	// Repeat-application restriction is unchanged: the same user cannot apply
	// for the same pet again.
	if _, err := svc.Submit(7, petID, `{"new":true}`); err == nil {
		t.Fatal("repeat submit after withdraw must still be rejected")
	} else if ae, ok := err.(*util.AppError); !ok || ae.HTTPStatus != 409 {
		t.Fatalf("want 409, got %v", err)
	}
}
