package sync

import (
	"fmt"
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

func (m *mockGrafanaClient) ClearTeamCache(name string) {
	// no-op: mock has no persistent cache between sync cycles
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

func TestTrigger(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: true, Interval: time.Hour}}
	w := NewSyncWorker(c, nil)
	w.Trigger()
	w.Trigger()
	w.Stop()
}

func TestStartSyncWorker(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: false}}
	w := StartSyncWorker(c, nil)
	if w == nil {
		t.Fatal("expected non-nil SyncWorker")
	}
	w.Stop()
}

func TestSyncWorker_Cache(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: false}}
	w := NewSyncWorker(c, nil)
	w.mu.Lock()
	w.cache["kyra"] = 123
	w.mu.Unlock()

	w.mu.RLock()
	id, ok := w.cache["kyra"]
	w.mu.RUnlock()
	if !ok || id != 123 {
		t.Fatalf("expected cached ID 123, got %d, ok=%v", id, ok)
	}
}

func TestStop_WithoutStart(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: false}}
	w := NewSyncWorker(c, nil)
	w.Stop()
}

func TestStart_AlreadyStopped(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: true, Interval: 50 * time.Millisecond}}
	w := NewSyncWorker(c, nil)
	w.Start()
	time.Sleep(10 * time.Millisecond)
	w.Stop()
	w.Stop()
}

func TestSyncWorker_WithMockClient(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: false}}
	w := NewSyncWorker(c, nil)
	if w == nil {
		t.Fatal("expected non-nil worker")
	}
	mock := newMockClient()
	_ = mock
}

func TestSyncWorker_EnabledConfig(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: true, Interval: 30 * time.Second}}
	w := NewSyncWorker(c, nil)
	if w.interval != 30*time.Second {
		t.Fatalf("expected 30s interval, got %v", w.interval)
	}
}

func TestSyncWorker_DisabledConfig(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: false}}
	w := NewSyncWorker(c, nil)
	w.Start() // should log "disabled by config"
	w.Stop()
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

func TestSyncWorker_IsStale(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: true, Interval: time.Hour}}
	w := NewSyncWorker(c, nil)

	// Before any sync, IsStale should return true for a worker with interval > 0
	if !w.IsStale() {
		t.Fatal("expected stale before any sync")
	}

	// After setting lastSyncTime to now, should not be stale
	w.mu.Lock()
	w.lastSyncTime = time.Now()
	w.mu.Unlock()
	if w.IsStale() {
		t.Fatal("expected not stale after recent sync")
	}

	// After setting lastSyncTime to far in the past, should be stale
	w.mu.Lock()
	w.lastSyncTime = time.Now().Add(-3 * time.Hour)
	w.mu.Unlock()
	if !w.IsStale() {
		t.Fatal("expected stale after 3x interval without sync")
	}

	// With interval = 0, IsStale should return false
	c2 := &cfg.Config{}
	w2 := NewSyncWorker(c2, nil)
	if w2.IsStale() {
		t.Fatal("expected not stale with zero interval")
	}
}

func TestSyncWorker_LastSyncAt(t *testing.T) {
	c := &cfg.Config{Sync: cfg.SyncConfig{Enabled: true, Interval: time.Hour}}
	w := NewSyncWorker(c, nil)

	// Initially zero
	if !w.LastSyncAt().IsZero() {
		t.Fatal("expected zero LastSyncAt initially")
	}

	// After setting, should return the value
	now := time.Now()
	w.mu.Lock()
	w.lastSyncTime = now
	w.mu.Unlock()

	if w.LastSyncAt().IsZero() {
		t.Fatal("expected non-zero LastSyncAt after set")
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
	time.Sleep(20 * time.Millisecond)
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
