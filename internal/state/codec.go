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

// encode serializes a state to its on-disk representation. The unencrypted
// metadata header precedes the newline delimiter, followed by the body
// encrypted by the codec (AES-256-GCM or plaintext fallback).
func (s *Store) encode(st *State) ([]byte, error) {
	header := stateHeader{
		SchemaVersion: st.SchemaVersion,
		SavedAt:       st.SavedAt,
		ExpiresAt:     st.ExpiresAt,
		KeySource:     st.KeySource,
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return nil, fmt.Errorf("marshal state header: %w", err)
	}

	payload, err := json.Marshal(st)
	if err != nil {
		return nil, fmt.Errorf("marshal state: %w", err)
	}
	body, err := s.codec().Encrypt(payload)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, len(fileMagic)+len(headerBytes)+1+len(body))
	out = append(out, fileMagic...)
	out = append(out, headerBytes...)
	out = append(out, '\n')
	out = append(out, body...)
	return out, nil
}

// decode parses a state file, validating the magic prefix, parsing the
// unencrypted header for v2+ format, and delegating payload decryption to the
// codec. Legacy v1 files (no header newline, or v1 schema) are decrypted
// directly for backward compatibility.
func (s *Store) decode(raw []byte) (*State, error) {
	if !bytes.HasPrefix(raw, fileMagic) {
		return nil, errors.New("file is not a symbrowse state file")
	}
	data := raw[len(fileMagic):]

	// Check for v2+ format with unencrypted header.
	if newlineIdx := bytes.IndexByte(data, '\n'); newlineIdx != -1 {
		var hdr stateHeader
		if err := json.Unmarshal(data[:newlineIdx], &hdr); err == nil && hdr.SchemaVersion >= 2 {
			body := data[newlineIdx+1:]
			var payload []byte
			if hdr.KeySource == string(KeySourceNone) || hdr.KeySource == "" {
				payload = body
			} else {
				var err error
				payload, err = s.codec().Decrypt(body)
				if err != nil {
					return nil, err
				}
			}
			var st State
			if err := json.Unmarshal(payload, &st); err != nil {
				return nil, fmt.Errorf("parse state payload: %w", err)
			}
			st.SchemaVersion = hdr.SchemaVersion
			st.SavedAt = hdr.SavedAt
			st.ExpiresAt = hdr.ExpiresAt
			st.KeySource = hdr.KeySource
			return &st, nil
		}
	}

	// Legacy v1 format:
	if s.keys != nil {
		payload, err := s.codec().Decrypt(data)
		if err == nil {
			var st State
			if unmarshalErr := json.Unmarshal(payload, &st); unmarshalErr == nil {
				if st.SchemaVersion == 0 {
					st.SchemaVersion = 1
				}
				return &st, nil
			}
		}
		// Check if it's a legacy v1 plaintext file loaded by an encrypted store.
		var st State
		if jsonErr := json.Unmarshal(data, &st); jsonErr == nil && (st.SchemaVersion == 1 || st.SchemaVersion == 0) {
			if st.SchemaVersion == 0 {
				st.SchemaVersion = 1
			}
			return &st, nil
		}
		if err != nil {
			return nil, err
		}
	}

	payload, err := s.codec().Decrypt(data)
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(payload, &st); err != nil {
		return nil, fmt.Errorf("parse state payload: %w", err)
	}
	if st.SchemaVersion == 0 {
		st.SchemaVersion = 1
	}
	return &st, nil
}
