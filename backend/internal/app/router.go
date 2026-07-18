package app

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Amsors/Bosun/backend/internal/apierr"
	"github.com/Amsors/Bosun/backend/internal/envelope"
)

type Pinger interface {
	Ping(context.Context) error
}

func NewRouter(component string, database Pinger) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
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
