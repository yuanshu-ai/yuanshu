package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
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
  yuanshu server init --config <absolute-path> [--mode local|lan-managed|public-ip-acme|external] [--data-dir <absolute-path>]
    [--listen <ip:port>] [--public-url https://host[:port]] [--tls-cert <absolute-path>]
    [--tls-key <absolute-path>] [--tls-termination server|proxy]
    [--acme-environment production|staging] [--acme-email <email>] [--acme-accept-terms]
    [--allowed-control-origin https://web-host[:port]]
    [--non-interactive] [--replace]
  yuanshu server setup --config <absolute-path>
  yuanshu server cert status --config <absolute-path> [--json]
  yuanshu server cert renew --config <absolute-path>
  yuanshu server cert export-ca --config <absolute-path> --output <absolute-path>
  yuanshu server cert backup-ca --config <absolute-path> --output <absolute-path> [--passphrase-file <absolute-path>]
  yuanshu server cert restore-ca --config <absolute-path> --from <absolute-path> [--passphrase-file <absolute-path>]
  yuanshu server backup --config <absolute-path> [--output <absolute-path>]
  yuanshu server restore --config <absolute-path> --from <absolute-path>
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
	if len(args) > 0 && args[0] == "init" {
		return initializeServer(ctx, args[1:], os.Stdin, stdout)
	}
	if len(args) > 0 && args[0] == "setup" {
		return setupServer(ctx, args[1:], stdout)
	}
	if len(args) > 0 && args[0] == "backup" {
		return backupServer(ctx, args[1:], stdout)
	}
	if len(args) > 0 && args[0] == "restore" {
		return restoreServer(ctx, args[1:], stdout)
	}
	if len(args) > 0 && args[0] == "cert" {
		return certificateCommand(ctx, args[1:], os.Stdin, stdout)
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
			if index >= len(args) || !validateControlOriginForMode(args[index], true) {
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
		options.DeploymentMode, options.TLSTermination, options.ACME = file.DeploymentMode, file.TLS.Termination, file.ACME
		options.TLSCertFile, options.TLSKeyFile = file.TLS.CertFile, file.TLS.KeyFile
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
	if (hasTLSCert || hasTLSKey) && options.PublicURL != "" && options.TLSCertFile != "" && options.TLSKeyFile != "" {
		options.DeploymentMode, options.TLSTermination = DeploymentExternal, "server"
		options.ACME = ACMEConfig{}
	}
	if options.DeploymentMode == "" {
		if options.PublicURL == "" {
			options.DeploymentMode = DeploymentLocal
		} else {
			options.DeploymentMode, options.TLSTermination = DeploymentExternal, "server"
		}
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
	DeploymentMode        string   `json:"deploymentMode,omitempty"`
	CertificateProvider   string   `json:"certificateProvider,omitempty"`
	TLS                   string   `json:"tls"`
	TLSSAN                []string `json:"tlsSan,omitempty"`
	TLSNotAfter           string   `json:"tlsNotAfter,omitempty"`
	TLSFingerprint        string   `json:"tlsFingerprint,omitempty"`
	TLSCAFingerprint      string   `json:"tlsCaFingerprint,omitempty"`
	TLSExpiryWarning      string   `json:"tlsExpiryWarning,omitempty"`
	TLSLastRenewed        string   `json:"tlsLastRenewed,omitempty"`
	TLSNextRenewal        string   `json:"tlsNextRenewal,omitempty"`
	TLSCABackupAt         string   `json:"tlsCaBackupAt,omitempty"`
	TLSError              string   `json:"tlsError,omitempty"`
	AllowedControlOrigins []string `json:"allowedControlOrigins,omitempty"`
	Revision              string   `json:"revision,omitempty"`
	Web                   string   `json:"web"`
	Admin                 string   `json:"admin"`
	Backup                string   `json:"backup"`
	BackupLastAt          string   `json:"backupLastAt,omitempty"`
	BackupSizeBytes       int64    `json:"backupSizeBytes,omitempty"`
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
	status := doctorStatus{Version: 1, State: "needs_attention", Config: "unavailable", TLS: "not_configured", Web: "enabled", Admin: "enabled", Backup: "backup_unavailable"}
	if configPath == "" {
		status.Config = "not_configured"
		return writeDoctorStatus(stdout, status, jsonOutput, false)
	}
	value, err := LoadConfigFile(configPath)
	if err != nil {
		return writeDoctorStatus(stdout, status, jsonOutput, false)
	}
	status.Config, status.DataDir, status.Listen, status.PublicURL = "ready", value.DataDir, value.Listen, value.PublicURL
	status.DeploymentMode = string(value.DeploymentMode)
	if status.Listen == "" {
		status.Listen = "127.0.0.1:7444"
	}
	status.AllowedControlOrigins = append([]string(nil), value.AllowedControlOrigins...)
	status.Revision = configRevision(value)
	if !embeddedWebEnabled(value.Web.Enabled) {
		status.Web = "disabled"
	}
	if !adminEnabled(value.Admin.Enabled) {
		status.Admin = "disabled"
	}
	if value.DeploymentMode == DeploymentExternal && value.TLS.Termination == "proxy" {
		status.TLS, status.CertificateProvider = "terminated_by_proxy", "external-proxy"
	} else if value.PublicURL != "" {
		provider, providerErr := newCertificateProvider(ctx, optionsFromConfig(value))
		if providerErr != nil {
			status.TLS, status.TLSError, status.State = "invalid", "certificate_unavailable_or_mismatched", "needs_attention"
			return writeDoctorStatus(stdout, status, jsonOutput, false)
		}
		defer provider.Close()
		certificate := provider.Status()
		status.TLS = certificate.State
		status.CertificateProvider = certificate.Provider
		status.TLSSAN = append([]string(nil), certificate.SAN...)
		if !certificate.NotAfter.IsZero() {
			status.TLSNotAfter = certificate.NotAfter.UTC().Format(time.RFC3339)
			status.TLSExpiryWarning = certificateExpiryWarningForProvider(certificate.Provider, time.Now().UTC(), certificate.NotAfter.UTC())
		}
		status.TLSFingerprint = certificate.Fingerprint
		status.TLSCAFingerprint = certificate.CAFingerprint
		if !certificate.LastRenewed.IsZero() {
			status.TLSLastRenewed = certificate.LastRenewed.UTC().Format(time.RFC3339)
		}
		if !certificate.NextRenewal.IsZero() {
			status.TLSNextRenewal = certificate.NextRenewal.UTC().Format(time.RFC3339)
		}
		if !certificate.CABackupAt.IsZero() {
			status.TLSCABackupAt = certificate.CABackupAt.UTC().Format(time.RFC3339)
		}
		status.TLSError = certificate.LastErrorCode
		if certificate.State != "ready" || certificate.NotAfter.IsZero() || !time.Now().Before(certificate.NotAfter) {
			if status.TLSError == "" {
				status.TLSError = "certificate_not_ready"
			}
			status.State = "needs_attention"
			return writeDoctorStatus(stdout, status, jsonOutput, false)
		}
	}
	if backups, backupErr := listBackupArchives(filepath.Join(value.DataDir, "backups")); backupErr == nil && len(backups) > 0 {
		latest := backups[0]
		status.Backup, status.BackupLastAt, status.BackupSizeBytes = "ready", latest.ModTime().UTC().Format(time.RFC3339Nano), latest.Size()
		if inspectBackupArchive(ctx, filepath.Join(value.DataDir, "backups", latest.Name()), value.DataDir) != nil {
			status.Backup = "backup_invalid"
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
		_, _ = fmt.Fprintf(stdout, "Yuanshu Server: %s\nConfig: %s\nMode: %s\nListen: %s\nPublic URL: %s\nTLS: %s (%s)\nWeb: %s\nAdmin: %s\nBackup: %s\n", status.State, status.Config, status.DeploymentMode, status.Listen, status.PublicURL, status.TLS, status.CertificateProvider, status.Web, status.Admin, status.Backup)
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
	mode := options.DeploymentMode
	if mode == "" {
		if options.PublicURL == "" {
			mode = DeploymentLocal
		} else {
			mode = DeploymentExternal
		}
	}
	termination := options.TLSTermination
	if termination == "" && mode == DeploymentExternal {
		termination = "server"
	}
	value := ConfigFile{
		ConfigVersion: CurrentConfigVersion, DeploymentMode: mode, DataDir: options.DataDir,
		Listen: options.Listen, PublicURL: options.PublicURL, AllowedControlOrigins: options.AllowedControlOrigins,
		TLS: TLSFileConfig{Termination: termination, CertFile: options.TLSCertFile, KeyFile: options.TLSKeyFile}, ACME: options.ACME,
	}
	return validDeploymentConfig(value)
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
