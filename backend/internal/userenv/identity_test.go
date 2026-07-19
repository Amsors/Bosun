package userenv

import (
	"regexp"
	"testing"
)

func TestNamespaceMatchesCRDPattern(t *testing.T) {
	// 必须匹配 UserEnvironment.spec.namespace 的冻结校验模式。
	pattern := regexp.MustCompile(`^bosun-u-[a-z0-9]{8,16}$`)
	ns := Namespace("018f9c6e-1234-7000-8000-abcdef012345")
	if !pattern.MatchString(ns) {
		t.Fatalf("namespace %q does not match CRD pattern", ns)
	}
}

func TestShortIDStableAndDistinct(t *testing.T) {
	a := ShortID("018f9c6e-1234-7000-8000-abcdef012345")
	if a != ShortID("018f9c6e-1234-7000-8000-abcdef012345") {
		t.Fatal("ShortID not stable for same user")
	}
	if a == ShortID("018f9c6e-1234-7000-8000-abcdef012346") {
		t.Fatal("ShortID collides for distinct users")
	}
	if CRName("u") != "usr-"+ShortID("u") {
		t.Fatal("CRName inconsistent with ShortID")
	}
}
