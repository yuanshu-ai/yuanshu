package config

import "errors"

var (
	ErrNotFound           = errors.New("node configuration was not found")
	ErrInvalid            = errors.New("node configuration is invalid")
	ErrUnsupportedVersion = errors.New("node configuration version is unsupported")
	ErrTooLarge           = errors.New("node configuration exceeds the size limit")
	ErrUnsafeFile         = errors.New("node configuration file type is unsafe")
	ErrIO                 = errors.New("node configuration storage failed")
	ErrSecretCheck        = errors.New("node configuration secret check failed")
)

type Error struct {
	Stage string
	Code  error
}

func (e *Error) Error() string {
	if e == nil {
		return "node configuration failed"
	}
	return "node configuration " + e.Stage + " failed"
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Code
}

func configError(stage string, code error) error {
	return &Error{Stage: stage, Code: code}
}
