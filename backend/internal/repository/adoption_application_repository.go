package repository

import (
	"gorm.io/gorm"

	"github.com/gbadopt/gbadopt/internal/model"
)

// AdoptionApplicationRepository handles application persistence.
type AdoptionApplicationRepository struct{ db *gorm.DB }

// NewAdoptionApplicationRepository creates the repository.
func NewAdoptionApplicationRepository(db *gorm.DB) *AdoptionApplicationRepository {
	return &AdoptionApplicationRepository{db: db}
}

// Create inserts an application.
func (r *AdoptionApplicationRepository) Create(a *model.AdoptionApplication) error {
	return translate(r.db.Create(a).Error)
}

// CreateTx inserts an application within an outer transaction.
func (r *AdoptionApplicationRepository) CreateTx(tx *gorm.DB, a *model.AdoptionApplication) error {
	return translate(tx.Create(a).Error)
}

// FindByID locates an application by id.
func (r *AdoptionApplicationRepository) FindByID(id uint) (*model.AdoptionApplication, error) {
	var a model.AdoptionApplication
	if err := translate(r.db.First(&a, id).Error); err != nil {
		return nil, err
	}
	return &a, nil
}

// Update persists an application.
func (r *AdoptionApplicationRepository) Update(a *model.AdoptionApplication) error {
	return translate(r.db.Save(a).Error)
}

// UpdateTx persists an application within an outer transaction.
func (r *AdoptionApplicationRepository) UpdateTx(tx *gorm.DB, a *model.AdoptionApplication) error {
	return translate(tx.Save(a).Error)
}

// UpdateStatusTx conditionally moves an application from wantCurrent to next,
// refreshing updated_at. The caller can set withdrawnAt to record a withdrawal.
// It returns true only when the row was actually moved; a false result means
// the status changed concurrently and the caller must abort the transaction.
func (r *AdoptionApplicationRepository) UpdateStatusTx(tx *gorm.DB, id uint, wantCurrent, next string, withdrawnAt interface{}) (bool, error) {
	updates := map[string]interface{}{
		"status":     next,
		"updated_at": gorm.Expr("NOW()"),
	}
	if withdrawnAt != nil {
		updates["withdrawn_at"] = withdrawnAt
	}
	res := tx.Model(&model.AdoptionApplication{}).
		Where("id = ? AND status = ?", id, wantCurrent).
		Updates(updates)
	if res.Error != nil {
		return false, translate(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// ListByUser returns applications of a user.
func (r *AdoptionApplicationRepository) ListByUser(userID uint) ([]model.AdoptionApplication, error) {
	var items []model.AdoptionApplication
	if err := r.db.Where("user_id = ?", userID).Order("id DESC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ListByOrg returns applications targeting an org.
func (r *AdoptionApplicationRepository) ListByOrg(orgID uint, status string) ([]model.AdoptionApplication, error) {
	var items []model.AdoptionApplication
	q := r.db.Where("org_id = ?", orgID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if err := q.Order("id DESC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// FindByUserAndPet checks an existing application for the same pet.
func (r *AdoptionApplicationRepository) FindByUserAndPet(userID, petID uint) (*model.AdoptionApplication, error) {
	var a model.AdoptionApplication
	if err := translate(r.db.Where("user_id = ? AND pet_id = ?", userID, petID).First(&a).Error); err != nil {
		return nil, err
	}
	return &a, nil
}
