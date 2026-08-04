//go:build linux

package platform

type linuxCurrentPlatform struct {
	unavailablePlatform
	ipc       LocalIPC
	inspector ProcessInspector
}

func Current() Platform {
	return &linuxCurrentPlatform{unavailablePlatform: unavailablePlatform{family: FamilyLinux}, ipc: newLinuxLocalIPC(), inspector: newLinuxProcessInspector()}
}

func (p *linuxCurrentPlatform) IPC() LocalIPC                      { return p.ipc }
func (p *linuxCurrentPlatform) ProcessInspector() ProcessInspector { return p.inspector }
