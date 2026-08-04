package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const (
	CurrentConfigVersion = 2
	LegacyConfigVersion  = 1
	MaxConfigBytes       = 1 << 20
	DefaultPort          = "9527"
	DefaultListenAddress = "127.0.0.1:" + DefaultPort
	DefaultIPv6Listen    = "[::1]:" + DefaultPort
)

type DeploymentMode string

const (
	DeploymentLocal        DeploymentMode = "local"
	DeploymentLANManaged   DeploymentMode = "lan-managed"
	DeploymentPublicIPACME DeploymentMode = "public-ip-acme"
	DeploymentExternal     DeploymentMode = "external"
)

type TLSFileConfig struct {
	Termination string `toml:"termination,omitempty" json:"termination,omitempty"`
	CertFile    string `toml:"cert_file,omitempty" json:"cert_file,omitempty"`
	KeyFile     string `toml:"key_file,omitempty" json:"key_file,omitempty"`
}

type ACMEConfig struct {
	Environment string `toml:"environment,omitempty" json:"environment,omitempty"`
	Email       string `toml:"email,omitempty" json:"email,omitempty"`
	AcceptTerms bool   `toml:"accept_terms,omitempty" json:"accept_terms,omitempty"`
}

// ConfigFile is the local, non-secret Server configuration. TLS private keys
// are referenced by path and are never read into this structure for logging or
// transport purposes.
type ConfigFile struct {
	ConfigVersion  int            `toml:"config_version" json:"config_version"`
	DeploymentMode DeploymentMode `toml:"deployment_mode,omitempty" json:"deployment_mode,omitempty"`
	DataDir        string         `toml:"data_dir" json:"data_dir"`
	Listen         string         `toml:"listen" json:"listen"`
	PublicURL      string         `toml:"public_url" json:"public_url"`
	// TLSCertFile and TLSKeyFile decode the v1 layout. Normalized v2 values
	// live under TLS and never marshal these legacy fields back to disk.
	TLSCertFile           string        `toml:"tls_cert_file,omitempty" json:"-"`
	TLSKeyFile            string        `toml:"tls_key_file,omitempty" json:"-"`
	AllowedControlOrigins []string      `toml:"allowed_control_origins" json:"allowed_control_origins"`
	TLS                   TLSFileConfig `toml:"tls,omitempty" json:"tls,omitempty"`
	ACME                  ACMEConfig    `toml:"acme,omitempty" json:"acme,omitempty"`
	Web                   WebConfig     `toml:"web" json:"web"`
	Admin                 AdminConfig   `toml:"admin" json:"admin"`
}

// WebConfig controls delivery of the embedded personal workbench. A nil
// Enabled value preserves the secure, single-service default: enabled.
type WebConfig struct {
	Enabled *bool `toml:"enabled,omitempty" json:"enabled,omitempty"`
}

type AdminConfig struct {
	Enabled            *bool `toml:"enabled,omitempty" json:"enabled,omitempty"`
	SessionIdleMinutes int   `toml:"session_idle_minutes,omitempty" json:"session_idle_minutes,omitempty"`
	SessionMaxHours    int   `toml:"session_max_hours,omitempty" json:"session_max_hours,omitempty"`
	AuditRetentionDays int   `toml:"audit_retention_days,omitempty" json:"audit_retention_days,omitempty"`
}

type ConfigFileStore struct {
	path       string
	backupPath string
	lock       *sync.Mutex
}

var configFileLocks sync.Map

func NewConfigFileStore(path string) (*ConfigFileStore, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, ErrInvalid
	}
	cleaned := filepath.Clean(path)
	lock, _ := configFileLocks.LoadOrStore(cleaned, &sync.Mutex{})
	return &ConfigFileStore{path: cleaned, backupPath: cleaned + ".bak", lock: lock.(*sync.Mutex)}, nil
}

