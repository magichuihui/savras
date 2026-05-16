package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"savras/internal/auth"

	httpconfig "savras/internal/config"
)

func TestNewProxyHandlerConstructor(t *testing.T) {
	cfg := &httpconfig.Config{
		Server: httpconfig.ServerConfig{GrafanaAddr: "http://localhost:3000"},
		Auth:   httpconfig.AuthConfig{CookieName: "savras_session"},
	}
	h := NewProxyHandler(cfg)
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	if _, ok := h.(*http.ServeMux); !ok {
		t.Fatalf("expected *http.ServeMux, got %T", h)
	}
}

func TestNewProxyHandler_DefaultGrafanaAddr(t *testing.T) {
	cfg := &httpconfig.Config{
		Auth: httpconfig.AuthConfig{CookieName: "savras_session"},
	}
	h := NewProxyHandler(cfg)
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}

func TestNewProxyHandler_InvalidURL(t *testing.T) {
	cfg := &httpconfig.Config{
		Server: httpconfig.ServerConfig{GrafanaAddr: "://invalid"},
		Auth:   httpconfig.AuthConfig{CookieName: "savras_session"},
	}
	h := NewProxyHandler(cfg)
	if h == nil {
		t.Fatal("expected non-nil handler even with invalid URL")
	}
}

func TestRBACMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	cfg := &httpconfig.Config{}
	handler := RBACMiddleware(next, cfg)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestHeaderInjectionMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-WEBAUTH-USER") != "testuser" {
			t.Errorf("expected X-WEBAUTH-USER header to be injected")
		}
		if r.Header.Get("X-WEBAUTH-EMAIL") != "test@example.com" {
			t.Errorf("expected X-WEBAUTH-EMAIL header to be injected")
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := HeaderInjectionMiddleware(next)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := context.WithValue(req.Context(), claimsContextKey, &auth.JWTClaims{
		Username: "testuser",
		Email:    "test@example.com",
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
}

func TestHeaderInjectionMiddleware_NoClaims(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-WEBAUTH-USER") != "" {
			t.Errorf("expected no X-WEBAUTH-USER header")
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := HeaderInjectionMiddleware(next)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
}

func TestHealthHandler(t *testing.T) {
	cfg := &httpconfig.Config{
		Server: httpconfig.ServerConfig{GrafanaAddr: "http://localhost:3000"},
	}
	handler := healthHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/-/savras/health", nil)
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusOK && rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 200 or 503, got %d", rr.Code)
	}
}

func TestHealthHandler_WrongMethod(t *testing.T) {
	cfg := &httpconfig.Config{}
	handler := healthHandler(cfg)

	req := httptest.NewRequest(http.MethodPost, "/-/savras/health", nil)
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

func TestSyncTriggerHandler(t *testing.T) {
	cfg := &httpconfig.Config{}
	handler := syncTriggerHandler(cfg)

	SetSyncTriggerFn(func(ctx context.Context) error {
		return nil
	})
	defer SetSyncTriggerFn(nil)

	req := httptest.NewRequest(http.MethodPost, "/-/savras/sync/trigger", nil)
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rr.Code)
	}
}

func TestSyncTriggerHandler_NotImplemented(t *testing.T) {
	cfg := &httpconfig.Config{}
	handler := syncTriggerHandler(cfg)

	SetSyncTriggerFn(nil)

	req := httptest.NewRequest(http.MethodPost, "/-/savras/sync/trigger", nil)
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rr.Code)
	}
}

func TestSyncTriggerHandler_WrongMethod(t *testing.T) {
	cfg := &httpconfig.Config{}
	handler := syncTriggerHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/-/savras/sync/trigger", nil)
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

func TestCheckLDAPConnectivity_NoAddr(t *testing.T) {
	// Clear env
	t.Setenv("LDAP_ADDR", "")
	result := checkLDAPConnectivity()
	if !result {
		t.Fatal("expected true when LDAP_ADDR is not set")
	}
}

func TestCheckGrafanaConnectivity(t *testing.T) {
	// Start a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &httpconfig.Config{
		Server: httpconfig.ServerConfig{GrafanaAddr: server.URL},
	}
	result := checkGrafanaConnectivity(cfg)
	if !result {
		t.Fatal("expected true for reachable Grafana")
	}
}

func TestCheckGrafanaConnectivity_DefaultAddr(t *testing.T) {
	cfg := &httpconfig.Config{}
	result := checkGrafanaConnectivity(cfg)
	// Will likely fail to connect, but shouldn't panic
	_ = result
}

func TestNewProxyHandler_WithGrafanaBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("grafana"))
	}))
	defer backend.Close()

	cfg := &httpconfig.Config{
		Server: httpconfig.ServerConfig{GrafanaAddr: backend.URL},
		Auth:   httpconfig.AuthConfig{CookieName: "savras_session"},
	}
	handler := NewProxyHandler(cfg)

	// Create a test request with a valid JWT cookie
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	// This will redirect due to no valid JWT, but shouldn't panic
	handler.ServeHTTP(rr, req)
}

