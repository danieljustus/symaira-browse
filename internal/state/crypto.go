package state

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

const maxEncryptedPlaintextBytes = 64 << 20

// KeySource is a stable label for where the encryption key came from. It is
// surfaced in state show and stored with each state.
type KeySource string

const (
	KeySourceVault    KeySource = "symvault"
	KeySourceKeychain KeySource = "keychain"
	KeySourceEnv      KeySource = "environment"
	KeySourceNone     KeySource = "none"
)

// KeyProvider resolves the 32-byte AES-256 key for state encryption. The
// resolution order is fixed by the architecture: symvault, then OS keychain,
// then the SYMBROWSE_ENCRYPTION_KEY environment variable.
type KeyProvider interface {
	// Key returns the key and its source. A nil key with KeySourceNone means
	// "no key configured" (plaintext fallback). An error aborts the operation.
	Key() ([]byte, KeySource, error)
	// Source reports the currently resolved source without returning the key.
	Source() (KeySource, error)
}

// gcmCodec encrypts state payloads with AES-256-GCM. The on-disk body is
// nonce || ciphertext; the nonce is random per write.
type gcmCodec struct {
	keys KeyProvider
}

func (c *gcmCodec) Encrypt(plaintext []byte) ([]byte, error) {
	key, source, err := c.keys.Key()
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	if len(plaintext) > maxEncryptedPlaintextBytes {
		return nil, fmt.Errorf("plaintext exceeds maximum size of %d bytes", maxEncryptedPlaintextBytes)
	}
	out := make([]byte, 0, len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, nil)
	_ = source
	return out, nil
}

func (c *gcmCodec) Decrypt(body []byte) ([]byte, error) {
	key, _, err := c.keys.Key()
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	if len(body) < gcm.NonceSize() {
		return nil, errors.New("encrypted state file is truncated")
	}
	nonce, ciphertext := body[:gcm.NonceSize()], body[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt state file: %w", err)
	}
	return plaintext, nil
}
