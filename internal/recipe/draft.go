package recipe

import (
	"errors"
	"fmt"
	"github.com/wyw14/cry-161/internal/model"
	"sync"
	"time"
)

// ReviewState tracks the sign-off lifecycle of a draft.
type ReviewState string

const (
	// ReviewPending means the draft has not been approved for freezing.
	ReviewPending ReviewState = "pending"
	// ReviewApproved means a reviewer signed off the current revision.
	ReviewApproved ReviewState = "approved"
	// ReviewRejected means a reviewer rejected the current revision.
	ReviewRejected ReviewState = "rejected"
)

// Draft wraps an editable recipe with optimistic-concurrency and review
// metadata. Revision is the version counter carried by every client submit.
type Draft struct {
	Recipe       *model.Recipe `json:"recipe"`
	BaseRevision int           `json:"base_revision"` // frozen revision this draft was seeded from
	Operator     string        `json:"operator"`      // last editor
	Reviewer     string        `json:"reviewer"`      // person who last set the review state
	ReviewStatus ReviewState   `json:"review_status"`
	ReviewedAt   time.Time     `json:"reviewed_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

// LayerChange is a submitted layer edit carrying the revision the editor based
// their change on and the operator making the change.
type LayerChange struct {
	RecipeID         model.ID
	ExpectedRevision int
	Layer            model.Layer
	Operator         string
}

// ConflictError reports a stale submit. The stored draft is never mutated on
// conflict and its latest state is attached so the caller can reconcile.
type ConflictError struct {
	Message string
	Latest  Draft
}

func (e *ConflictError) Error() string { return e.Message }

// NewConflictError builds the error returned for a stale submit.
func NewConflictError(message string, latest Draft) *ConflictError {
	return &ConflictError{Message: message, Latest: latest}
}

// IsConflict reports whether err is a draft revision conflict.
func IsConflict(err error) bool {
	var conflict *ConflictError
	return errors.As(err, &conflict)
}

type DraftBook struct {
	mu     sync.RWMutex
	drafts map[model.ID]*Draft
}

func NewDraftBook() *DraftBook {
	return &DraftBook{drafts: map[model.ID]*Draft{}}
}

// Save seeds (or re-seeds) a draft from a recipe, resetting review state.
func (b *DraftBook) Save(recipe *model.Recipe) *model.Recipe {
	return b.save(recipe, "")
}

func (b *DraftBook) save(recipe *model.Recipe, operator string) *model.Recipe {
	b.mu.Lock()
	defer b.mu.Unlock()
	copy := cloneRecipe(recipe)
	copy.Published = false
	draft := &Draft{
		Recipe:       copy,
		BaseRevision: recipe.Revision,
		Operator:     operator,
		ReviewStatus: ReviewPending,
		UpdatedAt:    time.Now().UTC(),
	}
	b.drafts[copy.ID] = draft
	return cloneRecipe(copy)
}

// SaveForOperator seeds a draft and records the initiating operator.
func (b *DraftBook) SaveForOperator(recipe *model.Recipe, operator string) *model.Recipe {
	return b.save(recipe, operator)
}

// GetDraft returns the full draft wrapper including revision and review state.

func (b *DraftBook) GetDraft(id model.ID) (Draft, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	value, ok := b.drafts[id]
	if !ok {
		return Draft{}, false
	}
	return cloneDraft(value), true
}

// Get returns the draft's recipe snapshot.
func (b *DraftBook) Get(id model.ID) (*model.Recipe, bool) {
	draft, ok := b.GetDraft(id)
	if !ok {
		return nil, false
	}
	return draft.Recipe, true
}

// UpdateLayer applies an edit without revision checks (audit/legacy path).
func (b *DraftBook) UpdateLayer(id model.ID, layer model.Layer) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	value, ok := b.drafts[id]
	if !ok {
		return false
	}
	value.Recipe.Layers[layer.Number] = layer
	value.Recipe.Revision++
	value.UpdatedAt = time.Now().UTC()
	value.ReviewStatus = ReviewPending
	value.Reviewer = ""
	value.ReviewedAt = time.Time{}
	return true
}

// SubmitLayer applies a layer edit with optimistic concurrency:
//   - unknown draft -> not found
//   - submitted layer already identical to the stored layer (a retried
//     request, possibly carrying an expected revision that has since
//     advanced) -> idempotent replay returning the current draft unchanged
//   - expected revision older than stored revision with divergent content ->
//     ConflictError, and the stored draft is left untouched (latest preserved)
//   - otherwise the edit is applied, revision advances and review resets.
//
// Operator records who made the change; review approval is invalidated by any
// applied change so a freeze can never follow a stale sign-off.
func (b *DraftBook) SubmitLayer(change LayerChange) (Draft, error) {
	if change.Operator == "" {
		return Draft{}, fmt.Errorf("operator is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	value, ok := b.drafts[change.RecipeID]
	if !ok {
		return Draft{}, fmt.Errorf("draft not found")
	}
	if existing, has := value.Recipe.Layers[change.Layer.Number]; has && layersEqual(existing, change.Layer) {
		// Idempotent replay: the requested end state is already the stored
		// state (typical for a network retry), so there is nothing to apply
		// and no update can be lost. Revision, operator and review state are
		// left untouched.
		return cloneDraft(value), nil
	}
	if change.ExpectedRevision != value.Recipe.Revision {
		return Draft{}, NewConflictError(fmt.Sprintf("revision conflict: expected %d but draft is at revision %d", change.ExpectedRevision, value.Recipe.Revision), cloneDraft(value))
	}
	value.Recipe.Layers[change.Layer.Number] = change.Layer
	value.Recipe.Revision++
	value.Operator = change.Operator
	value.UpdatedAt = time.Now().UTC()
	value.ReviewStatus = ReviewPending
	value.Reviewer = ""
	value.ReviewedAt = time.Time{}
	return cloneDraft(value), nil
}

// Review records a reviewer's decision for the draft's current revision.
// A draft must be pending review (i.e. edited since last sign-off) and the
// decision must be made against the current revision; otherwise it is a
// conflict carrying the latest draft.
func (b *DraftBook) Review(id model.ID, expectedRevision int, reviewer string, approved bool) (Draft, error) {
	if reviewer == "" {
		return Draft{}, fmt.Errorf("reviewer is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	value, ok := b.drafts[id]
	if !ok {
		return Draft{}, fmt.Errorf("draft not found")
	}
	if expectedRevision != value.Recipe.Revision {
		return Draft{}, NewConflictError(fmt.Sprintf("revision conflict: cannot review revision %d, draft is at revision %d", expectedRevision, value.Recipe.Revision), cloneDraft(value))
	}
	if approved {
		value.ReviewStatus = ReviewApproved
	} else {
		value.ReviewStatus = ReviewRejected
	}
	value.Reviewer = reviewer
	value.ReviewedAt = time.Now().UTC()
	return cloneDraft(value), nil
}

// FreezeReady reports whether the current revision may be frozen.
func (d Draft) FreezeReady() bool {
	return d.ReviewStatus == ReviewApproved
}

// Freeze compiles and freezes the draft's recipe into the given store. It is
// gated on review approval of the current revision and is idempotent: a
// duplicate freeze of the same revision returns the existing frozen revision.
func (b *DraftBook) Freeze(id model.ID, revisions *RevisionStore) (Revision, Draft, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	value, ok := b.drafts[id]
	if !ok {
		return Revision{}, Draft{}, fmt.Errorf("draft not found")
	}
	if value.ReviewStatus != ReviewApproved {
		return Revision{}, cloneDraft(value), fmt.Errorf("draft revision %d is not approved for freeze", value.Recipe.Revision)
	}
	// RevisionStore has its own lock and never calls back into the book, so
	// holding the book lock here keeps the approval check and the freeze
	// atomic against concurrent edits.
	revision, err := revisions.Freeze(value.Recipe)
	if err != nil {
		return Revision{}, cloneDraft(value), err
	}
	return revision, cloneDraft(value), nil
}

func cloneDraft(in *Draft) Draft {
	out := *in
	out.Recipe = cloneRecipe(in.Recipe)
	return out
}

func layersEqual(a, b model.Layer) bool {
	return a.Number == b.Number &&
		a.Material == b.Material &&
		a.RateNanometers == b.RateNanometers &&
		a.TargetNanometers == b.TargetNanometers
}
