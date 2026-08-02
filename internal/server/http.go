package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const maxClaimBytes = 16 << 10

type readiness interface {
	QuickCheck(context.Context) error
}

type attemptLimiter struct {
	mu     sync.Mutex
	clock  func() time.Time
	start  time.Time
	failed int
}

func newAttemptLimiter(clock func() time.Time) *attemptLimiter {
	return &attemptLimiter{clock: clock}
}

func (l *attemptLimiter) allowed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock().UTC()
	if l.start.IsZero() || now.Sub(l.start) >= time.Minute {
		l.start, l.failed = now, 0
	}
	return l.failed < 5
}

func (l *attemptLimiter) failure() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock().UTC()
	if l.start.IsZero() || now.Sub(l.start) >= time.Minute {
		l.start, l.failed = now, 0
	}
	l.failed++
}

func NewHandler(service *BootstrapService, ready readiness, hubs ...*Hub) (http.Handler, error) {
	if service == nil || ready == nil || len(hubs) > 1 || (len(hubs) == 1 && hubs[0] == nil) {
		return nil, ErrInvalid
	}
	limiter := newAttemptLimiter(service.clock)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, request *http.Request) {
		if err := ready.QuickCheck(request.Context()); err != nil {
			writeError(writer, http.StatusServiceUnavailable, "not_ready")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /v1/bootstrap/status", func(writer http.ResponseWriter, request *http.Request) {
		status, err := service.Status(request.Context())
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "internal")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]string{"status": string(status.State)})
	})
	mux.HandleFunc("POST /v1/bootstrap/claim", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Origin") != "" {
			writeError(writer, http.StatusForbidden, "forbidden")
			return
		}
		if !limiter.allowed() {
			writer.Header().Set("Retry-After", "60")
			writeError(writer, http.StatusTooManyRequests, "rate_limited")
			return
		}
		if mediaType := request.Header.Get("Content-Type"); mediaType != "application/json" {
			limiter.failure()
			writeError(writer, http.StatusUnsupportedMediaType, "invalid_request")
			return
		}
		secret, ok := bearerToken(request.Header.Get("Authorization"))
		if !ok {
			limiter.failure()
			writeError(writer, http.StatusUnauthorized, "unauthorized")
			return
		}
		var claim ClaimRequest
		request.Body = http.MaxBytesReader(writer, request.Body, maxClaimBytes)
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&claim); err != nil {
			limiter.failure()
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(writer, http.StatusRequestEntityTooLarge, "payload_too_large")
			} else {
				writeError(writer, http.StatusBadRequest, "invalid_request")
			}
			return
		}
		if err := ensureJSONEnd(decoder); err != nil {
			limiter.failure()
			writeError(writer, http.StatusBadRequest, "invalid_request")
			return
		}
		response, replayed, err := service.Claim(request.Context(), secret, claim)
		if err != nil {
			limiter.failure()
			switch {
			case errors.Is(err, ErrUnauthorized):
				writeError(writer, http.StatusUnauthorized, "unauthorized")
			case errors.Is(err, ErrInvalid):
				writeError(writer, http.StatusBadRequest, "invalid_request")
			case errors.Is(err, ErrConflict):
				writeError(writer, http.StatusConflict, "bootstrap_completed")
			default:
				writeError(writer, http.StatusInternalServerError, "internal")
			}
			return
		}
		status := http.StatusCreated
		if replayed {
			status = http.StatusOK
		}
		writeJSON(writer, status, response)
	})
	if len(hubs) == 1 {
		mux.HandleFunc("GET /node/connect", hubs[0].NodeHandler)
		mux.HandleFunc("GET /web/connect", hubs[0].ControlHandler)
	}
	return noStore(methodBoundary(mux)), nil
}

func methodBoundary(next http.Handler) http.Handler {
	methods := map[string]string{
		"/healthz":             http.MethodGet,
		"/readyz":              http.MethodGet,
		"/v1/bootstrap/status": http.MethodGet,
		"/v1/bootstrap/claim":  http.MethodPost,
		"/node/connect":        http.MethodGet,
		"/web/connect":         http.MethodGet,
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if method, known := methods[request.URL.Path]; known && request.Method != method {
			writer.Header().Set("Allow", method)
			writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(writer, request)
	})
}

func bearerToken(header string) (string, bool) {
	if !strings.HasPrefix(header, "Bearer ") || strings.Count(header, " ") != 1 {
		return "", false
	}
	value := strings.TrimPrefix(header, "Bearer ")
	return value, value != ""
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return ErrInvalid
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, code string) {
	writeJSON(writer, status, map[string]string{"code": code, "message": "request could not be completed"})
}
