package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"disabled defaults", defaults(), true},
		{"yandex needs url", Config{Enabled: true, Transport: "yandex", Mode: "l3", Codec: "batched"}, false},
		{"yandex url", Config{Enabled: true, Transport: "yandex", URL: "https://docs.yandex.ru/docs/view", Mode: "l3", Codec: "batched"}, true},
		{"cups no url", Config{Enabled: true, Transport: "cupsonline", Mode: "l4", Codec: "legacy"}, true},
		{"bad scheme", Config{Enabled: true, Transport: "mailru", URL: "file:///etc/passwd", Mode: "l3", Codec: "batched"}, false},
		{"unknown transport", Config{Transport: "other", Mode: "l3", Codec: "batched"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfig(tc.cfg)
			if (err == nil) != tc.ok {
				t.Fatalf("validateConfig() error = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestUsersBootstrapAndPasswordHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	store, err := loadUsers(path, "admin", "long-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.authenticate("admin", "long-test-password"); !ok {
		t.Fatal("admin cannot log in")
	}
	if _, ok := store.authenticate("admin", "wrong-password"); ok {
		t.Fatal("wrong password accepted")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "long-test-password") {
		t.Fatal("plaintext password was persisted")
	}
}

func TestConnectionIsolationAndCupsCode(t *testing.T) {
	owner, other := User{ID: "owner", Role: "user"}, User{ID: "other", Role: "user"}
	id := "first"
	m := &Manager{connections: []Connection{{ID: id, OwnerID: owner.ID, Name: "Cups", Config: Config{Transport: "cupsonline", Mode: "l4", Codec: "batched"}}, {ID: "second", OwnerID: other.ID, Name: "Other", Config: defaults()}}, processes: map[string]*processState{id: {}, "second": {}}}
	code := base64.RawURLEncoding.EncodeToString([]byte(`["12345678-1234-1234-1234-123456789abc"]`))
	m.logLocked(id, "=== COPY THIS TO CLIENT ===")
	m.logLocked(id, code)
	views := m.connectionViews(owner)
	if len(views) != 1 || views[0].ID != id || views[0].ClientCode != code {
		t.Fatalf("owner view: %+v", views)
	}
	if got := m.connectionViews(other); len(got) != 1 || got[0].ClientCode != "" {
		t.Fatalf("other view: %+v", got)
	}
	if got := m.connectionViews(User{Role: "admin"}); len(got) != 2 {
		t.Fatalf("admin sees %d connections", len(got))
	}
}

func TestStateRequiresSessionAndHidesOtherConnections(t *testing.T) {
	owner, other := User{ID: "owner", Username: "owner", Role: "user"}, User{ID: "other", Username: "other", Role: "user"}
	m := &Manager{connections: []Connection{{ID: "one", OwnerID: owner.ID, Name: "private", Config: defaults()}, {ID: "two", OwnerID: other.ID, Name: "hidden", Config: defaults()}}, processes: map[string]*processState{"one": {}, "two": {}}, nodes: []Node{}, traffic: []TrafficPoint{}}
	s := &server{mgr: m, users: &UserStore{users: []User{owner, other}}, sessions: map[string]session{"valid": {userID: owner.ID, expires: time.Now().Add(time.Hour)}}, attempts: map[string]loginAttempt{}}
	handler := s.auth(http.HandlerFunc(s.api))
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status %d", unauthorized.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	request.AddCookie(&http.Cookie{Name: "of_session", Value: "valid"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d", response.Code)
	}
	var result struct {
		Connections []ConnectionView `json:"connections"`
		Nodes       json.RawMessage  `json:"nodes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Connections) != 1 || result.Connections[0].ID != "one" || result.Nodes != nil {
		t.Fatalf("unfiltered response: %s", response.Body.String())
	}
	otherRestart := httptest.NewRequest(http.MethodPost, "/api/connections/two/restart", nil)
	otherRestart.AddCookie(&http.Cookie{Name: "of_session", Value: "valid"})
	otherRestart.Header.Set("X-OpenFlux-Action", "1")
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, otherRestart)
	if denied.Code != http.StatusNotFound { t.Fatalf("other user's connection action: %d", denied.Code) }
	usersRequest := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	usersRequest.AddCookie(&http.Cookie{Name: "of_session", Value: "valid"})
	usersResponse := httptest.NewRecorder()
	handler.ServeHTTP(usersResponse, usersRequest)
	if usersResponse.Code != http.StatusForbidden { t.Fatalf("user listing: %d", usersResponse.Code) }
}

func TestLegacyConnectionMigration(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "config.json")
	connections := filepath.Join(dir, "connections.json")
	old := Config{Transport: "yandex", URL: "https://docs.yandex.ru/example", Mode: "l4", Codec: "batched", AutoUpdate: true}
	if err := writeJSONFile(legacy, old); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(legacy, connections, filepath.Join(dir, "nodes.json"), "missing-binary", filepath.Join(dir, "version"), "missing-updater", "admin-id")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.connections) != 1 || m.connections[0].OwnerID != "admin-id" || m.connections[0].URL != old.URL {
		t.Fatalf("migration: %+v", m.connections)
	}
}

func TestValidateNode(t *testing.T) {
	n := Node{Name: "edge-1", BaseURL: "https://node.example:8088", Token: strings.Repeat("a", 32), TLSSHA256: strings.Repeat("ab", 32)}
	if err := validateNode(n); err != nil {
		t.Fatal(err)
	}
	n.BaseURL = "http://node.example:8088"
	if err := validateNode(n); err == nil {
		t.Fatal("expected HTTPS validation error")
	}
}
