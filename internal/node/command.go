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
  yuanshu node setup [--config <absolute-path>]
  yuanshu node status [--json]
  yuanshu node stop
  yuanshu node ui
  yuanshu node doctor [--config <absolute-path>] [--json]
  yuanshu node pairing create
  yuanshu node pairing list
  yuanshu node pairing approve <pairing-id>
  yuanshu node pairing reject <pairing-id>
  yuanshu node clients list
  yuanshu node clients revoke <client-id> <key-id>
  yuanshu node credential rotate
  yuanshu node enrollment create
  yuanshu node enrollment list
  yuanshu node enrollment approve <enrollment-id>
  yuanshu node enrollment reject <enrollment-id>
  yuanshu node enrollment join <join-url>
  yuanshu node devices list
  yuanshu node devices revoke <node-id>
  yuanshu node config show
  yuanshu node config pending
  yuanshu node config approve <change-id>
  yuanshu node config reject <change-id>
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
	if err := validateNodeArguments(args); err != nil {
		return err
	}
	current := platform.Current()
	command := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "run":
		defaults, err := defaultPaths()
		if err != nil {
			return err
		}
		configPath, background, _, err := parseNodeFlags(args, defaults.config, false, true)
		if err != nil {
			return err
		}
		return runHost(ctx, runOptions{paths: defaults, configPath: configPath, background: background, platform: current})
	case "setup":
		defaults, err := defaultPaths()
		if err != nil {
			return err
		}
		configPath, _, _, err := parseNodeFlags(args, defaults.config, false, false)
		if err != nil {
			return err
		}
		return runHost(ctx, runOptions{paths: defaults, configPath: configPath, platform: current, setup: true})
	case "status":
		_, _, jsonOutput, err := parseNodeFlags(args, "", true, false)
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
	case "ui":
		if len(args) != 0 {
			return ErrUsage
		}
		response, err := callLocalRequest(ctx, current.IPC(), localRequest{Protocol: localProtocol, Command: "ui_open"})
		if err != nil || !response.OK {
			return errors.New("node control center is unavailable")
		}
		fmt.Fprintln(stdout, "Yuanshu Node control center opened.")
		return nil
	case "doctor":
		defaults, err := defaultPaths()
		if err != nil {
			return err
		}
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
		defaults, err := defaultPaths()
		if err != nil {
			return err
		}
		return commandAutostart(ctx, current, defaults, args, stdout)
	case "pairing", "clients", "credential", "enrollment", "devices", "config":
		return commandLocalManagement(ctx, current.IPC(), command, args, stdout)
	default:
		return ErrUsage
	}
}

// validateNodeArguments keeps command syntax independent from platform
// capability discovery. In particular, invalid invocations must remain usage
// errors on platforms where the formal Node runtime is not yet available.
func validateNodeArguments(args []string) error {
	command := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "run":
		_, _, _, err := parseNodeFlags(args, "", false, true)
		return err
	case "setup":
		_, _, _, err := parseNodeFlags(args, "", false, false)
		return err
	case "status":
		_, _, _, err := parseNodeFlags(args, "", true, false)
		return err
	case "stop", "ui":
		if len(args) != 0 {
			return ErrUsage
		}
		return nil
	case "doctor":
		_, _, _, err := parseNodeFlags(args, "", true, false)
		return err
	case "autostart":
		return validateAutostartArguments(args)
	case "pairing":
		if len(args) == 1 && (args[0] == "create" || args[0] == "list") {
			return nil
		}
		if len(args) == 2 && (args[0] == "approve" || args[0] == "reject") && validLocalID(args[1]) {
			return nil
		}
		return ErrUsage
	case "clients":
		if len(args) == 1 && args[0] == "list" {
			return nil
		}
		if len(args) == 3 && args[0] == "revoke" && validLocalID(args[1]) && validLocalID(args[2]) {
			return nil
		}
		return ErrUsage
	case "credential":
		if len(args) == 1 && args[0] == "rotate" {
			return nil
		}
		return ErrUsage
	case "enrollment":
		if len(args) == 1 && (args[0] == "create" || args[0] == "list") {
			return nil
		}
		if len(args) == 2 && (args[0] == "approve" || args[0] == "reject") && validLocalID(args[1]) {
			return nil
		}
		if len(args) == 2 && args[0] == "join" && strings.HasPrefix(args[1], "https://") && len(args[1]) <= 2048 {
			return nil
		}
		return ErrUsage
	case "devices":
		if len(args) == 1 && args[0] == "list" {
			return nil
		}
		if len(args) == 2 && args[0] == "revoke" && validLocalID(args[1]) {
			return nil
		}
		return ErrUsage
	case "config":
		if len(args) == 1 && (args[0] == "show" || args[0] == "pending") {
			return nil
		}
		if len(args) == 2 && (args[0] == "approve" || args[0] == "reject") && validLocalID(args[1]) {
			return nil
		}
		return ErrUsage
	default:
		return ErrUsage
	}
}

