package internal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
)

type AppHandler func(w http.ResponseWriter, r *http.Request) error

func (fn AppHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	writeError := func(w http.ResponseWriter, status int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
			slog.Info("encode error response failed", "error", err)
		}
	}
	if err := fn(w, r); err != nil {
		var he *HttpError
		if errors.As(err, &he) {
			log.Printf("HTTP Error: %v", err)
			writeError(w, he.status, he.msg)
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Resource not found")
			return
		}
		log.Printf("HTTP Error: %v", err)
		writeError(w, http.StatusInternalServerError, "An unexpected error occurred")
	}
}

type HttpError struct {
	status int
	msg    string
	err    error
}

func (e *HttpError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s: %v", e.msg, e.err)
	}
	return e.msg
}

func (e *HttpError) Unwrap() error {
	return e.err
}

func BadRequest(msg string, err error) error {
	return &HttpError{status: http.StatusBadRequest, msg: msg, err: err}
}

func Conflict(msg string, err error) error {
	return &HttpError{status: http.StatusConflict, msg: msg, err: err}
}
