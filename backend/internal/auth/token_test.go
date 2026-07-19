package auth

import (
	"bytes"
	"testing"
)

func TestNewRefreshTokenUniqueAndHashed(t *testing.T) {
	raw1, hash1, err := NewRefreshToken()
	if err != nil {
		t.Fatalf("NewRefreshToken() error = %v", err)
	}
	raw2, hash2, err := NewRefreshToken()
	if err != nil {
		t.Fatalf("NewRefreshToken() error = %v", err)
	}
	if raw1 == raw2 {
		t.Fatal("two refresh tokens are identical; not random")
	}
	if len(hash1) != 32 {
		t.Fatalf("hash length = %d, want 32", len(hash1))
	}
	if bytes.Equal(hash1, hash2) {
		t.Fatal("distinct tokens produced identical hashes")
	}
	if !bytes.Equal(hash1, HashRefreshToken(raw1)) {
		t.Fatal("HashRefreshToken is not deterministic for the same input")
	}
}
