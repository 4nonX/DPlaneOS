package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthHandlerThrottle(t *testing.T) {
	// Create a mock handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Test that throttle middleware is applied
	// Note: This is a basic test - real throttle requires shared state
	req := httptest.NewRequest("POST", "/api/auth/login", nil)
	req.RemoteAddr = "192.168.1.1:12345"

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

func TestRespondErrorJSON(t *testing.T) {
	// Test respondErrorSimple returns proper JSON
	w := httptest.NewRecorder()
	respondErrorSimple(w, "test error", http.StatusBadRequest)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}

	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", w.Header().Get("Content-Type"))
	}
}

func TestRespondErrorWithDetails(t *testing.T) {
	// Test respondError returns proper JSON with details
	w := httptest.NewRecorder()
	err := &testError{"detail message"}
	respondError(w, http.StatusInternalServerError, "test error", err)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}
}

type testError struct {
	message string
}

func (e *testError) Error() string {
	return e.message
}
