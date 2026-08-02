//go:build linux

package platform

type linuxCurrentPlatform struct {
	unavailablePlatform
	ipc LocalIPC
}

func Current() Platform {
	return &linuxCurrentPlatform{unavailablePlatform: unavailablePlatform{family: FamilyLinux}, ipc: newLinuxLocalIPC()}
}

func (p *linuxCurrentPlatform) IPC() LocalIPC { return p.ipc }
