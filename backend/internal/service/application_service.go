package service

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/gbadopt/gbadopt/internal/constants"
	"github.com/gbadopt/gbadopt/internal/model"
	"github.com/gbadopt/gbadopt/internal/repository"
	"github.com/gbadopt/gbadopt/internal/util"
)

// ApplicationService implements the adoption application state machine.
type ApplicationService struct {
	db      *gorm.DB
	repo    *repository.AdoptionApplicationRepository
	petRepo *repository.PetRepository
	orgRepo *repository.OrganizationRepository
	logger  *slog.Logger
}

// NewApplicationService creates an ApplicationService.
func NewApplicationService(db *gorm.DB, repo *repository.AdoptionApplicationRepository, petRepo *repository.PetRepository, orgRepo *repository.OrganizationRepository, logger *slog.Logger) *ApplicationService {
	return &ApplicationService{db: db, repo: repo, petRepo: petRepo, orgRepo: orgRepo, logger: logger}
}

// Submit creates an application from a user to a pet.
func (s *ApplicationService) Submit(userID, petID uint, questionnaire string) (*model.AdoptionApplication, error) {
	pet, err := s.petRepo.FindByID(petID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, util.NewAppError(404, constants.CodeNotFound, fmt.Sprintf("Pet[id=%d] not found", petID))
		}
		return nil, fmt.Errorf("application submit pet find: %w", err)
	}
	if pet.Status != constants.PetStatusAvailable {
		return nil, util.NewAppError(409, constants.CodeConflict,
			fmt.Sprintf("Application[pet_id=%d] submit failed: pet not available (status=%s)", petID, pet.Status))
	}
	if exist, err := s.repo.FindByUserAndPet(userID, petID); err == nil && exist != nil {
		return nil, util.NewAppError(409, constants.CodeConflict,
			fmt.Sprintf("Application[user_id=%d pet_id=%d] submit failed: already applied", userID, petID))
	}
	a := &model.AdoptionApplication{
		UserID: userID, PetID: petID, OrgID: pet.OrgID,
		Questionnaire: questionnaire, Status: constants.AppStatusSubmitted,
	}
	if a.Questionnaire == "" {
		a.Questionnaire = "{}"
	}
	// pet becomes pending atomically with the application creation.
	pet.Status = constants.PetStatusPending
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.repo.CreateTx(tx, a); err != nil {
			return fmt.Errorf("application submit: %w", err)
		}
		if err := s.petRepo.UpdateTx(tx, pet); err != nil {
			return fmt.Errorf("application submit pet update: %w", err)
		}
		return nil
	})
	if err != nil {
		s.logger.Error(fmt.Sprintf(constants.LogAppSubmitFailed, petID), "error", err)
		return nil, err
	}
	s.logger.Info(fmt.Sprintf(constants.LogAppSubmitSuccess, a.ID, petID), "id", a.ID)
	return a, nil
}

