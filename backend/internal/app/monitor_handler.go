package app

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Amsors/Bosun/backend/internal/apierr"
	"github.com/Amsors/Bosun/backend/internal/envelope"
	"github.com/Amsors/Bosun/backend/internal/monitor"
)

type monitorService interface {
	Session(context.Context, uuid.UUID, uuid.UUID) (monitor.SessionSnapshot, error)
	Cluster(context.Context) (monitor.ClusterSnapshot, error)
	ResizeAgent(context.Context, uuid.UUID, monitor.ResizeRequest) (monitor.ResourceScalingResponse, error)
	RestoreAuto(context.Context, uuid.UUID) (monitor.ResourceScalingResponse, error)
}

type monitorHandler struct {
	svc monitorService
}

func (h *monitorHandler) session(c *gin.Context) {
	userID, sessionID, ok := sessionIDs(c)
	if !ok {
		apierr.Write(c, apierr.SessionNotFound)
		return
	}
	result, err := h.svc.Session(c.Request.Context(), userID, sessionID)
	if err != nil {
		apierr.Write(c, mapSessionError(err))
		return
	}
	envelope.OK(c, result)
}

func (h *monitorHandler) cluster(c *gin.Context) {
	result, err := h.svc.Cluster(c.Request.Context())
	if err != nil {
		apierr.Write(c, apierr.Internal)
		return
	}
	envelope.OK(c, result)
}

func (h *monitorHandler) resizeAgent(c *gin.Context) {
	sessionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		apierr.Write(c, apierr.SessionNotFound)
		return
	}
	var request monitor.ResizeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		apierr.Write(c, apierr.InvalidArgument)
		return
	}
	result, err := h.svc.ResizeAgent(c.Request.Context(), sessionID, request)
	if err != nil {
		switch {
		case errors.Is(err, monitor.ErrInvalidResize):
			apierr.Write(c, apierr.InvalidResources)
		default:
			apierr.Write(c, mapSessionError(err))
		}
		return
	}
	c.JSON(202, envelope.Response[monitor.ResourceScalingResponse]{Code: 0, Message: "ok", Data: result})
}

func (h *monitorHandler) restoreAuto(c *gin.Context) {
	sessionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		apierr.Write(c, apierr.SessionNotFound)
		return
	}
	result, err := h.svc.RestoreAuto(c.Request.Context(), sessionID)
	if err != nil {
		apierr.Write(c, mapSessionError(err))
		return
	}
	c.JSON(202, envelope.Response[monitor.ResourceScalingResponse]{Code: 0, Message: "ok", Data: result})
}
