package proxy

import (
	"context"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"log/slog"

	"savras/internal/auth"
	"savras/internal/config"
)

// context key for carrying JWT claims across middleware
type contextKey string

const claimsContextKey contextKey = "savras_claims"

// SyncTrigger injection point. Tests or other packages may set this
// to wire in the actual sync implementation from internal/sync.
var syncTriggerFn func(ctx context.Context) error

// SetSyncTriggerFn allows tests to inject a real sync function.
func SetSyncTriggerFn(fn func(ctx context.Context) error) {
	syncTriggerFn = fn
}

// syncReadyFn injection point. The health endpoint checks this before
// reporting healthy, to avoid serving traffic before initial sync completes.
var syncReadyFn func() bool

// SetSyncReadyFn sets a function that returns true once initial sync is done.
func SetSyncReadyFn(fn func() bool) {
	syncReadyFn = fn
}

// grafanaMonitor is the lifecycle monitor for the Grafana backend.
// When set, the reverse proxy error handler, auth middleware, and health
// endpoint all consult it to decide whether traffic should be blocked.
var grafanaMonitor *GrafanaMonitor

// SetGrafanaMonitor wires a GrafanaMonitor into the proxy layer.
func SetGrafanaMonitor(m *GrafanaMonitor) {
	grafanaMonitor = m
}

// NewProxyHandler creates the HTTP handler that proxies requests to Grafana
// while enforcing Savras authentication and header injection.
func NewProxyHandler(cfg *config.Config) http.Handler {
	// Grafana target (default to 127.0.0.1:3000)
	grafanaAddr := strings.TrimSpace(cfg.Server.GrafanaAddr)
	if grafanaAddr == "" {
		grafanaAddr = "http://127.0.0.1:3000"
	}

	target, err := url.Parse(grafanaAddr)
	if err != nil {
		// Fallback to default Grafana address if parsing fails
		target, _ = url.Parse("http://127.0.0.1:3000")
		slog.Error("invalid grafana address; falling back to default", "addr", grafanaAddr, "error", err)
	}

	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("proxy: failed to proxy request", "target", target, "path", r.URL.Path, "error", err)
		// Notify the lifecycle monitor so it can enter recovery mode.
		if grafanaMonitor != nil {
			grafanaMonitor.OnProxyError()
		}
		http.Error(w, "proxy error: unable to reach Grafana at "+target.String(), http.StatusBadGateway)
	}

	// BlockWhenDown blocks all traffic when Grafana is unreachable.
	// It is the outermost wrapper so it runs before auth/login handling.
	var handler http.Handler = rp
	handler = RBACMiddleware(handler, cfg)
	handler = HeaderInjectionMiddleware(handler)
	handler = AuthMiddleware(handler, cfg)
	handler = BlockWhenDownMiddleware(handler)

	mux := http.NewServeMux()
	mux.HandleFunc("/-/savras/health", healthHandler(cfg))
	mux.HandleFunc("/-/savras/sync/trigger", syncTriggerHandler(cfg))
	mux.HandleFunc("/login", loginHandler(cfg))
	mux.HandleFunc("/-/savras/logout", logoutHandler(cfg))
	// Intercept Grafana logout paths to clear Savras cookie
	mux.HandleFunc("/logout", grafanaLogoutHandler(cfg))
	mux.HandleFunc("/api/auth/logout", grafanaLogoutHandler(cfg))
	mux.Handle("/", handler)

	return mux
}

// RBACMiddleware is a placeholder RBAC middleware. It currently passes the request through.
func RBACMiddleware(next http.Handler, cfg *config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Placeholder: implement real RBAC checks here in the future
		next.ServeHTTP(w, r)
	})
}

// BlockWhenDownMiddleware blocks all traffic with 503 when the Grafana
// lifecycle monitor reports Grafana is unreachable.
func BlockWhenDownMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if grafanaMonitor != nil && grafanaMonitor.ShouldBlock() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "unavailable",
				"reason": "Grafana backend is unreachable, try again shortly",
			})
			slog.Warn("proxy: blocked request — Grafana backend is down",
				"path", r.URL.Path)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AuthMiddleware validates a JWT cookie and rejects/redirects unauthenticated users.
