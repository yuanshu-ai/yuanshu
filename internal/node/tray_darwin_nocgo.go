//go:build darwin && !cgo

package node

import (
	"context"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

type darwinUnavailableTray struct{}

func newPlatformTray(bool) tray { return darwinUnavailableTray{} }
func (darwinUnavailableTray) Run(context.Context, trayCallbacks) error {
	return platform.ErrUnavailable
}
func (darwinUnavailableTray) Update(Status)        {}
func (darwinUnavailableTray) OpenURL(string) error { return platform.ErrUnavailable }
