package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"golang.org/x/term"
)

const caRecoveryFormat = 1

type caRecoveryManifest struct {
	Format      int    `json:"format"`
	CreatedAt   string `json:"createdAt"`
	Fingerprint string `json:"fingerprint"`
	CertSHA256  string `json:"certSha256"`
	KeySHA256   string `json:"keySha256"`
}

type caRecoveryMetadata struct {
	CreatedAt   string `json:"createdAt"`
	Fingerprint string `json:"fingerprint"`
}

func certificateCommand(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return ErrUsage
	}
	switch args[0] {
	case "status":
		configPath, _, _, jsonOutput, err := parseCertificateArguments(args[1:], false, false)
		if err != nil {
			return err
		}
		return writeCertificateStatus(ctx, configPath, output, jsonOutput)
	case "renew":
		configPath, _, _, _, err := parseCertificateArguments(args[1:], false, false)
		if err != nil {
			return err
		}
		value, err := LoadConfigFile(configPath)
		if err != nil || value.DeploymentMode == DeploymentLocal || value.DeploymentMode == DeploymentExternal && value.TLS.Termination == "proxy" {
			return errors.New("certificate renewal is unavailable for this deployment mode")
		}
		lock, err := acquireDataLock(filepath.Join(value.DataDir, "server.lock"))
		if err != nil {
			return errors.New("server must be stopped before manual certificate renewal")
		}
		defer lock.Close()
		provider, err := newCertificateProvider(ctx, optionsFromConfig(value))
		if err != nil {
			return errors.New("certificate provider is unavailable")
		}
		defer provider.Close()
		var challengeServer *http.Server
		if value.DeploymentMode == DeploymentPublicIPACME {
			listener, listenErr := net.Listen("tcp", value.Listen)
			if listenErr != nil {
				return errors.New("ACME TLS-ALPN listener is unavailable")
			}
			challengeServer = &http.Server{
				Handler:           http.NotFoundHandler(),
				ReadHeaderTimeout: 5 * time.Second,
				IdleTimeout:       30 * time.Second,
			}
			defer challengeServer.Close()
			go func() {
				_ = challengeServer.Serve(tls.NewListener(listener, provider.TLSConfig()))
			}()
		}
		if err := provider.Renew(ctx); err != nil {
			return errors.New("certificate renewal failed")
		}
		return encodeCertificateStatus(output, provider.Status(), false)
	case "export-ca":
		configPath, target, _, _, err := parseCertificateArguments(args[1:], true, false)
		if err != nil {
			return err
		}
		return exportManagedCA(configPath, target)
	case "backup-ca":
		configPath, target, passphraseFile, _, err := parseCertificateArguments(args[1:], true, true)
		if err != nil {
			return err
		}
		passphrase, err := readCertificatePassphrase(input, passphraseFile, true)
		if err != nil {
			return err
		}
		defer clear(passphrase)
		return backupManagedCA(configPath, target, string(passphrase))
	case "restore-ca":
		configPath, source, passphraseFile, _, err := parseCertificateArguments(args[1:], true, true)
		if err != nil {
			return err
		}
		passphrase, err := readCertificatePassphrase(input, passphraseFile, false)
		if err != nil {
			return err
		}
		defer clear(passphrase)
		return restoreManagedCA(configPath, source, string(passphrase))
	default:
		return ErrUsage
	}
}

func parseCertificateArguments(args []string, pathRequired, passphraseAllowed bool) (configPath, pathValue, passphraseFile string, jsonOutput bool, err error) {
	seen := map[string]bool{}
	for index := 0; index < len(args); index++ {
		name := args[index]
		if seen[name] {
			return "", "", "", false, ErrUsage
		}
		seen[name] = true
		switch name {
		case "--json":
			jsonOutput = true
		case "--config", "--output", "--from", "--passphrase-file":
			index++
			if index >= len(args) || !filepath.IsAbs(args[index]) {
				return "", "", "", false, ErrUsage
			}
			value := filepath.Clean(args[index])
			switch name {
			case "--config":
				configPath = value
			case "--output", "--from":
				if pathValue != "" {
					return "", "", "", false, ErrUsage
				}
				pathValue = value
			case "--passphrase-file":
				if !passphraseAllowed {
					return "", "", "", false, ErrUsage
				}
				passphraseFile = value
			}
		default:
			return "", "", "", false, ErrUsage
		}
	}
	if configPath == "" || pathRequired && pathValue == "" {
		return "", "", "", false, ErrUsage
	}
	return configPath, pathValue, passphraseFile, jsonOutput, nil
}

