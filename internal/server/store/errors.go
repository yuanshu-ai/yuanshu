// Package store owns the versioned SQLite metadata used by Yuanshu Server.
package store

import "errors"

var (
	ErrInvalid            = errors.New("server store argument is invalid")
	ErrNotFound           = errors.New("server store record was not found")
	ErrConflict           = errors.New("server store record conflicts with existing state")
	ErrExpired            = errors.New("server store record is expired")
	ErrUnauthorized       = errors.New("server bootstrap authorization failed")
	ErrFutureSchema       = errors.New("server store schema is newer than this binary")
	ErrCorrupt            = errors.New("server store is invalid or corrupt")
	ErrClosed             = errors.New("server store is closed")
	ErrBootstrapCompleted = errors.New("server bootstrap is already completed")
)
