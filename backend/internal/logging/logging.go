package logging

import (
	"context"
	"log/slog"
	"os"
)

func New(level slog.Level, component string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				attr.Key = "timestamp"
			}
			return attr
		},
	})
	logger := slog.New(defaultFieldsHandler{
		Handler:   handler,
		component: component,
	})
	slog.SetDefault(logger)
	return logger
}

type defaultFieldsHandler struct {
	slog.Handler
	component string
}

func (h defaultFieldsHandler) Handle(ctx context.Context, record slog.Record) error {
	record.AddAttrs(
		slog.String("component", h.component),
		slog.String("request_id", ""),
		slog.String("session_id", ""),
	)
	return h.Handler.Handle(ctx, record)
}

func (h defaultFieldsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return defaultFieldsHandler{Handler: h.Handler.WithAttrs(attrs), component: h.component}
}

func (h defaultFieldsHandler) WithGroup(name string) slog.Handler {
	return defaultFieldsHandler{Handler: h.Handler.WithGroup(name), component: h.component}
}
