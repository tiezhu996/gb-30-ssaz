package repository

import (
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newDryRunDB builds a GORM DB that only renders SQL without executing it and
// captures the final UPDATE statement for assertions.
func newDryRunDB(t *testing.T) (*gorm.DB, *string) {
	t.Helper()
	sqlDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DryRun:                 true,
		SkipDefaultTransaction: true,
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm dry run: %v", err)
	}
	var captured string
	if err := db.Callback().Update().After("gorm:update").Register("capture_sql", func(tx *gorm.DB) {
		captured = tx.Statement.SQL.String()
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	return db, &captured
}

func TestUpdateStatusTxSQL(t *testing.T) {
	db, captured := newDryRunDB(t)
	repo := NewAdoptionApplicationRepository(db)
	when := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)

	_, err := repo.UpdateStatusTx(db, 7, "confirmed", "withdrawn", when)
	if err != nil {
		t.Fatalf("UpdateStatusTx: %v", err)
	}
	sql := *captured
	for _, want := range []string{
		`UPDATE "adoption_applications" SET `,
		`status`, `updated_at`, `withdrawn_at`,
		`WHERE id = $`, `AND status = $`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("generated SQL %q missing %q", sql, want)
		}
	}
}

func TestUpdateStatusTxSQLWithoutWithdrawnAt(t *testing.T) {
	db, captured := newDryRunDB(t)
	repo := NewAdoptionApplicationRepository(db)

	_, err := repo.UpdateStatusTx(db, 7, "submitted", "org_review", nil)
	if err != nil {
		t.Fatalf("UpdateStatusTx: %v", err)
	}
	if strings.Contains(*captured, "withdrawn_at") {
		t.Errorf("forward transition SQL must not touch withdrawn_at: %s", *captured)
	}
}

func TestPetUpdateStatusIfTxSQL(t *testing.T) {
	db, captured := newDryRunDB(t)
	repo := NewPetRepository(db)

	_, err := repo.UpdateStatusIfTx(db, 9, "pending", "available")
	if err != nil {
		t.Fatalf("UpdateStatusIfTx: %v", err)
	}
	sql := *captured
	if !strings.Contains(sql, `UPDATE "pets" SET "status"=`) {
		t.Errorf("unexpected pet update SQL: %s", sql)
	}
	if !strings.Contains(sql, `id = $`) || !strings.Contains(sql, `AND status = $`) {
		t.Errorf("pet update must be conditional on id AND current status: %s", sql)
	}
}
