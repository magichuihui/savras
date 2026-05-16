package sync

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"log/slog"
	grafConfig "savras/internal/config"
	grafana "savras/internal/grafana"

	ldap "github.com/go-ldap/ldap/v3"
)

// SyncWorker periodically syncs AD groups to Grafana teams.
type SyncWorker struct {
	cfg        *grafConfig.Config
	grafClient *grafana.Client
	trigger    chan struct{}
	stop       chan struct{}
	ready      chan struct{} // closed after initial sync completes
	interval   time.Duration
	cache      map[string]int64 // login -> Grafana user ID
	mu         sync.RWMutex
}

// NewSyncWorker creates a new worker instance.
func NewSyncWorker(cfg *grafConfig.Config, g *grafana.Client) *SyncWorker {
	w := &SyncWorker{
		cfg:        cfg,
		grafClient: g,
		trigger:    make(chan struct{}, 1),
		stop:       make(chan struct{}),
		ready:      make(chan struct{}),
		interval:   cfg.Sync.Interval,
		cache:      make(map[string]int64),
	}
	return w
}

// Start starts the sync loop in a new goroutine (if enabled).
func (w *SyncWorker) Start() {
	if w.cfg == nil || !w.cfg.Sync.Enabled {
		slog.Info("sync worker: disabled by config")
		return
	}
	go func() {
		// Delay initial sync to allow Grafana to finish startup,
		// especially after a Grafana restart.
		if d := w.cfg.Sync.StartupDelaySeconds; d > 0 {
			slog.Info("sync worker: delaying initial sync", "seconds", d)
			time.Sleep(time.Duration(d) * time.Second)
		}

		slog.Info("sync worker: initial sync")
		if err := w.syncOnce(); err != nil {
			slog.Error("sync worker: initial sync failed", "error", err)
		}
		close(w.ready)
		w.loop()
	}()
}

// Stop stops the sync loop.
func (w *SyncWorker) Stop() {
	select {
	case <-w.stop:
		// already closed
	default:
		close(w.stop)
	}
}

// Ready returns a channel that is closed after the first sync cycle completes.
// The health check waits on this before considering the service ready.
func (w *SyncWorker) Ready() <-chan struct{} {
	return w.ready
}

// Trigger allows manual triggering of a sync cycle.
func (w *SyncWorker) Trigger() {
	select {
	case w.trigger <- struct{}{}:
	default:
	}
}

func (w *SyncWorker) loop() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = w.syncOnce()
		case <-w.trigger:
			_ = w.syncOnce()
		case <-w.stop:
			return
		}
	}
}

