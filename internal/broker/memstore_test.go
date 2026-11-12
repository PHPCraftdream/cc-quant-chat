package broker

import (
	"sync"

	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// memStore keeps records like a database would, so a second broker can load them, and can be
// told to fail.
type memStore struct {
	memSecrets    // the broker's halves of protected keys (store.NickSecrets)
	store.Invites // only ChatName is answered (a chat is known once it has a record); an invitation needs the database

	mu        sync.Mutex
	recs      map[string][]store.Record
	loadErr   error
	appendErr error
	owner     []byte
	settings  map[string]map[string]string
	admin     []store.AdminEntry
}

// ChatName: the name of a chat that this store has records of.
func (s *memStore) ChatName(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.recs[id]) > 0 {
		return "team", nil
	}
	return "", nil
}

func newMemStore() *memStore { return &memStore{recs: map[string][]store.Record{}} }

func (s *memStore) Load(id, _ string) ([]store.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return append([]store.Record(nil), s.recs[id]...), nil
}

func (s *memStore) Append(id string, r store.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendErr != nil {
		return s.appendErr
	}
	if r.T == "msg" && r.ClientID != "" { // like the database's unique index
		for _, old := range s.recs[id] {
			if old.T == "msg" && old.ClientID == r.ClientID && old.Msg.From == r.Msg.From {
				return &store.DuplicateError{ID: old.Msg.ID}
			}
		}
	}
	s.recs[id] = append(s.recs[id], r)
	return nil
}

// The owner key, as the database keeps it.
func (s *memStore) Owner() ([]byte, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.owner, "2026-11-11T12:00:00Z", s.owner != nil, nil
}

func (s *memStore) SetOwner(pub []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != nil {
		return store.ErrOwnerExists
	}
	s.owner = append([]byte(nil), pub...)
	return nil
}

func (s *memStore) ReplaceOwner(old, pub []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if string(s.owner) != string(old) {
		return store.ErrOwnerChanged
	}
	s.owner = append([]byte(nil), pub...)
	return nil
}

// Settings and the administrator log, as the database keeps them.
func (s *memStore) Settings() (map[string]map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]map[string]string{}
	for scope, kv := range s.settings {
		out[scope] = map[string]string{}
		for k, v := range kv {
			out[scope][k] = v
		}
	}
	return out, nil
}

func (s *memStore) SetSetting(scope, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		s.settings = map[string]map[string]string{}
	}
	if s.settings[scope] == nil {
		s.settings[scope] = map[string]string{}
	}
	s.settings[scope][key] = value
	return nil
}

func (s *memStore) LogAdmin(e store.AdminEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admin = append(s.admin, e)
	return nil
}

func (s *memStore) fail(load, appendErr error) {
	s.mu.Lock()
	s.loadErr, s.appendErr = load, appendErr
	s.mu.Unlock()
}
