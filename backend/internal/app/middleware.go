package app

import (
	"net"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Amsors/Bosun/backend/internal/apierr"
	"github.com/Amsors/Bosun/backend/internal/auth"
)

const (
	ctxRequestID = "request_id"
	ctxUserID    = "user_id"
	headerReqID  = "X-Request-Id"
)

// requestIDMiddleware 复用或生成 request ID，写入响应头与上下文，供结构化日志关联。
func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(headerReqID)
		if id == "" {
			id = uuid.NewString()
		}
		c.Set(ctxRequestID, id)
		c.Header(headerReqID, id)
		c.Next()
	}
}

// requireAuth 校验 Bearer access token 并将用户 ID 注入上下文。缺失/无效返回 20001，过期返回 20002。
func (h *authHandler) requireAuth(c *gin.Context) {
	header := c.GetHeader("Authorization")
	prefix := "Bearer "
	if !strings.HasPrefix(header, prefix) {
		apierr.Write(c, apierr.InvalidCredentials)
		c.Abort()
		return
	}
	claims, err := h.jwt.Verify(strings.TrimSpace(header[len(prefix):]), h.now())
	if err != nil {
		if err == auth.ErrTokenExpired {
			apierr.Write(c, apierr.TokenExpired)
		} else {
			apierr.Write(c, apierr.InvalidCredentials)
		}
		c.Abort()
		return
	}
	c.Set(ctxUserID, claims.UserID)
	c.Next()
}

// clientIP 仅在明确配置了受信任转发头时采用该头的首个地址，否则回落到 RemoteAddr（spec/techspec §7.3）。
func clientIP(c *gin.Context, trustedHeader string) string {
	if trustedHeader != "" {
		if v := c.GetHeader(trustedHeader); v != "" {
			return strings.TrimSpace(strings.Split(v, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return c.Request.RemoteAddr
	}
	return host
}
