// Package server contains the Yuanshu Server composition boundary.
package server

import (
	"context"
	"errors"
)

// ErrNotImplemented makes the pre-alpha placeholder explicit.
var ErrNotImplemented = errors.New("server is not implemented")

// Run will compose the Web, control plane, and relay in a later task.
// It deliberately returns before opening ports or starting services.
func Run(context.Context) error {
	return ErrNotImplemented
}
