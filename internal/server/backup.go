package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	serverstore "github.com/yuanshu-ai/yuanshu/internal/server/store"
)

const backupFormatVersion = 1

type backupManifest struct {
	FormatVersion  int    `json:"formatVersion"`
	SchemaVersion  int    `json:"schemaVersion"`
	CreatedAt      string `json:"createdAt"`
	DatabaseBytes  int64  `json:"databaseBytes"`
	DatabaseSHA256 string `json:"databaseSha256"`
}

func backupServer(ctx context.Context, args []string, output io.Writer) error {
	configPath, destination, err := parseBackupArgs(args)
	if err != nil {
		return err
	}
	config, err := LoadConfigFile(configPath)
	if err != nil {
		return errors.New("server backup configuration is unavailable")
	}
	databasePath := filepath.Join(config.DataDir, "server.db")
	if _, err := serverstore.Inspect(ctx, databasePath); err != nil {
		return errors.New("server database is unavailable or invalid")
	}
	backupDir := filepath.Join(config.DataDir, "backups")
	if destination == "" {
		destination = filepath.Join(backupDir, "yuanshu-server-"+time.Now().UTC().Format("20060102T150405Z")+".tar.gz")
	} else {
		backupDir = filepath.Dir(destination)
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return errors.New("server backup directory could not be created")
	}
	if err := os.Chmod(backupDir, 0o700); err != nil {
		return errors.New("server backup directory permissions could not be applied")
	}
	if _, err := os.Lstat(destination); err == nil {
		return errors.New("server backup output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("server backup output is unavailable")
	}
	temporaryDir, err := os.MkdirTemp(backupDir, ".yuanshu-backup-")
	if err != nil {
		return errors.New("server backup workspace could not be created")
	}
	defer os.RemoveAll(temporaryDir)
	snapshot := filepath.Join(temporaryDir, "server.db")
	db, err := sql.Open("sqlite3", filepath.ToSlash(databasePath))
	if err != nil {
		return errors.New("server database could not be opened for backup")
	}
	_, snapshotErr := db.ExecContext(ctx, "VACUUM main INTO ?", filepath.ToSlash(snapshot))
	closeErr := db.Close()
	if snapshotErr != nil || closeErr != nil {
		return errors.New("server database snapshot failed")
	}
	inspection, err := serverstore.Inspect(ctx, snapshot)
	if err != nil {
		return errors.New("server database snapshot is invalid")
	}
	digest, size, err := fileDigest(snapshot)
	if err != nil {
		return errors.New("server database snapshot could not be verified")
	}
	manifest := backupManifest{FormatVersion: backupFormatVersion, SchemaVersion: inspection.SchemaVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), DatabaseBytes: size, DatabaseSHA256: digest}
	if err := writeBackupArchive(destination, snapshot, manifest); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(output, "Server backup created: %s\nSHA-256: %s\n", destination, digest)
	return nil
}

func restoreServer(ctx context.Context, args []string, output io.Writer) error {
	configPath, source, err := parseRestoreArgs(args)
	if err != nil {
		return err
	}
	config, err := LoadConfigFile(configPath)
	if err != nil {
		return errors.New("server restore configuration is unavailable")
	}
	lock, err := acquireDataLock(filepath.Join(config.DataDir, "server.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	temporaryDir, err := os.MkdirTemp(config.DataDir, ".yuanshu-restore-")
	if err != nil {
		return errors.New("server restore workspace could not be created")
	}
	defer os.RemoveAll(temporaryDir)
	snapshot, manifest, err := extractBackupArchive(source, temporaryDir)
	if err != nil {
		return err
	}
	inspection, err := serverstore.Inspect(ctx, snapshot)
	if err != nil || inspection.SchemaVersion != manifest.SchemaVersion {
		return errors.New("server backup database is invalid or incompatible")
	}
	digest, size, err := fileDigest(snapshot)
	if err != nil || digest != manifest.DatabaseSHA256 || size != manifest.DatabaseBytes {
		return errors.New("server backup checksum does not match")
	}
	databasePath := filepath.Join(config.DataDir, "server.db")
	if _, err := os.Lstat(databasePath); err == nil {
		preRestore := databasePath + ".pre-restore-" + time.Now().UTC().Format("20060102T150405Z")
		if err := copyPrivateFile(databasePath, preRestore); err != nil {
			return errors.New("current server database could not be preserved")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("current server database is unavailable")
	}
	if err := os.Chmod(snapshot, 0o600); err != nil {
		return errors.New("restored database permissions could not be applied")
	}
	if err := os.Rename(snapshot, databasePath); err != nil {
		return errors.New("restored database could not be installed")
	}
	if err := syncDirectory(config.DataDir); err != nil && runtime.GOOS != "windows" {
		return errors.New("restored database could not be synchronized")
	}
	_, _ = fmt.Fprintf(output, "Server database restored from: %s\n", source)
	return nil
}

func parseBackupArgs(args []string) (string, string, error) {
	var configPath, destination string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--config", "--output":
			name := args[index]
			index++
			if index >= len(args) || !filepath.IsAbs(args[index]) {
				return "", "", ErrUsage
			}
			if name == "--config" && configPath == "" {
				configPath = filepath.Clean(args[index])
			} else if name == "--output" && destination == "" {
				destination = filepath.Clean(args[index])
			} else {
				return "", "", ErrUsage
			}
		default:
			return "", "", ErrUsage
		}
	}
	if configPath == "" {
		return "", "", ErrUsage
	}
	return configPath, destination, nil
}

func parseRestoreArgs(args []string) (string, string, error) {
	var configPath, source string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--config", "--from":
			name := args[index]
			index++
			if index >= len(args) || !filepath.IsAbs(args[index]) {
				return "", "", ErrUsage
			}
			if name == "--config" && configPath == "" {
				configPath = filepath.Clean(args[index])
			} else if name == "--from" && source == "" {
				source = filepath.Clean(args[index])
			} else {
				return "", "", ErrUsage
			}
		default:
			return "", "", ErrUsage
		}
	}
	if configPath == "" || source == "" {
		return "", "", ErrUsage
	}
	return configPath, source, nil
}

