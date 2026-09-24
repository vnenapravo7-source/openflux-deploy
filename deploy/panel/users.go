package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Role         string `json:"role"`
	PasswordHash string `json:"password_hash"`
}

type UserView struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type UserStore struct {
	mu    sync.Mutex
	path  string
	users []User
}

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,32}$`)

const longPasswordPrefix = "sha256:"

func passwordInput(password string) []byte {
	digest := sha256.Sum256([]byte(password))
	return []byte(base64.RawURLEncoding.EncodeToString(digest[:]))
}

func hashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("password cannot be empty")
	}
	input, prefix := []byte(password), ""
	if len(input) > 72 {
		input, prefix = passwordInput(password), longPasswordPrefix
	}
	hash, err := bcrypt.GenerateFromPassword(input, bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return prefix + string(hash), nil
}

func passwordMatches(stored, password string) bool {
	if strings.HasPrefix(stored, longPasswordPrefix) {
		return bcrypt.CompareHashAndPassword([]byte(strings.TrimPrefix(stored, longPasswordPrefix)), passwordInput(password)) == nil
	}
	return bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) == nil
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func writeJSONFile(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".openflux-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func loadUsers(path, adminName, adminPassword string) (*UserStore, error) {
	s := &UserStore{path: path, users: []User{}}
	raw, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(raw, &s.users); err != nil {
			return nil, fmt.Errorf("users file: %w", err)
		}
		if len(s.users) == 0 {
			return nil, fmt.Errorf("users file contains no administrator")
		}
		admins := 0
		for _, u := range s.users {
			if u.Role == "admin" {
				admins++
			}
		}
		if admins == 0 {
			return nil, fmt.Errorf("users file contains no administrator")
		}
		return s, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !usernamePattern.MatchString(adminName) || adminPassword == "" {
		return nil, fmt.Errorf("bootstrap admin requires a valid username and a non-empty password")
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	hash, err := hashPassword(adminPassword)
	if err != nil {
		return nil, err
	}
	s.users = []User{{ID: id, Username: adminName, Role: "admin", PasswordHash: hash}}
	if err := writeJSONFile(path, s.users); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *UserStore) adminID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.Role == "admin" {
			return u.ID
		}
	}
	return ""
}

func (s *UserStore) authenticate(username, password string) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if strings.EqualFold(u.Username, username) && passwordMatches(u.PasswordHash, password) {
			return u, true
		}
	}
	return User{}, false
}

func (s *UserStore) get(id string) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.ID == id {
			return u, true
		}
	}
	return User{}, false
}

func (s *UserStore) verifyPassword(id, password string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.ID == id {
			return passwordMatches(u.PasswordHash, password)
		}
	}
	return false
}

func (s *UserStore) list() []UserView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]UserView, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, UserView{u.ID, u.Username, u.Role})
	}
	return out
}

func (s *UserStore) add(username, password, role string) (UserView, error) {
	if !usernamePattern.MatchString(username) {
		return UserView{}, fmt.Errorf("username: 3–32 Latin letters, digits, dot, underscore or dash")
	}
	if password == "" {
		return UserView{}, fmt.Errorf("password cannot be empty")
	}
	if role != "admin" && role != "user" {
		return UserView{}, fmt.Errorf("role must be admin or user")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return UserView{}, err
	}
	id, err := newID()
	if err != nil {
		return UserView{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if strings.EqualFold(u.Username, username) {
			return UserView{}, fmt.Errorf("username already exists")
		}
	}
	user := User{ID: id, Username: username, Role: role, PasswordHash: hash}
	next := append(append([]User(nil), s.users...), user)
	if err := writeJSONFile(s.path, next); err != nil {
		return UserView{}, err
	}
	s.users = next
	return UserView{id, username, role}, nil
}

func (s *UserStore) changePassword(id, password string) error {
	if password == "" {
		return fmt.Errorf("password cannot be empty")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := append([]User(nil), s.users...)
	for i := range next {
		if next[i].ID == id {
			next[i].PasswordHash = hash
			if err := writeJSONFile(s.path, next); err != nil {
				return err
			}
			s.users = next
			return nil
		}
	}
	return os.ErrNotExist
}

func (s *UserStore) changeUsername(id, username string) error {
	if !usernamePattern.MatchString(username) {
		return fmt.Errorf("username: 3–32 Latin letters, digits, dot, underscore or dash")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i, u := range s.users {
		if u.ID == id {
			index = i
			continue
		}
		if strings.EqualFold(u.Username, username) {
			return fmt.Errorf("username already exists")
		}
	}
	if index < 0 {
		return os.ErrNotExist
	}
	next := append([]User(nil), s.users...)
	next[index].Username = username
	if err := writeJSONFile(s.path, next); err != nil {
		return err
	}
	s.users = next
	return nil
}

func (s *UserStore) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i, u := range s.users {
		if u.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return os.ErrNotExist
	}
	if s.users[index].Role == "admin" {
		admins := 0
		for _, u := range s.users {
			if u.Role == "admin" {
				admins++
			}
		}
		if admins <= 1 {
			return fmt.Errorf("cannot delete the last administrator")
		}
	}
	next := append([]User(nil), s.users[:index]...)
	next = append(next, s.users[index+1:]...)
	if err := writeJSONFile(s.path, next); err != nil {
		return err
	}
	s.users = next
	return nil
}
