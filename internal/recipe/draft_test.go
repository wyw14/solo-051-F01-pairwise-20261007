package recipe

import (
	"errors"
	"github.com/wyw14/cry-161/internal/model"
	"testing"
)

func seedDraft(b *DraftBook) *model.Recipe {
	return b.Save(&model.Recipe{
		ID:       model.NewID(),
		Name:     "AR coating",
		Revision: 1,
		Layers:   map[int]model.Layer{1: {Number: 1, Material: "SiO2", TargetNanometers: 100, RateNanometers: 2}},
	}, "alice")
}

func TestUpdateLayerCarriesRevisionAndOperator(t *testing.T) {
	book := NewDraftBook()
	draft := seedDraft(book)
	edit := Edit{Revision: draft.Revision, Operator: "bob", Layer: model.Layer{Number: 1, Material: "SiO2", TargetNanometers: 120, RateNanometers: 2}}
	updated, duplicate, err := book.UpdateLayer(draft.ID, edit)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if duplicate {
		t.Fatal("first submission must not be a duplicate")
	}
	if updated.Revision != draft.Revision+1 {
		t.Fatalf("revision = %d, want %d", updated.Revision, draft.Revision+1)
	}
	if updated.Operator != "bob" {
		t.Fatalf("operator = %q, want bob", updated.Operator)
	}
	if updated.Layers[1].TargetNanometers != 120 {
		t.Fatalf("layer not applied: %+v", updated.Layers[1])
	}
	if updated.Review != model.ReviewPending {
		t.Fatalf("review = %q, want pending", updated.Review)
	}
}

func TestStaleRevisionConflictsAndKeepsLatest(t *testing.T) {
	book := NewDraftBook()
	draft := seedDraft(book)
	first := Edit{Revision: 1, Operator: "bob", Layer: model.Layer{Number: 1, Material: "SiO2", TargetNanometers: 120, RateNanometers: 2}}
	if _, _, err := book.UpdateLayer(draft.ID, first); err != nil {
		t.Fatalf("first update: %v", err)
	}
	stale := Edit{Revision: 1, Operator: "carol", Layer: model.Layer{Number: 1, Material: "TiO2", TargetNanometers: 60, RateNanometers: 3}}
	_, _, err := book.UpdateLayer(draft.ID, stale)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want ConflictError", err)
	}
	if conflict.Latest == nil || conflict.Latest.Revision != 2 {
		t.Fatalf("conflict must carry latest draft at revision 2: %+v", conflict.Latest)
	}
	latest, ok := book.Get(draft.ID)
	if !ok {
		t.Fatal("draft missing after conflict")
	}
	if latest.Revision != 2 || latest.Layers[1].Material != "SiO2" || latest.Layers[1].TargetNanometers != 120 {
		t.Fatalf("latest draft overwritten by stale submission: %+v", latest)
	}
}

func TestRepeatedSubmissionIsIdempotent(t *testing.T) {
	book := NewDraftBook()
	draft := seedDraft(book)
	edit := Edit{Revision: 1, Operator: "bob", Layer: model.Layer{Number: 1, Material: "SiO2", TargetNanometers: 120, RateNanometers: 2}}
	if _, _, err := book.UpdateLayer(draft.ID, edit); err != nil {
		t.Fatalf("first update: %v", err)
	}
	again, duplicate, err := book.UpdateLayer(draft.ID, edit)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !duplicate {
		t.Fatal("retry of the applied edit must be reported as duplicate")
	}
	if again.Revision != 2 {
		t.Fatalf("duplicate bumped revision to %d", again.Revision)
	}
	// Same layer content at a different base revision is a new edit, not a retry.
	other := Edit{Revision: 1, Operator: "carol", Layer: edit.Layer}
	if _, _, err := book.UpdateLayer(draft.ID, other); err == nil {
		t.Fatal("different operator at stale revision must conflict, not dedupe")
	}
}

