package define

import (
	"fmt"
	"net/http"
)

// Code はエラーの種類。プロトコルごとの表し方（HTTP の状態コードなど）は runtime が決める。
type Code string

const (
	CodeInvalidArgument    Code = "invalid_argument"
	CodeUnauthenticated    Code = "unauthenticated"
	CodePermissionDenied   Code = "permission_denied"
	CodeNotFound           Code = "not_found"
	CodeConflict           Code = "conflict"
	CodeFailedPrecondition Code = "failed_precondition"
	CodeUnavailable        Code = "unavailable"
	CodeInternal           Code = "internal"
)

// Error はハンドラーが返すエラー。*Error 以外の error は CodeInternal として扱う。
type Error struct {
	Code    Code
	Message string
	// Reasons は理由のコード（iam の拒否の理由など）。見せるかは公開範囲で決まる（design.md 4.1）。
	Reasons []string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

// Errorf は Error を作る。
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// HTTPStatus は Code の HTTP の状態コード。
func HTTPStatus(c Code) int {
	switch c {
	case CodeInvalidArgument:
		return http.StatusBadRequest
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodePermissionDenied:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict:
		return http.StatusConflict
	case CodeFailedPrecondition:
		return http.StatusPreconditionFailed
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
