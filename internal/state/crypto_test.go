package state

import (
	"bytes"
	"strings"
	"testing"
)

func TestGCMCodecRejectsOversizedPlaintext(t *testing.T) {
	codec := &gcmCodec{keys: &fakeKeyProvider{key: testKey(), source: KeySourceEnv}}
	plaintext := bytes.Repeat([]byte("x"), maxEncryptedPlaintextBytes+1)
	if _, err := codec.Encrypt(plaintext, nil); err == nil || !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Fatalf("oversized plaintext error = %v", err)
	}
}