// syncOnce performs a single synchronization pass.
func (w *SyncWorker) syncOnce() error {
	// Establish LDAP connection
	ldapURL := w.cfg.LDAP.URL()
	l, err := ldap.DialURL(ldapURL)
	if err != nil {
		slog.Error("ldap dial failed", "err", err, "url", ldapURL)
		return err
	}
	defer l.Close()

	// Bind if credentials provided
	if w.cfg.LDAP.BindDN != "" {
		if err := l.Bind(w.cfg.LDAP.BindDN, w.cfg.LDAP.BindPassword); err != nil {
			slog.Error("ldap bind failed", "err", err, "dn", w.cfg.LDAP.BindDN)
			return err
		}
	}

	// Search for groups under the configured base
	groupFilter := w.cfg.LDAP.GroupFilter
	if groupFilter == "" {
		groupFilter = "(objectClass=group)"
	}
	// Fetch group name and member DNs
	groupNames := []string{w.cfg.LDAP.GroupNameAttr}
	if w.cfg.LDAP.GroupMemberAttr != "" {
		groupNames = append(groupNames, w.cfg.LDAP.GroupMemberAttr)
	}
	searchReq := ldap.NewSearchRequest(
		w.cfg.LDAP.GroupBaseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		0,
		false,
		groupFilter,
		groupNames,
		nil,
	)
	sr, err := l.Search(searchReq)
	if err != nil {
		slog.Error("ldap group search failed", "err", err, "base", w.cfg.LDAP.GroupBaseDN)
		return err
	}
	// Build lookup map from GroupsMapping for quick check
	slog.Info("sync: LDAP group search complete", "count", len(sr.Entries))
	adToGrafana := make(map[string]string)
	for _, m := range w.cfg.Sync.GroupsMapping {
		adToGrafana[m.ADGroup] = m.GrafanaTeam
	}
	slog.Debug("sync: GroupsMapping loaded", "mapping", w.cfg.Sync.GroupsMapping, "map", adToGrafana)

	// Iterate all groups and sync those present in GroupsMapping
	for _, entry := range sr.Entries {
		adGroup := entry.GetAttributeValue(w.cfg.LDAP.GroupNameAttr)
		slog.Info("sync: found LDAP group", "name", adGroup, "dn", entry.DN)
		grafTeam, ok := adToGrafana[adGroup]
		if !ok {
			slog.Info("sync: LDAP group not in mapping, skipping", "group", adGroup)
			continue
		}
		slog.Info("sync: processing LDAP group", "group", adGroup, "grafanaTeam", grafTeam)

		memberDNs := entry.GetAttributeValues(w.cfg.LDAP.GroupMemberAttr)
		slog.Info("sync: group members from LDAP", "group", adGroup, "memberCount", len(memberDNs))
		// Map memberUid values directly to Grafana user IDs
		desiredIDs := []int64{}
		for _, member := range memberDNs {
			if member == "" {
				continue
			}
			var uid int64
			w.mu.RLock()
			if v, ok := w.cache[member]; ok {
				uid = v
			}
			w.mu.RUnlock()
			if uid == 0 {
				user, err := w.grafClient.LookupUser(member)
				if err != nil {
					slog.Debug("grafana user not found for login, skipping", "login", member)
					continue
				}
				uid = user.ID
				w.mu.Lock()
				w.cache[member] = uid
				w.mu.Unlock()
			}
			if uid != 0 {
				desiredIDs = append(desiredIDs, uid)
			}
		}

		// Get or create Grafana team
		team, tErr := w.grafClient.GetTeamByName(grafTeam)
		var teamID int64
		var teamFoundViaCreate bool
		if tErr != nil {
			// If not found, create
			if strings.Contains(strings.ToLower(tErr.Error()), "not found") {
				slog.Info("sync: team not found via search, creating", "team", grafTeam)
				id, cErr := w.grafClient.CreateTeam(grafTeam)
				if cErr != nil {
					slog.Error("failed to create grafana team", "team", grafTeam, "err", cErr)
					continue
				}
				teamID = id
				teamFoundViaCreate = true
			} else {
				slog.Error("failed to get grafana team", "team", grafTeam, "err", tErr)
				continue
			}
		} else {
			teamID = team.ID
			slog.Info("sync: team found via search", "team", grafTeam, "teamID", teamID)

			// Verify the team actually exists via the detail API. The search API may
			// return phantom entries (stale index entries pointing to non-existent teams).
			if _, vErr := w.grafClient.GetTeam(teamID); vErr != nil {
				slog.Warn("sync: team from search does not exist (phantom entry), recreating",
					"team", grafTeam, "teamID", teamID, "err", vErr)
				w.grafClient.ClearTeamCache(grafTeam)
				id, cErr := w.grafClient.CreateTeam(grafTeam)
				if cErr != nil {
					slog.Error("sync: failed to recreate team after phantom detection",
						"team", grafTeam, "err", cErr)
					continue
				}
				teamID = id
				teamFoundViaCreate = true
				slog.Info("sync: team recreated successfully", "team", grafTeam, "teamID", teamID)
			} else {
				slog.Info("sync: team verified", "team", grafTeam, "teamID", teamID)
			}
		}

		if teamID <= 0 {
			slog.Error("invalid team ID after lookup/creation", "team", grafTeam, "teamID", teamID)
			continue
		}
		slog.Info("sync: resolved team", "team", grafTeam, "teamID", teamID, "created", teamFoundViaCreate)

		// Current members of the Grafana team
		members, mErr := w.grafClient.GetTeamMembers(teamID)
		if mErr != nil {
			slog.Error("failed to fetch grafana team members", "team", grafTeam, "err", mErr)
			continue
		}
		slog.Info("sync: fetched team members", "team", grafTeam, "teamID", teamID, "memberCount", len(members))
		current := make(map[int64]struct{}, len(members))
		for _, mm := range members {
			current[mm.UserId] = struct{}{}
			slog.Debug("sync: current team member", "team", grafTeam, "userId", mm.UserId)
		}

		// Desired set
		desired := make(map[int64]struct{}, len(desiredIDs))
		for _, id := range desiredIDs {
			desired[id] = struct{}{}
			slog.Debug("sync: desired team member", "team", grafTeam, "userId", id)
		}

		// Add new members
		for id := range desired {
			if _, exists := current[id]; !exists {
				slog.Info("sync: adding team member", "team", grafTeam, "teamID", teamID, "userId", id)
				if aErr := w.grafClient.AddTeamMember(teamID, id); aErr != nil {
					slog.Error("failed to add member to grafana team", "team", grafTeam, "teamID", teamID, "userId", id, "err", aErr)
				} else {
					slog.Info("added grafana team member", "team", grafTeam, "teamID", teamID, "userId", id)
				}
			}
		}
		// Remove members not in AD group
		for id := range current {
			if _, should := desired[id]; !should {
				slog.Info("sync: removing team member", "team", grafTeam, "teamID", teamID, "userId", id)
				if rErr := w.grafClient.RemoveTeamMember(teamID, id); rErr != nil {
					slog.Error("failed to remove grafana team member", "team", grafTeam, "teamID", teamID, "userId", id, "err", rErr)
				} else {
					slog.Info("removed grafana team member", "team", grafTeam, "teamID", teamID, "userId", id)
				}
			}
		}
	}
	// Sync folder permissions — group by folder so each folder gets one API call
	// with all team permissions applied together (last-write-wins bug fix).
	folderPerms := make(map[string][]grafConfig.FolderPermission)
	for _, fp := range w.cfg.Sync.FolderPermissions {
		folderPerms[fp.Folder] = append(folderPerms[fp.Folder], fp)
	}
	for folderName, perms := range folderPerms {
		if err := w.syncFolderPermissions(folderName, perms); err != nil {
			slog.Error("failed to sync folder permissions", "folder", folderName, "err", err)
		}
	}

	return nil
}

