package grafana

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	grafConfig "savras/internal/config"
)

func TestNewClient_WithAPIToken(t *testing.T) {
	cfg := &grafConfig.GrafanaConfig{APIToken: "tok123"}
	c := NewClient("http://example.com", cfg)
	if c == nil {
		t.Fatal("expected non-nil Grafana client")
	}
	if c.baseURL != "http://example.com" {
		t.Fatalf("unexpected baseURL: %q", c.baseURL)
	}
	if c.token != "tok123" {
		t.Fatalf("expected token 'tok123', got %q", c.token)
	}
}

func TestNewClient_WithAdminCredentials(t *testing.T) {
	cfg := &grafConfig.GrafanaConfig{APIToken: "", AdminUser: "admin", AdminPassword: "pass"}
	c := NewClient("http://example.com", cfg)
	if c == nil {
		t.Fatal("expected non-nil Grafana client")
	}
	if c.username != "admin" || c.password != "pass" {
		t.Fatalf("unexpected credentials: username=%q password=%q", c.username, c.password)
	}
	if c.token != "" {
		t.Fatalf("expected empty token, got %q", c.token)
	}
}

func TestNewRequest_WithToken(t *testing.T) {
	cfg := &grafConfig.GrafanaConfig{APIToken: "tok123"}
	c := NewClient("http://example.com", cfg)
	req, err := c.newRequest("GET", "/api/teams", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Header.Get("Authorization") != "Bearer tok123" {
		t.Fatalf("expected Bearer token, got %q", req.Header.Get("Authorization"))
	}
}

func TestNewRequest_WithBasicAuth(t *testing.T) {
	cfg := &grafConfig.GrafanaConfig{AdminUser: "admin", AdminPassword: "pass"}
	c := NewClient("http://example.com", cfg)
	req, err := c.newRequest("GET", "/api/teams", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, _, ok := req.BasicAuth()
	if !ok {
		t.Fatal("expected basic auth to be set")
	}
}

func TestNewRequest_WithPayload(t *testing.T) {
	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient("http://example.com", cfg)
	payload := map[string]string{"name": "test-team"}
	req, err := c.newRequest("POST", "/api/teams", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", req.Header.Get("Content-Type"))
	}
	if req.Body == nil {
		t.Fatal("expected body to be set")
	}
}

func TestDo_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient(server.URL, cfg)
	req, _ := c.newRequest("GET", "/api/test", nil)
	var resp map[string]string
	err := c.do(req, &resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("unexpected response: %v", resp)
	}
}

func TestDo_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	}))
	defer server.Close()

	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient(server.URL, cfg)
	req, _ := c.newRequest("GET", "/api/test", nil)
	err := c.do(req, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("expected 'bad request' in error, got %v", err)
	}
}

func TestDo_NoResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient(server.URL, cfg)
	req, _ := c.newRequest("DELETE", "/api/test", nil)
	err := c.do(req, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateTeam(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"teamId": 123, "id": 123, "uid": "abc", "message": "Team created"})
	}))
	defer server.Close()

	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient(server.URL, cfg)
	id, err := c.CreateTeam("test-team")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 123 {
		t.Fatalf("expected team ID 123, got %d", id)
	}
	// Check cache
	cached := c.teamCache["test-team"]
	if cached != 123 {
		t.Fatalf("expected cached team ID 123, got %d", cached)
	}
}

func TestGetTeamByName_FromCache(t *testing.T) {
	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient("http://example.com", cfg)
	c.mu.Lock()
	c.teamCache["test-team"] = 456
	c.mu.Unlock()

	team, err := c.GetTeamByName("test-team")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if team.ID != 456 {
		t.Fatalf("expected team ID 456, got %d", team.ID)
	}
}

func TestGetTeamByName_FromAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"teams": []map[string]interface{}{
				{"id": 789, "name": "test-team"},
			},
		})
	}))
	defer server.Close()

	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient(server.URL, cfg)
	team, err := c.GetTeamByName("test-team")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if team.ID != 789 {
		t.Fatalf("expected team ID 789, got %d", team.ID)
	}
}

func TestGetTeamByName_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"teams": []map[string]interface{}{}})
	}))
	defer server.Close()

	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient(server.URL, cfg)
	_, err := c.GetTeamByName("non-existent")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' in error, got %v", err)
	}
}

func TestAddTeamMember(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient(server.URL, cfg)
	err := c.AddTeamMember(123, 456)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRemoveTeamMember(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient(server.URL, cfg)
	err := c.RemoveTeamMember(123, 456)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetTeamMembers_Format1(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{"userId": 1, "teamId": 100, "permission": 0},
		})
	}))
	defer server.Close()

	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient(server.URL, cfg)
	members, err := c.GetTeamMembers(100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(members) != 1 || members[0].UserId != 1 {
		t.Fatalf("unexpected members: %v", members)
	}
}

func TestGetTeamMembers_Format2(t *testing.T) {
	// Test the alternate response format with wrapper
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First call fails for direct array, should fallback to wrapper
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"members": []map[string]interface{}{
				{"userId": 2, "teamId": 100, "permission": 0},
			},
		})
	}))
	defer server.Close()

	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient(server.URL, cfg)
	members, err := c.GetTeamMembers(100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(members) != 1 || members[0].UserId != 2 {
		t.Fatalf("unexpected members: %v", members)
	}
}

func TestGetFolderByTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{"id": 1, "uid": "folder1", "title": "Test Folder"},
		})
	}))
	defer server.Close()

	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient(server.URL, cfg)
	folder, err := c.GetFolderByTitle("Test Folder")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if folder.ID != 1 || folder.UID != "folder1" {
		t.Fatalf("unexpected folder: %v", folder)
	}
}

func TestGetFolderByTitle_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]map[string]interface{}{})
	}))
	defer server.Close()

	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient(server.URL, cfg)
	_, err := c.GetFolderByTitle("Non-existent")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUpdateFolderPermissions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient(server.URL, cfg)
	perms := []FolderPermission{
		{TeamID: 100, Permission: "View"},
	}
	err := c.UpdateFolderPermissions("folder1", perms)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetTeam(t *testing.T) {
	// Grafana v12's GET /api/teams/{id} returns TeamDTO with "id" (not "teamId").
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"id": 123, "uid": "abc", "name": "test-team"})
	}))
	defer server.Close()

	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient(server.URL, cfg)
	team, err := c.GetTeam(123)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if team.ID != 123 || team.Name != "test-team" {
		t.Fatalf("unexpected team: %v", team)
	}
}

func TestNewClient_TrimURL(t *testing.T) {
	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient("http://example.com/", cfg)
	if c.baseURL != "http://example.com" {
		t.Fatalf("expected trimmed URL, got %q", c.baseURL)
	}
}

func TestClient_HTTPClientTimeout(t *testing.T) {
	cfg := &grafConfig.GrafanaConfig{APIToken: "tok"}
	c := NewClient("http://example.com", cfg)
	if c.httpClient.Timeout != 15*time.Second {
		t.Fatalf("expected timeout 15s, got %v", c.httpClient.Timeout)
	}
}
