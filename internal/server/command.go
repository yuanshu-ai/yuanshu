package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const Usage = `Usage:
  yuanshu server [run] [--config <absolute-path>] [--data-dir <absolute-path>] [--listen <ip:port>]
    [--public-url https://host[:port] --tls-cert <absolute-path> --tls-key <absolute-path>]
    [--allowed-control-origin https://web-host[:port]]
    [--web | --no-web]
  yuanshu server doctor [--config <absolute-path>] [--json]
  yuanshu server healthcheck [--address 127.0.0.1:7444]
`

var ErrUsage = errors.New("server command arguments are invalid")

func Command(ctx context.Context, args []string, stdout, _ io.Writer) error {
	if ctx == nil {
		return context.Canceled
	}
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(stdout, Usage)
		return nil
	}
	if len(args) > 0 && args[0] == "healthcheck" {
		return healthcheck(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "doctor" {
		return doctor(ctx, args[1:], stdout)
	}
	options, err := parseServerOptions(args)
	if err != nil {
		return err
	}
	options.Stdout = stdout
	return Run(ctx, options)
}

func parseServerArguments(args []string) (string, string, error) {
	options, err := parseServerOptions(args)
	return options.DataDir, options.Listen, err
}

func parseServerOptions(args []string) (Options, error) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if args[0] != "run" {
			return Options{}, ErrUsage
		}
		args = args[1:]
	}
	options := Options{Listen: "127.0.0.1:7444"}
	seen := make(map[string]bool)
	var configPath string
	var dataDir, listen, publicURL, tlsCert, tlsKey string
	var origins []string
	var webOverride *bool
	var hasDataDir, hasListen, hasPublicURL, hasTLSCert, hasTLSKey bool
	for index := 0; index < len(args); index++ {
		name := args[index]
		if seen[name] && name != "--allowed-control-origin" {
			return Options{}, ErrUsage
		}
		seen[name] = true
		switch name {
		case "--config":
			index++
			if index >= len(args) || !filepath.IsAbs(args[index]) {
				return Options{}, ErrUsage
			}
			configPath = filepath.Clean(args[index])
		case "--data-dir":
			index++
			if index >= len(args) || !filepath.IsAbs(args[index]) {
				return Options{}, ErrUsage
			}
			dataDir, hasDataDir = filepath.Clean(args[index]), true
		case "--listen":
			index++
			if index >= len(args) {
				return Options{}, ErrUsage
			}
			listen, hasListen = args[index], true
		case "--public-url":
			index++
			if index >= len(args) {
				return Options{}, ErrUsage
			}
			publicURL, hasPublicURL = args[index], true
		case "--tls-cert":
			index++
			if index >= len(args) || !filepath.IsAbs(args[index]) {
				return Options{}, ErrUsage
			}
			tlsCert, hasTLSCert = filepath.Clean(args[index]), true
		case "--tls-key":
			index++
			if index >= len(args) || !filepath.IsAbs(args[index]) {
				return Options{}, ErrUsage
			}
			tlsKey, hasTLSKey = filepath.Clean(args[index]), true
		case "--allowed-control-origin":
			index++
			if index >= len(args) || !validateControlOrigin(args[index]) {
				return Options{}, ErrUsage
			}
			origins = append(origins, strings.TrimSuffix(args[index], "/"))
		case "--web":
			if webOverride != nil {
				return Options{}, ErrUsage
			}
			value := true
			webOverride = &value
		case "--no-web":
			if webOverride != nil {
				return Options{}, ErrUsage
			}
			value := false
			webOverride = &value
		default:
			return Options{}, ErrUsage
		}
	}
	if configPath != "" {
		file, err := LoadConfigFile(configPath)
		if err != nil {
			return Options{}, ErrUsage
		}
		options.DataDir, options.Listen, options.PublicURL = file.DataDir, file.Listen, file.PublicURL
		options.TLSCertFile, options.TLSKeyFile = file.TLSCertFile, file.TLSKeyFile
		options.AllowedControlOrigins = append([]string(nil), file.AllowedControlOrigins...)
		options.WebEnabled = cloneBool(file.Web.Enabled)
		options.AdminEnabled = cloneBool(file.Admin.Enabled)
		options.AdminSessionIdle = adminIdleDuration(file.Admin.SessionIdleMinutes)
		options.AdminSessionMax = adminMaxDuration(file.Admin.SessionMaxHours)
		options.AdminAuditRetention = adminAuditRetention(file.Admin.AuditRetentionDays)
		options.ConfigRevision = configRevision(file)
		if options.Listen == "" {
			options.Listen = "127.0.0.1:7444"
		}
	}
	if hasDataDir {
		options.DataDir = dataDir
	}
	if hasListen {
		options.Listen = listen
	}
	if hasPublicURL {
		options.PublicURL = publicURL
	}
	if hasTLSCert {
		options.TLSCertFile = tlsCert
	}
	if hasTLSKey {
		options.TLSKeyFile = tlsKey
	}
	if len(origins) > 0 {
		options.AllowedControlOrigins = origins
	}
	if webOverride != nil {
		options.WebEnabled = webOverride
	}
	if options.DataDir == "" || !validListen(options.Listen) || !validPublicOptions(options) || !validControlOrigins(options.AllowedControlOrigins) {
		return Options{}, ErrUsage
	}
	return options, nil
}

