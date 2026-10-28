package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

type bodyKey struct{}

// ErrBodyTooLarge is returned when a request body exceeds its limit.
var ErrBodyTooLarge = errors.New("request body too large")

// BufferBody reads the body once (at most limit bytes) and keeps it on the request context,
// so the HMAC check and the handler see exactly the same bytes.
func BufferBody(r *http.Request, limit int64) (*http.Request, error) {
	if r.Body == nil || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return r.WithContext(context.WithValue(r.Context(), bodyKey{}, []byte{})), nil
	}
	if r.ContentLength > limit {
		return r, ErrBodyTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	_ = r.Body.Close()
	if err != nil {
		return r, err
	}
	if int64(len(data)) > limit {
		return r, ErrBodyTooLarge
	}
	r = r.WithContext(context.WithValue(r.Context(), bodyKey{}, data))
	r.Body = io.NopCloser(bytes.NewReader(data))
	return r, nil
}

// Body returns the buffered request body ("" when none).
func Body(r *http.Request) []byte {
	if b, ok := r.Context().Value(bodyKey{}).([]byte); ok {
		return b
	}
	return nil
}

// DecodeJSON decodes the buffered body into v. An empty body is reported as io.EOF.
func DecodeJSON(r *http.Request, v any) error {
	b := Body(r)
	if len(bytes.TrimSpace(b)) == 0 {
		return io.EOF
	}
	return json.Unmarshal(b, v)
}

// WithBody returns a copy of r whose buffered body is b (used to validate items of a bulk body).
func WithBody(r *http.Request, b []byte) *http.Request {
	r2 := r.WithContext(context.WithValue(r.Context(), bodyKey{}, b))
	r2.Body = io.NopCloser(bytes.NewReader(b))
	return r2
}
