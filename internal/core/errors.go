package core

import (
	"errors"
	"fmt"
)

// Error codes are part of the CLI contract. They map to process exit codes.
const (
	CodeUsage          = "USAGE"
	CodeGeneral        = "GENERAL"
	CodeConflict       = "CONFLICT"
	CodeBusy           = "BUSY"
	CodeIdempotent     = "IDEMPOTENT"
	CodeStale          = "STALE"
	CodeNotFound       = "NOT_FOUND"
	CodeNotImplemented = "NOT_IMPLEMENTED"
)

// Error is an error with a stable machine-readable code.
type Error struct {
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func Wrap(code, message string, err error) *Error {
	return &Error{Code: code, Message: message, Err: err}
}

// CodeOf returns the stable code for err, or GENERAL when err is not a coded
// error.
func CodeOf(err error) string {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return CodeGeneral
}
