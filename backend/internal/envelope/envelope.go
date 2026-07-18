package envelope

import "net/http"

type Response[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type JSONWriter interface {
	JSON(code int, obj any)
}

func OK[T any](c JSONWriter, data T) {
	c.JSON(http.StatusOK, Response[T]{Code: 0, Message: "ok", Data: data})
}
