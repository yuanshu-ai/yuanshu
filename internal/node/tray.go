package node

import (
	"context"
	"errors"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

type trayCallbacks struct {
	Status            func() Status
	Reload            func(context.Context) error
	Diagnostics       func() ([]byte, error)
	PendingConfig     func(context.Context) ([]ConfigChangeSummary, error)
	DecideConfig      func(context.Context, string, bool) error
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
		Status:      h.status.snapshot,
		Reload:      h.reloadConfiguration,
		Diagnostics: func() ([]byte, error) { return marshalStatus(h.status.snapshot(), true) },
		PendingConfig: func(ctx context.Context) ([]ConfigChangeSummary, error) {
			response := h.handleLocalManagement(ctx, localRequest{Protocol: localProtocol, Command: "config_pending"})
			if !response.OK {
				return nil, errors.New(response.Error)
			}
			return response.ConfigChanges, nil
		},
		DecideConfig: func(ctx context.Context, id string, approve bool) error {
			command := "config_reject"
			if approve {
				command = "config_approve"
			}
			response := h.handleLocalManagement(ctx, localRequest{Protocol: localProtocol, Command: command, ChangeID: id})
			if !response.OK {
				return errors.New(response.Error)
			}
			return nil
		},
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
	switch state {
	case "ready":
		return "Ready"
	case "unpaired":
		return "Unpaired"
	case "recovering", "starting":
		return "Recovering"
	default:
		return "Needs attention"
	}
}
