package terminal

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/Amsors/Bosun/backend/internal/auth"
	"github.com/Amsors/Bosun/backend/internal/session"
)

func TestHandlerProxiesReplayInputResizeAndActivity(t *testing.T) {
	handler, token, record, runtime := newHandlerFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler.ServeTerminal(writer, request, record.ID.String())
	}))
	defer server.Close()

	connection := dialTerminal(t, server.URL, token)
	defer func() { _ = connection.Close() }()
	var replay Frame
	if err := connection.ReadJSON(&replay); err != nil {
		t.Fatalf("read replay: %v", err)
	}
	if replay.Type != "stdout" || string(decodeFrame(t, replay)) != "captured" {
		t.Fatalf("replay = %#v", replay)
	}
	if runtime.activityCount() != 0 {
		t.Fatalf("stdout replay refreshed activity %d times", runtime.activityCount())
	}

	if err := connection.WriteJSON(Frame{Type: "resize", Data: `{"cols":120,"rows":32}`}); err != nil {
		t.Fatalf("write resize: %v", err)
	}
	if err := connection.WriteJSON(Frame{
		Type: "stdin", Data: base64.StdEncoding.EncodeToString([]byte("pwd\n")),
	}); err != nil {
		t.Fatalf("write stdin: %v", err)
	}

	var live Frame
	if err := connection.ReadJSON(&live); err != nil {
		t.Fatalf("read live output: %v", err)
	}
	if string(decodeFrame(t, live)) != "live" {
		t.Fatalf("live frame = %#v", live)
	}
	select {
	case resize := <-runtime.resizes:
		if resize.Width != 120 || resize.Height != 32 {
			t.Fatalf("resize = %#v", resize)
		}
	case <-time.After(time.Second):
		t.Fatal("resize was not forwarded")
	}
	if runtime.activityCount() != 1 {
		t.Fatalf("activity updates = %d, want throttled to 1", runtime.activityCount())
	}
}

func TestHandlerRejectsCrossUserBeforeUpgrade(t *testing.T) {
	handler, _, record, _ := newHandlerFixture(t)
	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	otherIssuer, err := auth.NewJWTIssuer("bosun", privateKey, time.Minute)
	if err != nil {
		t.Fatalf("NewJWTIssuer() error = %v", err)
	}
	handler.jwt = otherIssuer
	otherUser, _ := uuid.NewV7()
	token, err := otherIssuer.Sign(otherUser.String(), time.Now().UTC())
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler.ServeTerminal(writer, request, record.ID.String())
	}))
	defer server.Close()

	header := http.Header{"Sec-WebSocket-Protocol": {
		Subprotocol + ", " + bearerProtocolPrefix + token,
	}}
	_, response, err := websocket.DefaultDialer.Dial(toWebSocketURL(server.URL), header)
	if err == nil {
		t.Fatal("cross-user terminal dial unexpectedly succeeded")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-user response = %#v error = %v", response, err)
	}
	_ = response.Body.Close()
}

func TestHandlerNewLeaseClosesPreviousClientExplicitly(t *testing.T) {
	handler, token, record, _ := newHandlerFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler.ServeTerminal(writer, request, record.ID.String())
	}))
	defer server.Close()

	first := dialTerminal(t, server.URL, token)
	defer func() { _ = first.Close() }()
	var replay Frame
	if err := first.ReadJSON(&replay); err != nil {
		t.Fatalf("read first replay: %v", err)
	}
	second := dialTerminal(t, server.URL, token)
	defer func() { _ = second.Close() }()
	if err := second.ReadJSON(&replay); err != nil {
		t.Fatalf("read second replay: %v", err)
	}

	_ = first.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err := first.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != closeReplaced || closeErr.Text != replacedReason {
		t.Fatalf("first connection close = %v", err)
	}
}

