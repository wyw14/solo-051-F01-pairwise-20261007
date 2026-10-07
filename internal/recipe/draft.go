package recipe

import (
	"errors"
	"fmt"
	"github.com/wyw14/cry-161/internal/model"
	"sync"
)

var (
	ErrDraftNotFound = errors.New("draft not found")
	ErrDraftFrozen   = errors.New("draft is frozen")
	ErrNotApproved   = errors.New("draft review is not approved")
)

// ConflictError reports a submission based on a stale revision. The stored
// draft is left untouched and the latest version is handed back to the
// caller so it can rebase.
type ConflictError struct {
	Submitted int
	Current   int
	Latest    *model.Recipe
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("draft revision conflict: submitted %d, current %d", e.Submitted, e.Current)
}

// Edit carries one layer change against a draft: the revision the client
// based the change on, the operator performing it, and the layer payload.
type Edit struct {
	Revision int
	Operator string
	Layer    model.Layer
}

type draftEntry struct {
	recipe   *model.Recipe
	lastEdit *Edit
}

type DraftBook struct {
	mu      sync.RWMutex
	entries map[model.ID]*draftEntry
}

func NewDraftBook() *DraftBook { return &DraftBook{entries: map[model.ID]*draftEntry{}} }

func (b *DraftBook) Save(recipe *model.Recipe, operator string) *model.Recipe {
	b.mu.Lock()
	defer b.mu.Unlock()
	copy := cloneRecipe(recipe)
	copy.Published = false
	copy.Frozen = false
	copy.Operator = operator
	if copy.Review == "" {
		copy.Review = model.ReviewPending
	}
	b.entries[copy.ID] = &draftEntry{recipe: copy}
	return cloneRecipe(copy)
}

func (b *DraftBook) Get(id model.ID) (*model.Recipe, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	entry, ok := b.entries[id]
	if !ok {
		return nil, false
	}
	return cloneRecipe(entry.recipe), true
}

// UpdateLayer applies an edit with optimistic concurrency control. The
// returned bool reports a duplicate submission: a retry of the edit that
// produced the current revision is a no-op and returns the stored draft.
func (b *DraftBook) UpdateLayer(id model.ID, edit Edit) (*model.Recipe, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.entries[id]
	if !ok {
		return nil, false, ErrDraftNotFound
	}
	if entry.lastEdit != nil && *entry.lastEdit == edit {
		return cloneRecipe(entry.recipe), true, nil
	}
	if entry.recipe.Frozen {
		return nil, false, ErrDraftFrozen
	}
	if edit.Revision != entry.recipe.Revision {
		return nil, false, &ConflictError{Submitted: edit.Revision, Current: entry.recipe.Revision, Latest: cloneRecipe(entry.recipe)}
	}
	entry.recipe.Layers[edit.Layer.Number] = edit.Layer
	entry.recipe.Revision++
	entry.recipe.Operator = edit.Operator
	// Content changed: any earlier review decision no longer applies.
	entry.recipe.Review = model.ReviewPending
	applied := edit
	entry.lastEdit = &applied
	return cloneRecipe(entry.recipe), false, nil
}

// Review records a review decision at the current revision. Repeating the
// same decision is naturally idempotent; a stale revision conflicts.
func (b *DraftBook) Review(id model.ID, revision int, reviewer string, status model.ReviewStatus) (*model.Recipe, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.entries[id]
	if !ok {
		return nil, ErrDraftNotFound
	}
	if entry.recipe.Frozen {
		return nil, ErrDraftFrozen
	}
	if status != model.ReviewApproved && status != model.ReviewRejected {
		return nil, fmt.Errorf("invalid review status %q", status)
	}
	if revision != entry.recipe.Revision {
		return nil, &ConflictError{Submitted: revision, Current: entry.recipe.Revision, Latest: cloneRecipe(entry.recipe)}
	}
	entry.recipe.Review = status
	entry.recipe.Operator = reviewer
	return cloneRecipe(entry.recipe), nil
}

// Freeze locks the draft against further edits. It requires an approved
// review at the current revision. The returned bool reports a duplicate:
// freezing an already frozen draft at the same revision is a no-op.
func (b *DraftBook) Freeze(id model.ID, revision int) (*model.Recipe, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.entries[id]
	if !ok {
		return nil, false, ErrDraftNotFound
	}
	if entry.recipe.Frozen {
		if entry.recipe.Revision == revision {
			return cloneRecipe(entry.recipe), true, nil
		}
		return nil, false, &ConflictError{Submitted: revision, Current: entry.recipe.Revision, Latest: cloneRecipe(entry.recipe)}
	}
	if revision != entry.recipe.Revision {
		return nil, false, &ConflictError{Submitted: revision, Current: entry.recipe.Revision, Latest: cloneRecipe(entry.recipe)}
	}
	if entry.recipe.Review != model.ReviewApproved {
		return nil, false, ErrNotApproved
	}
	entry.recipe.Frozen = true
	return cloneRecipe(entry.recipe), false, nil
}
