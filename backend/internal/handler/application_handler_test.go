package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/gbadopt/gbadopt/internal/config"
	"github.com/gbadopt/gbadopt/internal/constants"
	"github.com/gbadopt/gbadopt/internal/middleware"
	"github.com/gbadopt/gbadopt/internal/repository"
	"github.com/gbadopt/gbadopt/internal/service"
	"github.com/gbadopt/gbadopt/internal/util"
)

func newWithdrawRouter(t *testing.T) (*gin.Engine, sqlmock.Sqlmock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}
	appRepo := repository.NewAdoptionApplicationRepository(db)
	petRepo := repository.NewPetRepository(db)
	orgRepo := repository.NewOrganizationRepository(db)
	log := util.NewLogger()
	svc := service.NewApplicationService(db, appRepo, petRepo, orgRepo, log)
	h := NewApplicationHandler(svc, log)

	cfg := config.Load()
	r := gin.New()
	r.Use(middleware.ErrorHandler(log))
	v1 := r.Group("/api/v1")
	apps := v1.Group("/applications", middleware.AuthRequired(cfg))
	apps.PUT("/:id/withdraw", middleware.RequireRole("user"), h.Withdraw)
	return r, mock
}

func appRowsFor(status string) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows(
		[]string{"id", "user_id", "pet_id", "org_id", "questionnaire", "status", "withdrawn_at", "created_at", "updated_at"},
	).AddRow(1, 10, 20, 30, "{}", status, nil, now, now)
}

func userToken(t *testing.T, role string) string {
	t.Helper()
	cfg := config.Load()
	tok, err := util.GenerateToken(10, "adopter", role, cfg.JWTSecret, time.Hour)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	return tok
}

func doWithdraw(r *gin.Engine, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/v1/applications/1/withdraw", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestWithdrawHandlerSuccess(t *testing.T) {
	r, mock := newWithdrawRouter(t)
	mock.MatchExpectationsInOrder(true)
	mock.ExpectQuery(`SELECT \* FROM "adoption_applications"`).
		WillReturnRows(appRowsFor(constants.AppStatusSubmitted))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "adoption_applications"`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "pets"`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT \* FROM "adoption_applications"`).
		WillReturnRows(appRowsFor(constants.AppStatusWithdrawn))

	w := doWithdraw(r, userToken(t, "user"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Status      string     `json:"status"`
			WithdrawnAt *time.Time `json:"withdrawn_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != 0 || body.Data.Status != "withdrawn" {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestWithdrawHandlerUnauthenticated(t *testing.T) {
	r, _ := newWithdrawRouter(t)
	w := doWithdraw(r, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestWithdrawHandlerForbiddenForOrg(t *testing.T) {
	r, _ := newWithdrawRouter(t)
	// RequireRole rejects before any DB access.
	w := doWithdraw(r, userToken(t, "org"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 body=%s", w.Code, w.Body.String())
	}
}

func TestWithdrawHandlerConflictAfterOfflineInterview(t *testing.T) {
	r, mock := newWithdrawRouter(t)
	mock.ExpectQuery(`SELECT \* FROM "adoption_applications"`).
		WillReturnRows(appRowsFor(constants.AppStatusOfflineInterview))

	w := doWithdraw(r, userToken(t, "user"))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not withdrawable") {
		t.Errorf("body should explain not withdrawable: %s", w.Body.String())
	}
}

func TestWithdrawHandlerConflictAfterConcurrentAdvance(t *testing.T) {
	r, mock := newWithdrawRouter(t)
	// Read still sees confirmed; the conditional update then matches zero rows
	// because the org advanced it concurrently.
	mock.ExpectQuery(`SELECT \* FROM "adoption_applications"`).
		WillReturnRows(appRowsFor(constants.AppStatusConfirmed))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "adoption_applications"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	mock.ExpectQuery(`SELECT \* FROM "adoption_applications"`).
		WillReturnRows(appRowsFor(constants.AppStatusOfflineInterview))

	w := doWithdraw(r, userToken(t, "user"))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 body=%s", w.Code, w.Body.String())
	}
	// The org's just-committed status must be reported so the client refreshes
	// to it.
	if !strings.Contains(w.Body.String(), "offline_interview") {
		t.Errorf("body should report current org status: %s", w.Body.String())
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
