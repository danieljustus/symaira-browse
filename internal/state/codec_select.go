package state

// codec returns the cipher used for state files. The default store has no
// key source and therefore uses plaintext; a store configured with a
// KeyProvider (see crypto.go) transparently upgrades to AES-256-GCM without
// changing the file layout.
func (s *Store) codec() codec {
	if s.keys != nil {
		return &gcmCodec{keys: s.keys}
	}
	return plainCodec{}
}

type codec interface {
	// Encrypt returns the on-disk body for a plaintext payload.
	Encrypt(plaintext []byte) ([]byte, error)
	// Decrypt returns the plaintext payload for an on-disk body.
	Decrypt(body []byte) ([]byte, error)
}

// plainCodec stores payloads without encryption. It is the default for
// stores without a key source and keeps symbrowse fully usable standalone.
type plainCodec struct{}

func (plainCodec) Encrypt(plaintext []byte) ([]byte, error) { return plaintext, nil }
func (plainCodec) Decrypt(body []byte) ([]byte, error)      { return body, nil }