func writeCertificateStatus(ctx context.Context, configPath string, output io.Writer, jsonOutput bool) error {
	value, err := LoadConfigFile(configPath)
	if err != nil {
		return errors.New("server configuration is unavailable")
	}
	if value.DeploymentMode == DeploymentLocal || value.DeploymentMode == DeploymentExternal && value.TLS.Termination == "proxy" {
		status := CertificateStatus{Provider: "none", State: "not_applicable"}
		if value.DeploymentMode == DeploymentExternal {
			status.Provider, status.State = "external-proxy", "terminated_by_proxy"
		}
		return encodeCertificateStatus(output, status, jsonOutput)
	}
	provider, err := newCertificateProvider(ctx, optionsFromConfig(value))
	if err != nil {
		return errors.New("certificate provider is unavailable")
	}
	defer provider.Close()
	return encodeCertificateStatus(output, provider.Status(), jsonOutput)
}

func encodeCertificateStatus(output io.Writer, status CertificateStatus, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(output).Encode(status)
	}
	_, err := fmt.Fprintf(output, "Certificate: %s\nProvider: %s\nSAN: %s\nFingerprint: %s\nCA fingerprint: %s\nNot after: %s\nNext renewal: %s\n", status.State, status.Provider, strings.Join(status.SAN, ", "), status.Fingerprint, status.CAFingerprint, formatOptionalTime(status.NotAfter), formatOptionalTime(status.NextRenewal))
	return err
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func exportManagedCA(configPath, outputPath string) error {
	value, err := LoadConfigFile(configPath)
	if err != nil || value.DeploymentMode != DeploymentLANManaged {
		return errors.New("managed CA export is unavailable")
	}
	raw, err := readManagedCAPublic(value.DataDir)
	if err != nil {
		return err
	}
	if err := ensureNewPrivateOutput(outputPath); err != nil {
		return err
	}
	return atomicWriteServerFile(outputPath, raw)
}

func backupManagedCA(configPath, outputPath, passphrase string) error {
	value, err := LoadConfigFile(configPath)
	if err != nil || value.DeploymentMode != DeploymentLANManaged || len(passphrase) < 12 {
		return errors.New("managed CA backup is unavailable")
	}
	certPath, keyPath := managedCAPaths(value.DataDir)
	certificate, private, err := loadManagedCA(certPath, keyPath, time.Now())
	if err != nil || !private.PublicKey.Equal(certificate.PublicKey) {
		return errors.New("managed CA is invalid")
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return errors.New("managed CA is unavailable")
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return errors.New("managed CA is unavailable")
	}
	manifest := caRecoveryManifest{Format: caRecoveryFormat, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Fingerprint: certificateFingerprint(certificate.Raw), CertSHA256: sha256Hex(certPEM), KeySHA256: sha256Hex(keyPEM)}
	plain, err := encodeCARecoveryArchive(manifest, certPEM, keyPEM)
	if err != nil {
		return errors.New("managed CA recovery package could not be created")
	}
	defer clear(plain)
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return errors.New("managed CA recovery encryption failed")
	}
	var encrypted bytes.Buffer
	writer, err := age.Encrypt(&encrypted, recipient)
	if err != nil {
		return errors.New("managed CA recovery encryption failed")
	}
	if _, err := writer.Write(plain); err != nil || writer.Close() != nil {
		return errors.New("managed CA recovery encryption failed")
	}
	if err := ensureNewPrivateOutput(outputPath); err != nil {
		return err
	}
	if err := atomicWriteServerFile(outputPath, encrypted.Bytes()); err != nil {
		return err
	}
	metadata, _ := json.Marshal(caRecoveryMetadata{CreatedAt: manifest.CreatedAt, Fingerprint: manifest.Fingerprint})
	if err := atomicWriteServerFile(filepath.Join(value.DataDir, "pki", "managed", "recovery-backup.json"), metadata); err != nil {
		return errors.New("managed CA backup metadata could not be stored")
	}
	return nil
}

