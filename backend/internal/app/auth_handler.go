package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Amsors/Bosun/backend/internal/apierr"
	"github.com/Amsors/Bosun/backend/internal/auth"
	"github.com/Amsors/Bosun/backend/internal/idempotency"
	"github.com/Amsors/Bosun/backend/internal/ratelimit"
)

// CookieConfig 描述 refresh token cookie 的下发属性。
type CookieConfig struct {
	Name   string
	Path   string
	Secure bool
	TTL    time.Duration
}

type authHandler struct {
	svc                *auth.Service
	jwt                *auth.JWTIssuer
	store              auth.IdempotencyStore
	loginByIP          *ratelimit.Limiter
	loginByEmail       *ratelimit.Limiter
	cookie             CookieConfig
	trustedProxyHeader string
	now                func() time.Time
}

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userDTO struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	CreatedAt string `json:"createdAt"`
}

type registerResponse struct {
	User             userDTO `json:"user"`
	EnvironmentPhase string  `json:"environmentPhase"`
}

type tokenResponse struct {
	AccessToken     string  `json:"accessToken"`
	TokenType       string  `json:"tokenType"`
	AccessExpiresAt string  `json:"accessExpiresAt"`
	User            userDTO `json:"user"`
}

type meResponse struct {
	User             userDTO `json:"user"`
	EnvironmentPhase string  `json:"environmentPhase"`
}

func toUserDTO(u auth.User) userDTO {
	return userDTO{ID: u.ID.String(), Email: u.Email, CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339)}
}

// register 要求 Idempotency-Key，并在同一 key 上重放首次响应或对不同请求返回 10002。
func (h *authHandler) register(c *gin.Context) {
	key := c.GetHeader("Idempotency-Key")
	if key == "" {
		writeErrorMessage(c, apierr.InvalidArgument, "缺少 Idempotency-Key 请求头")
		return
	}
	body, err := c.GetRawData()
	if err != nil {
		apierr.Write(c, apierr.InvalidArgument)
		return
	}
	requestHash := idempotency.RequestHash(c.Request.Method, c.FullPath(), body)

	var status int
	var payload []byte
	err = h.store.WithIdempotencyLock(c.Request.Context(), uuid.Nil, key, func() error {
		existing, err := h.store.GetIdempotencyKey(c.Request.Context(), uuid.Nil, key)
		if err != nil {
			return fmt.Errorf("get idempotency key: %w", err)
		}
		switch idempotency.Decide(toIdemRecord(existing), requestHash) {
		case idempotency.Replay:
			status, payload = existing.Status, existing.Body
			return nil
		case idempotency.Conflict:
			status, payload = renderError(apierr.IdempotencyConflict, apierr.IdempotencyConflict.Message)
			return nil
		}

		var req registerRequest
		if err := json.Unmarshal(body, &req); err != nil {
			status, payload = renderError(apierr.InvalidArgument, apierr.InvalidArgument.Message)
			return nil
		}
		res, regErr := h.svc.Register(c.Request.Context(), req.Email, req.Password)
		if regErr != nil {
			e := mapAuthError(regErr)
			if errors.Is(regErr, auth.ErrEmailTaken) {
				status, payload = renderError(e, "该邮箱已被注册")
			} else {
				status, payload = renderError(e, e.Message)
			}
			return nil
		}
		status, payload = renderOK(registerResponse{
			User:             toUserDTO(res.User),
			EnvironmentPhase: res.EnvironmentPhase,
		})
		rec := auth.IdempotencyRecord{
			Scope:       uuid.Nil,
			Key:         key,
			Method:      c.Request.Method,
			Path:        c.FullPath(),
			RequestHash: requestHash,
			Status:      status,
			Body:        payload,
			ExpiresAt:   h.now().Add(24 * time.Hour),
		}
		inserted, err := h.store.InsertIdempotencyKey(c.Request.Context(), rec)
		if err != nil {
			return fmt.Errorf("insert idempotency key: %w", err)
		}
		if !inserted {
			return errors.New("active idempotency key appeared while lock was held")
		}
		return nil
	})
	if err != nil {
		apierr.Write(c, apierr.Internal)
		return
	}
	c.Data(status, "application/json; charset=utf-8", payload)
}

