package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Amsors/Bosun/backend/internal/apierr"
	"github.com/Amsors/Bosun/backend/internal/idempotency"
	"github.com/Amsors/Bosun/backend/internal/session"
)

type sessionHandler struct {
	svc sessionService
}

type sessionService interface {
	Create(context.Context, uuid.UUID, string, string, string, []byte, session.CreateRequest) (session.CreateOutput, error)
	List(context.Context, uuid.UUID, int32, int32) (session.Page, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (session.Session, error)
	Transition(context.Context, uuid.UUID, uuid.UUID, session.Action) (session.Session, error)
	Delete(context.Context, uuid.UUID, uuid.UUID) (session.Session, error)
}

func (h *sessionHandler) create(c *gin.Context) {
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
	var req session.CreateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		apierr.Write(c, apierr.InvalidArgument)
		return
	}
	userID, ok := authenticatedUserID(c)
	if !ok {
		apierr.Write(c, apierr.InvalidCredentials)
		return
	}
	output, err := h.svc.Create(
		c.Request.Context(), userID, key, c.Request.Method, c.FullPath(),
		idempotency.RequestHash(c.Request.Method, c.FullPath(), body), req,
	)
	if err != nil {
		apierr.Write(c, mapSessionError(err))
		return
	}
	c.Data(output.Status, "application/json; charset=utf-8", output.Body)
}

func (h *sessionHandler) list(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		apierr.Write(c, apierr.InvalidCredentials)
		return
	}
	page, ok := pagination(c)
	if !ok {
		apierr.Write(c, apierr.InvalidArgument)
		return
	}
	result, err := h.svc.List(c.Request.Context(), userID, page.number, page.size)
	if err != nil {
		apierr.Write(c, mapSessionError(err))
		return
	}
	items := make([]session.DTO, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, session.ToDTO(item))
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": gin.H{
		"items": items, "total": result.Total,
	}})
}

func (h *sessionHandler) get(c *gin.Context) {
	userID, sessionID, ok := sessionIDs(c)
	if !ok {
		apierr.Write(c, apierr.SessionNotFound)
		return
	}
	result, err := h.svc.Get(c.Request.Context(), userID, sessionID)
	if err != nil {
		apierr.Write(c, mapSessionError(err))
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": session.ToDTO(result)})
}

func (h *sessionHandler) transition(action session.Action) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, sessionID, ok := sessionIDs(c)
		if !ok {
			apierr.Write(c, apierr.SessionNotFound)
			return
		}
		result, err := h.svc.Transition(c.Request.Context(), userID, sessionID, action)
		if err != nil {
			apierr.Write(c, mapSessionError(err))
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"code": 0, "message": "ok", "data": session.ToDTO(result)})
	}
}

func (h *sessionHandler) delete(c *gin.Context) {
	userID, sessionID, ok := sessionIDs(c)
	if !ok {
		apierr.Write(c, apierr.SessionNotFound)
		return
	}
	result, err := h.svc.Delete(c.Request.Context(), userID, sessionID)
	if err != nil {
		apierr.Write(c, mapSessionError(err))
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"code":    0,
		"message": "删除已受理；Pod 与工作区 PVC 将被永久删除，此操作不可恢复",
		"data":    session.ToDTO(result),
	})
}

func authenticatedUserID(c *gin.Context) (uuid.UUID, bool) {
	raw, ok := c.Get(ctxUserID)
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw.(string))
	return id, err == nil
}

func sessionIDs(c *gin.Context) (uuid.UUID, uuid.UUID, bool) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	sessionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil, uuid.Nil, false
	}
	return userID, sessionID, true
}

type pageParams struct {
	number int32
	size   int32
}

func pagination(c *gin.Context) (pageParams, bool) {
	page := int64(1)
	size := int64(20)
	var err error
	if raw := c.Query("page"); raw != "" {
		page, err = strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return pageParams{}, false
		}
	}
	if raw := c.Query("page_size"); raw != "" {
		size, err = strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return pageParams{}, false
		}
	}
	if page < 1 || size < 1 || size > 100 {
		return pageParams{}, false
	}
	return pageParams{number: int32(page), size: int32(size)}, true
}

func mapSessionError(err error) apierr.Error {
	switch {
	case errors.Is(err, session.ErrValidation):
		return apierr.InvalidArgument
	case errors.Is(err, session.ErrIdempotency):
		return apierr.IdempotencyConflict
	case errors.Is(err, session.ErrNotFound):
		return apierr.SessionNotFound
	case errors.Is(err, session.ErrInvalidTransition), errors.Is(err, session.ErrConcurrentUpdate):
		return apierr.InvalidTransition
	case errors.Is(err, session.ErrCapacity):
		return apierr.CapacityUnavailable
	case errors.Is(err, session.ErrEnvironmentFailed):
		return apierr.EnvironmentFailed
	case errors.Is(err, session.ErrEnvironmentReady):
		return apierr.EnvironmentNotReady
	default:
		return apierr.Internal
	}
}
