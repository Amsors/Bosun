package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	p := DefaultArgon2Params()
	// 用更小的参数加速测试，同时保持逻辑一致。
	p.Memory = 8 * 1024
	p.Iterations = 1

	encoded, err := HashPassword("correct horse battery staple", p)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$") {
		t.Fatalf("encoded hash missing prefix: %q", encoded)
	}

	ok, err := VerifyPassword("correct horse battery staple", encoded)
	if err != nil {
		t.Fatalf("VerifyPassword() error = %v", err)
	}
	if !ok {
		t.Fatal("VerifyPassword() = false, want true for matching password")
	}

	ok, err = VerifyPassword("wrong password", encoded)
	if err != nil {
		t.Fatalf("VerifyPassword() error = %v", err)
	}
	if ok {
		t.Fatal("VerifyPassword() = true, want false for wrong password")
	}
}

func TestHashPasswordUsesRandomSalt(t *testing.T) {
	p := DefaultArgon2Params()
	p.Memory = 8 * 1024
	p.Iterations = 1
	a, err := HashPassword("same", p)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	b, err := HashPassword("same", p)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if a == b {
		t.Fatal("two hashes of the same password are identical; salt is not random")
	}
}

func TestVerifyPasswordRejectsMalformed(t *testing.T) {
	for _, encoded := range []string{
		"",
		"not-a-hash",
		"$argon2id$v=19$m=8$only",
		"$bcrypt$v=19$m=8,t=1,p=1$c2FsdA$aGFzaA",
	} {
		if _, err := VerifyPassword("x", encoded); err == nil {
			t.Fatalf("VerifyPassword(%q) expected error", encoded)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	weak := DefaultArgon2Params()
	weak.Memory = 8 * 1024
	weak.Iterations = 1
	encoded, err := HashPassword("pw", weak)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	if !NeedsRehash(encoded, DefaultArgon2Params()) {
		t.Fatal("NeedsRehash() = false, want true when stored params are weaker")
	}
	if NeedsRehash(encoded, weak) {
		t.Fatal("NeedsRehash() = true, want false when stored params match current")
	}
	if !NeedsRehash("garbage", DefaultArgon2Params()) {
		t.Fatal("NeedsRehash() = false, want true for unparseable hash")
	}
}