func (s *ConfigFileStore) Load(ctx context.Context) (ConfigFile, error) {
	if s == nil || ctx == nil {
		return ConfigFile{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return ConfigFile{}, err
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	value, err := loadServerConfigFile(s.path)
	if err == nil {
		return value, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return ConfigFile{}, err
	}
	return loadServerConfigFile(s.backupPath)
}

func (s *ConfigFileStore) Save(ctx context.Context, value ConfigFile) error {
	if s == nil || ctx == nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	value, err := normalizeConfigFile(value)
	if err != nil {
		return err
	}
	encoded, err := toml.Marshal(value)
	if err != nil || len(encoded) > MaxConfigBytes {
		return ErrInvalid
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if err := validateConfigParent(s.path); err != nil {
		return err
	}
	if raw, readErr := os.ReadFile(s.path); readErr == nil {
		if err := atomicWriteServerFile(s.backupPath, raw); err != nil {
			return err
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return ErrInvalid
	}
	return atomicWriteServerFile(s.path, encoded)
}

func LoadConfigFile(path string) (ConfigFile, error) {
	store, err := NewConfigFileStore(path)
	if err != nil {
		return ConfigFile{}, err
	}
	return store.Load(context.Background())
}

func ValidateConfigFile(value ConfigFile) error {
	value, err := normalizeConfigFile(value)
	if err != nil || value.DataDir == "" || !filepath.IsAbs(value.DataDir) {
		return ErrInvalid
	}
	if value.Listen == "" {
		value.Listen = DefaultListenAddress
	}
	if !validListen(value.Listen) || !validDeploymentConfig(value) {
		return ErrInvalid
	}
	if !validControlOriginsForMode(value.AllowedControlOrigins, value.DeploymentMode == DeploymentLocal) {
		return ErrInvalid
	}
	if (value.Admin.SessionIdleMinutes != 0 && (value.Admin.SessionIdleMinutes < 5 || value.Admin.SessionIdleMinutes > 120)) ||
		(value.Admin.SessionMaxHours != 0 && (value.Admin.SessionMaxHours < 1 || value.Admin.SessionMaxHours > 24)) ||
		(value.Admin.AuditRetentionDays != 0 && (value.Admin.AuditRetentionDays < 7 || value.Admin.AuditRetentionDays > 365)) {
		return ErrInvalid
	}
	for _, path := range []string{value.TLS.CertFile, value.TLS.KeyFile} {
		if path != "" && (!filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0) {
			return ErrInvalid
		}
	}
	return nil
}

func normalizeConfigFile(value ConfigFile) (ConfigFile, error) {
	switch value.ConfigVersion {
	case LegacyConfigVersion:
		if value.DeploymentMode != "" || value.TLS.Termination != "" || value.TLS.CertFile != "" || value.TLS.KeyFile != "" || value.ACME.Environment != "" || value.ACME.Email != "" || value.ACME.AcceptTerms {
			return ConfigFile{}, ErrInvalid
		}
		value.ConfigVersion = CurrentConfigVersion
		if value.PublicURL == "" {
			value.DeploymentMode = DeploymentLocal
		} else {
			value.DeploymentMode = DeploymentExternal
			value.TLS = TLSFileConfig{Termination: "server", CertFile: value.TLSCertFile, KeyFile: value.TLSKeyFile}
		}
		value.TLSCertFile, value.TLSKeyFile = "", ""
	case CurrentConfigVersion:
		if value.TLSCertFile != "" || value.TLSKeyFile != "" {
			return ConfigFile{}, ErrInvalid
		}
	default:
		return ConfigFile{}, ErrInvalid
	}
	if value.DeploymentMode == "" {
		if value.PublicURL == "" && value.TLS == (TLSFileConfig{}) {
			value.DeploymentMode = DeploymentLocal
		} else if value.PublicURL != "" && value.TLS.Termination == "server" {
			value.DeploymentMode = DeploymentExternal
		} else {
			return ConfigFile{}, ErrInvalid
		}
	}
	if value.ACME.Environment == "" && value.DeploymentMode == DeploymentPublicIPACME {
		value.ACME.Environment = "production"
	}
	return value, nil
}

func validDeploymentConfig(value ConfigFile) bool {
	parsed, publicIP, publicOK := parsePublicEndpoint(value.PublicURL)
	switch value.DeploymentMode {
	case DeploymentLocal:
		return isExactLoopbackListen(value.Listen) && value.PublicURL == "" && value.TLS == (TLSFileConfig{}) && value.ACME == (ACMEConfig{}) && validControlOriginsForMode(value.AllowedControlOrigins, true)
	case DeploymentLANManaged:
		return publicOK && parsed.Scheme == "https" && publicIP != nil && isPrivateAddress(publicIP) && !isLoopbackListen(value.Listen) && value.TLS == (TLSFileConfig{}) && value.ACME == (ACMEConfig{}) && validControlOriginsForMode(value.AllowedControlOrigins, false)
	case DeploymentPublicIPACME:
		return publicOK && parsed.Scheme == "https" && publicIP != nil && isGlobalRoutableIP(publicIP) && effectiveURLPort(parsed) == "443" && !isLoopbackListen(value.Listen) && value.TLS == (TLSFileConfig{}) && (value.ACME.Environment == "production" || value.ACME.Environment == "staging") && value.ACME.AcceptTerms && validACMEEmail(value.ACME.Email) && validControlOriginsForMode(value.AllowedControlOrigins, false)
	case DeploymentExternal:
		if !publicOK || parsed.Scheme != "https" || value.ACME != (ACMEConfig{}) || !validControlOriginsForMode(value.AllowedControlOrigins, false) {
			return false
		}
		switch value.TLS.Termination {
		case "server":
			return value.TLS.CertFile != "" && value.TLS.KeyFile != ""
		case "proxy":
			return isExactLoopbackListen(value.Listen) && value.TLS.CertFile == "" && value.TLS.KeyFile == ""
		default:
			return false
		}
	default:
		return false
	}
}

func parsePublicEndpoint(raw string) (*url.URL, net.IP, bool) {
	if raw == "" {
		return nil, nil, false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, nil, false
	}
	return parsed, net.ParseIP(parsed.Hostname()), true
}

func effectiveURLPort(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	if parsed.Port() != "" {
		return parsed.Port()
	}
	if parsed.Scheme == "https" {
		return "443"
	}
	return "80"
}

func isPrivateAddress(ip net.IP) bool {
	return ip != nil && ip.IsPrivate() && !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsMulticast()
}

func isGlobalRoutableIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, rawPrefix := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"64:ff9b:1::/48", "100::/64", "2001:db8::/32",
	} {
		prefix := netip.MustParsePrefix(rawPrefix)
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func validACMEEmail(value string) bool {
	if value == "" {
		return true
	}
	return len(value) <= 254 && !strings.ContainsAny(value, "\x00\r\n") && strings.Count(value, "@") == 1
}

func validControlOriginsForMode(origins []string, local bool) bool {
	for _, origin := range origins {
		if !validateControlOriginForMode(origin, local) {
			return false
		}
	}
	return true
}

func adminEnabled(value *bool) bool { return value == nil || *value }
func adminIdleDuration(minutes int) time.Duration {
	if minutes == 0 {
		minutes = 30
	}
	return time.Duration(minutes) * time.Minute
}
func adminMaxDuration(hours int) time.Duration {
	if hours == 0 {
		hours = 8
	}
	return time.Duration(hours) * time.Hour
}
func adminAuditRetention(days int) time.Duration {
	if days == 0 {
		days = 90
	}
	return time.Duration(days) * 24 * time.Hour
}

func loadServerConfigFile(path string) (ConfigFile, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return ConfigFile{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > MaxConfigBytes {
		return ConfigFile{}, ErrInvalid
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > MaxConfigBytes {
		return ConfigFile{}, ErrInvalid
	}
	decoder := toml.NewDecoder(bytes.NewReader(raw)).DisallowUnknownFields()
	var value ConfigFile
	if err := decoder.Decode(&value); err != nil {
		return ConfigFile{}, ErrInvalid
	}
	value, err = normalizeConfigFile(value)
	if err != nil {
		return ConfigFile{}, err
	}
	if err := ValidateConfigFile(value); err != nil {
		return ConfigFile{}, err
	}
	return value, nil
}

func validateConfigParent(path string) error {
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalid
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return ErrInvalid
	}
	return nil
}

func atomicWriteServerFile(path string, value []byte) error {
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return ErrInvalid
	}
	if _, err := file.Write(value); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return ErrInvalid
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return ErrInvalid
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return ErrInvalid
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return ErrInvalid
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return ErrInvalid
	}
	directory, err := os.Open(filepath.Dir(path))
	if err == nil {
		err = directory.Sync()
		_ = directory.Close()
	}
	if err != nil && runtime.GOOS != "windows" {
		return ErrInvalid
	}
	return nil
}

func syncServerDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	_ = directory.Close()
	if err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

func configRevision(value ConfigFile) string {
	encoded, _ := toml.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validateControlOrigin(value string) bool {
	return validateControlOriginForMode(value, false)
}

func validateControlOriginForMode(value string, local bool) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return parsed.Path == "" || parsed.Path == "/"
	}
	return local && parsed.Scheme == "http" && net.ParseIP(parsed.Hostname()) != nil && net.ParseIP(parsed.Hostname()).IsLoopback() && (parsed.Path == "" || parsed.Path == "/")
}

func controlOrigin(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func isLoopbackListen(value string) bool {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isExactLoopbackListen(value string) bool {
	host, _, err := net.SplitHostPort(value)
	return err == nil && (host == "127.0.0.1" || host == "::1")
}
