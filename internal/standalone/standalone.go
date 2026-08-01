// Package standalone contains the combined Server and local Node boundary.
package standalone

import (
	"context"
	"errors"
)

// ErrNotImplemented makes the pre-alpha placeholder explicit.
var ErrNotImplemented = errors.New("standalone is not implemented")

// Run will compose Server and Node modules through StandaloneTransport later.
// It deliberately returns before opening ports or starting an agent.
func Run(context.Context) error {
	return ErrNotImplemented
}
