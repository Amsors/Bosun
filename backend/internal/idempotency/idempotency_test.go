package idempotency

import (
	"bytes"
	"testing"
)

func TestRequestHashStableAndSensitive(t *testing.T) {
	a := RequestHash("POST", "/api/v1/auth/register", []byte(`{"email":"a@b.c"}`))
	b := RequestHash("POST", "/api/v1/auth/register", []byte(`{"email":"a@b.c"}`))
	if !bytes.Equal(a, b) {
		t.Fatal("RequestHash not stable for identical inputs")
	}
	if bytes.Equal(a, RequestHash("POST", "/api/v1/auth/register", []byte(`{"email":"x@b.c"}`))) {
		t.Fatal("RequestHash collides for different bodies")
	}
	if bytes.Equal(a, RequestHash("PUT", "/api/v1/auth/register", []byte(`{"email":"a@b.c"}`))) {
		t.Fatal("RequestHash collides for different methods")
	}
	// 分隔符防止字段边界歧义：method+path 拼接不应与另一种切分相同。
	if bytes.Equal(
		RequestHash("POST", "/a", []byte("b")),
		RequestHash("POST", "/", []byte("ab")),
	) {
		t.Fatal("RequestHash ambiguous across field boundaries")
	}
}

func TestDecide(t *testing.T) {
	hash := RequestHash("POST", "/x", []byte("body"))

	if got := Decide(nil, hash); got != Proceed {
		t.Fatalf("Decide(nil) = %v, want Proceed", got)
	}
	if got := Decide(&Record{RequestHash: hash}, hash); got != Replay {
		t.Fatalf("Decide(match) = %v, want Replay", got)
	}
	if got := Decide(&Record{RequestHash: []byte("other")}, hash); got != Conflict {
		t.Fatalf("Decide(mismatch) = %v, want Conflict", got)
	}
}
