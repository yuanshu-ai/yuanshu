package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

type DetectorOptions struct {
	Config    config.CodexAdapterConfig
	Processes platform.ProcessManager
	Inspector platform.ProcessInspector
}

type Detector struct{ options DetectorOptions }

var _ adapter.Detector = (*Detector)(nil)

func NewDetector(options DetectorOptions) (*Detector, error) {
	if !options.Config.Enabled || options.Config.RuntimeMode != "stdio" || strings.TrimSpace(options.Config.Binary) == "" ||
		options.Processes == nil || !options.Processes.Available() {
		return nil, adapter.ErrInvalid
	}
	return &Detector{options: options}, nil
}

func (*Detector) Agent() adapter.AgentDescriptor {
	return adapter.AgentDescriptor{Type: AdapterID, DisplayName: "Codex"}
}

func (d *Detector) Detect(ctx context.Context) ([]adapter.InstallationDescriptor, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	item := adapter.InstallationDescriptor{
		ID: "codex-configured", Agent: d.Agent(), Configured: true,
		State: adapter.InstallationUnavailable, Process: adapter.ProcessUnknown,
	}
	executable, _, err := resolveConfiguredCommand(d.options.Config.Binary)
	if err != nil || !commandExists(executable) {
		item.State = adapter.InstallationNotInstalled
		return []adapter.InstallationDescriptor{item}, nil
	}
	installation, err := detectInstallation(ctx, d.options.Config, d.options.Processes)
	if err != nil {
		return []adapter.InstallationDescriptor{item}, nil
	}
	item.Installation = installation
	item.State = adapter.InstallationInstalled
	if installation.Compatibility == adapter.CompatibilityUnsupported {
		item.State = adapter.InstallationIncompatible
	}
	if d.options.Inspector != nil && d.options.Inspector.Available() {
		names := []string{"codex", "codex.exe"}
		if base := filepath.Base(executable); base != "" {
			names = append(names, base)
		}
		if summary, inspectErr := d.options.Inspector.Inspect(ctx, platform.ProcessQuery{ExecutableNames: names}); inspectErr == nil {
			item.Process = adapter.ProcessState(summary.State)
		}
	}
	return []adapter.InstallationDescriptor{item}, nil
}

func commandExists(executable string) bool {
	if filepath.IsAbs(executable) || strings.ContainsAny(executable, `/\\`) {
		info, err := os.Stat(executable)
		return err == nil && !info.IsDir()
	}
	_, err := exec.LookPath(executable)
	return err == nil
}

func detectInstallation(ctx context.Context, configuration config.CodexAdapterConfig, processes platform.ProcessManager) (adapter.Installation, error) {
	value := &Adapter{options: Options{Config: configuration, Processes: processes}}
	output, err := value.runVersion(ctx)
	if err != nil {
		return adapter.Installation{}, err
	}
	version := normalizeDetectedVersion(output)
	if version == "" {
		return adapter.Installation{}, adapter.ErrUnavailable
	}
	protocol := ProtocolVersion
	compatibility := adapter.CompatibilityUnverified
	if profile, known := compatibilityForVersion(version); known {
		compatibility = adapter.CompatibilityKnown
		if profile.Protocol != "" {
			protocol = profile.Protocol
		}
	}
	return adapter.Installation{Detected: true, Version: version, Protocol: protocol, Compatibility: compatibility}, nil
}
