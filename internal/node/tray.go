package node

import "context"

type trayCallbacks struct {
	Status       func() Status
	Reload       func(context.Context) error
	Diagnostics  func() ([]byte, error)
	OpenConfig   func() error
	SetAutostart func(context.Context, bool) error
	Stop         func()
}

type tray interface {
	Run(context.Context, trayCallbacks) error
	Update(Status)
}

func (h *host) trayCallbacks(stop context.CancelFunc) trayCallbacks {
	return trayCallbacks{
		Status:       h.status.snapshot,
		Reload:       h.reload,
		Diagnostics:  func() ([]byte, error) { return marshalStatus(h.status.snapshot(), true) },
		OpenConfig:   func() error { return openConfiguration(h.options.configPath, h.options.paths.root) },
		SetAutostart: h.setAutostart,
		Stop:         stop,
	}
}
