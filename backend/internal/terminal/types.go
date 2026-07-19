// Package terminal implements the authenticated WebSocket proxy for the
// persistent tmux PTY in an AgentSession Pod.
package terminal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/Amsors/Bosun/backend/internal/auth"
	"github.com/Amsors/Bosun/backend/internal/session"
)

const (
	Subprotocol           = "bosun-terminal-v1"
	bearerProtocolPrefix  = "bearer."
	closeReplaced         = 4001
	closeSlowClient       = 4002
	closeInvalidFrame     = 4003
	closeRuntimeEnded     = 4004
	replacedReason        = "replaced_by_new_connection"
	slowClientReason      = "slow_client"
	invalidFrameReason    = "invalid_terminal_frame"
	runtimeEndedReason    = "terminal_runtime_ended"
	agentContainerName    = "agent"
	kubernetesCallTimeout = 5 * time.Second
	captureCommandTimeout = 10 * time.Second
)

var (
	ErrForbidden  = errors.New("terminal access forbidden")
	ErrNotRunning = errors.New("session does not allow terminal attach")
	ErrSlowClient = errors.New("terminal client is too slow")
)

type Frame struct {
	Type string `json:"t"`
	Data string `json:"d"`
}

type Resize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

type Target struct {
	SessionID uuid.UUID
	UserID    uuid.UUID
	Namespace string
	CRName    string
	PodName   string
}

type TokenVerifier interface {
	Verify(string, time.Time) (auth.Claims, error)
}

type SessionLookup interface {
	GetByID(context.Context, uuid.UUID) (session.Session, error)
}

type Runtime interface {
	Authorize(context.Context, session.Session) (Target, error)
	Capture(context.Context, Target) ([]byte, error)
	Attach(context.Context, Target, io.Reader, io.Writer, io.Writer, remotecommand.TerminalSizeQueue) error
	UpdateActivity(context.Context, Target, time.Time, time.Duration) error
}

type Config struct {
	WriteQueueCapacity  int
	InputQueueCapacity  int
	MaxFrameBytes       int64
	WriteTimeout        time.Duration
	PongTimeout         time.Duration
	PingInterval        time.Duration
	ActivityMinInterval time.Duration
	Now                 func() time.Time
}

// Service is the narrow route-facing interface used by the Gin application.
type Service interface {
	ServeTerminal(http.ResponseWriter, *http.Request, string)
	MetricsHandler() http.Handler
}
