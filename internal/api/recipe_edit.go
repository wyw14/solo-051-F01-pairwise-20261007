package api

import (
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/wyw14/cry-161/internal/model"
	"github.com/wyw14/cry-161/internal/recipe"
	"github.com/wyw14/cry-161/internal/service"
	"net/http"
)

type layerSubmitRequest struct {
	ExpectedRevision int     `json:"expected_revision"`
	Number           int     `json:"number"`
	Material         string  `json:"material"`
	TargetNanometers float64 `json:"target_nm"`
	RateNanometers   float64 `json:"rate_nm_s"`
	Operator         string  `json:"operator"`
}

type reviewRequest struct {
	ExpectedRevision int    `json:"expected_revision"`
	Approved         bool   `json:"approved"`
	Reviewer         string `json:"reviewer"`
}

func recipes(svc *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { writeJSON(w, svc.RecipesList()) }
}

// recipeEdit is retained for backwards compatibility on PUT /api/recipes/{id}.
// Draft editing lives under /api/recipes/{id}/draft*.
func recipeEdit(svc *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recipeID, ok := model.ParseID(chi.URLParam(r, "recipeID"))
		if !ok {
			http.Error(w, "invalid recipe id", http.StatusBadRequest)
			return
		}
		var input layerSubmitRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		input.Operator = resolveActor(r, input.Operator)
		if _, err := svc.GetDraft(recipeID); err != nil {
			if _, err := svc.CreateDraft(recipeID, input.Operator); err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
		}
		draft, err := svc.SubmitDraftLayer(recipe.LayerChange{
			RecipeID:         recipeID,
			ExpectedRevision: input.ExpectedRevision,
			Layer:            model.Layer{Number: input.Number, Material: input.Material, TargetNanometers: input.TargetNanometers, RateNanometers: input.RateNanometers},
			Operator:         input.Operator,
		})
		if err != nil {
			writeDraftError(w, err)
			return
		}
		writeJSON(w, draft)
	}
}

// createDraft opens a draft from the published recipe.
func createDraft(svc *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recipeID, ok := model.ParseID(chi.URLParam(r, "recipeID"))
		if !ok {
			http.Error(w, "invalid recipe id", http.StatusBadRequest)
			return
		}
		operator := resolveActor(r, "")
		draft, err := svc.CreateDraft(recipeID, operator)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, draft)
	}
}

// getDraft returns the draft with its current revision and review status.
func getDraft(svc *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recipeID, ok := model.ParseID(chi.URLParam(r, "recipeID"))
		if !ok {
			http.Error(w, "invalid recipe id", http.StatusBadRequest)
			return
		}
		draft, err := svc.GetDraft(recipeID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, draft)
	}
}

// submitDraftLayer applies a revision-checked layer edit. Stale submits get
// 409 with the untouched latest draft in the body; identical replays are
// idempotent (200, revision unchanged).
func submitDraftLayer(svc *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recipeID, ok := model.ParseID(chi.URLParam(r, "recipeID"))
		if !ok {
			http.Error(w, "invalid recipe id", http.StatusBadRequest)
			return
		}
		var input layerSubmitRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		input.Operator = resolveActor(r, input.Operator)
		draft, err := svc.SubmitDraftLayer(recipe.LayerChange{
			RecipeID:         recipeID,
			ExpectedRevision: input.ExpectedRevision,
			Layer:            model.Layer{Number: input.Number, Material: input.Material, TargetNanometers: input.TargetNanometers, RateNanometers: input.RateNanometers},
			Operator:         input.Operator,
		})
		if err != nil {
			writeDraftError(w, err)
			return
		}
		writeJSON(w, draft)
	}
}

// reviewDraft records a review decision against a specific revision.
func reviewDraft(svc *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recipeID, ok := model.ParseID(chi.URLParam(r, "recipeID"))
		if !ok {
			http.Error(w, "invalid recipe id", http.StatusBadRequest)
			return
		}
		var input reviewRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		input.Reviewer = resolveActor(r, input.Reviewer)
		draft, err := svc.ReviewDraft(recipeID, input.ExpectedRevision, input.Reviewer, input.Approved)
		if err != nil {
			writeDraftError(w, err)
			return
		}
		writeJSON(w, draft)
	}
}

// freezeDraft freezes an approved draft revision; repeat calls on the same
// revision return the same frozen revision.
func freezeDraft(svc *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recipeID, ok := model.ParseID(chi.URLParam(r, "recipeID"))
		if !ok {
			http.Error(w, "invalid recipe id", http.StatusBadRequest)
			return
		}
		revision, _, err := svc.FreezeDraft(recipeID)
		if err != nil {
			writeDraftError(w, err)
			return
		}
		writeJSON(w, revision)
	}
}

// resolveActor prefers an explicit body value, then the X-Operator header,
// falling back to a labelled local operator.
func resolveActor(r *http.Request, value string) string {
	if value != "" {
		return value
	}
	if header := r.Header.Get("X-Operator"); header != "" {
		return header
	}
	return "local-operator"
}

// writeDraftError maps a revision conflict to 409 (carrying the latest draft),
// not-found to 404 and everything else to 400.
func writeDraftError(w http.ResponseWriter, err error) {
	var conflict *service.ErrDraftConflict
	if errors.As(err, &conflict) {
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, map[string]any{"error": err.Error(), "latest": conflict.Latest})
		return
	}
	if err.Error() == "draft not found" || err.Error() == "recipe not found" {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}
