package terminal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/Amsors/Bosun/backend/internal/apierr"
	"github.com/Amsors/Bosun/backend/internal/auth"
	"github.com/Amsors/Bosun/backend/internal/session"
)

type Handler struct {
	config   Config
	jwt      TokenVerifier
	sessions SessionLookup
	runtime  Runtime
	upgrader websocket.Upgrader
	leases   *leaseRegistry
	activity *activityLimiter
	metrics  *Metrics
	logger   *slog.Logger
}

func NewHandler(
	config Config,
	jwt TokenVerifier,
	sessions SessionLookup,
	runtime Runtime,
	logger *slog.Logger,
) (*Handler, error) {
	if jwt == nil || sessions == nil || runtime == nil {
		return nil, errors.New("terminal handler requires jwt verifier, session lookup and runtime")
	}
	if config.WriteQueueCapacity <= 0 || config.InputQueueCapacity <= 0 ||
		config.MaxFrameBytes <= 0 || config.WriteTimeout <= 0 ||
		config.PongTimeout <= 0 || config.PingInterval <= 0 ||
		config.PingInterval >= config.PongTimeout || config.ActivityMinInterval <= 0 {
		return nil, errors.New("terminal handler configuration is invalid")
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		config: config, jwt: jwt, sessions: sessions, runtime: runtime,
		upgrader: websocket.Upgrader{
			Subprotocols: []string{Subprotocol},
		},
		leases: newLeaseRegistry(), activity: newActivityLimiter(config.ActivityMinInterval),
		metrics: newMetrics(), logger: logger,
	}, nil
}

func (h *Handler) MetricsHandler() http.Handler {
	return h.metrics.handler()
}

func (h *Handler) ServeTerminal(writer http.ResponseWriter, request *http.Request, rawSessionID string) {
	sessionID, err := uuid.Parse(rawSessionID)
	if err != nil {
		writeAPIError(writer, apierr.SessionNotFound)
		return
	}
	token, ok := terminalBearerProtocol(request)
	if !ok {
		writeAPIError(writer, apierr.InvalidCredentials)
		return
	}
	claims, err := h.jwt.Verify(token, h.config.Now())
	if err != nil {
		if errors.Is(err, auth.ErrTokenExpired) {
			writeAPIError(writer, apierr.TokenExpired)
		} else {
			writeAPIError(writer, apierr.InvalidCredentials)
		}
		return
	}
	userID, err := uuid.Parse(claims.UserID)
	if err != nil {
		writeAPIError(writer, apierr.InvalidCredentials)
		return
	}
	record, err := h.sessions.GetByID(request.Context(), sessionID)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			writeAPIError(writer, apierr.SessionNotFound)
		} else {
			writeAPIError(writer, apierr.Internal)
		}
		return
	}
	if record.UserID != userID {
		writeEnvelope(writer, http.StatusForbidden, apierr.InvalidCredentials.Code, "无权访问此会话")
		return
	}
	target, err := h.runtime.Authorize(request.Context(), record)
	if err != nil {
		h.writeAuthorizationError(writer, err)
		return
	}

	connection, err := h.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	h.serveConnection(request.Context(), connection, target)
}

func (h *Handler) writeAuthorizationError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		writeEnvelope(writer, http.StatusForbidden, apierr.InvalidCredentials.Code, "无权访问此会话")
	case errors.Is(err, ErrNotRunning):
		writeAPIError(writer, apierr.SessionNotRunning)
	default:
		h.logger.Error("terminal authorization failed", "reason", err)
		writeAPIError(writer, apierr.Internal)
	}
}