func AuthMiddleware(next http.Handler, cfg *config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for login/public endpoints if needed in the future
		cookie, err := r.Cookie(cfg.Auth.CookieName)
		if err != nil {
			// Redirect to login if cookie is missing or invalid
			http.Redirect(w, r, "/login", http.StatusFound)
			slog.Info("auth: redirect to login due to missing cookie", "path", r.URL.Path)
			return
		}

		claims, err := auth.ValidateJWT(cookie.Value)
		if err != nil || claims == nil {
			http.SetCookie(w, &http.Cookie{
				Name:     cfg.Auth.CookieName,
				Value:    "",
				Path:     "/",
				MaxAge:   -1,
				HttpOnly: true,
				Secure:   true,
				SameSite: http.SameSiteLaxMode,
			})
			http.Redirect(w, r, "/login", http.StatusFound)
			slog.Info("auth: redirect to login due to invalid token", "path", r.URL.Path)
			return
		}

		// Carry claims to downstream middlewares/handlers
		ctx := context.WithValue(r.Context(), claimsContextKey, claims)
		slog.Info("auth: user authenticated", "path", r.URL.Path, "user", claims.Username)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// HeaderInjectionMiddleware injects authentication headers required by downstream services.
func HeaderInjectionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if claims, ok := r.Context().Value(claimsContextKey).(*auth.JWTClaims); ok {
			if claims.Username != "" {
				r.Header.Set("X-WEBAUTH-USER", claims.Username)
			}
			if claims.Email != "" {
				r.Header.Set("X-WEBAUTH-EMAIL", claims.Email)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// healthHandler exposes a health endpoint. Returns 503 Service Unavailable
// until the initial sync cycle completes, to avoid serving traffic before
// team permissions are applied.
func healthHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		// If sync is configured but initial sync hasn't completed, not ready.
		if syncReadyFn != nil && !syncReadyFn() {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "initializing",
				"reason": "initial sync not yet complete",
			})
			return
		}

		// If the Grafana lifecycle monitor reports backend is down, not ready.
		if grafanaMonitor != nil && grafanaMonitor.ShouldBlock() {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "unavailable",
				"reason": "Grafana backend is unreachable",
			})
			return
		}

		grafState := "reachable"
		if grafanaMonitor != nil && grafanaMonitor.State() == StateDown {
			grafState = "unreachable"
		}
		ldapOK := checkLDAPConnectivity()
		grafOK := checkGrafanaConnectivity(cfg)

		status := map[string]any{
			"status":        "ok",
			"grafana_state": grafState,
			"ldap":          ldapOK,
			"grafana":       grafOK,
		}

		if ldapOK && grafOK {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(status)
	}
}

// syncTriggerHandler triggers a downstream sync via internal/sync (injectable).
func syncTriggerHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if syncTriggerFn == nil {
			http.Error(w, "sync trigger not configured", http.StatusNotImplemented)
			slog.Info("sync trigger requested but not configured")
			return
		}
		if err := syncTriggerFn(r.Context()); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			slog.Info("sync trigger failed", "error", err.Error())
			return
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"status": "triggered"})
		slog.Info("sync trigger dispatched")
	}
}

// checkLDAPConnectivity attempts to connect to LDAP if LDAP_ADDR is provided via env.
func checkLDAPConnectivity() bool {
	ldapAddr := strings.TrimSpace(os.Getenv("LDAP_ADDR"))
	if ldapAddr == "" {
		// If not configured, treat as healthy for this proxy layer.
		return true
	}
	conn, err := net.DialTimeout("tcp", ldapAddr, 2*time.Second)
	if err != nil {
		slog.Info("ldap connectivity check failed", "addr", ldapAddr, "error", err)
		return false
	}
	_ = conn.Close()
	return true
}

