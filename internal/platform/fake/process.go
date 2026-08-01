package fake

import (
	"context"
	"errors"
	"io"
	"sync"

	platformpkg "github.com/yuanshu-ai/yuanshu/internal/platform"
)

type ProcessManager struct {
	failure   injectedFailure
	mu        sync.RWMutex
	processes []*Process
}

var _ platformpkg.ProcessManager = (*ProcessManager)(nil)

func NewProcessManager() *ProcessManager { return &ProcessManager{} }

func (*ProcessManager) Available() bool { return true }

func (m *ProcessManager) SetError(err error) { m.failure.set(err) }

func (m *ProcessManager) Start(ctx context.Context, spec platformpkg.ProcessSpec) (platformpkg.Process, error) {
	if err := m.failure.get(ctx); err != nil {
		return nil, err
	}
	if spec.Executable == "" {
		return nil, platformpkg.ErrInvalidArgument
	}
	process := newProcess(cloneProcessSpec(spec))
	m.mu.Lock()
	m.processes = append(m.processes, process)
	m.mu.Unlock()
	return process, nil
}

func (m *ProcessManager) Started() []platformpkg.ProcessSpec {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]platformpkg.ProcessSpec, len(m.processes))
	for index, process := range m.processes {
		result[index] = process.Spec()
	}
	return result
}

func (m *ProcessManager) LastProcess() *Process {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.processes) == 0 {
		return nil
	}
	return m.processes[len(m.processes)-1]
}

func cloneProcessSpec(spec platformpkg.ProcessSpec) platformpkg.ProcessSpec {
	copyOfSpec := spec
	copyOfSpec.Args = append([]string(nil), spec.Args...)
	if spec.Env != nil {
		copyOfSpec.Env = append([]string{}, spec.Env...)
	}
	return copyOfSpec
}

type Process struct {
	mu sync.RWMutex

	spec platformpkg.ProcessSpec
	exit platformpkg.ProcessExit
	done chan struct{}

	stdinReader  *io.PipeReader
	stdinWriter  *io.PipeWriter
	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter
	stderrReader *io.PipeReader
	stderrWriter *io.PipeWriter
}

var _ platformpkg.Process = (*Process)(nil)

func newProcess(spec platformpkg.ProcessSpec) *Process {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	return &Process{
		spec:         spec,
		done:         make(chan struct{}),
		stdinReader:  stdinReader,
		stdinWriter:  stdinWriter,
		stdoutReader: stdoutReader,
		stdoutWriter: stdoutWriter,
		stderrReader: stderrReader,
		stderrWriter: stderrWriter,
	}
}

func (p *Process) Spec() platformpkg.ProcessSpec {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneProcessSpec(p.spec)
}

func (p *Process) Stdin() io.WriteCloser { return p.stdinWriter }
func (p *Process) Stdout() io.ReadCloser { return p.stdoutReader }
func (p *Process) Stderr() io.ReadCloser { return p.stderrReader }
func (p *Process) Input() io.Reader      { return p.stdinReader }

func (p *Process) WriteStdout(value []byte) error {
	return p.writeOutput(p.stdoutWriter, value)
}

func (p *Process) WriteStderr(value []byte) error {
	return p.writeOutput(p.stderrWriter, value)
}

func (p *Process) writeOutput(writer *io.PipeWriter, value []byte) error {
	p.mu.RLock()
	select {
	case <-p.done:
		p.mu.RUnlock()
		return platformpkg.ErrProcessStopped
	default:
	}
	p.mu.RUnlock()
	_, err := writer.Write(value)
	if errors.Is(err, io.ErrClosedPipe) {
		return platformpkg.ErrProcessStopped
	}
	return err
}

func (p *Process) Complete(code int) error {
	_, completed := p.complete(platformpkg.ProcessExit{Code: code})
	if !completed {
		return platformpkg.ErrProcessStopped
	}
	return nil
}

func (p *Process) Wait(ctx context.Context) (platformpkg.ProcessExit, error) {
	if ctx == nil {
		return platformpkg.ProcessExit{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return platformpkg.ProcessExit{}, err
	}
	select {
	case <-ctx.Done():
		return platformpkg.ProcessExit{}, ctx.Err()
	case <-p.done:
		p.mu.RLock()
		exit := p.exit
		p.mu.RUnlock()
		return exit, nil
	}
}

func (p *Process) Stop(ctx context.Context) (platformpkg.ProcessExit, error) {
	if ctx == nil {
		return platformpkg.ProcessExit{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return platformpkg.ProcessExit{}, err
	}
	exit, _ := p.complete(platformpkg.ProcessExit{Code: -1, Forced: true})
	return exit, nil
}

func (p *Process) complete(exit platformpkg.ProcessExit) (platformpkg.ProcessExit, bool) {
	p.mu.Lock()
	select {
	case <-p.done:
		existing := p.exit
		p.mu.Unlock()
		return existing, false
	default:
	}
	p.exit = exit
	close(p.done)
	_ = p.stdinReader.CloseWithError(platformpkg.ErrProcessStopped)
	_ = p.stdoutWriter.Close()
	_ = p.stderrWriter.Close()
	p.mu.Unlock()
	return exit, true
}
