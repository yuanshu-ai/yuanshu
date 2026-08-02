//go:build !windows

package node

import (
	"context"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

type unavailableTray struct{}

func newPlatformTray(bool) tray                                  { return unavailableTray{} }
func (unavailableTray) Run(context.Context, trayCallbacks) error { return platform.ErrUnavailable }
func (unavailableTray) Update(Status)                            {}
func openConfiguration(string, string) error                     { return platform.ErrUnavailable }
