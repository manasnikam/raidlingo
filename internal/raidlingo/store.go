package raidlingo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Token struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    int64    `json:"expiresAt"`
	UserID       string   `json:"userId"`
	Login        string   `json:"login"`
	Scopes       []string `json:"scopes"`
}
type tokenDocument struct {
	Version int              `json:"version"`
	Seats   map[string]Token `json:"seats"`
}
type TokenStore struct {
	mu      sync.Mutex
	path    string
	records map[string]Token
}

func OpenTokenStore(path string) (*TokenStore, error) {
	s := &TokenStore{path: path, records: map[string]Token{}}
	data, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return s, nil
	}
	if e != nil {
		return nil, e
	}
	var doc tokenDocument
	if json.Unmarshal(data, &doc) != nil || doc.Version != 1 || doc.Seats == nil {
		return nil, errors.New("invalid token store; move it aside and reconnect accounts")
	}
	for slot, t := range doc.Seats {
		if slot != "A" && slot != "B" && slot != "bot" {
			continue
		}
		if t.AccessToken == "" || t.RefreshToken == "" || t.UserID == "" || t.Login == "" || t.ExpiresAt < 0 || t.Scopes == nil {
			return nil, fmt.Errorf("invalid token record for %s", slot)
		}
		s.records[slot] = t
	}
	if e = os.Chmod(path, 0600); e != nil {
		return nil, e
	}
	return s, nil
}
func (s *TokenStore) Get(slot string) (Token, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.records[slot]
	t.Scopes = append([]string{}, t.Scopes...)
	return t, ok
}
func (s *TokenStore) Set(slot string, t Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(slot, t)
}
func (s *TokenStore) Replace(slot string, old, next Token) (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.records[slot]
	if current.AccessToken != old.AccessToken || current.RefreshToken != old.RefreshToken {
		return current, nil
	}
	if e := s.write(slot, next); e != nil {
		return Token{}, e
	}
	return next, nil
}
func (s *TokenStore) write(slot string, t Token) error {
	next := cloneMap(s.records)
	next[slot] = t
	data, e := json.MarshalIndent(tokenDocument{1, next}, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(s.path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(s.path), ".tokens-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(f.Name(), s.path); e != nil {
		return e
	}
	s.records = next
	return nil
}
