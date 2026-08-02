//go:build linux

package platform

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

type linuxProcessManager struct{}

func newLinuxProcessManager() ProcessManager { return linuxProcessManager{} }
func (linuxProcessManager) Available() bool  { return true }

func (linuxProcessManager) Start(ctx context.Context, spec ProcessSpec) (Process, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.Executable == "" || strings.IndexByte(spec.Executable, 0) >= 0 || strings.HasSuffix(strings.ToLower(spec.Executable), ".sh") || len(spec.Args) > 1024 {
		return nil, ErrInvalidArgument
	}
	command := exec.Command(spec.Executable, append([]string(nil), spec.Args...)...)
	if spec.Env != nil {
		command.Env = append([]string{}, spec.Env...)
	}
	command.Dir = spec.Directory
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, errors.New("platform process stdio is unavailable")
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, errors.New("platform process stdio is unavailable")
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, errors.New("platform process stdio is unavailable")
	}
	if err := command.Start(); err != nil {
		return nil, errors.New("platform process could not be started")
	}
	process := &linuxProcess{command: command, stdin: stdin, stdout: stdout, stderr: stderr, done: make(chan struct{})}
	go process.reap()
	return process, nil
}

type linuxProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	done    chan struct{}
	mu      sync.RWMutex
	exit    ProcessExit
	forced  bool
	stop    sync.Once
}

func (p *linuxProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *linuxProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *linuxProcess) Stderr() io.ReadCloser { return p.stderr }

func (p *linuxProcess) reap() {
	err := p.command.Wait()
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

func (p *linuxProcess) Wait(ctx context.Context) (ProcessExit, error) {
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

func (p *linuxProcess) Stop(ctx context.Context) (ProcessExit, error) {
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