func (w *SyncWorker) syncFolderPermissions(folderName string, fps []grafConfig.FolderPermission) error {
	folder, err := w.grafClient.GetFolderByTitle(folderName)
	if err != nil {
		return fmt.Errorf("get folder: %w", err)
	}

	var newPerms []grafana.FolderPermission
	for _, fp := range fps {
		team, err := w.grafClient.GetTeamByName(fp.Team)
		if err != nil {
			teamID, err2 := w.grafClient.CreateTeam(fp.Team)
			if err2 != nil {
				return fmt.Errorf("get/create team %s: %w", fp.Team, err2)
			}
			team = &grafana.Team{ID: teamID, Name: fp.Team}
		} else {
			// Verify the team actually exists (search may return phantom entries).
			if _, vErr := w.grafClient.GetTeam(team.ID); vErr != nil {
				slog.Warn("sync: folder perm team from search does not exist, recreating",
					"team", fp.Team, "teamID", team.ID, "err", vErr)
				w.grafClient.ClearTeamCache(fp.Team)
				teamID, err2 := w.grafClient.CreateTeam(fp.Team)
				if err2 != nil {
					return fmt.Errorf("get/create team after phantom: %w", err2)
				}
				team = &grafana.Team{ID: teamID, Name: fp.Team}
			}
		}

		newPerms = append(newPerms, grafana.FolderPermission{
			TeamID:     team.ID,
			Permission: fp.Permission,
		})
	}

	if err := w.grafClient.UpdateFolderPermissions(folder.UID, newPerms); err != nil {
		return fmt.Errorf("update folder permissions: %w", err)
	}

	slog.Info("synced folder permissions", "folder", folderName, "count", len(newPerms))
	return nil
}

// StartSyncWorker is a convenience wrapper to create and start a worker.
func StartSyncWorker(cfg *grafConfig.Config, g *grafana.Client) *SyncWorker {
	w := NewSyncWorker(cfg, g)
	w.Start()
	return w
}
