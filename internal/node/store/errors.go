// Package store owns the versioned local SQLite state used by a Yuanshu Node.
package store

import "errors"

var (
	ErrNotFound     = errors.New("node store record was not found")
	ErrConflict     = errors.New("node store record conflicts with existing state")
	ErrInvalid      = errors.New("node store argument is invalid")
	ErrFutureSchema = errors.New("node store schema is newer than this binary")
	ErrCorrupt      = errors.New("node store is invalid or corrupt")
	ErrClosed       = errors.New("node store is closed")
)
