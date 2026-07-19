package sessionidentity

import "testing"

func TestStableSessionNames(t *testing.T) {
	t.Parallel()
	const id = "018f9c6e-1234-7000-8000-abcdef012345"
	tests := map[string]string{
		"cr":  CRName(id),
		"pod": PodName(id),
		"pvc": PVCName(id),
		"sa":  ServiceAccountName(id),
	}
	for kind, name := range tests {
		if name == "" || len(name) > 63 {
			t.Fatalf("%s name = %q, want non-empty DNS label", kind, name)
		}
	}
	if got, want := CRName(id), "sess-"+ShortID(id); got != want {
		t.Fatalf("CRName() = %q, want %q", got, want)
	}
}