func (h *Handler) serveConnection(parent context.Context, connection *websocket.Conn, target Target) {
	ctx, cancel := context.WithCancel(parent)
	client := &terminalClient{
		connection: connection,
		cancel:     cancel,
		outgoing:   make(chan Frame, h.config.WriteQueueCapacity),
		input:      make(chan []byte, h.config.InputQueueCapacity),
		sizes:      newSizeQueue(),
		metrics:    h.metrics,
	}
	release := h.leases.acquire(target.SessionID, client)
	h.metrics.connections.Inc()
	defer func() {
		release()
		cancel()
		client.sizes.close()
		_ = connection.Close()
		h.metrics.connections.Dec()
	}()

	replay, err := h.runtime.Capture(ctx, target)
	if err != nil {
		h.logger.Warn("terminal capture failed", "session_id", target.SessionID.String(), "reason", err)
		client.stop(websocket.CloseInternalServerErr, runtimeEndedReason)
		return
	}
	if len(replay) > 0 {
		_ = connection.SetWriteDeadline(time.Now().Add(h.config.WriteTimeout))
		if err := connection.WriteJSON(Frame{Type: "stdout", Data: base64.StdEncoding.EncodeToString(replay)}); err != nil {
			client.stop(websocket.CloseGoingAway, runtimeEndedReason)
			return
		}
	}

	_ = connection.SetReadDeadline(time.Now().Add(h.config.PongTimeout))
	connection.SetReadLimit(h.config.MaxFrameBytes*2 + 1024)
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(h.config.PongTimeout))
	})

	stdinReader, stdinWriter := io.Pipe()
	defer func() {
		_ = stdinReader.Close()
		_ = stdinWriter.Close()
	}()
	go h.writeLoop(ctx, client)
	go h.inputLoop(ctx, client, stdinWriter)
	go func() {
		output := &boundedOutputWriter{client: client}
		err := h.runtime.Attach(ctx, target, stdinReader, output, output, client.sizes)
		if ctx.Err() == nil {
			if err != nil && !errors.Is(err, context.Canceled) {
				h.logger.Warn("terminal attach ended", "session_id", target.SessionID.String(), "reason", err)
			}
			client.stop(closeRuntimeEnded, runtimeEndedReason)
		}
	}()

	h.readLoop(ctx, client, target)
}

func (h *Handler) writeLoop(ctx context.Context, client *terminalClient) {
	ticker := time.NewTicker(h.config.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-client.outgoing:
			_ = client.connection.SetWriteDeadline(time.Now().Add(h.config.WriteTimeout))
			if err := client.connection.WriteJSON(frame); err != nil {
				client.stop(websocket.CloseGoingAway, runtimeEndedReason)
				return
			}
		case <-ticker.C:
			_ = client.connection.SetWriteDeadline(time.Now().Add(h.config.WriteTimeout))
			if err := client.connection.WriteMessage(websocket.PingMessage, nil); err != nil {
				client.stop(websocket.CloseGoingAway, runtimeEndedReason)
				return
			}
		}
	}
}

func (h *Handler) inputLoop(ctx context.Context, client *terminalClient, writer *io.PipeWriter) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-client.input:
			if _, err := writer.Write(data); err != nil {
				client.stop(closeRuntimeEnded, runtimeEndedReason)
				return
			}
		}
	}
}

func (h *Handler) readLoop(ctx context.Context, client *terminalClient, target Target) {
	for ctx.Err() == nil {
		var frame Frame
		if err := client.connection.ReadJSON(&frame); err != nil {
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) {
				h.metrics.disconnects.WithLabelValues(closeReason(closeErr.Code)).Inc()
			}
			return
		}
		switch frame.Type {
		case "stdin":
			data, err := base64.StdEncoding.DecodeString(frame.Data)
			if err != nil || int64(len(data)) > h.config.MaxFrameBytes {
				client.stop(closeInvalidFrame, invalidFrameReason)
				return
			}
			select {
			case client.input <- data:
				h.markActivity(ctx, target)
			default:
				h.metrics.dropped.WithLabelValues("stdin").Inc()
				client.stop(closeSlowClient, slowClientReason)
				return
			}
		case "resize":
			var resize Resize
			if err := json.Unmarshal([]byte(frame.Data), &resize); err != nil ||
				resize.Cols == 0 || resize.Rows == 0 {
				client.stop(closeInvalidFrame, invalidFrameReason)
				return
			}
			client.sizes.push(remotecommand.TerminalSize{Width: resize.Cols, Height: resize.Rows})
			h.markActivity(ctx, target)
		default:
			client.stop(closeInvalidFrame, invalidFrameReason)
			return
		}
	}
}

