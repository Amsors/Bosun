package apierr

import "net/http"

type Error struct {
	Code       int
	Name       string
	Message    string
	HTTPStatus int
}

func (e Error) Error() string {
	return e.Name
}

var (
	InvalidArgument     = Error{Code: 10001, Name: "invalid_argument", Message: "请求参数无效", HTTPStatus: http.StatusBadRequest}
	IdempotencyConflict = Error{Code: 10002, Name: "idempotency_conflict", Message: "幂等键与原请求冲突", HTTPStatus: http.StatusConflict}
	InvalidCredentials  = Error{Code: 20001, Name: "invalid_credentials", Message: "认证凭据无效", HTTPStatus: http.StatusUnauthorized}
	TokenExpired        = Error{Code: 20002, Name: "token_expired", Message: "访问凭据已过期", HTTPStatus: http.StatusUnauthorized}
	RateLimited         = Error{Code: 20003, Name: "rate_limited", Message: "请求过于频繁", HTTPStatus: http.StatusTooManyRequests}
	SessionNotFound     = Error{Code: 30001, Name: "session_not_found", Message: "会话不存在", HTTPStatus: http.StatusNotFound}
	InvalidTransition   = Error{Code: 30002, Name: "invalid_transition", Message: "会话状态转换非法", HTTPStatus: http.StatusConflict}
	CapacityUnavailable = Error{Code: 30003, Name: "capacity_unavailable", Message: "最多保留 20 个会话", HTTPStatus: http.StatusConflict}
	SessionNotRunning   = Error{Code: 30004, Name: "session_not_running", Message: "会话未运行", HTTPStatus: http.StatusConflict}
	EnvironmentFailed   = Error{Code: 30005, Name: "environment_failed", Message: "用户环境初始化失败", HTTPStatus: http.StatusConflict}
	EnvironmentNotReady = Error{Code: 30006, Name: "environment_not_ready", Message: "用户环境尚未就绪", HTTPStatus: http.StatusConflict}
	Internal            = Error{Code: 50001, Name: "internal_error", Message: "服务内部错误", HTTPStatus: http.StatusInternalServerError}
)

func Write(c JSONWriter, err Error) {
	c.JSON(err.HTTPStatus, map[string]any{
		"code":    err.Code,
		"message": err.Message,
		"data":    nil,
	})
}

type JSONWriter interface {
	JSON(code int, obj any)
}