func restoreManagedCA(configPath, sourcePath, passphrase string) error {
	value, err := LoadConfigFile(configPath)
	if err != nil || value.DeploymentMode != DeploymentLANManaged || passphrase == "" {
		return errors.New("managed CA restore is unavailable")
	}
	lock, err := acquireDataLock(filepath.Join(value.DataDir, "server.lock"))
	if err != nil {
		return errors.New("server must be stopped before managed CA restore")
	}
	defer lock.Close()
	info, err := os.Lstat(sourcePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 2<<20 {
		return errors.New("managed CA recovery package is unsafe")
	}
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return errors.New("managed CA recovery package is unavailable")
	}
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return errors.New("managed CA recovery passphrase is invalid")
	}
	reader, err := age.Decrypt(bytes.NewReader(raw), identity)
	if err != nil {
		return errors.New("managed CA recovery package could not be decrypted")
	}
	plain, err := io.ReadAll(io.LimitReader(reader, 2<<20))
	if err != nil || len(plain) >= 2<<20 {
		return errors.New("managed CA recovery package is invalid")
	}
	defer clear(plain)
	manifest, certPEM, keyPEM, err := decodeCARecoveryArchive(plain)
	if err != nil || manifest.Format != caRecoveryFormat || sha256Hex(certPEM) != manifest.CertSHA256 || sha256Hex(keyPEM) != manifest.KeySHA256 {
		return errors.New("managed CA recovery package is invalid")
	}
	timestamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	temporaryRoot := filepath.Join(value.DataDir, "pki", ".restore-data-"+timestamp)
	temporary := filepath.Join(temporaryRoot, "pki", "managed")
	if err := os.RemoveAll(temporaryRoot); err != nil || preparePKIDirectory(temporary) != nil {
		return errors.New("managed CA restore staging is unavailable")
	}
	defer os.RemoveAll(temporaryRoot)
	certCandidate, keyCandidate := filepath.Join(temporary, "ca.pem"), filepath.Join(temporary, "ca-key.pem")
	if atomicWriteServerFile(certCandidate, certPEM) != nil || atomicWriteServerFile(keyCandidate, keyPEM) != nil {
		return errors.New("managed CA restore staging failed")
	}
	metadata, _ := json.Marshal(caRecoveryMetadata{CreatedAt: manifest.CreatedAt, Fingerprint: manifest.Fingerprint})
	if atomicWriteServerFile(filepath.Join(temporary, "recovery-backup.json"), metadata) != nil {
		return errors.New("managed CA restore metadata failed")
	}
	certificate, _, err := loadManagedCA(certCandidate, keyCandidate, time.Now())
	if err != nil || certificateFingerprint(certificate.Raw) != manifest.Fingerprint {
		return errors.New("managed CA recovery identity is invalid")
	}
	stagedValue := value
	stagedValue.DataDir = temporaryRoot
	provider, err := newManagedCertificateProvider(context.Background(), optionsFromConfig(stagedValue))
	if err != nil {
		return errors.New("managed CA restore validation failed")
	}
	_ = provider.Close()
	directory := filepath.Join(value.DataDir, "pki", "managed")
	backup := directory + ".pre-restore-" + timestamp
	hadCurrent := false
	if _, statErr := os.Lstat(directory); statErr == nil {
		hadCurrent = true
		if err := os.Rename(directory, backup); err != nil {
			return errors.New("managed CA pre-restore preservation failed")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("managed CA directory is unavailable")
	}
	if err := os.Rename(temporary, directory); err != nil {
		if hadCurrent {
			_ = os.Rename(backup, directory)
		}
		return errors.New("managed CA restore failed")
	}
	if err := syncServerDirectory(filepath.Dir(directory)); err != nil {
		_ = os.Rename(directory, temporary)
		if hadCurrent {
			_ = os.Rename(backup, directory)
		}
		return errors.New("managed CA restore failed")
	}
	return nil
}

