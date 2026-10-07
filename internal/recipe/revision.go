package recipe

import (
	"github.com/wyw14/cry-161/internal/model"
	"sort"
	"sync"
	"time"
)

type Revision struct {
	ID        model.ID      `json:"id"`
	RecipeID  model.ID      `json:"recipe_id"`
	Number    int           `json:"number"`
	CreatedAt time.Time     `json:"created_at"`
	Layers    []model.Layer `json:"layers"`
}

type RevisionStore struct {
	mu     sync.RWMutex
	values map[model.ID][]Revision
}

func NewRevisionStore() *RevisionStore {
	return &RevisionStore{values: map[model.ID][]Revision{}}
}

// Freeze compiles a recipe into an immutable revision. Freezing the same
// recipe revision again is idempotent: the previously stored revision with
// that number is returned unchanged instead of an error.
func (s *RevisionStore) Freeze(recipe *model.Recipe) (Revision, error) {
	s.mu.Lock()
	for _, value := range s.values[recipe.ID] {
		if value.Number == recipe.Revision {
			s.mu.Unlock()
			return value, nil
		}
	}
	s.mu.Unlock()

	plan, err := Compile(recipe)
	if err != nil {
		return Revision{}, err
	}
	revision := Revision{ID: model.NewID(), RecipeID: plan.RecipeID, Number: plan.Revision, CreatedAt: time.Now().UTC(), Layers: append([]model.Layer(nil), plan.Layers...)}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range s.values[recipe.ID] {
		if value.Number == revision.Number {
			// Lost a concurrent race with another freezer; its revision wins
			// and this call stays idempotent.
			return value, nil
		}
	}
	s.values[recipe.ID] = append(s.values[recipe.ID], revision)
	return revision, nil
}

func (s *RevisionStore) List(recipeID model.ID) []Revision {
	s.mu.RLock()
	result := append([]Revision(nil), s.values[recipeID]...)
	s.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Number < result[j].Number })
	return result
}
