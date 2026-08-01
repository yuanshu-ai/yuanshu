// Package node contains the local Yuanshu Node composition boundary.
package node

import (
	"context"
	"errors"
)

// ErrNotImplemented makes the pre-alpha placeholder explicit.
var ErrNotImplemented = errors.New("node is not implemented")

// Run will compose local policy, transport, and agent adapters in later tasks.
// It deliberately returns before starting an agent or creating local data.
func Run(context.Context) error {
	return ErrNotImplemented
}
