package repository

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

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

// UpdateStatusCAS conditionally transitions an application from fromStatus to
// toStatus within a transaction. It returns ErrConcurrentConflict when the
// application's current status no longer matches fromStatus (e.g. a concurrent
// withdrawal or org-side status push), so the caller can fail the whole
// operation and keep the other side's freshly written status untouched.
func (r *AdoptionApplicationRepository) UpdateStatusCAS(tx *gorm.DB, id uint, fromStatus, toStatus string, extra map[string]interface{}) error {
	values := map[string]interface{}{
		"status":     toStatus,
		"updated_at": time.Now(),
	}
	for k, v := range extra {
		values[k] = v
	}
	res := tx.Model(&model.AdoptionApplication{}).
		Where("id = ? AND status = ?", id, fromStatus).
		Updates(values)
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrConcurrentConflict
	}
	return nil
}

// FindByIDForUpdate locates an application by id and takes a row lock,
// valid for the duration of the surrounding transaction.
func (r *AdoptionApplicationRepository) FindByIDForUpdate(tx *gorm.DB, id uint) (*model.AdoptionApplication, error) {
	var a model.AdoptionApplication
	if err := translate(tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&a, id).Error); err != nil {
		return nil, err
	}
	return &a, nil
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
