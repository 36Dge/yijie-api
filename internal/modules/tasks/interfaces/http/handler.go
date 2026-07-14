package taskhttp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/36Dge/yijie-api/internal/interfaces/publicapi"
	"github.com/36Dge/yijie-api/internal/modules/tasks/application"
	"github.com/36Dge/yijie-api/internal/modules/tasks/domain"
	"github.com/google/uuid"
)

const maxRequestBodyBytes = 1 << 20

type Handler struct {
	service *application.Service
}

func New(service *application.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	w.Header().Set("X-Request-ID", requestID)

	var body publicapi.CreateTaskRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must match the create task contract")
		return
	}
	input, err := json.Marshal(body.Input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "input must be a JSON object")
		return
	}

	task, err := h.service.Create(r.Context(), domain.CreateTask{
		TenantID:  body.TenantId.String(),
		TaskType:  body.TaskType,
		Title:     body.Title,
		Input:     input,
		RequestID: requestID,
		TraceID:   strings.TrimSpace(r.Header.Get("X-Trace-ID")),
	})
	if err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "persistence_error", "task could not be persisted")
		return
	}
	response, err := toResponse(task)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "response_error", "task could not be represented")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	task, err := h.service.Get(r.Context(), r.PathValue("task_id"))
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalid):
			writeError(w, http.StatusBadRequest, "invalid_task_id", "task_id must be a UUID")
		case errors.Is(err, domain.ErrNotFound):
			writeError(w, http.StatusNotFound, "task_not_found", "task was not found")
		default:
			writeError(w, http.StatusInternalServerError, "persistence_error", "task could not be read")
		}
		return
	}
	response, err := toResponse(task)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "response_error", "task could not be represented")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func toResponse(task domain.Task) (publicapi.Task, error) {
	id, err := uuid.Parse(task.ID)
	if err != nil {
		return publicapi.Task{}, err
	}
	tenantID, err := uuid.Parse(task.TenantID)
	if err != nil {
		return publicapi.Task{}, err
	}
	var input map[string]any
	if err := json.Unmarshal(task.Input, &input); err != nil {
		return publicapi.Task{}, err
	}
	var result *map[string]any
	if len(task.Result) > 0 {
		value := make(map[string]any)
		if err := json.Unmarshal(task.Result, &value); err != nil {
			return publicapi.Task{}, err
		}
		result = &value
	}
	return publicapi.Task{
		Id:           id,
		TenantId:     tenantID,
		TaskType:     task.TaskType,
		Title:        task.Title,
		Status:       publicapi.TaskStatus(task.Status),
		Input:        input,
		Result:       result,
		ErrorMessage: task.ErrorMessage,
		CreatedAt:    task.CreatedAt,
		UpdatedAt:    task.UpdatedAt,
	}, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, publicapi.ErrorResponse{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