func fileDigest(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	return hex.EncodeToString(hash.Sum(nil)), size, err
}

func writeBackupArchive(path, databasePath string, manifest backupManifest) error {
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("server backup output is unavailable")
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(temporary)
		}
	}()
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	manifestBytes, _ := json.Marshal(manifest)
	if err := writeTarBytes(tarWriter, "manifest.json", manifestBytes); err != nil {
		return errors.New("server backup manifest could not be written")
	}
	database, err := os.Open(databasePath)
	if err != nil {
		return errors.New("server backup snapshot is unavailable")
	}
	info, err := database.Stat()
	if err != nil {
		_ = database.Close()
		return errors.New("server backup snapshot is unavailable")
	}
	if err := tarWriter.WriteHeader(&tar.Header{Name: "server.db", Mode: 0o600, Size: info.Size(), ModTime: time.Unix(0, 0)}); err != nil {
		_ = database.Close()
		return err
	}
	if _, err := io.Copy(tarWriter, database); err != nil {
		_ = database.Close()
		return err
	}
	_ = database.Close()
	if tarWriter.Close() != nil || gzipWriter.Close() != nil || file.Sync() != nil || file.Close() != nil {
		return errors.New("server backup could not be finalized")
	}
	if err := os.Rename(temporary, path); err != nil {
		return errors.New("server backup could not be installed")
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil && runtime.GOOS != "windows" {
		return errors.New("server backup could not be synchronized")
	}
	ok = true
	return nil
}

func writeTarBytes(writer *tar.Writer, name string, value []byte) error {
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(value)), ModTime: time.Unix(0, 0)}); err != nil {
		return err
	}
	_, err := writer.Write(value)
	return err
}

func extractBackupArchive(path, directory string) (string, backupManifest, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", backupManifest{}, errors.New("server backup is unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", backupManifest{}, errors.New("server backup is unavailable")
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return "", backupManifest{}, errors.New("server backup format is invalid")
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	seen := map[string]bool{}
	var manifest backupManifest
	databasePath := filepath.Join(directory, "server.db")
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", manifest, errors.New("server backup archive is invalid")
		}
		if seen[header.Name] || header.Typeflag != tar.TypeReg || (header.Name != "manifest.json" && header.Name != "server.db") {
			return "", manifest, errors.New("server backup archive contains unexpected entries")
		}
		seen[header.Name] = true
		switch header.Name {
		case "manifest.json":
			if header.Size > 64<<10 || json.NewDecoder(io.LimitReader(reader, 64<<10)).Decode(&manifest) != nil {
				return "", manifest, errors.New("server backup manifest is invalid")
			}
		case "server.db":
			output, err := os.OpenFile(databasePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return "", manifest, errors.New("server backup database could not be extracted")
			}
			written, copyErr := io.Copy(output, io.LimitReader(reader, header.Size))
			syncErr := output.Sync()
			closeErr := output.Close()
			if copyErr != nil || syncErr != nil || closeErr != nil || written != header.Size {
				return "", manifest, errors.New("server backup database could not be extracted")
			}
		}
	}
	if !seen["manifest.json"] || !seen["server.db"] || manifest.FormatVersion != backupFormatVersion || manifest.SchemaVersion != serverstore.CurrentSchemaVersion || manifest.DatabaseBytes < 1 || len(manifest.DatabaseSHA256) != 64 {
		return "", manifest, errors.New("server backup manifest is incompatible")
	}
	return databasePath, manifest, nil
}

func inspectBackupArchive(ctx context.Context, path, workspaceRoot string) error {
	temporaryDir, err := os.MkdirTemp(workspaceRoot, ".yuanshu-backup-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporaryDir)
	databasePath, manifest, err := extractBackupArchive(path, temporaryDir)
	if err != nil {
		return err
	}
	digest, size, err := fileDigest(databasePath)
	if err != nil || digest != manifest.DatabaseSHA256 || size != manifest.DatabaseBytes {
		return errors.New("server backup checksum does not match")
	}
	inspection, err := serverstore.Inspect(ctx, databasePath)
	if err != nil || inspection.SchemaVersion != manifest.SchemaVersion {
		return errors.New("server backup database is invalid or incompatible")
	}
	return nil
}

func copyPrivateFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = output.Close()
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func listBackupArchives(directory string) ([]os.FileInfo, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	result := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && filepath.Ext(entry.Name()) == ".gz" {
			info, err := entry.Info()
			if err == nil {
				result = append(result, info)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ModTime().After(result[j].ModTime()) })
	return result, nil
}