type doctorStatus struct {
	Version               int      `json:"version"`
	State                 string   `json:"state"`
	Config                string   `json:"config"`
	DataDir               string   `json:"dataDir,omitempty"`
	Listen                string   `json:"listen,omitempty"`
	PublicURL             string   `json:"publicUrl,omitempty"`
	TLS                   string   `json:"tls"`
	TLSSAN                []string `json:"tlsSan,omitempty"`
	TLSNotAfter           string   `json:"tlsNotAfter,omitempty"`
	TLSError              string   `json:"tlsError,omitempty"`
	AllowedControlOrigins []string `json:"allowedControlOrigins,omitempty"`
	Revision              string   `json:"revision,omitempty"`
	Web                   string   `json:"web"`
}

func doctor(ctx context.Context, args []string, stdout io.Writer) error {
	var configPath string
	var jsonOutput bool
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--config":
			index++
			if index >= len(args) || !filepath.IsAbs(args[index]) || configPath != "" {
				return ErrUsage
			}
			configPath = filepath.Clean(args[index])
		case "--json":
			if jsonOutput {
				return ErrUsage
			}
			jsonOutput = true
		default:
			return ErrUsage
		}
	}
	status := doctorStatus{Version: 1, State: "needs_attention", Config: "unavailable", TLS: "not_configured", Web: "enabled"}
	if configPath == "" {
		status.Config = "not_configured"
		return writeDoctorStatus(stdout, status, jsonOutput, false)
	}
	value, err := LoadConfigFile(configPath)
	if err != nil {
		return writeDoctorStatus(stdout, status, jsonOutput, false)
	}
	status.Config, status.DataDir, status.Listen, status.PublicURL = "ready", value.DataDir, value.Listen, value.PublicURL
	if status.Listen == "" {
		status.Listen = "127.0.0.1:7444"
	}
	status.AllowedControlOrigins = append([]string(nil), value.AllowedControlOrigins...)
	status.Revision = configRevision(value)
	if !embeddedWebEnabled(value.Web.Enabled) {
		status.Web = "disabled"
	}
	if value.PublicURL != "" {
		tlsConfig, tlsErr := loadTLSConfig(Options{PublicURL: value.PublicURL, TLSCertFile: value.TLSCertFile, TLSKeyFile: value.TLSKeyFile})
		if tlsErr != nil {
			status.TLS, status.TLSError, status.State = "invalid", "certificate_unavailable_or_mismatched", "needs_attention"
			return writeDoctorStatus(stdout, status, jsonOutput, false)
		}
		status.TLS = "ready"
		if len(tlsConfig.Certificates) > 0 && tlsConfig.Certificates[0].Leaf != nil {
			leaf := tlsConfig.Certificates[0].Leaf
			status.TLSSAN = append([]string(nil), leaf.DNSNames...)
			for _, ip := range leaf.IPAddresses {
				status.TLSSAN = append(status.TLSSAN, ip.String())
			}
			status.TLSNotAfter = leaf.NotAfter.UTC().Format(time.RFC3339)
		}
	}
	status.State = "ready"
	return writeDoctorStatus(stdout, status, jsonOutput, true)
}

func writeDoctorStatus(stdout io.Writer, status doctorStatus, jsonOutput, healthy bool) error {
	if jsonOutput {
		if err := json.NewEncoder(stdout).Encode(status); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "Yuanshu Server: %s\nConfig: %s\nListen: %s\nPublic URL: %s\nTLS: %s\nWeb: %s\n", status.State, status.Config, status.Listen, status.PublicURL, status.TLS, status.Web)
	}
	if !healthy {
		return errors.New("server requires attention")
	}
	return nil
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validListen(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return false
	}
	if net.ParseIP(host) == nil {
		return false
	}
	parsedPort, err := strconv.Atoi(port)
	return err == nil && parsedPort > 0 && parsedPort <= 65535
}

func validPublicOptions(options Options) bool {
	tlsCount := 0
	for _, value := range []string{options.PublicURL, options.TLSCertFile, options.TLSKeyFile} {
		if value != "" {
			tlsCount++
		}
	}
	if tlsCount != 0 && tlsCount != 3 {
		return false
	}
	host, _, err := net.SplitHostPort(options.Listen)
	if err != nil {
		return false
	}
	if host != "127.0.0.1" && host != "::1" && tlsCount != 3 {
		return false
	}
	if options.PublicURL == "" {
		return true
	}
	parsed, err := url.Parse(options.PublicURL)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && (parsed.Path == "" || parsed.Path == "/")
}

func publicURLHost(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func healthcheck(ctx context.Context, args []string) error {
	address := "127.0.0.1:7444"
	if len(args) != 0 {
		if len(args) != 2 || args[0] != "--address" || !validListen(args[1]) {
			return ErrUsage
		}
		address = args[1]
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return errors.New("server healthcheck failed")
	}
	return connection.Close()
}
