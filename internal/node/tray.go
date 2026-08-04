package node

import (
	"context"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
	"github.com/yuanshu-ai/yuanshu/internal/statuscatalog"
)

type trayCallbacks struct {
	Status            func() Status
	Reload            func(context.Context) error
	Diagnostics       func() ([]byte, error)
	OpenControlCenter func(context.Context) error
	SetAutostart      func(context.Context, bool) error
	Stop              func()
}

type tray interface {
	Run(context.Context, trayCallbacks) error
	Update(Status)
	OpenURL(string) error
}

func (h *host) trayCallbacks(stop context.CancelFunc) trayCallbacks {
	return trayCallbacks{
		Status:            h.status.snapshot,
		Reload:            h.reloadConfiguration,
		Diagnostics:       func() ([]byte, error) { return marshalStatus(h.status.snapshot(), true) },
		OpenControlCenter: h.openControlCenter,
		SetAutostart:      h.setAutostart,
		Stop:              stop,
	}
}

func (h *host) openControlCenter(ctx context.Context) error {
	if h.controlCenter == nil || h.options.tray == nil {
		return platform.ErrUnavailable
	}
	value, err := h.controlCenter.Open(ctx)
	if err != nil {
		return err
	}
	return h.options.tray.OpenURL(value)
}

func trayStateLabel(state string) string {
	code := state
	switch state {
	case "ready":
		code = "online"
	case "unpaired":
		return "Unpaired"
	case "recovering", "starting":
		code = "reconnecting"
	case "needs_attention":
		code = "setup_required"
	default:
		code = state
	}
	if value, ok := statuscatalog.Lookup(code); ok {
		return value.Title
	}
	return "Needs attention"
}
