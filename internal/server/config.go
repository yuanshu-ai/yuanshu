package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

const (
	CurrentConfigVersion = 1
	MaxConfigBytes       = 1 << 20
)

// ConfigFile is the local, non-secret Server configuration. TLS private keys
// are referenced by path and are never read into this structure for logging or
// transport purposes.
type ConfigFile struct {
	ConfigVersion         int      `toml:"config_version" json:"config_version"`
	DataDir               string   `toml:"data_dir" json:"data_dir"`
	Listen                string   `toml:"listen" json:"listen"`
	PublicURL             string   `toml:"public_url" json:"public_url"`
	TLSCertFile           string   `toml:"tls_cert_file" json:"tls_cert_file"`
	TLSKeyFile            string   `toml:"tls_key_file" json:"tls_key_file"`
	AllowedControlOrigins []string `toml:"allowed_control_origins" json:"allowed_control_origins"`
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
	if err := ValidateConfigFile(value); err != nil {
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
	if value.ConfigVersion != CurrentConfigVersion || value.DataDir == "" || !filepath.IsAbs(value.DataDir) {
		return ErrInvalid
	}
	if value.Listen == "" {
		value.Listen = "127.0.0.1:7444"
	}
	if !validListen(value.Listen) || !validPublicOptions(Options{
		DataDir: value.DataDir, Listen: value.Listen, PublicURL: value.PublicURL,
		TLSCertFile: value.TLSCertFile, TLSKeyFile: value.TLSKeyFile,
		AllowedControlOrigins: value.AllowedControlOrigins,
	}) {
		return ErrInvalid
	}
	if !validControlOrigins(value.AllowedControlOrigins) {
		return ErrInvalid
	}
	for _, path := range []string{value.TLSCertFile, value.TLSKeyFile} {
		if path != "" && (!filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0) {
			return ErrInvalid
		}
	}
	return nil
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

func configRevision(value ConfigFile) string {
	encoded, _ := toml.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validateControlOrigin(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Path == "" || parsed.Path == "/"
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
