package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Amsors/Bosun/backend/internal/apierr"
	"github.com/Amsors/Bosun/backend/internal/auth"
	"github.com/Amsors/Bosun/backend/internal/envelope"
	"github.com/Amsors/Bosun/backend/internal/ratelimit"
	"github.com/Amsors/Bosun/backend/internal/session"
	"github.com/Amsors/Bosun/backend/internal/terminal"
)

type Pinger interface {
	Ping(context.Context) error
}

// newEngine 构造带 recovery、request ID 与健康端点的基础 gin 引擎，api 与 gateway 共用。
func newEngine(component string, database Pinger) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(recoveryMiddleware(), requestIDMiddleware())
	router.GET("/healthz", func(c *gin.Context) {
		envelope.OK(c, gin.H{"status": "ok", "component": component})
	})
	router.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := database.Ping(ctx); err != nil {
			apierr.Write(c, apierr.Internal)
			return
		}
		envelope.OK(c, gin.H{"status": "ready", "component": component})
	})
	return router
}

// recoveryMiddleware intentionally never dumps request headers. The terminal
// access JWT is carried in Sec-WebSocket-Protocol and must not enter logs even
// when a handler panics.
func recoveryMiddleware() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, _ any) {
		requestID, _ := c.Get(ctxRequestID)
		slog.Error("request panic recovered", "request_id", requestID, "reason", "panic")
		apierr.Write(c, apierr.Internal)
		c.Abort()
	})
}

// NewRouter 返回仅含健康端点的路由，供 gateway 等无业务路由的组件使用。
func NewRouter(component string, database Pinger) http.Handler {
	return newEngine(component, database)
}

// APIDeps 汇总 backend API 路由所需的依赖。
type APIDeps struct {
	Database           Pinger
	Auth               *auth.Service
	JWT                *auth.JWTIssuer
	Store              auth.IdempotencyStore
	LoginByIP          *ratelimit.Limiter
	LoginByEmail       *ratelimit.Limiter
	Cookie             CookieConfig
	TrustedProxyHeader string
	Now                func() time.Time
	Sessions           sessionService
	Terminal           terminal.Service
}

// NewAPIRouter 构造 backend API 的完整路由（认证 + 当前用户）。
func NewAPIRouter(deps APIDeps) http.Handler {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	h := &authHandler{
		svc:                deps.Auth,
		jwt:                deps.JWT,
		store:              deps.Store,
		loginByIP:          deps.LoginByIP,
		loginByEmail:       deps.LoginByEmail,
		cookie:             deps.Cookie,
		trustedProxyHeader: deps.TrustedProxyHeader,
		now:                now,
	}

	router := newEngine("api", deps.Database)
	v1 := router.Group("/api/v1")
	authGroup := v1.Group("/auth")
	authGroup.POST("/register", h.register)
	authGroup.POST("/login", h.login)
	authGroup.POST("/refresh", h.refresh)
	authGroup.POST("/logout", h.logout)
	v1.GET("/me", h.requireAuth, h.me)
	if deps.Sessions != nil {
		sessions := &sessionHandler{svc: deps.Sessions}
		group := v1.Group("/sessions", h.requireAuth)
		group.POST("", sessions.create)
		group.GET("", sessions.list)
		group.GET("/:id", sessions.get)
		group.DELETE("/:id", sessions.delete)
		group.POST("/:id/hibernate", sessions.transition(session.ActionHibernate))
		group.POST("/:id/resume", sessions.transition(session.ActionResume))
		group.POST("/:id/retry", sessions.transition(session.ActionRetry))
	}
	if deps.Terminal != nil {
		v1.GET("/sessions/:id/terminal", func(c *gin.Context) {
			deps.Terminal.ServeTerminal(c.Writer, c.Request, c.Param("id"))
		})
		router.GET("/metrics", gin.WrapH(deps.Terminal.MetricsHandler()))
	}
	return router
}
