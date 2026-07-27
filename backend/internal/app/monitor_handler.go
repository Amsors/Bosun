package app

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Amsors/Bosun/backend/internal/apierr"
	"github.com/Amsors/Bosun/backend/internal/envelope"
	"github.com/Amsors/Bosun/backend/internal/monitor"
)

type monitorService interface {
	Session(context.Context, uuid.UUID, uuid.UUID) (monitor.SessionSnapshot, error)
	Cluster(context.Context) (monitor.ClusterSnapshot, error)
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
