package agentd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

var ErrMissingCodexHome = errors.New("agentd requires an explicit attempt-specific CODEX_HOME")

// Process owns one App Server stdio child. It deliberately starts the official
// `codex app-server --listen stdio://` command supplied by its caller; agentd
// does not implement a replacement coding runtime.
type Process struct {
	Client *Client
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	done   chan struct{}
	mu     sync.Mutex
	wait   error
}

func StartProcess(ctx context.Context, executable string, arguments, environment []string) (*Process, error) {
	cmd := exec.CommandContext(ctx, executable, arguments...)
	cmd.Env = append(os.Environ(), environment...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open App Server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open App Server stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start App Server: %w", err)
	}
	process := &Process{Client: NewClient(stdout, stdin), cmd: cmd, stdin: stdin, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		process.mu.Lock()
		process.wait = err
		process.mu.Unlock()
		close(process.done)
	}()
	return process, nil
}

func StartCodexAppServer(ctx context.Context, executable string, environment []string) (*Process, error) {
	if !hasEnvironmentVariable(environment, "CODEX_HOME") {
		return nil, ErrMissingCodexHome
	}
	return StartProcess(ctx, executable, []string{"app-server", "--listen", "stdio://"}, environment)
}

func hasEnvironmentVariable(environment []string, name string) bool {
	prefix := name + "="
	for _, entry := range environment {
		if len(entry) > len(prefix) && entry[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func (p *Process) Done() <-chan struct{} {
	return p.done
}

func (p *Process) Close() error {
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("stop App Server: %w", err)
		}
	}
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.wait != nil {
		if _, ok := p.wait.(*exec.ExitError); ok {
			return nil
		}
		return p.wait
	}
	return nil
}
