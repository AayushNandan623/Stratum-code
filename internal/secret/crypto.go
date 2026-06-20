// Secret encryption: AES-256-GCM with a per-org key derived from the master
// key via HKDF-SHA256. Each org's derived key is independent, so compromising
// one org's key does not affect others. Stored blobs are nonce(12) || ciphertext.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/crypto/hkdf"
)

// Crypto encrypts and decrypts secret values using org-derived keys.
type Crypto struct {
	masterKey []byte
	cache     sync.Map // map[uuid.UUID][]byte of derived keys
}

// NewCrypto decodes the hex-encoded 32-byte master key and returns a Crypto.
func NewCrypto(hexKey string) (*Crypto, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("secret: decode encryption key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secret: encryption key must be 32 bytes, got %d", len(key))
	}
	return &Crypto{masterKey: key}, nil
}

// deriveKey returns the 32-byte AES key for an org, deriving and caching it on
// first use. HKDF salt is the org ID; info is "secrets".
func (c *Crypto) deriveKey(orgID uuid.UUID) ([]byte, error) {
	if k, ok := c.cache.Load(orgID); ok {
		return k.([]byte), nil
	}
	derived := make([]byte, 32)
	r := hkdf.New(sha256.New, c.masterKey, orgID[:], []byte("secrets"))
	if _, err := io.ReadFull(r, derived); err != nil {
		return nil, fmt.Errorf("secret: derive key: %w", err)
	}
	c.cache.Store(orgID, derived)
	return derived, nil
}

// Encrypt seals plaintext under the org's key with the secret name as
// additional authenticated data. Returns nonce || ciphertext.
func (c *Crypto) Encrypt(orgID uuid.UUID, name string, plaintext []byte) ([]byte, error) {
	key, err := c.deriveKey(orgID)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plaintext, []byte(name))
	return append(nonce, sealed...), nil
}

// Decrypt opens a nonce || ciphertext blob. The name must match the one used at
// encryption time (it is the additional authenticated data).
func (c *Crypto) Decrypt(orgID uuid.UUID, name string, blob []byte) ([]byte, error) {
	key, err := c.deriveKey(orgID)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(blob) < ns+1 {
		return nil, fmt.Errorf("secret: ciphertext too short")
	}
	nonce, ciphertext := blob[:ns], blob[ns:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(name))
	if err != nil {
		return nil, fmt.Errorf("secret: decrypt failed")
	}
	return plaintext, nil
}
