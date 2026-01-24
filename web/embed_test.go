package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler_ServesIndexHTML(t *testing.T) {
	handler := Handler()

	// Test root path serves index.html
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET / status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") && !strings.Contains(body, "<html") {
		t.Errorf("GET / body doesn't look like HTML: %s", body[:min(100, len(body))])
	}
}

func TestHandler_ServesStaticAssets(t *testing.T) {
	handler := Handler()

	// Test that assets directory is accessible
	// Note: /index.html redirects to / (standard http.FileServer behavior)
	// So we test /assets/ path instead
	req := httptest.NewRequest("GET", "/assets/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// 200 if directory listing allowed, or serves index.html via SPA fallback
	if rec.Code != http.StatusOK {
		t.Errorf("GET /assets/ status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandler_SPAFallback(t *testing.T) {
	handler := Handler()

	// Test SPA fallback - unknown paths should serve index.html
	req := httptest.NewRequest("GET", "/some/unknown/route", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /some/unknown/route status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") && !strings.Contains(body, "<html") {
		t.Errorf("SPA fallback didn't serve index.html")
	}
}