func TestBoundedOutputWriterDisconnectsWhenQueueIsFull(t *testing.T) {
	metrics := newMetrics()
	ctx, cancel := context.WithCancel(context.Background())
	client := &terminalClient{
		cancel: cancel, outgoing: make(chan Frame, 1), metrics: metrics, sizes: newSizeQueue(),
	}
	writer := &boundedOutputWriter{client: client}
	if _, err := writer.Write([]byte("one")); err != nil {
		t.Fatalf("first Write() error = %v", err)
	}
	if _, err := writer.Write([]byte("two")); !errors.Is(err, ErrSlowClient) {
		t.Fatalf("second Write() error = %v", err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("full output queue did not stop the client")
	}
}

func TestActivityLimiterAllowsAtMostOneUpdatePerInterval(t *testing.T) {
	limiter := newActivityLimiter(15 * time.Second)
	sessionID, _ := uuid.NewV7()
	target := Target{SessionID: sessionID}
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	updates := 0
	update := func(context.Context, Target, time.Time) error {
		updates++
		return nil
	}
	for _, offset := range []time.Duration{0, time.Second, 14 * time.Second, 15 * time.Second} {
		if err := limiter.mark(context.Background(), target, start.Add(offset), update); err != nil {
			t.Fatalf("mark(%s) error = %v", offset, err)
		}
	}
	if updates != 2 {
		t.Fatalf("activity updates = %d, want 2", updates)
	}
}

func newHandlerFixture(t *testing.T) (*Handler, string, session.Session, *fakeRuntime) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	issuer, err := auth.NewJWTIssuer("bosun", privateKey, 15*time.Minute)
	if err != nil {
		t.Fatalf("NewJWTIssuer() error = %v", err)
	}
	userID, _ := uuid.NewV7()
	sessionID, _ := uuid.NewV7()
	record := session.Session{
		ID: sessionID, UserID: userID, CRNamespace: "bosun-u-test", CRName: "sess-test",
		Phase: "Running",
	}
	token, err := issuer.Sign(userID.String(), time.Now().UTC())
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	runtime := &fakeRuntime{
		target: Target{
			SessionID: sessionID, UserID: userID,
			Namespace: record.CRNamespace, CRName: record.CRName, PodName: "agent-test",
		},
		resizes: make(chan remotecommand.TerminalSize, 2),
	}
	handler, err := NewHandler(Config{
		WriteQueueCapacity: 4, InputQueueCapacity: 4, MaxFrameBytes: 4096,
		WriteTimeout: time.Second, PongTimeout: 2 * time.Second, PingInterval: time.Second,
		ActivityMinInterval: 15 * time.Second,
	}, issuer, fakeLookup{record: record}, runtime, nil)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler, token, record, runtime
}

func dialTerminal(t *testing.T, serverURL, token string) *websocket.Conn {
	t.Helper()
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{Subprotocol, bearerProtocolPrefix + token}
	connection, response, err := dialer.Dial(toWebSocketURL(serverURL), nil)
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatalf("terminal dial error = %v", err)
	}
	if connection.Subprotocol() != Subprotocol {
		t.Fatalf("subprotocol = %q", connection.Subprotocol())
	}
	return connection
}

func toWebSocketURL(serverURL string) string {
	return "ws" + strings.TrimPrefix(serverURL, "http")
}

func decodeFrame(t *testing.T, frame Frame) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(frame.Data)
	if err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	return data
}

type fakeLookup struct {
	record session.Session
}

func (f fakeLookup) GetByID(_ context.Context, id uuid.UUID) (session.Session, error) {
	if id != f.record.ID {
		return session.Session{}, session.ErrNotFound
	}
	return f.record, nil
}

type fakeRuntime struct {
	target     Target
	resizes    chan remotecommand.TerminalSize
	mu         sync.Mutex
	activities []time.Time
}

func (f *fakeRuntime) Authorize(_ context.Context, _ session.Session) (Target, error) {
	return f.target, nil
}

func (f *fakeRuntime) Capture(context.Context, Target) ([]byte, error) {
	return []byte("captured"), nil
}

func (f *fakeRuntime) Attach(
	ctx context.Context,
	_ Target,
	stdin io.Reader,
	stdout io.Writer,
	_ io.Writer,
	sizes remotecommand.TerminalSizeQueue,
) error {
	go func() {
		if size := sizes.Next(); size != nil {
			f.resizes <- *size
		}
	}()
	buffer := make([]byte, len("pwd\n"))
	if _, err := io.ReadFull(stdin, buffer); err != nil {
		return err
	}
	if string(buffer) != "pwd\n" {
		return errors.New("unexpected stdin")
	}
	if _, err := stdout.Write([]byte("live")); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeRuntime) UpdateActivity(_ context.Context, _ Target, at time.Time, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activities = append(f.activities, at)
	return nil
}

func (f *fakeRuntime) activityCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.activities)
}

func TestTerminalFrameJSONContract(t *testing.T) {
	body, err := json.Marshal(Frame{Type: "resize", Data: `{"cols":80,"rows":24}`})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(body) != `{"t":"resize","d":"{\"cols\":80,\"rows\":24}"}` {
		t.Fatalf("frame JSON = %s", body)
	}
}