func (h *Handler) markActivity(ctx context.Context, target Target) {
	at := h.config.Now()
	update := func(ctx context.Context, target Target, at time.Time) error {
		return h.runtime.UpdateActivity(ctx, target, at, h.config.ActivityMinInterval)
	}
	if err := h.activity.mark(ctx, target, at, update); err != nil {
		h.logger.Warn("terminal activity update failed", "session_id", target.SessionID.String(), "reason", err)
	}
}

type terminalClient struct {
	connection *websocket.Conn
	cancel     context.CancelFunc
	outgoing   chan Frame
	input      chan []byte
	sizes      *sizeQueue
	metrics    *Metrics
	stopOnce   sync.Once
}

func (c *terminalClient) stop(code int, reason string) {
	c.stopOnce.Do(func() {
		c.metrics.disconnects.WithLabelValues(reason).Inc()
		if c.connection != nil {
			deadline := time.Now().Add(time.Second)
			_ = c.connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), deadline)
			_ = c.connection.Close()
		}
		c.cancel()
	})
}

type boundedOutputWriter struct {
	client *terminalClient
}

func (w *boundedOutputWriter) Write(data []byte) (int, error) {
	frame := Frame{Type: "stdout", Data: base64.StdEncoding.EncodeToString(data)}
	select {
	case w.client.outgoing <- frame:
		return len(data), nil
	default:
		w.client.metrics.dropped.WithLabelValues("stdout").Inc()
		w.client.stop(closeSlowClient, slowClientReason)
		return 0, ErrSlowClient
	}
}

type sizeQueue struct {
	once  sync.Once
	sizes chan *remotecommand.TerminalSize
}

func newSizeQueue() *sizeQueue {
	return &sizeQueue{sizes: make(chan *remotecommand.TerminalSize, 1)}
}

func (q *sizeQueue) Next() *remotecommand.TerminalSize {
	return <-q.sizes
}

func (q *sizeQueue) push(size remotecommand.TerminalSize) {
	value := size
	select {
	case q.sizes <- &value:
	default:
		select {
		case <-q.sizes:
		default:
		}
		q.sizes <- &value
	}
}

func (q *sizeQueue) close() {
	q.once.Do(func() { close(q.sizes) })
}

func terminalBearerProtocol(request *http.Request) (string, bool) {
	foundTerminal := false
	token := ""
	for _, protocol := range websocket.Subprotocols(request) {
		switch {
		case protocol == Subprotocol:
			foundTerminal = true
		case strings.HasPrefix(protocol, bearerProtocolPrefix) && token == "":
			token = strings.TrimPrefix(protocol, bearerProtocolPrefix)
		}
	}
	return token, foundTerminal && token != ""
}

func writeAPIError(writer http.ResponseWriter, value apierr.Error) {
	writeEnvelope(writer, value.HTTPStatus, value.Code, value.Message)
}

func writeEnvelope(writer http.ResponseWriter, status, code int, message string) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"code": code, "message": message, "data": nil,
	})
}

func closeReason(code int) string {
	switch code {
	case websocket.CloseNormalClosure:
		return "client_closed"
	case closeReplaced:
		return replacedReason
	case closeSlowClient:
		return slowClientReason
	case closeInvalidFrame:
		return invalidFrameReason
	case closeRuntimeEnded:
		return runtimeEndedReason
	default:
		return fmt.Sprintf("websocket_%d", code)
	}
}
