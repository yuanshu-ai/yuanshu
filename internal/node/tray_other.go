//go:build !windows && !darwin

package node

import (
	"context"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

type headlessTray struct{}

func newPlatformTray(bool) tray                                     { return headlessTray{} }
func (headlessTray) Run(ctx context.Context, _ trayCallbacks) error { <-ctx.Done(); return ctx.Err() }
func (headlessTray) Update(Status)                                  {}
func (headlessTray) OpenURL(string) error                           { return platform.ErrUnavailable }