func TestFreezeRequiresApproval(t *testing.T) {
	book := NewDraftBook()
	draft := seedDraft(book)
	if _, _, err := book.Freeze(draft.ID, draft.Revision); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("freeze before review: err = %v, want ErrNotApproved", err)
	}
	if _, err := book.Review(draft.ID, draft.Revision, "carol", model.ReviewRejected); err != nil {
		t.Fatalf("review: %v", err)
	}
	if _, _, err := book.Freeze(draft.ID, draft.Revision); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("freeze after rejection: err = %v, want ErrNotApproved", err)
	}
	if _, err := book.Review(draft.ID, draft.Revision, "carol", model.ReviewApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	frozen, duplicate, err := book.Freeze(draft.ID, draft.Revision)
	if err != nil {
		t.Fatalf("freeze after approval: %v", err)
	}
	if duplicate || !frozen.Frozen {
		t.Fatalf("first freeze: duplicate=%v frozen=%v", duplicate, frozen.Frozen)
	}
}

func TestFreezeIsIdempotent(t *testing.T) {
	book := NewDraftBook()
	draft := seedDraft(book)
	if _, err := book.Review(draft.ID, draft.Revision, "carol", model.ReviewApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, _, err := book.Freeze(draft.ID, draft.Revision); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	again, duplicate, err := book.Freeze(draft.ID, draft.Revision)
	if err != nil {
		t.Fatalf("repeated freeze: %v", err)
	}
	if !duplicate || !again.Frozen {
		t.Fatalf("repeated freeze: duplicate=%v frozen=%v", duplicate, again.Frozen)
	}
}

func TestFrozenDraftRejectsNewEditsButAllowsRetry(t *testing.T) {
	book := NewDraftBook()
	draft := seedDraft(book)
	edit := Edit{Revision: 1, Operator: "bob", Layer: model.Layer{Number: 1, Material: "SiO2", TargetNanometers: 120, RateNanometers: 2}}
	updated, _, err := book.UpdateLayer(draft.ID, edit)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := book.Review(draft.ID, updated.Revision, "carol", model.ReviewApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, _, err := book.Freeze(draft.ID, updated.Revision); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	// A client retrying the edit that led to the frozen revision still succeeds.
	if _, duplicate, err := book.UpdateLayer(draft.ID, edit); err != nil || !duplicate {
		t.Fatalf("retry after freeze: duplicate=%v err=%v", duplicate, err)
	}
	next := Edit{Revision: updated.Revision, Operator: "bob", Layer: model.Layer{Number: 2, Material: "TiO2", TargetNanometers: 60, RateNanometers: 3}}
	if _, _, err := book.UpdateLayer(draft.ID, next); !errors.Is(err, ErrDraftFrozen) {
		t.Fatalf("edit after freeze: err = %v, want ErrDraftFrozen", err)
	}
}

func TestEditAfterApprovalResetsReview(t *testing.T) {
	book := NewDraftBook()
	draft := seedDraft(book)
	if _, err := book.Review(draft.ID, draft.Revision, "carol", model.ReviewApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	edit := Edit{Revision: draft.Revision, Operator: "bob", Layer: model.Layer{Number: 2, Material: "TiO2", TargetNanometers: 60, RateNanometers: 3}}
	updated, _, err := book.UpdateLayer(draft.ID, edit)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Review != model.ReviewPending {
		t.Fatalf("review after edit = %q, want pending", updated.Review)
	}
	if _, _, err := book.Freeze(draft.ID, updated.Revision); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("freeze after post-approval edit: err = %v, want ErrNotApproved", err)
	}
}

func TestReviewWithStaleRevisionConflicts(t *testing.T) {
	book := NewDraftBook()
	draft := seedDraft(book)
	edit := Edit{Revision: 1, Operator: "bob", Layer: model.Layer{Number: 1, Material: "SiO2", TargetNanometers: 120, RateNanometers: 2}}
	if _, _, err := book.UpdateLayer(draft.ID, edit); err != nil {
		t.Fatalf("update: %v", err)
	}
	_, err := book.Review(draft.ID, 1, "carol", model.ReviewApproved)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want ConflictError", err)
	}
	latest, _ := book.Get(draft.ID)
	if latest.Review != model.ReviewPending {
		t.Fatalf("stale review changed status to %q", latest.Review)
	}
}
