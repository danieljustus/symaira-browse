package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// fileMagic prefixes every state file so the format is self-describing and
// corrupt or foreign files are rejected early.
var fileMagic = []byte("SYMBROWSE-STATE\x00")

// encode serializes a state to its on-disk representation. The concrete
// cipher is provided by the codec, which may be plaintext (no key) or
// AES-256-GCM (see crypto.go).
func (s *Store) encode(st *State) ([]byte, error) {
	payload, err := json.Marshal(st)
	if err != nil {
		return nil, fmt.Errorf("marshal state: %w", err)
	}
	body, err := s.codec().Encrypt(payload)
	if err != nil {
		return nil, err
	}
	out := append([]byte{}, fileMagic...)
	out = append(out, body...)
	return out, nil
}

// decode parses a state file, validating the magic prefix and delegating the
// payload decryption to the codec.
func (s *Store) decode(raw []byte) (*State, error) {
	if !bytes.HasPrefix(raw, fileMagic) {
		return nil, errors.New("file is not a symbrowse state file")
	}
	payload, err := s.codec().Decrypt(raw[len(fileMagic):])
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(payload, &st); err != nil {
		return nil, fmt.Errorf("parse state payload: %w", err)
	}
	if st.SchemaVersion == 0 {
		st.SchemaVersion = SchemaVersion
	}
	return &st, nil
}
