package apierr

import "testing"

type writer struct {
	status int
	body   any
}

func (w *writer) JSON(status int, body any) {
	w.status = status
	w.body = body
}

func TestWrite(t *testing.T) {
	w := &writer{}
	Write(w, InvalidArgument)
	if w.status != InvalidArgument.HTTPStatus {
		t.Fatalf("status = %d, want %d", w.status, InvalidArgument.HTTPStatus)
	}
	envelope, ok := w.body.(map[string]any)
	if !ok || envelope["code"] != InvalidArgument.Code {
		t.Fatalf("unexpected body: %#v", w.body)
	}
}