// checkGrafanaConnectivity performs a light health check against the Grafana backend.
func checkGrafanaConnectivity(cfg *config.Config) bool {
	grafanaAddr := strings.TrimSpace(cfg.Server.GrafanaAddr)
	if grafanaAddr == "" {
		grafanaAddr = "http://127.0.0.1:3000"
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(grafanaAddr)
	if err != nil {
		slog.Info("grafana connectivity check failed", "addr", grafanaAddr, "error", err)
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

const loginHTML = `<!DOCTYPE html>
<html lang="en-US">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width">
<title>Grafana</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&display=swap" rel="stylesheet">
<style>
*{box-sizing:border-box}
body{
  margin:0;
  font-family:'Inter','Helvetica','Arial',sans-serif;
  color:rgb(204,204,220);
  min-height:100vh;
  display:flex;
  flex-direction:column;
  align-items:center;
  justify-content:center;
  background:#000;
}
body::before{
  content:"";
  position:fixed;
  top:0;left:0;right:0;bottom:0;
  background:
    radial-gradient(ellipse 200% 50% at 50% -20%,rgba(67,84,230,0.18) 0%,transparent 60%),
    radial-gradient(ellipse 120% 80% at 70% -20%,rgba(248,151,151,0.10) 0%,rgba(214,118,230,0.04) 50%,transparent 70%),
    radial-gradient(ellipse 120% 100% at 20% -40%,rgba(255,138,54,0.14) 0%,rgba(251,90,103,0.06) 40%,transparent 70%),
    radial-gradient(ellipse 60% 60% at 25% -30%,rgba(255,138,54,0.10) 0%,transparent 60%),
    radial-gradient(ellipse 60% 30% at 25% -20%,rgba(251,197,90,0.06) 0%,transparent 60%);
  pointer-events:none;
}
.login-wrap{
  width:100%;
  max-width:478px;
  width:calc(100% - 2rem);
  position:relative;
  z-index:1;
}
.login-card{
  background:rgba(24,27,31,0.7);
  border-radius:8px;
  padding:16px 0;
  display:flex;
  flex-direction:column;
  align-items:center;
  min-height:320px;
  justify-content:center;
}
.login-logo{
  display:flex;
  align-items:center;
  justify-content:center;
  flex-direction:column;
  padding:24px 24px 0;
}
.login-logo svg{
  width:100%;
  max-width:60px;
  height:auto;
  display:block;
  margin-bottom:16px;
}
@media(min-width:500px){
  .login-logo svg{max-width:100px}
}
.login-header{
  text-align:center;
  padding:0 24px;
}
.login-header h1{
  color:#fff;
  font-size:22px;
  font-weight:500;
  margin:0 0 24px;
}
@media(min-width:500px){
  .login-header h1{font-size:32px}
}
.login-form{
  width:100%;
  max-width:415px;
  padding:0 16px 16px;
}
.field{
  margin-bottom:12px;
}
.field label{
  display:block;
  margin-bottom:8px;
  font-size:14px;
  font-weight:500;
  color:rgb(204,204,220);
}
.field input{
  width:100%;
  height:32px;
  padding:0 8px;
  background:#111217;
  border:1px solid rgba(204,204,220,0.20);
  border-radius:4px;
  color:rgb(204,204,220);
  font-size:14px;
  line-height:1.571;
  outline:none;
  transition:border-color .15s;
}
.field input:hover{
  border-color:rgba(204,204,220,0.30);
}
.field input:focus{
  outline:2px dotted transparent;
  outline-offset:2px;
  box-shadow:0 0 0 2px #111217,0 0 0 4px #3d71d9;
  border-color:#6e9fff;
}
.field input::placeholder{
  color:rgba(204,204,220,0.61);
  opacity:1;
}
.btn-login{
  display:inline-flex;
  align-items:center;
  justify-content:center;
  width:100%;
  height:32px;
  padding:0 16px;
  background:#3d71d9;
  color:#fff;
  border:none;
  border-radius:4px;
  font-size:14px;
  font-weight:500;
  line-height:1.571;
  cursor:pointer;
  transition:background .15s;
  margin-top:4px;
}
.btn-login:hover{background:#1f62e0}
.forgot-row{
  display:flex;
  justify-content:flex-end;
  margin-top:4px;
}
.forgot-link{
  display:inline-flex;
  align-items:center;
  justify-content:center;
  padding:0;
  border:none;
  border-radius:4px;
  background:transparent;
  color:#6e9fff;
  font-size:14px;
  font-weight:500;
  line-height:1.571;
  text-decoration:none;
  cursor:pointer;
  transition:color .15s;
}
.forgot-link:hover{color:#fff;text-decoration:underline}
.error-msg{
  color:#d10e5c;
  font-size:13px;
  margin:-4px 0 8px;
  text-align:center;
}
</style>
</head>
<body>
<div class="login-wrap">
<div class="login-card">
<div class="login-logo">
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 351 365" style="enable-background:new 0 0 351 365">
<linearGradient id="a" gradientUnits="userSpaceOnUse" x1="175.5" y1="30%" x2="175.5" y2="99%">
<stop offset="0" stop-color="#F05A28"/>
<stop offset="1" stop-color="#FBCA0A"/>
</linearGradient>
<path fill="url(#a)" d="M342,161.2c-0.6-6.1-1.6-13.1-3.6-20.9c-2-7.7-5-16.2-9.4-25c-4.4-8.8-10.1-17.9-17.5-26.8c-2.9-3.5-6.1-6.9-9.5-10.2c5.1-20.3-6.2-37.9-6.2-37.9c-19.5-1.2-31.9,6.1-36.5,9.4c-0.8-0.3-1.5-0.7-2.3-1c-3.3-1.3-6.7-2.6-10.3-3.7c-3.5-1.1-7.1-2.1-10.8-3c-3.7-0.9-7.4-1.6-11.2-2.2c-0.7-0.1-1.3-0.2-2-0.3c-8.5-27.2-32.9-38.6-32.9-38.6c-27.3,17.3-32.4,41.5-32.4,41.5s-0.1,0.5-0.3,1.4c-1.5,0.4-3,0.9-4.5,1.3c-2.1,0.6-4.2,1.4-6.2,2.2c-2.1,0.8-4.1,1.6-6.2,2.5c-4.1,1.8-8.2,3.8-12.2,6c-3.9,2.2-7.7,4.6-11.4,7.1c-0.5-0.2-1-0.4-1-0.4c-37.8-14.4-71.3,2.9-71.3,2.9c-3.1,40.2,15.1,65.5,18.7,70.1c-0.9,2.5-1.7,5-2.5,7.5c-2.8,9.1-4.9,18.4-6.2,28.1c-0.2,1.4-0.4,2.8-0.5,4.2C18.8,192.7,8.5,228,8.5,228c29.1,33.5,63.1,35.6,63.1,35.6c0,0,0.1-0.1,0.1-0.1c4.3,7.7,9.3,15,14.9,21.9c2.4,2.9,4.8,5.6,7.4,8.3c-10.6,30.4,1.5,55.6,1.5,55.6c32.4,1.2,53.7-14.2,58.2-17.7c3.2,1.1,6.5,2.1,9.8,2.9c10,2.6,20.2,4.1,30.4,4.5c2.5,0.1,5.1,0.2,7.6,0.1l1.2,0l0.8,0l1.6,0l1.6-0.1l0,0.1c15.3,21.8,42.1,24.9,42.1,24.9c19.1-20.1,20.2-40.1,20.2-44.4l0,0c0,0,0-0.1,0-0.3c0-0.4,0-0.6,0-0.6l0,0c0-0.3,0-0.6,0-0.9c4-2.8,7.8-5.8,11.4-9.1c7.6-6.9,14.3-14.8,19.9-23.3c0.5-0.8,1-1.6,1.5-2.4c21.6,1.2,36.9-13.4,36.9-13.4c-3.6-22.5-16.4-33.5-19.1-35.6l0,0c0,0-0.1-0.1-0.3-0.2c-0.2-0.1-0.2-0.2-0.2-0.2c0,0,0,0,0,0c-0.1-0.1-0.3-0.2-0.5-0.3c0.1-1.4,0.2-2.7,0.3-4.1c0.2-2.4,0.2-4.9,0.2-7.3l0-1.8l0-0.9l0-0.5c0-0.6,0-0.4,0-0.6l-0.1-1.5l-0.1-2c0-0.7-0.1-1.3-0.2-1.9c-0.1-0.6-0.1-1.3-0.2-1.9l-0.2-1.9l-0.3-1.9c-0.4-2.5-0.8-4.9-1.4-7.4c-2.3-9.7-6.1-18.9-11-27.2c-5-8.3-11.2-15.6-18.3-21.8c-7-6.2-14.9-11.2-23.1-14.9c-8.3-3.7-16.9-6.1-25.5-7.2c-4.3-0.6-8.6-0.8-12.9-0.7l-1.6,0l-0.4,0c-0.1,0-0.6,0-0.5,0l-0.7,0l-1.6,0.1c-0.6,0-1.2,0.1-1.7,0.1c-2.2,0.2-4.4,0.5-6.5,0.9c-8.6,1.6-16.7,4.7-23.8,9c-7.1,4.3-13.3,9.6-18.3,15.6c-5,6-8.9,12.7-11.6,19.6c-2.7,6.9-4.2,14.1-4.6,21c-0.1,1.7-0.1,3.5-0.1,5.2c0,0.4,0,0.9,0,1.3l0.1,1.4c0.1,0.8,0.1,1.7,0.2,2.5c0.3,3.5,1,6.9,1.9,10.1c1.9,6.5,4.9,12.4,8.6,17.4c3.7,5,8.2,9.1,12.9,12.4c4.7,3.2,9.8,5.5,14.8,7c5,1.5,10,2.1,14.7,2.1c0.6,0,1.2,0,1.7,0c0.3,0,0.6,0,0.9,0c0.3,0,0.6,0,0.9-0.1c0.5,0,1-0.1,1.5-0.1c0.1,0,0.3,0,0.4-0.1l0.5-0.1c0.3,0,0.6-0.1,0.9-0.1c0.6-0.1,1.1-0.2,1.7-0.3c0.6-0.1,1.1-0.2,1.6-0.4c1.1-0.2,2.1-0.6,3.1-0.9c2-0.7,4-1.5,5.7-2.4c1.8-0.9,3.4-2,5-3c0.4-0.3,0.9-0.6,1.3-1c1.6-1.3,1.9-3.7,0.6-5.3c-1.1-1.4-3.1-1.8-4.7-0.9c-0.4,0.2-0.8,0.4-1.2,0.6c-1.4,0.7-2.8,1.3-4.3,1.8c-1.5,0.5-3.1,0.9-4.7,1.2c-0.8,0.1-1.6,0.2-2.5,0.3c-0.4,0-0.8,0.1-1.3,0.1c-0.4,0-0.9,0-1.2,0c-0.4,0-0.8,0-1.2,0c-0.5,0-1,0-1.5-0.1c0,0-0.3,0-0.1,0l-0.2,0l-0.3,0c-0.2,0-0.5,0-0.7-0.1c-0.5-0.1-0.9-0.1-1.4-0.2c-3.7-0.5-7.4-1.6-10.9-3.2c-3.6-1.6-7-3.8-10.1-6.6c-3.1-2.8-5.8-6.1-7.9-9.9c-2.1-3.8-3.6-8-4.3-12.4c-0.3-2.2-0.5-4.5-0.4-6.7c0-0.6,0.1-1.2,0.1-1.8c0,0.2,0-0.1,0-0.1l0-0.2l0-0.5c0-0.3,0.1-0.6,0.1-0.9c0.1-1.2,0.3-2.4,0.5-3.6c1.7-9.6,6.5-19,13.9-26.1c1.9-1.8,3.9-3.4,6-4.9c2.1-1.5,4.4-2.8,6.8-3.9c2.4-1.1,4.8-2,7.4-2.7c2.5-0.7,5.1-1.1,7.8-1.4c1.3-0.1,2.6-0.2,4-0.2c0.4,0,0.6,0,0.9,0l1.1,0l0.7,0c0.3,0,0,0,0.1,0l0.3,0l1.1,0.1c2.9,0.2,5.7,0.6,8.5,1.3c5.6,1.2,11.1,3.3,16.2,6.1c10.2,5.7,18.9,14.5,24.2,25.1c2.7,5.3,4.6,11,5.5,16.9c0.2,1.5,0.4,3,0.5,4.5l0.1,1.1l0.1,1.1c0,0.4,0,0.8,0,1.1c0,0.4,0,0.8,0,1.1l0,1l0,1.1c0,0.7-0.1,1.9-0.1,2.6c-0.1,1.6-0.3,3.3-0.5,4.9c-0.2,1.6-0.5,3.2-0.8,4.8c-0.3,1.6-0.7,3.2-1.1,4.7c-0.8,3.1-1.8,6.2-3,9.3c-2.4,6-5.6,11.8-9.4,17.1c-7.7,10.6-18.2,19.2-30.2,24.7c-6,2.7-12.3,4.7-18.8,5.7c-3.2,0.6-6.5,0.9-9.8,1l-0.6,0l-0.5,0l-1.1,0l-1.6,0l-0.8,0c0.4,0-0.1,0-0.1,0l-0.3,0c-1.8,0-3.5-0.1-5.3-0.3c-7-0.5-13.9-1.8-20.7-3.7c-6.7-1.9-13.2-4.6-19.4-7.8c-12.3-6.6-23.4-15.6-32-26.5c-4.3-5.4-8.1-11.3-11.2-17.4c-3.1-6.1-5.6-12.6-7.4-19.1c-1.8-6.6-2.9-13.3-3.4-20.1l-0.1-1.3l0-0.3l0-0.3l0-0.6l0-1.1l0-0.3l0-0.4l0-0.8l0-1.6l0-0.3c0,0,0,0.1,0-0.1l0-0.6c0-0.8,0-1.7,0-2.5c0.1-3.3,0.4-6.8,0.8-10.2c0.4-3.4,1-6.9,1.7-10.3c0.7-3.4,1.5-6.8,2.5-10.2c1.9-6.7,4.3-13.2,7.1-19.3c5.7-12.2,13.1-23.1,22-31.8c2.2-2.2,4.5-4.2,6.9-6.2c2.4-1.9,4.9-3.7,7.5-5.4c2.5-1.7,5.2-3.2,7.9-4.6c1.3-0.7,2.7-1.4,4.1-2c0.7-0.3,1.4-0.6,2.1-0.9c0.7-0.3,1.4-0.6,2.1-0.9c2.8-1.2,5.7-2.2,8.7-3.1c0.7-0.2,1.5-0.4,2.2-0.7c0.7-0.2,1.5-0.4,2.2-0.6c1.5-0.4,3-0.8,4.5-1.1c0.7-0.2,1.5-0.3,2.3-0.5c0.8-0.2,1.5-0.3,2.3-0.5c0.8-0.1,1.5-0.3,2.3-0.4l1.1-0.2l1.2-0.2c0.8-0.1,1.5-0.2,2.3-0.3c0.9-0.1,1.7-0.2,2.6-0.3c0.7-0.1,1.9-0.2,2.6-0.3c0.5-0.1,1.1-0.1,1.6-0.2l1.1-0.1l0.5-0.1l0.6,0c0.9-0.1,1.7-0.1,2.6-0.2l1.3-0.1c0,0,0.5,0,0.1,0l0.3,0l0.6,0c0.7,0,1.5-0.1,2.2-0.1c2.9-0.1,5.9-0.1,8.8,0c5.8,0.2,11.5,0.9,17,1.9c11.1,2.1,21.5,5.6,31,10.3c9.5,4.6,17.9,10.3,25.3,16.5c0.5,0.4,0.9,0.8,1.4,1.2c0.4,0.4,0.9,0.8,1.3,1.2c0.9,0.8,1.7,1.6,2.6,2.4c0.9,0.8,1.7,1.6,2.5,2.4c0.8,0.8,1.6,1.6,2.4,2.5c3.1,3.3,6,6.6,8.6,10c5.2,6.7,9.4,13.5,12.7,19.9c0.2,0.4,0.4,0.8,0.6,1.2c0.2,0.4,0.4,0.8,0.6,1.2c0.4,0.8,0.8,1.6,1.1,2.4c0.4,0.8,0.7,1.5,1.1,2.3c0.3,0.8,0.7,1.5,1,2.3c1.2,3,2.4,5.9,3.3,8.6c1.5,4.4,2.6,8.3,3.5,11.7c0.3,1.4,1.6,2.3,3,2.1c1.5-0.1,2.6-1.3,2.6-2.8C342.6,170.4,342.5,166.1,342,161.2z"/>
</svg>
</div>
<div class="login-header">
<h1>Welcome to Grafana</h1>
</div>
<div class="login-form">
<form method="POST">
<div class="field">
<label for="username">Email or username</label>
<input id="username" type="text" name="username" placeholder="email or username" required autofocus>
</div>
<div class="field">
<label for="password">Password</label>
<input id="password" type="password" name="password" placeholder="password" required>
</div>
{{if .Error}}<div class="error-msg">{{.Error}}</div>{{end}}
<button type="submit" class="btn-login">Log in</button>
<div class="forgot-row">
<a href="/user/password/send-reset-email" class="forgot-link">Forgot your password?</a>
</div>
</form>
</div>
</div>
</div>
</body>
</html>
`

var loginTmpl = template.Must(template.New("login").Parse(loginHTML))

type loginData struct {
	Error string
}

func loginHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			loginTmpl.Execute(w, loginData{})
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		r.ParseForm()
		username := r.FormValue("username")
		password := r.FormValue("password")

		if username == "" || password == "" {
			loginTmpl.Execute(w, loginData{Error: "Username and password are required"})
			return
		}

		user, err := auth.Authenticate(username, password)
		if err != nil || user == nil {
			slog.Info("auth: login failed", "user", username, "error", err)
			loginTmpl.Execute(w, loginData{Error: "Invalid username or password"})
			return
		}

		token, err := auth.GenerateJWT(user)
		if err != nil {
			slog.Error("auth: failed to generate JWT", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     cfg.Auth.CookieName,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})

		if syncTriggerFn != nil {
			go func() {
				if err := syncTriggerFn(context.Background()); err != nil {
					slog.Error("auth: post-login sync failed", "user", username, "error", err)
				}
			}()
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// logoutHandler clears the JWT cookie and redirects to login page.
func logoutHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     cfg.Auth.CookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// grafanaLogoutHandler intercepts Grafana logout paths, clears the Savras cookie,
// and then proxies the request to Grafana so it can complete its own logout.
func grafanaLogoutHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Strip Savras cookie from request before proxying to Grafana
		cookies := r.Cookies()
		newCookies := make([]*http.Cookie, 0, len(cookies))
		for _, c := range cookies {
			if c.Name != cfg.Auth.CookieName {
				newCookies = append(newCookies, c)
			}
		}
		r.Header.Del("Cookie")
		for _, c := range newCookies {
			r.AddCookie(c)
		}
		target, _ := url.Parse(strings.TrimSpace(cfg.Server.GrafanaAddr))
		if target == nil {
			target, _ = url.Parse("http://127.0.0.1:3000")
		}
		rp := httputil.NewSingleHostReverseProxy(target)
		// Use a wrapper that adds the Savras cookie-clearing Set-Cookie header
		// AFTER the reverse proxy has written Grafana's response headers.
		// This is necessary because ReverseProxy copies upstream headers to w,
		// which would overwrite any Set-Cookie set before rp.ServeHTTP.
		lw := &logoutResponseWriter{
			ResponseWriter: w,
			cookie: &http.Cookie{
				Name:     cfg.Auth.CookieName,
				Value:    "",
				Path:     "/",
				MaxAge:   -1,
				HttpOnly: true,
				Secure:   true,
				SameSite: http.SameSiteLaxMode,
			},
		}
		rp.ServeHTTP(lw, r)
	}
}

// logoutResponseWriter wraps http.ResponseWriter to inject a cookie-clearing
// Set-Cookie header after the upstream handler has written its own headers.
type logoutResponseWriter struct {
	http.ResponseWriter
	cookie  *http.Cookie
	cleared bool
}

func (w *logoutResponseWriter) WriteHeader(statusCode int) {
	if !w.cleared {
		http.SetCookie(w.ResponseWriter, w.cookie)
		w.cleared = true
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *logoutResponseWriter) Write(b []byte) (int, error) {
	if !w.cleared {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
