package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

const Usage = `Usage:
  yuanshu node [run] [--config <absolute-path>] [--background]
  yuanshu node status [--json]
  yuanshu node stop
  yuanshu node doctor [--config <absolute-path>] [--json]
  yuanshu node autostart enable [--config <absolute-path>]
  yuanshu node autostart disable
  yuanshu node autostart status [--json]
`

var ErrUsage = errors.New("node command arguments are invalid")

// Command runs the formal local Node command surface.
func Command(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if ctx == nil {
		return context.Canceled
	}
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(stdout, Usage)
		return nil
	}
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	current := platform.Current()
	command := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "run":
		configPath, background, _, err := parseNodeFlags(args, defaults.config, false, true)
		if err != nil {
			return err
		}
		return runHost(ctx, runOptions{paths: defaults, configPath: configPath, background: background, platform: current})
	case "status":
		_, _, jsonOutput, err := parseNodeFlags(args, defaults.config, true, false)
		if err != nil {
			return err
		}
		response, err := callLocal(ctx, current.IPC(), "status")
		if err != nil || !response.OK || response.Status == nil {
			return errors.New("node is not running")
		}
		return writeStatus(stdout, *response.Status, jsonOutput)
	case "stop":
		if len(args) != 0 {
			return ErrUsage
		}
		_, err := callLocal(ctx, current.IPC(), "stop")
		if errors.Is(err, platform.ErrNotFound) {
			fmt.Fprintln(stdout, "Yuanshu Node is already stopped.")
			return nil
		}
		return err
	case "doctor":
		configPath, _, jsonOutput, err := parseNodeFlags(args, defaults.config, true, false)
		if err != nil {
			return err
		}
		status, healthy := diagnose(ctx, current, defaults, configPath)
		if err := writeStatus(stdout, status, jsonOutput); err != nil {
			return err
		}
		if !healthy {
			return errors.New("node requires attention")
		}
		return nil
	case "autostart":
		return commandAutostart(ctx, current, defaults, args, stdout)
	default:
		return ErrUsage
	}
}

func parseNodeFlags(args []string, defaultConfig string, allowJSON, allowBackground bool) (string, bool, bool, error) {
	configPath := defaultConfig
	var background, jsonOutput bool
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--config":
			index++
			if index >= len(args) || !filepath.IsAbs(args[index]) {
				return "", false, false, ErrUsage
			}
			configPath = filepath.Clean(args[index])
		case "--background":
			if !allowBackground {
				return "", false, false, ErrUsage
			}
			background = true
		case "--json":
			if !allowJSON {
				return "", false, false, ErrUsage
			}
			jsonOutput = true
		default:
			return "", false, false, ErrUsage
		}
	}
	return configPath, background, jsonOutput, nil
}

func commandAutostart(ctx context.Context, current platform.Platform, defaults paths, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return ErrUsage
	}
	manager := current.Autostart()
	if manager == nil || !manager.Available() {
		return platform.ErrUnavailable
	}
	switch action := args[0]; action {
	case "enable":
		configPath, _, _, err := parseNodeFlags(args[1:], defaults.config, false, false)
		if err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return platform.ErrUnavailable
		}
		arguments := []string{"node", "--background"}
		if configPath != defaults.config {
			arguments = append(arguments, "--config", configPath)
		}
		if err := manager.Install(ctx, platform.AutostartEntry{ID: autostartID, Executable: executable, Args: arguments}); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Start at login enabled.")
		return nil
	case "disable":
		if len(args) != 1 {
			return ErrUsage
		}
		err := manager.Remove(ctx, autostartID)
		if err != nil && !errors.Is(err, platform.ErrNotFound) {
			return err
		}
		fmt.Fprintln(stdout, "Start at login disabled.")
		return nil
	case "status":
		_, _, jsonOutput, err := parseNodeFlags(args[1:], defaults.config, true, false)
		if err != nil {
			return err
		}
		status, err := manager.Status(ctx, autostartID)
		if err != nil {
			return err
		}
		if jsonOutput {
			return json.NewEncoder(stdout).Encode(map[string]any{"enabled": status.Installed})
		}
		if status.Installed {
			fmt.Fprintln(stdout, "Start at login: enabled")
		} else {
			fmt.Fprintln(stdout, "Start at login: disabled")
		}
		return nil
	default:
		return ErrUsage
	}
}

func marshalStatus(status Status, pretty bool) ([]byte, error) {
	if pretty {
		return json.MarshalIndent(status, "", "  ")
	}
	return json.Marshal(status)
}

func writeStatus(writer io.Writer, status Status, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(writer).Encode(status)
	}
	_, err := fmt.Fprintf(writer,
		"Yuanshu Node: %s\nPlatform: %s\nConfig: %s\nIdentity: %s\nDatabase: %s\nWorkspaces: %d\nCodex: %s\nAuthentication: %s\nRecovery: %s\nRemote control: %s\nStart at login: %s\n",
		status.State, status.Platform, status.Config, status.Identity, status.Database,
		status.Workspaces, status.Codex, status.Authentication, status.Recovery,
		status.RemoteControl, status.Autostart,
	)
	return err
}
