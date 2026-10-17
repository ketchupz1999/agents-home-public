package server

import "github.com/ketchupz1999/agents-home-public/internal/store"

func (k *Key) readScopes() []string {
	if k.Owner {
		return []string{"*"}
	}
	if k.Read == nil {
		return []string{}
	}
	return k.Read
}

// Model parsing must not read a broader tree than the requesting identity.
// Enumeration stays internal; only successfully authorized reads become nodes.
type authorizedSource struct {
	*store.Store
	key *Key
}

func (a authorizedSource) Read(rel string) ([]byte, error) {
	if !a.key.CanRead(rel) {
		return nil, store.ErrNotFound
	}
	b, _, err := a.Store.ReadScopedWithSHA(rel, a.key.readScopes())
	return b, err
}
