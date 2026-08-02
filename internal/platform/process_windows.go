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
	"unsafe"

	"golang.org/x/sys/windows"
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
	job, err := createKillOnCloseJob()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, ErrUnavailable
	}
	if err := command.Start(); err != nil {
		_ = windows.CloseHandle(job)
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, ErrUnavailable
	}
	processHandle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, processHandle)
		_ = windows.CloseHandle(processHandle)
	}
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = windows.CloseHandle(job)
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, ErrUnavailable
	}
	process := &windowsProcess{
		command: command,
		job:     job,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		done:    make(chan struct{}),
	}
	go process.reap()
	return process, nil
}

func createKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
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
	job     windows.Handle
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
	p.mu.RLock()
	job := p.job
	p.mu.RUnlock()
	if job != 0 {
		if err := windows.TerminateJobObject(job, 1); err != nil {
			select {
			case <-p.done:
			default:
				return ProcessExit{}, ErrUnavailable
			}
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
	job := p.job
	p.job = 0
	p.mu.Unlock()
	if job != 0 {
		_ = windows.CloseHandle(job)
	}
	close(p.done)
}
