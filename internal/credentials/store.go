package credentials

import (
	"context"
	"fmt"

	"research-scout/internal/storage"
)

// Store persists encrypted credential bytes in zotero_connections. The
// plaintext secret is encrypted with the workspace-independent Cipher before it
// ever reaches PostgreSQL.
type Store struct {
	Store  *storage.Store
	Cipher *Cipher
}

// NewStore derives a Cipher from secret and wraps the store.
func NewStore(store *storage.Store, secret string) (*Store, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	cipher, err := NewCipher(secret)
	if err != nil {
		return nil, err
	}
	return &Store{Store: store, Cipher: cipher}, nil
}

// Put encrypts and upserts the credential for a connection.
func (s *Store) Put(ctx context.Context, workspaceID, connectionID string, secret []byte) error {
	if s == nil || s.Store == nil || s.Cipher == nil {
		return fmt.Errorf("credential store is not initialized")
	}
	encoded, err := s.Cipher.Encrypt(secret)
	if err != nil {
		return err
	}
	tag, err := s.Store.Pool().Exec(ctx, `
		UPDATE zotero_connections
		SET encrypted_credential=$1, credential_key_version=$2, updated_at=now()
		WHERE id=$3 AND workspace_id=$4
	`, encoded, "v1", connectionID, workspaceID)
	if err != nil {
		return fmt.Errorf("persist credential: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("connection not found: %s", connectionID)
	}
	return nil
}

// Get returns decrypted credential bytes.
func (s *Store) Get(ctx context.Context, workspaceID, connectionID string) ([]byte, error) {
	if s == nil || s.Store == nil || s.Cipher == nil {
		return nil, fmt.Errorf("credential store is not initialized")
	}
	var encoded string
	if err := s.Store.Pool().QueryRow(ctx, `
		SELECT COALESCE(encrypted_credential,'') FROM zotero_connections
		WHERE id=$1 AND workspace_id=$2
	`, connectionID, workspaceID).Scan(&encoded); err != nil {
		return nil, err
	}
	if encoded == "" {
		return nil, fmt.Errorf("credential not found")
	}
	return s.Cipher.Decrypt(encoded)
}

// Delete removes any stored credential for a connection.
func (s *Store) Delete(ctx context.Context, workspaceID, connectionID string) error {
	if s == nil || s.Store == nil {
		return fmt.Errorf("credential store is not initialized")
	}
	_, err := s.Store.Pool().Exec(ctx, `
		UPDATE zotero_connections
		SET encrypted_credential=NULL, credential_key_version=NULL, updated_at=now()
		WHERE id=$1 AND workspace_id=$2
	`, connectionID, workspaceID)
	return err
}
