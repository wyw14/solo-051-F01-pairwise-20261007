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

func recipes(svc *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { writeJSON(w, svc.RecipesList()) }
}

type layerEditRequest struct {
	Revision int         `json:"revision"`
	Operator string      `json:"operator"`
	Layer    model.Layer `json:"layer"`
}

func recipeEdit(svc *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := model.ParseID(chi.URLParam(r, "recipeID"))
		if !ok {
			http.Error(w, "invalid recipe id", http.StatusBadRequest)
			return
		}
		var input layerEditRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if input.Operator == "" {
			http.Error(w, "operator is required", http.StatusBadRequest)
			return
		}
		edit := recipe.Edit{Revision: input.Revision, Operator: input.Operator, Layer: input.Layer}
		draft, duplicate, err := svc.UpdateDraft(id, edit)
		if err != nil {
			writeDraftError(w, err)
			return
		}
		writeJSON(w, map[string]any{"draft": draft, "duplicate": duplicate})
	}
}

type reviewRequest struct {
	Revision int    `json:"revision"`
	Operator string `json:"operator"`
	Status   string `json:"status"`
}

func recipeReview(svc *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := model.ParseID(chi.URLParam(r, "recipeID"))
		if !ok {
			http.Error(w, "invalid recipe id", http.StatusBadRequest)
			return
		}
		var input reviewRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if input.Operator == "" {
			http.Error(w, "operator is required", http.StatusBadRequest)
			return
		}
		draft, err := svc.ReviewDraft(id, input.Revision, input.Operator, model.ReviewStatus(input.Status))
		if err != nil {
			writeDraftError(w, err)
			return
		}
		writeJSON(w, map[string]any{"draft": draft})
	}
}

type freezeRequest struct {
	Revision int `json:"revision"`
}

func recipeFreeze(svc *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := model.ParseID(chi.URLParam(r, "recipeID"))
		if !ok {
			http.Error(w, "invalid recipe id", http.StatusBadRequest)
			return
		}
		var input freezeRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		draft, err := svc.FreezeDraft(id, input.Revision)
		if err != nil {
			writeDraftError(w, err)
			return
		}
		writeJSON(w, map[string]any{"draft": draft})
	}
}

func writeDraftError(w http.ResponseWriter, err error) {
	var conflict *recipe.ConflictError
	switch {
	case errors.As(err, &conflict):
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, map[string]any{"error": conflict.Error(), "draft": conflict.Latest})
	case errors.Is(err, recipe.ErrDraftNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, recipe.ErrDraftFrozen), errors.Is(err, recipe.ErrNotApproved):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}
