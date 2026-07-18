package envelope

import "testing"

type writer struct {
	status int
	body   any
}

func (w *writer) JSON(status int, body any) {
	w.status = status
	w.body = body
}

func TestOK(t *testing.T) {
	w := &writer{}
	OK(w, map[string]string{"status": "ok"})
	got, ok := w.body.(Response[map[string]string])
	if !ok || got.Code != 0 || got.Data["status"] != "ok" {
		t.Fatalf("unexpected body: %#v", w.body)
	}
}
