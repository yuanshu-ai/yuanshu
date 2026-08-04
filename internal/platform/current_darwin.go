//go:build darwin

package platform

type darwinPlatform struct {
	secure     SecureStore
	processes  ProcessManager
	inspector  ProcessInspector
	ipc        LocalIPC
	autostart  AutostartManager
	workspaces WorkspaceInspector
	picker     DirectoryPicker
}

func Current() Platform {
	return &darwinPlatform{
		secure:     newDarwinKeychain(),
		processes:  newDarwinProcessManager(),
		inspector:  newDarwinProcessInspector(),
		ipc:        newDarwinLocalIPC(),
		autostart:  newDarwinAutostartManager(),
		workspaces: newDarwinWorkspaceInspector(),
		picker:     newDarwinDirectoryPicker(),
	}
}

func (p *darwinPlatform) Family() Family                     { return FamilyDarwin }
func (p *darwinPlatform) SecureStore() SecureStore           { return p.secure }
func (p *darwinPlatform) Processes() ProcessManager          { return p.processes }
func (p *darwinPlatform) ProcessInspector() ProcessInspector { return p.inspector }
func (p *darwinPlatform) IPC() LocalIPC                      { return p.ipc }
func (p *darwinPlatform) Autostart() AutostartManager        { return p.autostart }
func (p *darwinPlatform) Workspaces() WorkspaceInspector     { return p.workspaces }
func (p *darwinPlatform) DirectoryPicker() DirectoryPicker   { return p.picker }
