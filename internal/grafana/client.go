package grafana

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	grafConfig "savras/internal/config"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
	token      string
	username   string
	password   string
	teamCache  map[string]int64
	mu         sync.RWMutex
}

type Team struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Email       string `json:"email,omitempty"`
	MemberCount int64  `json:"memberCount,omitempty"`
}

type TeamMember struct {
	UserId     int64 `json:"userId"`
	TeamId     int64 `json:"teamId"`
	Permission int   `json:"permission"`
}

type User struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

type Folder struct {
	ID    int64  `json:"id"`
	UID   string `json:"uid"`
	Title string `json:"title"`
}

type FolderPermission struct {
	ID         int64  `json:"id,omitempty"`
	TeamID     int64  `json:"teamId,omitempty"`
	UserID     int64  `json:"userId,omitempty"`
	Permission string `json:"permission"`
}

func NewClient(baseURL string, cfg *grafConfig.GrafanaConfig) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 15 * time.Second},
		token:      cfg.APIToken,
		teamCache:  make(map[string]int64),
	}
	if cfg.APIToken == "" {
		c.username = cfg.AdminUser
		c.password = cfg.AdminPassword
	}
	return c
}

func (c *Client) newRequest(method, path string, payload interface{}) (*http.Request, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}

	fullURL := c.baseURL + path
	req, err := http.NewRequest(method, fullURL, body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	} else {
		req.SetBasicAuth(c.username, c.password)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) do(req *http.Request, v interface{}) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", req.Method, req.URL.RequestURI(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("grafana API error (%s %s, status %d): %s", req.Method, req.URL.RequestURI(), resp.StatusCode, strings.TrimSpace(string(b)))
	}

	if v == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(v); err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	return nil
}

func (c *Client) CreateTeam(name string) (int64, error) {
	payload := map[string]string{"name": name}
	req, err := c.newRequest("POST", "/api/teams", payload)
	if err != nil {
		return 0, err
	}
	var resp struct {
		TeamID int64  `json:"teamId"`
		ID     int64  `json:"id"`
		Name   string `json:"name"`
	}
	if err := c.do(req, &resp); err != nil {
		return 0, err
	}
	teamID := resp.TeamID
	if teamID == 0 {
		teamID = resp.ID
	}
	if teamID == 0 {
		return 0, fmt.Errorf("create team succeeded but response contained no team ID: %+v", resp)
	}
	c.mu.Lock()
	c.teamCache[name] = teamID
	c.mu.Unlock()
	return teamID, nil
}

// teamSearchResult matches the Grafana team search API response format,
// which uses "id" instead of "teamId" (unlike the create team API).
type teamSearchResult struct {
	ID          int64  `json:"id"`
	OrgID       int64  `json:"orgId"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	MemberCount int64  `json:"memberCount"`
}

func (c *Client) GetTeamByName(name string) (*Team, error) {
	c.mu.RLock()
	if id, ok := c.teamCache[name]; ok {
		c.mu.RUnlock()
		return &Team{ID: id, Name: name}, nil
	}
	c.mu.RUnlock()

	escaped := url.QueryEscape(name)
	req, err := c.newRequest("GET", "/api/teams/search?query="+escaped, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Teams []teamSearchResult `json:"teams"`
	}
	if err := c.do(req, &resp); err != nil {
		return nil, err
	}
	for _, t := range resp.Teams {
		if t.Name == name {
			c.mu.Lock()
			c.teamCache[name] = t.ID
			c.mu.Unlock()
			return &Team{ID: t.ID, Name: t.Name}, nil
		}
	}
	return nil, fmt.Errorf("team not found: %s", name)
}

func (c *Client) AddTeamMember(teamID, userID int64) error {
	payload := map[string]interface{}{"userId": userID}
	path := fmt.Sprintf("/api/teams/%d/members", teamID)
	req, err := c.newRequest("POST", path, payload)
	if err != nil {
		return err
	}
	if err := c.do(req, nil); err != nil {
		return err
	}
	return nil
}

func (c *Client) RemoveTeamMember(teamID, userID int64) error {
	path := fmt.Sprintf("/api/teams/%d/members/%d", teamID, userID)
	req, err := c.newRequest("DELETE", path, nil)
	if err != nil {
		return err
	}
	if err := c.do(req, nil); err != nil {
		return err
	}
	return nil
}

func (c *Client) GetTeamMembers(teamID int64) ([]TeamMember, error) {
	path := fmt.Sprintf("/api/teams/%d/members", teamID)
	req, err := c.newRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("grafana API error (%s %s, status %d): %s", req.Method, req.URL.RequestURI(), resp.StatusCode, strings.TrimSpace(string(b)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var members []TeamMember
	if err := json.Unmarshal(body, &members); err == nil {
		return members, nil
	}

	var wrapper struct {
		Members []TeamMember `json:"members"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil, err
	}
	return wrapper.Members, nil
}

// LookupUser looks up a user by login or email using Grafana's global lookup API.
func (c *Client) LookupUser(loginOrEmail string) (*User, error) {
	q := url.QueryEscape(loginOrEmail)
	req, err := c.newRequest("GET", "/api/users/lookup?loginOrEmail="+q, nil)
	if err != nil {
		return nil, err
	}
	var user User
	if err := c.do(req, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

func (c *Client) GetFolderByTitle(title string) (*Folder, error) {
	req, err := c.newRequest("GET", "/api/folders", nil)
	if err != nil {
		return nil, err
	}
	var folders []Folder
	if err := c.do(req, &folders); err != nil {
		return nil, err
	}
	for _, f := range folders {
		if f.Title == title {
			return &f, nil
		}
	}
	return nil, fmt.Errorf("folder not found: %s", title)
}

// UpdateFolderPermissions replaces all permissions on a folder with the given
// team permissions. It uses the legacy /api/folders/:uid/permissions endpoint
// with integer permission values (1=View, 2=Edit, 4=Admin).
func (c *Client) UpdateFolderPermissions(folderUID string, perms []FolderPermission) error {
	permMap := map[string]int{"View": 1, "Edit": 2, "Admin": 4}
	type permItem struct {
		TeamID     int64 `json:"teamId"`
		Permission int   `json:"permission"`
	}
	items := make([]permItem, 0, len(perms))
	for _, p := range perms {
		v, ok := permMap[p.Permission]
		if !ok {
			v = 1
		}
		items = append(items, permItem{TeamID: p.TeamID, Permission: v})
	}
	path := fmt.Sprintf("/api/folders/%s/permissions", folderUID)
	payload := map[string]interface{}{
		"items": items,
	}
	req, err := c.newRequest("POST", path, payload)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

// ClearTeamCache removes a team from the in-memory cache, forcing the next
// GetTeamByName call to re-query the Grafana search API. Used when a cached
// team ID is discovered to be stale (phantom entry).
func (c *Client) ClearTeamCache(name string) {
	c.mu.Lock()
	delete(c.teamCache, name)
	c.mu.Unlock()
}

func (c *Client) GetTeam(teamID int64) (*Team, error) {
	path := fmt.Sprintf("/api/teams/%d", teamID)
	req, err := c.newRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	var team Team
	if err := c.do(req, &team); err != nil {
		return nil, err
	}
	return &team, nil
}
