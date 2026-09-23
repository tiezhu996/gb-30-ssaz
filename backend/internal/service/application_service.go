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

// errAppStatusConflict signals that an application moved between the
// read and the conditional write, so the whole operation must fail.
var errAppStatusConflict = errors.New("application status changed concurrently")

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
	// The move is conditional: if the applicant withdraws (or anyone else
	// moves the row) concurrently, the transition fails and the freshly
	// observed status is kept.
	current := a.Status
	if next == constants.AppStatusApproved {
		pet, err := s.petRepo.FindByID(a.PetID)
		if err != nil {
			return nil, fmt.Errorf("application status pet find: %w", err)
		}
		pet.Status = constants.PetStatusAdopted
		err = s.db.Transaction(func(tx *gorm.DB) error {
			ok, err := s.repo.UpdateStatusTx(tx, a.ID, current, next, nil)
			if err != nil {
				return fmt.Errorf("application status update: %w", err)
			}
			if !ok {
				return errAppStatusConflict
			}
			if err := s.petRepo.UpdateTx(tx, pet); err != nil {
				return fmt.Errorf("application status pet update: %w", err)
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, errAppStatusConflict) {
				return nil, s.statusConflictError(id)
			}
			s.logger.Error(fmt.Sprintf(constants.LogAppStatusChangeFailed, id), "error", err)
			return nil, err
		}
	} else {
		err = s.db.Transaction(func(tx *gorm.DB) error {
			ok, err := s.repo.UpdateStatusTx(tx, a.ID, current, next, nil)
			if err != nil {
				return fmt.Errorf("application status update: %w", err)
			}
			if !ok {
				return errAppStatusConflict
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, errAppStatusConflict) {
				return nil, s.statusConflictError(id)
			}
			s.logger.Error(fmt.Sprintf(constants.LogAppStatusChangeFailed, id), "error", err)
			return nil, err
		}
	}
	a, err = s.repo.FindByID(id)
	if err != nil {
		return nil, fmt.Errorf("application status reload: %w", err)
	}
	s.logger.Info(fmt.Sprintf(constants.LogAppStatusChanged, id, next), "id", id)
	return a, nil
}

// Withdraw lets an applicant pull back an application that has not reached the
// offline interview. The application becomes a terminal "withdrawn" row and
// the pet becomes adoptable again, but only while this application is still
// the one holding the pet (status pending). If the org has meanwhile advanced
// the application past the withdrawable point, the whole withdrawal fails and
// the org's just-updated status is preserved.
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
	if !constants.IsWithdrawableStatus(a.Status) {
		return nil, util.NewAppError(409, constants.CodeConflict,
			fmt.Sprintf("AdoptionApplication[id=%d] withdraw failed: status=%s not withdrawable", id, a.Status))
	}
	current := a.Status
	now := time.Now()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// Conditional on the status observed at read time: a concurrent org
		// advancement (e.g. confirmed -> offline_interview) makes this match
		// zero rows and the withdrawal is rejected wholesale.
		ok, err := s.repo.UpdateStatusTx(tx, a.ID, current, constants.AppStatusWithdrawn, now)
		if err != nil {
			return fmt.Errorf("application withdraw update: %w", err)
		}
		if !ok {
			return errAppStatusConflict
		}
		// The pet returns to available only while this application still holds
		// it (pending). Any other state is left untouched.
		if _, err := s.petRepo.UpdateStatusIfTx(tx, a.PetID, constants.PetStatusPending, constants.PetStatusAvailable); err != nil {
			return fmt.Errorf("application withdraw pet update: %w", err)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errAppStatusConflict) {
			return nil, s.statusConflictError(id)
		}
		s.logger.Error(fmt.Sprintf(constants.LogAppWithdrawFailed, id), "error", err)
		return nil, err
	}
	a, err = s.repo.FindByID(id)
	if err != nil {
		return nil, fmt.Errorf("application withdraw reload: %w", err)
	}
	s.logger.Info(fmt.Sprintf(constants.LogAppWithdrawn, id), "id", id)
	return a, nil
}

// statusConflictError re-reads the application and reports the status the org
// just committed, so callers can converge to it after a failed concurrent move.
func (s *ApplicationService) statusConflictError(id uint) error {
	fresh, err := s.repo.FindByID(id)
	if err != nil {
		return util.NewAppError(409, constants.CodeConflict,
			fmt.Sprintf("AdoptionApplication[id=%d] status changed concurrently", id))
	}
	return util.NewAppError(409, constants.CodeConflict,
		fmt.Sprintf("AdoptionApplication[id=%d] status changed concurrently: current=%s", id, fresh.Status))
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
