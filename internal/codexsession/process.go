package codexsession

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// AppServerProcess owns one isolated Codex App Server child and its JSON-RPC
// client. The process receives CODEX_HOME but not provider API-key variables.
type AppServerProcess struct {
	Client *AppServerClient

	cmd       *exec.Cmd
	stdin     io.WriteCloser
	protocol  *ProtocolClient
	done      chan struct{}
	mu        sync.Mutex
	waitErr   error
	closeOnce sync.Once
	closeErr  error
}

func BuildSafeAppServerEnvironment(environment []string, codexHome string) ([]string, error) {
	codexHome = strings.TrimSpace(codexHome)
	if codexHome == "" || !filepath.IsAbs(codexHome) {
		return nil, errors.New("CODEX_HOME must be an absolute path")
	}
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "USERPROFILE": true,
		"SYSTEMROOT": true, "WINDIR": true, "APPDATA": true, "LOCALAPPDATA": true,
		"TEMP": true, "TMP": true, "TMPDIR": true, "LANG": true, "LC_ALL": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
	}
	if runtime.GOOS != "windows" {
		delete(allowed, "USERPROFILE")
		delete(allowed, "SYSTEMROOT")
		delete(allowed, "WINDIR")
		delete(allowed, "APPDATA")
		delete(allowed, "LOCALAPPDATA")
	}
	result := make([]string, 0, len(allowed)+1)
	seen := make(map[string]bool)
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		key = strings.ToUpper(key)
		if key == "CODEX_HOME" {
			continue
		}
		if allowed[key] && !seen[key] {
			result = append(result, entry)
			seen[key] = true
		}
	}
	result = append(result, "CODEX_HOME="+filepath.Clean(codexHome))
	return result, nil
}

func StartAppServerProcess(ctx context.Context, executable, codexHome string) (*AppServerProcess, error) {
	if strings.TrimSpace(executable) == "" {
		return nil, errors.New("Codex executable is required")
	}
	info, err := os.Stat(codexHome)
	if err != nil || !info.IsDir() {
		return nil, errors.New("CODEX_HOME must be an existing directory")
	}
	environment, err := BuildSafeAppServerEnvironment(os.Environ(), codexHome)
	if err != nil {
		return nil, err
	}
	return startAppServerCommand(ctx, executable, []string{"app-server", "--listen", "stdio://"}, environment)
}

func startAppServerCommand(ctx context.Context, executable string, arguments, environment []string) (*AppServerProcess, error) {
	if strings.TrimSpace(executable) == "" {
		return nil, errors.New("Codex executable is required")
	}
	cmd := exec.CommandContext(ctx, executable, arguments...)
	cmd.Env = append([]string(nil), environment...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open App Server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open App Server stdout: %w", err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start Codex App Server: %w", err)
	}
	protocol := NewProtocolClient(stdout, stdin)
	process := &AppServerProcess{
		Client:   NewAppServerClient(protocol),
		cmd:      cmd,
		stdin:    stdin,
		protocol: protocol,
		done:     make(chan struct{}),
	}
	if err := protocol.Start(); err != nil {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return nil, err
	}
	go func() {
		<-protocol.Done()
		_ = stdin.Close()
		wait := make(chan error, 1)
		go func() { wait <- cmd.Wait() }()
		var err error
		select {
		case err = <-wait:
		case <-time.After(5 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			err = <-wait
		}
		process.mu.Lock()
		process.waitErr = err
		process.mu.Unlock()
		close(process.done)
	}()
	return process, nil
}

func (p *AppServerProcess) Done() <-chan struct{} { return p.done }

func (p *AppServerProcess) Close(ctx context.Context) error {
	p.closeOnce.Do(func() {
		_ = p.stdin.Close()
		select {
		case <-p.done:
		case <-ctx.Done():
			if p.cmd.Process != nil {
				_ = p.cmd.Process.Kill()
			}
			<-p.done
			p.closeErr = ctx.Err()
		}
		_ = p.protocol.Close()
		if p.closeErr == nil {
			p.mu.Lock()
			waitErr := p.waitErr
			p.mu.Unlock()
			if waitErr != nil {
				if _, ok := waitErr.(*exec.ExitError); ok {
					p.closeErr = errors.New("Codex App Server exited with a failure status")
				} else {
					p.closeErr = errors.New("Codex App Server process wait failed")
				}
			}
		}
	})
	return p.closeErr
}