func readCertificatePassphrase(input io.Reader, path string, confirm bool) ([]byte, error) {
	if path != "" {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || validatePrivateKeyPermissions(path) != nil || info.Size() > 4096 {
			return nil, errors.New("passphrase file is unsafe")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.New("passphrase file is unavailable")
		}
		value := bytes.TrimSpace(raw)
		if len(value) < 12 {
			return nil, errors.New("passphrase must contain at least 12 characters")
		}
		return append([]byte(nil), value...), nil
	}
	file, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return nil, errors.New("passphrase requires a TTY or --passphrase-file")
	}
	fmt.Fprint(os.Stderr, "CA recovery passphrase: ")
	first, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil || len(first) < 12 {
		clear(first)
		return nil, errors.New("passphrase must contain at least 12 characters")
	}
	if confirm {
		fmt.Fprint(os.Stderr, "Confirm passphrase: ")
		second, secondErr := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(os.Stderr)
		if secondErr != nil || !bytes.Equal(first, second) {
			clear(first)
			clear(second)
			return nil, errors.New("passphrases do not match")
		}
		clear(second)
	}
	return first, nil
}

func encodeCARecoveryArchive(manifest caRecoveryManifest, certPEM, keyPEM []byte) ([]byte, error) {
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	for _, item := range []struct {
		name string
		mode int64
		data []byte
	}{{"manifest.json", 0o600, manifestJSON}, {"ca.pem", 0o600, certPEM}, {"ca-key.pem", 0o600, keyPEM}} {
		if err := tarWriter.WriteHeader(&tar.Header{Name: item.name, Mode: item.mode, Size: int64(len(item.data)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tarWriter.Write(item.data); err != nil {
			return nil, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func decodeCARecoveryArchive(raw []byte) (caRecoveryManifest, []byte, []byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return caRecoveryManifest{}, nil, nil, err
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	items := map[string][]byte{}
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 1<<20 || header.Name != filepath.Base(header.Name) {
			return caRecoveryManifest{}, nil, nil, errors.New("invalid archive entry")
		}
		if _, exists := items[header.Name]; exists {
			return caRecoveryManifest{}, nil, nil, errors.New("duplicate archive entry")
		}
		value, err := io.ReadAll(io.LimitReader(tarReader, header.Size+1))
		if err != nil || int64(len(value)) != header.Size {
			return caRecoveryManifest{}, nil, nil, errors.New("invalid archive entry")
		}
		items[header.Name] = value
	}
	if len(items) != 3 {
		return caRecoveryManifest{}, nil, nil, errors.New("unexpected archive entries")
	}
	var manifest caRecoveryManifest
	if err := json.Unmarshal(items["manifest.json"], &manifest); err != nil || len(items["ca.pem"]) == 0 || len(items["ca-key.pem"]) == 0 {
		return caRecoveryManifest{}, nil, nil, errors.New("invalid archive manifest")
	}
	return manifest, items["ca.pem"], items["ca-key.pem"], nil
}

func managedCAPaths(dataDir string) (string, string) {
	directory := filepath.Join(dataDir, "pki", "managed")
	return filepath.Join(directory, "ca.pem"), filepath.Join(directory, "ca-key.pem")
}

func readManagedCABackupTime(directory, fingerprint string) time.Time {
	path := filepath.Join(directory, "recovery-backup.json")
	if validateManagedPKIFile(path) != nil {
		return time.Time{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	var metadata caRecoveryMetadata
	if json.Unmarshal(raw, &metadata) != nil || metadata.Fingerprint == "" || metadata.Fingerprint != fingerprint {
		return time.Time{}
	}
	createdAt, parseErr := time.Parse(time.RFC3339Nano, metadata.CreatedAt)
	if parseErr != nil {
		return time.Time{}
	}
	return createdAt.UTC()
}

func readManagedCAPublic(dataDir string) ([]byte, error) {
	certPath, _ := managedCAPaths(dataDir)
	if err := validateManagedPKIFile(certPath); err != nil {
		return nil, errors.New("managed CA is unavailable")
	}
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return nil, errors.New("managed CA is unavailable")
	}
	block, rest := pemDecodeSingleCertificate(raw)
	if block == nil || len(rest) != 0 {
		return nil, errors.New("managed CA is invalid")
	}
	return raw, nil
}

func pemDecodeSingleCertificate(raw []byte) ([]byte, []byte) {
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, rest
	}
	return block.Bytes, rest
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func ensureNewPrivateOutput(path string) error {
	if !filepath.IsAbs(path) {
		return ErrUsage
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("output path is unavailable")
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return errors.New("output directory is unavailable")
	}
	if info, err := os.Lstat(parent); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("output directory is unsafe")
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return errors.New("output directory permissions are unsafe")
	}
	return nil
}
