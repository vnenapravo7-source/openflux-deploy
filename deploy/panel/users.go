package main

import (
	"crypto/rand"
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
	if !usernamePattern.MatchString(adminName) || len(adminPassword) < 12 {
		return nil, fmt.Errorf("bootstrap admin requires a valid username and password of at least 12 characters")
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	s.users = []User{{ID: id, Username: adminName, Role: "admin", PasswordHash: string(hash)}}
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
		if strings.EqualFold(u.Username, username) && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil {
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
	if len(password) < 12 {
		return UserView{}, fmt.Errorf("password must contain at least 12 characters")
	}
	if role != "admin" && role != "user" {
		return UserView{}, fmt.Errorf("role must be admin or user")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
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
	user := User{ID: id, Username: username, Role: role, PasswordHash: string(hash)}
	next := append(append([]User(nil), s.users...), user)
	if err := writeJSONFile(s.path, next); err != nil {
		return UserView{}, err
	}
	s.users = next
	return UserView{id, username, role}, nil
}

func (s *UserStore) changePassword(id, password string) error {
	if len(password) < 12 {
		return fmt.Errorf("password must contain at least 12 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := append([]User(nil), s.users...)
	for i := range next {
		if next[i].ID == id {
			next[i].PasswordHash = string(hash)
			if err := writeJSONFile(s.path, next); err != nil {
				return err
			}
			s.users = next
			return nil
		}
	}
	return os.ErrNotExist
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
