package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wyw14/cry-161/internal/model"
	"github.com/wyw14/cry-161/internal/recipe"
	"github.com/wyw14/cry-161/internal/service"
)

type draftResponse struct {
	Recipe       *model.Recipe      `json:"recipe"`
	Operator     string             `json:"operator"`
	Reviewer     string             `json:"reviewer"`
	ReviewStatus recipe.ReviewState `json:"review_status"`
}

func doDraftRequest(t *testing.T, handler http.Handler, method, path string, body any, operator string) (int, []byte) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if operator != "" {
		req.Header.Set("X-Operator", operator)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func decodeDraft(t *testing.T, data []byte) draftResponse {
	t.Helper()
	var draft draftResponse
	if err := json.Unmarshal(data, &draft); err != nil {
		t.Fatalf("decode draft: %v: %s", err, data)
	}
	return draft
}

func TestDraftWorkflowOverHTTP(t *testing.T) {
	svc := service.New()
	handler := NewRouter(svc)
	recipeID := svc.RecipesList()[0].ID
	base := "/api/recipes/" + string(recipeID)

	// Open the draft.
	code, body := doDraftRequest(t, handler, http.MethodPost, base+"/draft", nil, "alice")
	if code != http.StatusCreated {
		t.Fatalf("create draft: status %d body %s", code, body)
	}
	draft := decodeDraft(t, body)
	if draft.Operator != "alice" || draft.ReviewStatus != recipe.ReviewPending {
		t.Fatalf("unexpected seeded draft: %+v", draft)
	}
	startRevision := draft.Recipe.Revision

	layerPayload := func(rate float64) map[string]any {
		return map[string]any{
			"expected_revision": startRevision,
			"number":            1,
			"material":          "SiO2",
			"target_nm":         100,
			"rate_nm_s":         rate,
		}
	}

	// alice applies the first edit; revision advances.
	code, body = doDraftRequest(t, handler, http.MethodPut, base+"/draft/layers", layerPayload(2.5), "alice")
	if code != http.StatusOK {
		t.Fatalf("first submit: status %d body %s", code, body)
	}
	updated := decodeDraft(t, body)
	if updated.Recipe.Revision != startRevision+1 {
		t.Fatalf("expected revision %d, got %d", startRevision+1, updated.Recipe.Revision)
	}

	// A network retry of the very same request (identical payload, even with
	// the pre-edit expected_revision) is idempotent: 200, revision unchanged.
	code, body = doDraftRequest(t, handler, http.MethodPut, base+"/draft/layers", layerPayload(2.5), "alice")
	if code != http.StatusOK {
		t.Fatalf("identical retry: expected 200, got %d body %s", code, body)
	}
	if replayed := decodeDraft(t, body); replayed.Recipe.Revision != startRevision+1 {
		t.Fatalf("identical retry must not bump revision, got %d", replayed.Recipe.Revision)
	}

	// bob submits divergent content based on the stale revision: 409 carrying
	// the latest draft, and the stored draft must be untouched.
	code, body = doDraftRequest(t, handler, http.MethodPut, base+"/draft/layers", layerPayload(9.9), "bob")
	if code != http.StatusConflict {
		t.Fatalf("stale divergent submit: expected 409, got %d body %s", code, body)
	}
	var conflict struct {
		Error  string        `json:"error"`
		Latest draftResponse `json:"latest"`
	}
	if err := json.Unmarshal(body, &conflict); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if conflict.Latest.Recipe.Revision != startRevision+1 || conflict.Latest.Operator != "alice" {
		t.Fatalf("409 must carry latest draft, got %+v", conflict.Latest)
	}
	if got := conflict.Latest.Recipe.Layers[1].RateNanometers; got != 2.5 {
		t.Fatalf("409 must preserve alice's layer (2.5), got %v", got)
	}

	// bob retries against the latest revision with his own change: succeeds.
	currentRevision := startRevision + 1
	payload := map[string]any{
		"expected_revision": currentRevision,
		"number":            2,
		"material":          "TiO2",
		"target_nm":         60,
		"rate_nm_s":         3.5,
	}
	code, body = doDraftRequest(t, handler, http.MethodPut, base+"/draft/layers", payload, "bob")
	if code != http.StatusOK {
		t.Fatalf("retried submit: status %d body %s", code, body)
	}
	updated = decodeDraft(t, body)
	if updated.Recipe.Revision != currentRevision+1 || updated.Operator != "bob" {
		t.Fatalf("expected revision %d by bob, got rev=%d operator=%q", currentRevision+1, updated.Recipe.Revision, updated.Operator)
	}
	latestRevision := currentRevision + 1

	// Identical resubmit at the same revision is idempotent.
	code, body = doDraftRequest(t, handler, http.MethodPut, base+"/draft/layers", payload, "bob")
	if code != http.StatusOK {
		t.Fatalf("idempotent resubmit: status %d body %s", code, body)
	}
	replayed := decodeDraft(t, body)
	if replayed.Recipe.Revision != latestRevision {
		t.Fatalf("idempotent resubmit bumped revision to %d", replayed.Recipe.Revision)
	}

	// Freeze before review: rejected.
	code, body = doDraftRequest(t, handler, http.MethodPost, base+"/draft/freeze", nil, "")
	if code != http.StatusBadRequest {
		t.Fatalf("freeze before approval: expected 400, got %d body %s", code, body)
	}

	// Review against a stale revision: 409.
	staleReview := map[string]any{"expected_revision": currentRevision, "approved": true, "reviewer": "rev-1"}
	code, _ = doDraftRequest(t, handler, http.MethodPost, base+"/draft/review", staleReview, "")
	if code != http.StatusConflict {
		t.Fatalf("stale review: expected 409, got %d", code)
	}

	// Approve the current revision, then freeze.
	review := map[string]any{"expected_revision": latestRevision, "approved": true, "reviewer": "rev-1"}
	code, body = doDraftRequest(t, handler, http.MethodPost, base+"/draft/review", review, "")
	if code != http.StatusOK {
		t.Fatalf("approve: status %d body %s", code, body)
	}
	if got := decodeDraft(t, body); got.ReviewStatus != recipe.ReviewApproved {
		t.Fatalf("expected approved, got %q", got.ReviewStatus)
	}

	code, body = doDraftRequest(t, handler, http.MethodPost, base+"/draft/freeze", nil, "")
	if code != http.StatusOK {
		t.Fatalf("freeze after approval: status %d body %s", code, body)
	}
	var frozen recipe.Revision
	if err := json.Unmarshal(body, &frozen); err != nil {
		t.Fatalf("decode frozen revision: %v", err)
	}
	if frozen.Number != latestRevision {
		t.Fatalf("expected frozen number %d, got %d", latestRevision, frozen.Number)
	}
	frozenID := frozen.ID

	// Duplicate freeze is idempotent: same revision record.
	code, body = doDraftRequest(t, handler, http.MethodPost, base+"/draft/freeze", nil, "")
	if code != http.StatusOK {
		t.Fatalf("duplicate freeze: status %d body %s", code, body)
	}
	if err := json.Unmarshal(body, &frozen); err != nil {
		t.Fatalf("decode second frozen revision: %v", err)
	}
	if frozen.ID != frozenID {
		t.Fatalf("duplicate freeze created a new revision %s (was %s)", frozen.ID, frozenID)
	}
}
