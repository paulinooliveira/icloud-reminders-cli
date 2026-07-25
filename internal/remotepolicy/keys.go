// Package remotepolicy implements Hindsight-style bearer keys for remote MCP.
package remotepolicy

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

type Policy struct {
	ID      string   `json:"id"`
	KeyHash string   `json:"key_hash"`
	Lists   []string `json:"lists"`
	Write   bool     `json:"write"`
	Enabled bool     `json:"enabled"`
}

func (p Policy) AllowsList(list string) bool {
	for _, allowed := range p.Lists {
		if strings.EqualFold(strings.TrimSpace(allowed), strings.TrimSpace(list)) {
			return true
		}
	}
	return false
}

type document struct {
	Keys []Policy `json:"keys"`
}

type Store struct {
	path     string
	mu       sync.RWMutex
	keys     []Policy
	fileHash [sha256.Size]byte
	loaded   bool
}

func NewStore(path string) (*Store, error) {
	store := &Store{path: path}
	if err := store.reload(true); err != nil {
		return nil, err
	}
	return store, nil
}

func HashToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func (s *Store) Authenticate(token string) (Policy, bool) {
	if strings.TrimSpace(token) == "" {
		return Policy{}, false
	}
	if err := s.reload(false); err != nil {
		// Authorization state is security-critical: an unreadable, missing,
		// malformed, or newly-insecure key file revokes every key until fixed.
		s.mu.Lock()
		s.keys = nil
		s.fileHash = [sha256.Size]byte{}
		s.loaded = false
		s.mu.Unlock()
		return Policy{}, false
	}
	digest := sha256.Sum256([]byte(token))
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, key := range s.keys {
		decoded, err := hex.DecodeString(key.KeyHash)
		if err == nil && len(decoded) == len(digest) && subtle.ConstantTimeCompare(decoded, digest[:]) == 1 {
			if !key.Enabled {
				return Policy{}, false
			}
			return clonePolicy(key), true
		}
	}
	return Policy{}, false
}

func (s *Store) reload(initial bool) error {
	info, err := os.Stat(s.path)
	if err != nil {
		return fmt.Errorf("keys file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("keys file permissions must be 0600 or stricter")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read keys file: %w", err)
	}
	hash := sha256.Sum256(data)
	s.mu.RLock()
	unchanged := !initial && s.loaded && subtle.ConstantTimeCompare(hash[:], s.fileHash[:]) == 1
	s.mu.RUnlock()
	if unchanged {
		return nil
	}
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse keys file: %w", err)
	}
	keys, err := validate(doc.Keys)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.keys = keys
	s.fileHash = hash
	s.loaded = true
	s.mu.Unlock()
	return nil
}

func validate(keys []Policy) ([]Policy, error) {
	ids, hashes := map[string]bool{}, map[string]bool{}
	validated := make([]Policy, 0, len(keys))
	for i, key := range keys {
		key.ID = strings.TrimSpace(key.ID)
		key.KeyHash = strings.ToLower(strings.TrimSpace(key.KeyHash))
		if key.ID == "" {
			return nil, fmt.Errorf("keys[%d].id is required", i)
		}
		decoded, err := hex.DecodeString(key.KeyHash)
		if err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("keys[%d].key_hash must be SHA-256 hex", i)
		}
		if len(key.Lists) == 0 {
			return nil, fmt.Errorf("keys[%d].lists must not be empty", i)
		}
		seenLists := map[string]bool{}
		for j, list := range key.Lists {
			list = strings.TrimSpace(list)
			if list == "" {
				return nil, fmt.Errorf("keys[%d].lists[%d] is empty", i, j)
			}
			folded := strings.ToLower(list)
			if seenLists[folded] {
				return nil, fmt.Errorf("keys[%d].lists has duplicate %q", i, list)
			}
			seenLists[folded] = true
			key.Lists[j] = list
		}
		sort.Strings(key.Lists)
		if ids[key.ID] || hashes[key.KeyHash] {
			return nil, fmt.Errorf("duplicate key id or hash")
		}
		ids[key.ID], hashes[key.KeyHash] = true, true
		validated = append(validated, clonePolicy(key))
	}
	return validated, nil
}

func clonePolicy(policy Policy) Policy {
	policy.Lists = append([]string(nil), policy.Lists...)
	return policy
}