func commandLocalManagement(ctx context.Context, ipc platform.LocalIPC, command string, args []string, stdout io.Writer) error {
	request := localRequest{}
	switch command {
	case "pairing":
		switch args[0] {
		case "create":
			request.Command = "pairing_create"
		case "list":
			request.Command = "pairing_list"
		case "approve":
			request.Command, request.PairingID = "pairing_accept", args[1]
		case "reject":
			request.Command, request.PairingID = "pairing_decline", args[1]
		}
	case "clients":
		if args[0] == "list" {
			request.Command = "client_list"
		} else {
			request.Command, request.ClientID, request.KeyID = "client_revoke", args[1], args[2]
		}
	case "credential":
		request.Command = "credential_rotate"
	case "enrollment":
		switch args[0] {
		case "create":
			request.Command = "enrollment_create"
		case "list":
			request.Command = "enrollment_list"
		case "approve":
			request.Command, request.EnrollmentID = "enrollment_accept", args[1]
		case "reject":
			request.Command, request.EnrollmentID = "enrollment_decline", args[1]
		case "join":
			request.Command, request.JoinURL = "enrollment_join", args[1]
		}
	case "devices":
		if args[0] == "list" {
			request.Command = "device_list"
		} else {
			request.Command, request.NodeID = "device_revoke", args[1]
		}
	case "config":
		switch args[0] {
		case "show":
			request.Command = "config_show"
		case "pending":
			request.Command = "config_pending"
		case "approve":
			request.Command, request.ChangeID = "config_approve", args[1]
		case "reject":
			request.Command, request.ChangeID = "config_reject", args[1]
		}
	}
	response, err := callLocalRequest(ctx, ipc, request)
	if err != nil || !response.OK {
		return errors.New("node local operation failed")
	}
	switch request.Command {
	case "pairing_create":
		fmt.Fprintln(stdout, response.PairingURL)
	case "pairing_list":
		if len(response.Pairings) == 0 {
			fmt.Fprintln(stdout, "No pending pairing requests.")
		}
		for _, item := range response.Pairings {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", item.PairingID, item.Name, item.Fingerprint, item.ExpiresAt)
		}
	case "client_list":
		if len(response.Clients) == 0 {
			fmt.Fprintln(stdout, "No trusted control clients.")
		}
		for _, item := range response.Clients {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", item.ClientID, item.KeyID, item.Fingerprint, item.Status)
		}
	case "pairing_accept":
		fmt.Fprintln(stdout, "Control client approved.")
	case "pairing_decline":
		fmt.Fprintln(stdout, "Control client declined.")
	case "client_revoke":
		fmt.Fprintln(stdout, "Control client revoked.")
	case "credential_rotate":
		fmt.Fprintln(stdout, "Node connection credential rotated.")
	case "enrollment_create":
		fmt.Fprintln(stdout, response.EnrollmentURL)
	case "enrollment_list":
		if len(response.Enrollments) == 0 {
			fmt.Fprintln(stdout, "No pending node enrollments.")
		}
		for _, item := range response.Enrollments {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", item.EnrollmentID, item.Name, item.OS, item.Fingerprint, item.ExpiresAt)
		}
	case "enrollment_accept":
		fmt.Fprintln(stdout, "Node enrollment approved.")
	case "enrollment_decline":
		fmt.Fprintln(stdout, "Node enrollment declined.")
	case "enrollment_join":
		fmt.Fprintln(stdout, "Node enrollment claimed; approve it on the existing Node.")
	case "device_list":
		for _, item := range response.Devices {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%t\n", item.NodeID, item.Name, item.OS, item.Status, item.Online)
		}
	case "device_revoke":
		fmt.Fprintln(stdout, "Node revoked.")
	case "config_show":
		if err := json.NewEncoder(stdout).Encode(response.Config); err != nil {
			return err
		}
	case "config_pending":
		if len(response.ConfigChanges) == 0 {
			fmt.Fprintln(stdout, "No pending configuration changes.")
		}
		for _, item := range response.ConfigChanges {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", item.ID, item.State, item.BaseRevision, item.CreatedAt)
		}
	case "config_approve":
		fmt.Fprintln(stdout, "Configuration change approved; Node is reloading safely.")
	case "config_reject":
		fmt.Fprintln(stdout, "Configuration change rejected.")
	}
	return nil
}

func validLocalID(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0
}

func validateAutostartArguments(args []string) error {
	if len(args) == 0 {
		return ErrUsage
	}
	switch args[0] {
	case "enable":
		_, _, _, err := parseNodeFlags(args[1:], "", false, false)
		return err
	case "disable":
		if len(args) != 1 {
			return ErrUsage
		}
		return nil
	case "status":
		_, _, _, err := parseNodeFlags(args[1:], "", true, false)
		return err
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
		"Yuanshu Node: %s\nPlatform: %s\nConfig: %s\nIdentity: %s\nDatabase: %s\nWorkspaces: %d (%s)\nCodex: %s\nCompatibility: %s\nAuthentication: %s\nCredential: %s\nRecovery: %s\nRemote control: %s\nRelay last error: %s\nRelay last seen: %s\nStart at login: %s\n",
		status.State, status.Platform, status.Config, status.Identity, status.Database,
		status.Workspaces, status.WorkspaceStatus, status.Codex, status.Compatibility, status.Authentication, status.Credential, status.Recovery,
		status.RemoteControl, status.RelayLastError, status.RelayLastSeen, status.Autostart,
	)
	return err
}
