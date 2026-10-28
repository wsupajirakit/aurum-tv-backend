// Package httpx holds JSON response helpers and the shared error shape.
package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Error is an expected failure sent to the client as {"error": message, "code": code}.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// JSON writes v with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// ErrorBody is the error shape; extra fields (e.g. serverTime) are merged in.
func ErrorBody(code, message string, extra map[string]any) map[string]any {
	body := map[string]any{"error": message, "code": code}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

// Fail writes an error response.
func Fail(w http.ResponseWriter, status int, code, message string) {
	JSON(w, status, ErrorBody(code, message, nil))
}

// WriteError writes err: an *Error keeps its status/code, anything else is a 500.
func WriteError(w http.ResponseWriter, err error) {
	var he *Error
	if errors.As(err, &he) {
		Fail(w, he.Status, he.Code, he.Message)
		return
	}
	Fail(w, http.StatusInternalServerError, "internal_error", "internal server error")
}
