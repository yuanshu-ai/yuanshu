//go:build darwin

package platform

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

type darwinProcessManager struct{}

func newDarwinProcessManager() ProcessManager { return darwinProcessManager{} }
func (darwinProcessManager) Available() bool  { return true }

func (darwinProcessManager) Start(ctx context.Context, spec ProcessSpec) (Process, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.Executable == "" || strings.IndexByte(spec.Executable, 0) >= 0 ||
		strings.HasSuffix(strings.ToLower(spec.Executable), ".sh") || len(spec.Args) > 1024 {
		return nil, ErrInvalidArgument
	}
	command := exec.Command(spec.Executable, append([]string(nil), spec.Args...)...)
	command.Dir = spec.Directory
	if spec.Env != nil {
		command.Env = append([]string{}, spec.Env...)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, errors.New("platform process stdio is unavailable")
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, errors.New("platform process stdio is unavailable")
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		return nil, errors.New("platform process stdio is unavailable")
	}
	command.Stdout = stdoutWriter
	command.Stderr = stderrWriter
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, errors.New("platform process could not be started")
	}
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	process := &darwinProcess{command: command, stdin: stdin, stdout: stdout, stderr: stderr, stdoutWriter: stdoutWriter, stderrWriter: stderrWriter, done: make(chan struct{})}
	go process.reap()
	return process, nil
}

type darwinProcess struct {
	command      *exec.Cmd
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	stderr       io.ReadCloser
	stdoutWriter *os.File
	stderrWriter *os.File
	done         chan struct{}

	mu     sync.RWMutex
	exit   ProcessExit
	forced bool
	stop   sync.Once
}

func (p *darwinProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *darwinProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *darwinProcess) Stderr() io.ReadCloser { return p.stderr }

func (p *darwinProcess) reap() {
	err := p.command.Wait()
	_ = p.stdoutWriter.Close()
	_ = p.stderrWriter.Close()
	code := 0
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			code = exitError.ExitCode()
		} else {
			code = -1
		}
	}
	p.mu.Lock()
	p.exit = ProcessExit{Code: code, Forced: p.forced}
	close(p.done)
	p.mu.Unlock()
}

func (p *darwinProcess) Wait(ctx context.Context) (ProcessExit, error) {
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
		exit := p.exit
		p.mu.RUnlock()
		return exit, nil
	}
}

func (p *darwinProcess) Stop(ctx context.Context) (ProcessExit, error) {
	if ctx == nil {
		return ProcessExit{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return ProcessExit{}, err
	}
	select {
	case <-p.done:
		return p.Wait(context.Background())
	default:
	}
	p.stop.Do(func() {
		p.mu.Lock()
		p.forced = true
		p.mu.Unlock()
		_ = syscall.Kill(-p.command.Process.Pid, syscall.SIGTERM)
	})
	select {
	case <-p.done:
		return p.Wait(context.Background())
	case <-ctx.Done():
		_ = syscall.Kill(-p.command.Process.Pid, syscall.SIGKILL)
		<-p.done
		exit, _ := p.Wait(context.Background())
		return exit, nil
	}
}
