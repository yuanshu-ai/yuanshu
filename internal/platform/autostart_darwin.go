//go:build darwin

package platform

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
)

const darwinLaunchAgentLabel = "com.yuanshu.node"

type darwinAutostartManager struct{}

func newDarwinAutostartManager() AutostartManager { return darwinAutostartManager{} }
func (darwinAutostartManager) Available() bool    { return true }

func (darwinAutostartManager) Install(ctx context.Context, entry AutostartEntry) error {
	if err := darwinAutostartContext(ctx); err != nil {
		return err
	}
	if !validDarwinAutostartEntry(entry) {
		return ErrInvalidArgument
	}
	path, err := darwinLaunchAgentPath(entry.ID)
	if err != nil {
		return err
	}
	if err := ensureDarwinLaunchAgentDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	content := darwinLaunchAgentPlist(entry)
	if err := writeDarwinLaunchAgent(path, content); err != nil {
		return err
	}
	_ = darwinLaunchctl(ctx, "bootout", darwinLaunchDomain(), path)
	if err := darwinLaunchctl(ctx, "bootstrap", darwinLaunchDomain(), path); err != nil {
		return err
	}
	return nil
}

func writeDarwinLaunchAgent(path, content string) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".yuanshu-launch-agent-*")
	if err != nil {
		return ErrUnavailable
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return ErrUnavailable
	}
	if _, err := io.WriteString(temporary, content); err != nil {
		return ErrUnavailable
	}
	if err := temporary.Sync(); err != nil || temporary.Close() != nil {
		return ErrUnavailable
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return ErrUnavailable
	}
	committed = true
	return nil
}

func ensureDarwinLaunchAgentDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnavailable
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return ErrUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0o022 != 0 {
		return ErrUnavailable
	}
	return nil
}

func (darwinAutostartManager) Remove(ctx context.Context, id string) error {
	if err := darwinAutostartContext(ctx); err != nil {
		return err
	}
	path, err := darwinLaunchAgentPath(id)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnavailable
	}
	_ = darwinLaunchctl(ctx, "bootout", darwinLaunchDomain(), path)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return ErrUnavailable
	}
	return nil
}

func (darwinAutostartManager) Status(ctx context.Context, id string) (AutostartStatus, error) {
	if err := darwinAutostartContext(ctx); err != nil {
		return AutostartStatus{}, err
	}
	path, err := darwinLaunchAgentPath(id)
	if err != nil {
		return AutostartStatus{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return AutostartStatus{Installed: false}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return AutostartStatus{}, ErrUnavailable
	}
	raw, err := os.ReadFile(path)
	if err != nil || !validDarwinLaunchAgentPlist(raw) {
		return AutostartStatus{}, ErrUnavailable
	}
	arguments, err := parseDarwinLaunchAgentArguments(raw)
	if err != nil {
		return AutostartStatus{}, ErrUnavailable
	}
	if err := darwinLaunchctl(ctx, "print", darwinLaunchDomain()+"/"+darwinLaunchAgentLabel); err != nil {
		return AutostartStatus{Installed: false}, nil
	}
	return AutostartStatus{Installed: true, Entry: AutostartEntry{ID: id, Executable: arguments[0], Args: arguments[1:]}}, nil
}

func darwinLaunchAgentPath(id string) (string, error) {
	if !validDarwinAutostartID(id) {
		return "", ErrInvalidArgument
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", ErrUnavailable
	}
	if id != "yuanshu-node" {
		return "", ErrInvalidArgument
	}
	return filepath.Join(home, "Library", "LaunchAgents", darwinLaunchAgentLabel+".plist"), nil
}

func darwinLaunchDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func darwinLaunchctl(ctx context.Context, args ...string) error {
	if err := darwinAutostartContext(ctx); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "/bin/launchctl", args...)
	if err := command.Run(); err != nil {
		return ErrUnavailable
	}
	return nil
}

func darwinLaunchAgentPlist(entry AutostartEntry) string {
	var builder strings.Builder
	builder.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	builder.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	builder.WriteString("<plist version=\"1.0\"><dict>")
	builder.WriteString("<key>Label</key><string>" + xmlEscape(darwinLaunchAgentLabel) + "</string>")
	builder.WriteString("<key>ProgramArguments</key><array>")
	builder.WriteString("<string>" + xmlEscape(entry.Executable) + "</string>")
	for _, argument := range entry.Args {
		builder.WriteString("<string>" + xmlEscape(argument) + "</string>")
	}
	builder.WriteString("</array>")
	builder.WriteString("<key>RunAtLoad</key><true/>")
	builder.WriteString("<key>KeepAlive</key><true/>")
	builder.WriteString("<key>ProcessType</key><string>Background</string>")
	builder.WriteString("</dict></plist>\n")
	return builder.String()
}

func validDarwinLaunchAgentPlist(raw []byte) bool {
	text := string(raw)
	return strings.Contains(text, "<key>Label</key><string>"+darwinLaunchAgentLabel+"</string>") &&
		strings.Contains(text, "<key>RunAtLoad</key><true/>") &&
		strings.Contains(text, "<key>KeepAlive</key><true/>") &&
		strings.Contains(text, "<key>ProcessType</key><string>Background</string>")
}

func parseDarwinLaunchAgentArguments(raw []byte) ([]string, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(raw)))
	insideArguments := false
	arguments := make([]string, 0, 4)
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrUnavailable
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Local == "key" {
				var key string
				if err := decoder.DecodeElement(&key, &value); err != nil {
					return nil, ErrUnavailable
				}
				if key == "ProgramArguments" {
					insideArguments = true
				}
			} else if value.Name.Local == "string" && insideArguments {
				var argument string
				if err := decoder.DecodeElement(&argument, &value); err != nil {
					return nil, ErrUnavailable
				}
				arguments = append(arguments, argument)
			}
		case xml.EndElement:
			if value.Name.Local == "array" {
				insideArguments = false
			}
		}
	}
	if len(arguments) == 0 || !filepath.IsAbs(arguments[0]) {
		return nil, ErrUnavailable
	}
	return arguments, nil
}

func xmlEscape(value string) string {
	var builder strings.Builder
	_ = xml.EscapeText(&builder, []byte(value))
	return builder.String()
}

func validDarwinAutostartEntry(entry AutostartEntry) bool {
	if !validDarwinAutostartID(entry.ID) || entry.Env != nil || entry.Directory != "" || !filepath.IsAbs(entry.Executable) {
		return false
	}
	info, err := os.Lstat(entry.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if len(entry.Args) > 32 {
		return false
	}
	for _, argument := range entry.Args {
		if len(argument) > 4096 || strings.IndexFunc(argument, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}

func validDarwinAutostartID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, character := range id {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func darwinAutostartContext(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}
