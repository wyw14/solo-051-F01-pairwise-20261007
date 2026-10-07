package service

import (
	"fmt"
	"github.com/wyw14/cry-161/internal/model"
	"github.com/wyw14/cry-161/internal/recipe"
)

// ErrDraftConflict is the service-level marker for stale-revision submits.
// The latest draft is always attached so callers can reconcile without a
// second round trip.
type ErrDraftConflict struct {
	Latest recipe.Draft
}

func (e *ErrDraftConflict) Error() string {
	return fmt.Sprintf("draft revision conflict for recipe %s", e.Latest.Recipe.ID)
}

func wrapDraftError(err error) error {
	if conflict, ok := err.(*recipe.ConflictError); ok {
		return &ErrDraftConflict{Latest: conflict.Latest}
	}
	return err
}

// CreateDraft opens an editable draft from the active published recipe.
func (s *Service) CreateDraft(recipeID model.ID, operator string) (recipe.Draft, error) {
	active, err := s.Recipes.Active(recipeID)
	if err != nil {
		return recipe.Draft{}, err
	}
	saved := s.Drafts.SaveForOperator(active, operator)
	draft, ok := s.Drafts.GetDraft(saved.ID)
	if !ok {
		return recipe.Draft{}, fmt.Errorf("draft not found")
	}
	return draft, nil
}

// GetDraft returns a draft including its revision and review status.
func (s *Service) GetDraft(recipeID model.ID) (recipe.Draft, error) {
	draft, ok := s.Drafts.GetDraft(recipeID)
	if !ok {
		return recipe.Draft{}, fmt.Errorf("draft not found")
	}
	return draft, nil
}

// SubmitDraftLayer applies a layer edit with optimistic concurrency. A stale
// expected revision returns *ErrDraftConflict carrying the untouched latest
// draft; resubmitting the identical layer at the same revision is a no-op.
func (s *Service) SubmitDraftLayer(change recipe.LayerChange) (recipe.Draft, error) {
	draft, err := s.Drafts.SubmitLayer(change)
	if err != nil {
		return recipe.Draft{}, wrapDraftError(err)
	}
	return draft, nil
}

// ReviewDraft records a reviewer's approval or rejection against a specific
// revision. Reviewing a stale revision conflicts and keeps the latest draft.
func (s *Service) ReviewDraft(recipeID model.ID, expectedRevision int, reviewer string, approved bool) (recipe.Draft, error) {
	draft, err := s.Drafts.Review(recipeID, expectedRevision, reviewer, approved)
	if err != nil {
		return recipe.Draft{}, wrapDraftError(err)
	}
	return draft, nil
}

// FreezeDraft freezes the current draft revision into a revision record.
// Freezing is rejected until the current revision is review-approved and is
// idempotent for repeated calls on the same revision.
func (s *Service) FreezeDraft(recipeID model.ID) (recipe.Revision, recipe.Draft, error) {
	revision, draft, err := s.Drafts.Freeze(recipeID, s.Revisions)
	if err != nil {
		return recipe.Revision{}, recipe.Draft{}, err
	}
	return revision, draft, nil
}