func TestLogoutHandler(t *testing.T) {
	cfg := &httpconfig.Config{
		Auth: httpconfig.AuthConfig{CookieName: "savras_session"},
	}
	handler := logoutHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/-/savras/logout", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// Check cookie was cleared
	cookies := rr.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == "savras_session" && c.MaxAge < 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected savras_session cookie to be cleared")
	}

	// Check redirect to login
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected status %d, got %d", http.StatusSeeOther, rr.Code)
	}
	if rr.Header().Get("Location") != "/login" {
		t.Fatalf("expected redirect to /login, got %s", rr.Header().Get("Location"))
	}
}

func TestGrafanaLogoutHandler(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := r.Cookie("savras_session")
		if err == nil {
			t.Error("expected savras_session cookie to be removed from request")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := &httpconfig.Config{
		Server: httpconfig.ServerConfig{GrafanaAddr: backend.URL},
		Auth:   httpconfig.AuthConfig{CookieName: "savras_session"},
	}
	handler := grafanaLogoutHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: "savras_session", Value: "test-token"})
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	cookies := rr.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == "savras_session" && c.MaxAge < 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected savras_session cookie to be cleared in response")
	}
}

func TestLoginHandler_Get(t *testing.T) {
	cfg := &httpconfig.Config{
		Auth: httpconfig.AuthConfig{CookieName: "savras_session"},
	}
	handler := loginHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Welcome to Grafana") {
		t.Fatal("expected login page HTML")
	}
}

func TestLoginHandler_PostEmptyCredentials(t *testing.T) {
	cfg := &httpconfig.Config{
		Auth: httpconfig.AuthConfig{CookieName: "savras_session"},
	}
	handler := loginHandler(cfg)

	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "required") {
		t.Fatal("expected error message for empty credentials")
	}
}

func TestLoginHandler_InvalidMethod(t *testing.T) {
	cfg := &httpconfig.Config{
		Auth: httpconfig.AuthConfig{CookieName: "savras_session"},
	}
	handler := loginHandler(cfg)

	req := httptest.NewRequest(http.MethodPut, "/login", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

func TestAuthMiddleware_NoCookie(t *testing.T) {
	cfg := &httpconfig.Config{
		Auth: httpconfig.AuthConfig{CookieName: "savras_session"},
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), cfg)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if loc != "/login" {
		t.Fatalf("expected redirect to /login, got %s", loc)
	}
}

func TestHealthHandler_BlocksUntilSyncReady(t *testing.T) {
	// Reset global state
	SetSyncReadyFn(nil)

	// Without sync ready function, health should pass through normally
	cfg := &httpconfig.Config{Server: httpconfig.ServerConfig{GrafanaAddr: "http://localhost:3000"}}
	handler := healthHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/-/savras/health", nil)

	// When no sync ready function is set, health returns 200 or 503
	rr := httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusOK && rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 200 or 503, got %d", rr.Code)
	}

	// When sync ready function returns false, must be 503
	SetSyncReadyFn(func() bool { return false })
	rr2 := httptest.NewRecorder()
	handler(rr2, req)
	if rr2.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when sync not ready, got %d", rr2.Code)
	}

	// When sync ready function returns true, passes through
	SetSyncReadyFn(func() bool { return true })
	rr3 := httptest.NewRecorder()
	handler(rr3, req)
	if rr3.Code != http.StatusOK && rr3.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 200 or 503, got %d", rr3.Code)
	}

	// Clean up
	SetSyncReadyFn(nil)
}

func TestAuthMiddleware_BlocksWhenSyncStale(t *testing.T) {
	SetSyncReadyFn(nil)

	// Succeeding auth has a valid JWT in the cookie
	auth.Init(&httpconfig.Config{
		Auth: httpconfig.AuthConfig{JwtSecret: "test-secret", JwtExpiryDuration: time.Hour},
	})
	token, err := auth.GenerateJWT(&auth.AuthResult{Username: "testuser"})
	if err != nil {
		t.Fatalf("failed to generate JWT: %v", err)
	}

	cfg := &httpconfig.Config{
		Auth: httpconfig.AuthConfig{CookieName: "test_cookie"},
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := AuthMiddleware(next, cfg)

	req := httptest.NewRequest(http.MethodGet, "/d/test", nil)
	req.AddCookie(&http.Cookie{Name: "test_cookie", Value: token})

	// When sync ready returns false, AuthMiddleware should block with 503
	SetSyncReadyFn(func() bool { return false })
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when sync stale, got %d", rr.Code)
	}

	// When sync ready returns true, request passes through
	SetSyncReadyFn(func() bool { return true })
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 when sync ready, got %d", rr2.Code)
	}

	// Clean up
	SetSyncReadyFn(nil)
}
