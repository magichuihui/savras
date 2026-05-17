package sync

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	cfg "savras/internal/config"
	grafana "savras/internal/grafana"
)

type mockGrafanaClient struct {
	mu            sync.RWMutex
	teams         map[string]*grafana.Team
	teamMembers   map[int64][]grafana.TeamMember
	users         map[string]*grafana.User
	folders       map[string]*grafana.Folder
	createTeamErr error
	getTeamErr    error
	addMemberErr  error
	findUserErr   error
	getFolderErr  error
	updatePermErr error
}

func newMockClient() *mockGrafanaClient {
	return &mockGrafanaClient{
		teams:       make(map[string]*grafana.Team),
		teamMembers: make(map[int64][]grafana.TeamMember),
		users:       make(map[string]*grafana.User),
		folders:     make(map[string]*grafana.Folder),
	}
}

func (m *mockGrafanaClient) CreateTeam(name string) (int64, error) {
	if m.createTeamErr != nil {
		return 0, m.createTeamErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id := int64(len(m.teams) + 1)
	m.teams[name] = &grafana.Team{ID: id, Name: name}
	return id, nil
}

func (m *mockGrafanaClient) GetTeamByName(name string) (*grafana.Team, error) {
	if m.getTeamErr != nil {
		return nil, m.getTeamErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if t, ok := m.teams[name]; ok {
		return t, nil
	}
	return nil, nil
}

func (m *mockGrafanaClient) AddTeamMember(teamID, userID int64) error {
	return m.addMemberErr
}

func (m *mockGrafanaClient) RemoveTeamMember(teamID, userID int64) error {
	return nil
}

func (m *mockGrafanaClient) GetTeamMembers(teamID int64) ([]grafana.TeamMember, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.teamMembers[teamID], nil
}

func (m *mockGrafanaClient) LookupUser(loginOrEmail string) (*grafana.User, error) {
	if m.findUserErr != nil {
		return nil, m.findUserErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if u, ok := m.users[loginOrEmail]; ok {
		return u, nil
	}
	return nil, fmt.Errorf("user not found: %s", loginOrEmail)
}

func (m *mockGrafanaClient) GetFolderByTitle(title string) (*grafana.Folder, error) {
	if m.getFolderErr != nil {
		return nil, m.getFolderErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if f, ok := m.folders[title]; ok {
		return f, nil
	}
	return nil, nil
}

func (m *mockGrafanaClient) UpdateFolderPermissions(folderUID string, perms []grafana.FolderPermission) error {
	return m.updatePermErr
}

func (m *mockGrafanaClient) GetTeam(teamID int64) (*grafana.Team, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, t := range m.teams {
		if t.ID == teamID {
			return t, nil
		}
	}
	return nil, nil
}

func TestNewSyncWorkerConstructor(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: false, Interval: 100 * time.Millisecond}}
	w := NewSyncWorker(c, nil)
	if w == nil {
		t.Fatal("expected non-nil SyncWorker")
	}
	if w.interval != 100*time.Millisecond {
		t.Fatalf("unexpected interval: %v", w.interval)
	}
}

func TestNewSyncWorker_Enabled(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: true, Interval: 50 * time.Millisecond}}
	w := NewSyncWorker(c, nil)
	if w == nil {
		t.Fatal("expected non-nil SyncWorker")
	}
	if !w.cfg.Sync.Enabled {
		t.Fatal("expected sync to be enabled")
	}
}

func TestNewSyncWorker_Disabled(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: false}}
	w := NewSyncWorker(c, nil)
	if w == nil {
		t.Fatal("expected non-nil SyncWorker")
	}
}

func TestStart_Stop(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: true, Interval: 50 * time.Millisecond}}
	w := NewSyncWorker(c, nil)
	w.Start()
	time.Sleep(10 * time.Millisecond)
	w.Stop()
}

func TestSyncNow_NilGrafClient(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: false}}
	w := NewSyncWorker(c, nil)
	err := w.SyncNow()
	if err == nil {
		t.Fatal("expected error for nil grafClient")
	}
}

func TestSyncWorker_Ready(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: true, Interval: time.Hour}}
	w := NewSyncWorker(c, nil)

	// Ready must block before Start
	select {
	case <-w.Ready():
		t.Fatal("Ready() should not fire before Start()")
	default:
		// expected
	}
}

func TestSyncWorker_TriggerChannel(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: true, Interval: time.Hour}}
	w := NewSyncWorker(c, nil)
	// Fill the trigger channel to test non-blocking behavior
	w.Trigger()
	w.Trigger()
	w.Trigger()
	w.Stop()
}

func TestSyncWorker_StartStop(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: true, Interval: 10 * time.Millisecond}}
	w := NewSyncWorker(c, nil)
	w.Start()
	time.Sleep(10 * time.Millisecond)
	w.Stop()
}

func TestSyncOnce_NotEnabled(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: false}}
	w := NewSyncWorker(c, nil)
	w.syncOnce()
}

func TestSyncOnce_NoClient(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: true}}
	w := NewSyncWorker(c, nil)
	err := w.syncOnce()
	if err == nil {
		t.Fatal("expected error for nil grafClient")
	}
}

func TestSyncFolderPermissions_NoFolder(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: false}}
	w := NewSyncWorker(c, nil)

	// syncFolderPermissions will fail because grafClient is nil
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic or error for nil grafClient")
		}
	}()

	fps := []cfg.FolderPermission{
		{Folder: "Non-existent", Team: "Team", Permission: "View"},
	}
	_ = w.syncFolderPermissions("Non-existent", fps)
}

func TestLoop_NotStartedWhenDisabled(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: false, Interval: 50 * time.Millisecond}}
	w := NewSyncWorker(c, nil)
	// loop() should not start when disabled
	// Just verify we can call Start and it logs the disabled message
	w.Start()
	time.Sleep(10 * time.Millisecond)
	w.Stop()
}

func TestStartSyncWorker_Disabled(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: false}}
	w := StartSyncWorker(c, nil)
	if w == nil {
		t.Fatal("expected non-nil SyncWorker")
	}
	w.Stop()
}

func TestStartSyncWorker_Enabled(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: true, Interval: 50 * time.Millisecond, StartupDelaySeconds: 0}}
	w := StartSyncWorker(c, nil)
	if w == nil {
		t.Fatal("expected non-nil SyncWorker")
	}
	time.Sleep(20 * time.Millisecond)
	w.Stop()
}

func TestAddTeamMemberWithRetry_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	gc := grafana.NewClient(server.URL, "", "", "")
	err := addTeamMemberWithRetry(gc, 1, 1)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestAddTeamMemberWithRetry_RetriesExhausted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	gc := grafana.NewClient(server.URL, "", "", "")
	err := addTeamMemberWithRetry(gc, 1, 1)
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
}
