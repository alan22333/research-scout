// Package credentials provides envelope-free symmetric encryption for Zotero
// credentials held by the server. Plaintext only exists in process memory and
// never in the database, logs, or fixtures.
package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// versionPrefix identifies the on-disk ciphertext format so the format can be
// rotated later without ambiguity. It is deliberately not part of the base64
// payload so an accidental copy-paste is easy to spot.
const versionPrefix = "v1:"

// Cipher encrypts and decrypts short-lived credential secrets with AES-256-GCM.
// The key is derived from the operator-supplied secret with SHA-256.
type Cipher struct {
	aead cipher.AEAD
}

// ErrCiphertextInvalid is returned when the ciphertext is malformed or cannot
// be authenticated with the current key.
var ErrCiphertextInvalid = errors.New("credential ciphertext is invalid")

// NewCipher derives an AES-256-GCM cipher from secret. The secret must not be
// empty. In production this value comes from the SECRET_KEY environment
// variable or a permission-restricted local file.
func NewCipher(secret string) (*Cipher, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("credential secret is required")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt returns a base64 version-prefixed string. The nonce is prepended to
// the ciphertext; AES-GCM provides authentication so a wrong key or tampered
// payload fails closed.
func (c *Cipher) Encrypt(plaintext []byte) (string, error) {
	if c == nil || c.aead == nil {
		return "", errors.New("cipher is not initialized")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := c.aead.Seal(nil, nonce, plaintext, nil)
	payload := append(append([]byte(nil), nonce...), sealed...)
	return versionPrefix + base64.RawStdEncoding.EncodeToString(payload), nil
}

// Decrypt reverses Encrypt. A wrong key or corrupted payload returns an error
// and never partial plaintext.
func (c *Cipher) Decrypt(encoded string) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, errors.New("cipher is not initialized")
	}
	encoded = strings.TrimSpace(encoded)
	if !strings.HasPrefix(encoded, versionPrefix) {
		return nil, ErrCiphertextInvalid
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(encoded, versionPrefix))
	if err != nil {
		return nil, ErrCiphertextInvalid
	}
	nonceSize := c.aead.NonceSize()
	if len(raw) < nonceSize {
		return nil, ErrCiphertextInvalid
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrCiphertextInvalid
	}
	return plaintext, nil
}