// UpdateStatus transitions an application along the state machine.
//
// The transition is applied with a compare-and-swap on the current status so
// that an org-side push can never overwrite a concurrent withdrawal (and vice
// versa). On a lost race the operation fails and the freshly persisted status
// is reported back to the caller.
func (s *ApplicationService) UpdateStatus(userID, id uint, role string, next string) (*model.AdoptionApplication, error) {
	if !constants.IsValidApplicationStatus(next) {
		return nil, util.NewAppError(422, constants.CodeValidationError,
			fmt.Sprintf("Application[id=%d] status=%s invalid", id, next))
	}
	a, err := s.repo.FindByID(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, util.NewAppError(404, constants.CodeNotFound, fmt.Sprintf("AdoptionApplication[id=%d] not found", id))
		}
		return nil, fmt.Errorf("application status find: %w", err)
	}
	if role == "org" {
		org, err := s.orgRepo.FindByUserID(userID)
		if err != nil || org.ID != a.OrgID {
			return nil, util.NewAppError(403, constants.CodeForbidden,
				fmt.Sprintf("AdoptionApplication[id=%d] status change failed: user_id=%d not org owner", id, userID))
		}
	} else if role == "user" && a.UserID != userID {
		return nil, util.NewAppError(403, constants.CodeForbidden,
			fmt.Sprintf("AdoptionApplication[id=%d] status change failed: not owner", id))
	}
	allowed := false
	for _, s2 := range constants.NextApplicationStatuses(a.Status) {
		if s2 == next {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, util.NewAppError(409, constants.CodeConflict,
			fmt.Sprintf("AdoptionApplication[id=%d] status change failed: %s -> %s not allowed", id, a.Status, next))
	}
	fromStatus := a.Status
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// Lock the application row first, then the pet row, matching the lock
		// order used by Withdraw to avoid deadlocks under concurrency.
		if err := s.repo.UpdateStatusCAS(tx, id, fromStatus, next, nil); err != nil {
			if errors.Is(err, repository.ErrConcurrentConflict) {
				return errConcurrentStatus(id)
			}
			return fmt.Errorf("application status update: %w", err)
		}
		if next == constants.AppStatusApproved {
			pet, err := s.petRepo.FindByID(a.PetID)
			if err != nil {
				return fmt.Errorf("application status pet find: %w", err)
			}
			pet.Status = constants.PetStatusAdopted
			if err := s.petRepo.UpdateTx(tx, pet); err != nil {
				return fmt.Errorf("application status pet update: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, repository.ErrConcurrentConflict) {
			return nil, s.statusConflictError(id)
		}
		s.logger.Error(fmt.Sprintf(constants.LogAppStatusChangeFailed, id), "error", err)
		return nil, err
	}
	s.logger.Info(fmt.Sprintf(constants.LogAppStatusChanged, id, next), "id", id)
	return s.repo.FindByID(id)
}

// Withdraw lets an applicant withdraw an application that has not yet entered
// the offline interview. The application becomes "withdrawn" (questionnaire and
// timeline retained) and the pet is restored to available in the same
// transaction. If the org has already advanced the application past the
// withdrawable stages, the whole withdrawal fails and the org's freshly
// updated status is preserved.
func (s *ApplicationService) Withdraw(userID, id uint) (*model.AdoptionApplication, error) {
	a, err := s.repo.FindByID(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, util.NewAppError(404, constants.CodeNotFound, fmt.Sprintf("AdoptionApplication[id=%d] not found", id))
		}
		return nil, fmt.Errorf("application withdraw find: %w", err)
	}
	if a.UserID != userID {
		return nil, util.NewAppError(403, constants.CodeForbidden,
			fmt.Sprintf("AdoptionApplication[id=%d] withdraw failed: not owner", id))
	}
	if !constants.IsWithdrawableApplicationStatus(a.Status) {
		return nil, util.NewAppError(409, constants.CodeConflict,
			fmt.Sprintf("AdoptionApplication[id=%d] withdraw failed: status=%s no longer withdrawable", id, a.Status))
	}
	fromStatus := a.Status
	now := time.Now()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// Lock the application row and re-check under the lock so a concurrent
		// org-side push and a withdrawal cannot interleave.
		locked, err := s.repo.FindByIDForUpdate(tx, id)
		if err != nil {
			return fmt.Errorf("application withdraw lock: %w", err)
		}
		if !constants.IsWithdrawableApplicationStatus(locked.Status) {
			return errConcurrentStatus(id)
		}
		if err := s.repo.UpdateStatusCAS(tx, id, locked.Status, constants.AppStatusWithdrawn,
			map[string]interface{}{"withdrawn_at": now}); err != nil {
			if errors.Is(err, repository.ErrConcurrentConflict) {
				return errConcurrentStatus(id)
			}
			return fmt.Errorf("application withdraw update: %w", err)
		}
		pet, err := s.petRepo.FindByID(locked.PetID)
		if err != nil {
			return fmt.Errorf("application withdraw pet find: %w", err)
		}
		pet.Status = constants.PetStatusAvailable
		if err := s.petRepo.UpdateTx(tx, pet); err != nil {
			return fmt.Errorf("application withdraw pet update: %w", err)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, repository.ErrConcurrentConflict) {
			s.logger.Warn(fmt.Sprintf(constants.LogAppWithdrawConflict, id, fromStatus), "id", id)
			return nil, s.statusConflictError(id)
		}
		s.logger.Error(fmt.Sprintf(constants.LogAppWithdrawFailed, id), "error", err)
		return nil, err
	}
	s.logger.Info(fmt.Sprintf(constants.LogAppWithdrawn, id), "id", id)
	return s.repo.FindByID(id)
}

// statusConflictError reloads the application and reports its current status so
// the client can refresh and converge with the concurrent decision.
func (s *ApplicationService) statusConflictError(id uint) error {
	current, findErr := s.repo.FindByID(id)
	currentStatus := "unknown"
	if findErr == nil {
		currentStatus = current.Status
	}
	return util.NewAppError(409, constants.CodeConflict,
		fmt.Sprintf("AdoptionApplication[id=%d] update failed: concurrent status change, current status=%s", id, currentStatus))
}

// errConcurrentStatus tags an in-transaction lost race so it can be translated
// to a 409 after rollback.
func errConcurrentStatus(id uint) error {
	return fmt.Errorf("%w: application %d", repository.ErrConcurrentConflict, id)
}

// ListByUser returns a user's applications.
func (s *ApplicationService) ListByUser(userID uint) ([]model.AdoptionApplication, error) {
	items, err := s.repo.ListByUser(userID)
	if err != nil {
		return nil, fmt.Errorf("application list by user: %w", err)
	}
	return items, nil
}

// ListByOrg returns applications for an org.
func (s *ApplicationService) ListByOrg(userID uint, status string) ([]model.AdoptionApplication, error) {
	org, err := s.orgRepo.FindByUserID(userID)
	if err != nil {
		return nil, util.NewAppError(403, constants.CodeForbidden, "org profile not found")
	}
	items, err := s.repo.ListByOrg(org.ID, status)
	if err != nil {
		return nil, fmt.Errorf("application list by org: %w", err)
	}
	return items, nil
}
