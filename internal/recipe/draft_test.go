package recipe

import (
	"errors"
	"testing"

	"github.com/wyw14/cry-161/internal/model"
)

func seedDraft(t *testing.T) (*DraftBook, model.ID) {
	t.Helper()
	book := NewDraftBook()
	recipe := &model.Recipe{
		ID:       model.NewID(),
		Name:     "AR coating",
		Revision: 3,
		Layers: map[int]model.Layer{
			1: {Number: 1, Material: "SiO2", TargetNanometers: 100, RateNanometers: 2},
		},
	}
	saved := book.SaveForOperator(recipe, "alice")
	return book, saved.ID
}

func layer(n int, rate float64) model.Layer {
	return model.Layer{Number: n, Material: "SiO2", TargetNanometers: 100, RateNanometers: rate}
}

func TestSubmitLayerStaleRevisionConflictsAndPreservesLatest(t *testing.T) {
	book, id := seedDraft(t)

	// Operator A advances revision 3 -> 4.
	updated, err := book.SubmitLayer(LayerChange{RecipeID: id, ExpectedRevision: 3, Layer: layer(1, 2.5), Operator: "alice"})
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if updated.Recipe.Revision != 4 {
		t.Fatalf("expected revision 4, got %d", updated.Recipe.Revision)
	}

	// Operator B submits based on the stale revision 3: must conflict and
	// leave the latest draft (rate 2.5 from alice) untouched.
	_, err = book.SubmitLayer(LayerChange{RecipeID: id, ExpectedRevision: 3, Layer: layer(1, 9.9), Operator: "bob"})
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}
	if conflict.Latest.Recipe.Revision != 4 {
		t.Fatalf("conflict must carry latest revision 4, got %d", conflict.Latest.Recipe.Revision)
	}
	if got := conflict.Latest.Recipe.Layers[1].RateNanometers; got != 2.5 {
		t.Fatalf("latest draft layer must be preserved (2.5), got %v", got)
	}

	stored, ok := book.GetDraft(id)
	if !ok {
		t.Fatal("draft missing")
	}
	if stored.Recipe.Revision != 4 || stored.Recipe.Layers[1].RateNanometers != 2.5 {
		t.Fatalf("stored draft was mutated by the conflicting submit: rev=%d rate=%v", stored.Recipe.Revision, stored.Recipe.Layers[1].RateNanometers)
	}
	if stored.Operator != "alice" {
		t.Fatalf("operator must remain alice, got %q", stored.Operator)
	}
}

func TestSubmitLayerIdempotentReplay(t *testing.T) {
	book, id := seedDraft(t)

	first, err := book.SubmitLayer(LayerChange{RecipeID: id, ExpectedRevision: 3, Layer: layer(1, 2.5), Operator: "alice"})
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	// Same payload, same base revision: replay must not bump revision nor
	// change the operator.
	replay, err := book.SubmitLayer(LayerChange{RecipeID: id, ExpectedRevision: 3, Layer: layer(1, 2.5), Operator: "bob"})
	if err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	if replay.Recipe.Revision != first.Recipe.Revision {
		t.Fatalf("replay changed revision: %d -> %d", first.Recipe.Revision, replay.Recipe.Revision)
	}
	if replay.Operator != "alice" {
		t.Fatalf("replay must not overwrite operator, got %q", replay.Operator)
	}
	if replay.ReviewStatus != ReviewPending {
		t.Fatalf("unexpected review status %q", replay.ReviewStatus)
	}
}

func TestSubmitRequiresOperator(t *testing.T) {
	book, id := seedDraft(t)
	if _, err := book.SubmitLayer(LayerChange{RecipeID: id, ExpectedRevision: 3, Layer: layer(1, 2.5)}); err == nil {
		t.Fatal("expected error when operator is missing")
	}
}

func TestEditAfterApprovalResetsReview(t *testing.T) {
	book, id := seedDraft(t)
	if _, err := book.SubmitLayer(LayerChange{RecipeID: id, ExpectedRevision: 3, Layer: layer(1, 2.5), Operator: "alice"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	approved, err := book.Review(id, 4, "reviewer-1", true)
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if approved.ReviewStatus != ReviewApproved || approved.Reviewer != "reviewer-1" {
		t.Fatalf("unexpected approved draft: %+v", approved)
	}

	// A further edit against revision 4 invalidates the sign-off.
	edited, err := book.SubmitLayer(LayerChange{RecipeID: id, ExpectedRevision: 4, Layer: layer(1, 2.6), Operator: "alice"})
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}
	if edited.ReviewStatus != ReviewPending || edited.Reviewer != "" {
		t.Fatalf("edit must reset review, got status=%q reviewer=%q", edited.ReviewStatus, edited.Reviewer)
	}
}

func TestReviewStaleRevisionConflicts(t *testing.T) {
	book, id := seedDraft(t)
	if _, err := book.SubmitLayer(LayerChange{RecipeID: id, ExpectedRevision: 3, Layer: layer(1, 2.5), Operator: "alice"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := book.Review(id, 3, "reviewer-1", true); err == nil {
		t.Fatal("expected conflict when approving stale revision 3 (draft at 4)")
	} else if !IsConflict(err) {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

func TestFreezeRequiresApproval(t *testing.T) {
	book, id := seedDraft(t)
	revisions := NewRevisionStore()

	if _, _, err := book.Freeze(id, revisions); err == nil {
		t.Fatal("freeze must be rejected before review approval")
	}
	if len(revisions.List(id)) != 0 {
		t.Fatal("no revision may be frozen before approval")
	}

	if _, err := book.Review(id, 3, "reviewer-1", true); err != nil {
		t.Fatalf("review: %v", err)
	}
	frozen, draft, err := book.Freeze(id, revisions)
	if err != nil {
		t.Fatalf("freeze after approval: %v", err)
	}
	if frozen.Number != 3 {
		t.Fatalf("expected frozen revision 3, got %d", frozen.Number)
	}
	if !draft.FreezeReady() {
		t.Fatal("approved draft should report freeze-ready")
	}
}

func TestFreezeIdempotent(t *testing.T) {
	book, id := seedDraft(t)
	revisions := NewRevisionStore()
	if _, err := book.Review(id, 3, "reviewer-1", true); err != nil {
		t.Fatalf("review: %v", err)
	}
	first, _, err := book.Freeze(id, revisions)
	if err != nil {
		t.Fatalf("first freeze: %v", err)
	}

	// Repeated freeze of the same approved revision returns the same record.
	again, _, err := book.Freeze(id, revisions)
	if err != nil {
		t.Fatalf("duplicate freeze must be idempotent, got %v", err)
	}
	if again.ID != first.ID || again.Number != first.Number {
		t.Fatalf("duplicate freeze returned a different revision: %+v vs %+v", first, again)
	}
	if listed := revisions.List(id); len(listed) != 1 {
		t.Fatalf("expected exactly one frozen revision, got %d", len(listed))
	}
}

func TestRevisionStoreFreezeIdempotent(t *testing.T) {
	revisions := NewRevisionStore()
	recipe := &model.Recipe{ID: model.NewID(), Name: "x", Revision: 7, Layers: map[int]model.Layer{1: layer(1, 1)}}
	first, err := revisions.Freeze(recipe)
	if err != nil {
		t.Fatalf("first freeze: %v", err)
	}
	again, err := revisions.Freeze(recipe)
	if err != nil {
		t.Fatalf("duplicate freeze must not error, got %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("expected identical frozen revision, got %s vs %s", first.ID, again.ID)
	}
}
