//go:build windows

package platform

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type windowsProcessManager struct{}

func newWindowsProcessManager() ProcessManager { return &windowsProcessManager{} }

func (*windowsProcessManager) Available() bool { return true }

func (*windowsProcessManager) Start(ctx context.Context, spec ProcessSpec) (Process, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.Executable == "" {
		return nil, ErrInvalidArgument
	}
	executable, err := resolveWindowsExecutable(spec.Executable)
	if err != nil {
		return nil, err
	}
	command := exec.Command(executable, append([]string(nil), spec.Args...)...)
	command.Dir = spec.Directory
	if spec.Env != nil {
		command.Env = append([]string{}, spec.Env...)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, ErrUnavailable
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, ErrUnavailable
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, ErrUnavailable
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, ErrUnavailable
	}
	process := &windowsProcess{
		command: command,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		done:    make(chan struct{}),
	}
	go process.reap()
	return process, nil
}

func resolveWindowsExecutable(value string) (string, error) {
	extension := strings.ToLower(filepath.Ext(value))
	if extension == ".cmd" || extension == ".bat" || extension == ".ps1" {
		return "", ErrInvalidArgument
	}
	if extension == "" && !strings.ContainsAny(value, `\\/`) {
		for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
			candidate := filepath.Join(directory, value+".exe")
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate, nil
			}
		}
	}
	resolved, err := exec.LookPath(value)
	if err != nil {
		return "", ErrNotFound
	}
	extension = strings.ToLower(filepath.Ext(resolved))
	if extension != ".exe" && extension != ".com" {
		return "", ErrInvalidArgument
	}
	return resolved, nil
}

type windowsProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	done    chan struct{}

	mu       sync.RWMutex
	exit     ProcessExit
	waitErr  error
	stopping bool
}

func (p *windowsProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *windowsProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *windowsProcess) Stderr() io.ReadCloser { return p.stderr }

func (p *windowsProcess) Wait(ctx context.Context) (ProcessExit, error) {
	if ctx == nil {
		return ProcessExit{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return ProcessExit{}, err
	}
	select {
	case <-ctx.Done():
		return ProcessExit{}, ctx.Err()
	case <-p.done:
		p.mu.RLock()
		defer p.mu.RUnlock()
		return p.exit, p.waitErr
	}
}

func (p *windowsProcess) Stop(ctx context.Context) (ProcessExit, error) {
	if ctx == nil {
		return ProcessExit{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return ProcessExit{}, err
	}
	select {
	case <-p.done:
		return p.Wait(ctx)
	default:
	}
	p.mu.Lock()
	p.stopping = true
	p.mu.Unlock()
	if p.command.Process != nil {
		if err := p.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return ProcessExit{}, ErrUnavailable
		}
	}
	return p.Wait(ctx)
}

func (p *windowsProcess) reap() {
	err := p.command.Wait()
	p.mu.Lock()
	p.exit.Forced = p.stopping
	if p.command.ProcessState != nil {
		p.exit.Code = p.command.ProcessState.ExitCode()
	}
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			p.waitErr = ErrUnavailable
		}
	}
	p.mu.Unlock()
	close(p.done)
}
