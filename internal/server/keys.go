package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
)

// Key is one agent identity. Only the sha256 of the secret is stored.
type Key struct {
	Name    string   `json:"name"`
	Hash    string   `json:"hash"`
	Owner   bool     `json:"owner,omitempty"`
	Approve bool     `json:"approve,omitempty"` // may apply/reject agent branches on the owner's word
	Read    []string `json:"read"`              // path prefixes; "*" means everything
}

// CanDecide reports whether the key may apply or reject agent branches.
func (k *Key) CanDecide() bool { return k.Owner || k.Approve }

// CanRead reports whether rel falls under one of the key's read prefixes.
func (k *Key) CanRead(rel string) bool {
	if k.Owner {
		return true
	}
	for _, p := range k.Read {
		if p == "*" || rel == strings.TrimSuffix(p, "/") || strings.HasPrefix(rel, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

type KeyFile struct {
	Path string
	mu   sync.RWMutex
	keys []Key
}

func LoadKeys(path string) (*KeyFile, error) {
	kf := &KeyFile{Path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return kf, nil
	}
	if err != nil {
		return nil, err
	}
	return kf, json.Unmarshal(b, &kf.keys)
}

func hashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func (kf *KeyFile) Lookup(secret string) *Key {
	h := hashSecret(secret)
	kf.mu.RLock()
	defer kf.mu.RUnlock()
	for i := range kf.keys {
		if subtle.ConstantTimeCompare([]byte(kf.keys[i].Hash), []byte(h)) == 1 {
			k := kf.keys[i]
			return &k
		}
	}
	return nil
}

// Add creates or replaces the key for name and returns the new secret.
func (kf *KeyFile) Add(name string, owner, approve bool, read []string) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	secret := "ask_" + hex.EncodeToString(b)
	kf.mu.Lock()
	defer kf.mu.Unlock()
	out := kf.keys[:0]
	for _, k := range kf.keys {
		if k.Name != name {
			out = append(out, k)
		}
	}
	kf.keys = append(out, Key{Name: name, Hash: hashSecret(secret), Owner: owner, Approve: approve, Read: read})
	data, _ := json.MarshalIndent(kf.keys, "", "  ")
	return secret, os.WriteFile(kf.Path, data, 0o600)
}

// Revoke removes the key for name; it reports whether one existed.
func (kf *KeyFile) Revoke(name string) (bool, error) {
	kf.mu.Lock()
	defer kf.mu.Unlock()
	out := kf.keys[:0]
	found := false
	for _, k := range kf.keys {
		if k.Name == name {
			found = true
			continue
		}
		out = append(out, k)
	}
	kf.keys = out
	data, _ := json.MarshalIndent(kf.keys, "", "  ")
	return found, os.WriteFile(kf.Path, data, 0o600)
}

// OwnerHash returns the owner key's hash. It is stable for a given key, so it
// can seed session cookies without ever holding the secret itself.
func (kf *KeyFile) OwnerHash() []byte {
	kf.mu.RLock()
	defer kf.mu.RUnlock()
	for _, k := range kf.keys {
		if k.Owner {
			return []byte(k.Hash)
		}
	}
	return []byte("no-owner-key")
}

func (kf *KeyFile) List() []Key {
	kf.mu.RLock()
	defer kf.mu.RUnlock()
	return append([]Key(nil), kf.keys...)
}