func (h *authHandler) login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierr.Write(c, apierr.InvalidArgument)
		return
	}
	// 按 IP 与规范化邮箱两个维度分别限流；任一超限即拒绝。
	now := h.now()
	ip := clientIP(c, h.trustedProxyHeader)
	emailKey := normalizeForKey(req.Email)
	if !h.loginByIP.Allow(ip, now) || !h.loginByEmail.Allow(emailKey, now) {
		apierr.Write(c, apierr.RateLimited)
		return
	}

	tok, err := h.svc.Login(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		apierr.Write(c, mapAuthError(err))
		return
	}
	h.setRefreshCookie(c, tok.RefreshToken)
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": tokenResponse{
		AccessToken:     tok.AccessToken,
		TokenType:       "Bearer",
		AccessExpiresAt: tok.AccessExpiresAt.UTC().Format(time.RFC3339),
		User:            toUserDTO(tok.User),
	}})
}

func (h *authHandler) refresh(c *gin.Context) {
	raw, _ := c.Cookie(h.cookie.Name)
	tok, err := h.svc.Refresh(c.Request.Context(), raw)
	if err != nil {
		h.clearRefreshCookie(c)
		apierr.Write(c, mapAuthError(err))
		return
	}
	h.setRefreshCookie(c, tok.RefreshToken)
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": tokenResponse{
		AccessToken:     tok.AccessToken,
		TokenType:       "Bearer",
		AccessExpiresAt: tok.AccessExpiresAt.UTC().Format(time.RFC3339),
		User:            toUserDTO(tok.User),
	}})
}

func (h *authHandler) logout(c *gin.Context) {
	raw, _ := c.Cookie(h.cookie.Name)
	if err := h.svc.Logout(c.Request.Context(), raw); err != nil {
		apierr.Write(c, apierr.Internal)
		return
	}
	h.clearRefreshCookie(c)
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": gin.H{"status": "logged_out"}})
}

func (h *authHandler) me(c *gin.Context) {
	userIDStr, _ := c.Get(ctxUserID)
	userID, err := uuid.Parse(userIDStr.(string))
	if err != nil {
		apierr.Write(c, apierr.InvalidCredentials)
		return
	}
	res, err := h.svc.Me(c.Request.Context(), userID)
	if err != nil {
		apierr.Write(c, mapAuthError(err))
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": meResponse{
		User:             toUserDTO(res.User),
		EnvironmentPhase: res.EnvironmentPhase,
	}})
}

func (h *authHandler) setRefreshCookie(c *gin.Context, raw string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     h.cookie.Name,
		Value:    raw,
		Path:     h.cookie.Path,
		MaxAge:   int(h.cookie.TTL.Seconds()),
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (h *authHandler) clearRefreshCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     h.cookie.Name,
		Value:    "",
		Path:     h.cookie.Path,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func toIdemRecord(r *auth.IdempotencyRecord) *idempotency.Record {
	if r == nil {
		return nil
	}
	return &idempotency.Record{RequestHash: r.RequestHash, Status: r.Status, Body: r.Body}
}

func mapAuthError(err error) apierr.Error {
	switch {
	case errors.Is(err, auth.ErrValidation):
		return apierr.InvalidArgument
	case errors.Is(err, auth.ErrEmailTaken):
		return apierr.InvalidArgument
	case errors.Is(err, auth.ErrInvalidCredentials):
		return apierr.InvalidCredentials
	case errors.Is(err, auth.ErrRefreshExpired):
		return apierr.TokenExpired
	default:
		return apierr.Internal
	}
}

func renderOK(data any) (int, []byte) {
	b, _ := json.Marshal(gin.H{"code": 0, "message": "ok", "data": data})
	return http.StatusOK, b
}

func renderError(e apierr.Error, message string) (int, []byte) {
	b, _ := json.Marshal(gin.H{"code": e.Code, "message": message, "data": nil})
	return e.HTTPStatus, b
}

func writeErrorMessage(c *gin.Context, e apierr.Error, message string) {
	c.JSON(e.HTTPStatus, gin.H{"code": e.Code, "message": message, "data": nil})
}

func normalizeForKey(email string) string {
	return strings.ToLower(strings.TrimSpace(email)) // 仅作为限流 key 归一，不用于认证
}
