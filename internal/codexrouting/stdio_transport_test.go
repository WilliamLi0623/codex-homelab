package codexrouting

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCodexAppServerTransportInitializesBeforeReadingRateLimits(t *testing.T) {
	helper, marker := buildStdioTransportHelper(t, "success")
	environment := helperEnvironment(t, marker, "success")
	transport := CodexAppServerTransport{Executable: helper, Environment: environment}

	snapshot, err := (AppServerQuotaClient{Transport: transport}).Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if snapshot.OrdinaryUsageAllowed == nil || !*snapshot.OrdinaryUsageAllowed {
		t.Fatalf("OrdinaryUsageAllowed = %v, want true", snapshot.OrdinaryUsageAllowed)
	}
	if snapshot.RateLimitReachedType != "" {
		t.Fatalf("RateLimitReachedType = %q, want empty", snapshot.RateLimitReachedType)
	}
	assertMarkerLines(t, marker, []string{"initialize", "initialized", accountRateLimitsReadMethod})
}

func TestCodexAppServerTransportRejectsMalformedResponse(t *testing.T) {
	helper, marker := buildStdioTransportHelper(t, "malformed")
	transport := CodexAppServerTransport{Executable: helper, Environment: helperEnvironment(t, marker, "malformed")}

	_, err := transport.Call(context.Background(), accountRateLimitsReadMethod, struct{}{})
	if err == nil {
		t.Fatal("Call() succeeded for malformed JSON-RPC response")
	}
	if strings.Contains(err.Error(), "ordinaryUsageAllowed") || strings.Contains(err.Error(), "rateLimits") {
		t.Fatalf("Call() exposed response payload in error: %v", err)
	}
}

func TestCodexAppServerTransportRejectsMissingResponse(t *testing.T) {
	helper, marker := buildStdioTransportHelper(t, "missing")
	transport := CodexAppServerTransport{Executable: helper, Environment: helperEnvironment(t, marker, "missing")}

	_, err := transport.Call(context.Background(), accountRateLimitsReadMethod, struct{}{})
	if err == nil {
		t.Fatal("Call() succeeded without a JSON-RPC response")
	}
	if strings.Contains(err.Error(), "ordinaryUsageAllowed") || strings.Contains(err.Error(), "rateLimits") {
		t.Fatalf("Call() exposed response payload in error: %v", err)
	}
}

func TestCodexAppServerTransportDoesNotExposeAppServerErrorText(t *testing.T) {
	helper, marker := buildStdioTransportHelper(t, "error")
	transport := CodexAppServerTransport{Executable: helper, Environment: helperEnvironment(t, marker, "error")}

	_, err := transport.Call(context.Background(), accountRateLimitsReadMethod, struct{}{})
	if err == nil {
		t.Fatal("Call() succeeded for JSON-RPC error response")
	}
	if strings.Contains(err.Error(), "account-secret") || strings.Contains(err.Error(), "private-limit") {
		t.Fatalf("Call() exposed App Server error text: %v", err)
	}
}

func TestCodexAppServerTransportHonorsCancellationAndCleansUpChild(t *testing.T) {
	helper, marker := buildStdioTransportHelper(t, "hang")
	transport := CodexAppServerTransport{Executable: helper, Environment: helperEnvironment(t, marker, "hang")}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	_, err := transport.Call(ctx, accountRateLimitsReadMethod, struct{}{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("Call() took %s after cancellation", elapsed)
	}
	if !markerContains(t, marker, accountRateLimitsReadMethod) {
		t.Fatalf("helper marker does not show rate-limits request: %s", readMarker(t, marker))
	}
}

func TestCodexAppServerTransportStopsChildAfterSuccessfulCall(t *testing.T) {
	helper, marker := buildStdioTransportHelper(t, "exit")
	transport := CodexAppServerTransport{Executable: helper, Environment: helperEnvironment(t, marker, "exit")}

	if _, err := transport.Call(context.Background(), accountRateLimitsReadMethod, struct{}{}); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !markerContains(t, marker, "exiting") {
		t.Fatalf("helper did not reach exit after successful response: %s", readMarker(t, marker))
	}
}

func helperEnvironment(t *testing.T, marker, mode string) []string {
	t.Helper()
	return []string{
		"CODEX_HOME=" + t.TempDir(),
		"CODEX_WORKSPACE=" + t.TempDir(),
		"P27_STDIO_HELPER_MARKER=" + marker,
		"P27_STDIO_HELPER_MODE=" + mode,
	}
}

func buildStdioTransportHelper(t *testing.T, mode string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "helper.go")
	binaryName := "stdio-helper"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binary := filepath.Join(dir, binaryName)
	if mode == "" {
		t.Fatal("helper mode is required")
	}
	if err := os.WriteFile(source, []byte(stdioTransportHelperSource), 0600); err != nil {
		t.Fatalf("write helper source: %v", err)
	}
	command := exec.Command("go", "build", "-o", binary, source)
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, output)
	}
	marker := filepath.Join(dir, "requests.log")
	return binary, marker
}

func assertMarkerLines(t *testing.T, marker string, want []string) {
	t.Helper()
	got := strings.Fields(readMarker(t, marker))
	if len(got) < len(want) {
		t.Fatalf("helper request sequence = %v, want prefix %v", got, want)
	}
	for i, expected := range want {
		if got[i] != expected {
			t.Fatalf("helper request sequence = %v, want prefix %v", got, want)
		}
	}
}

func markerContains(t *testing.T, marker, want string) bool {
	t.Helper()
	return strings.Contains(readMarker(t, marker), want)
}

func readMarker(t *testing.T, marker string) string {
	t.Helper()
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read helper marker: %v", err)
	}
	return string(data)
}

const stdioTransportHelperSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	marker := os.Getenv("P27_STDIO_HELPER_MARKER")
	mode := os.Getenv("P27_STDIO_HELPER_MODE")
	appendMarker := func(value string) {
		file, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintln(file, value)
		_ = file.Close()
	}
	write := func(value any) {
		encoded, err := json.Marshal(value)
		if err == nil {
			_, _ = fmt.Fprintln(os.Stdout, string(encoded))
		}
	}

	scanner := bufio.NewScanner(os.Stdin)
	initialized := false
	for scanner.Scan() {
		var request struct {
			ID     int    ` + "`json:\"id\"`" + `
			Method string ` + "`json:\"method\"`" + `
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		appendMarker(request.Method)
		switch request.Method {
		case "initialize":
			initialized = true
			write(map[string]any{"id": request.ID, "result": map[string]any{}})
		case "initialized":
			// Notification: no response.
		case "account/rateLimits/read":
			if !initialized {
				write(map[string]any{"id": request.ID, "error": map[string]string{"message": "read before initialize"}})
				continue
			}
			switch mode {
			case "error":
				write(map[string]any{"id": request.ID, "error": map[string]string{"message": "account-secret private-limit"}})
				return
			case "malformed":
				_, _ = fmt.Fprintln(os.Stdout, "{malformed")
				return
			case "missing":
				return
			case "hang":
				for {
					time.Sleep(time.Second)
				}
			case "exit":
				write(map[string]any{"id": request.ID, "result": map[string]any{"ordinaryUsageAllowed": true, "rateLimits": map[string]any{}}})
				appendMarker("exiting")
				return
			default:
				write(map[string]any{"id": request.ID, "result": map[string]any{"ordinaryUsageAllowed": true, "rateLimits": map[string]any{}}})
				return
			}
		}
	}
}
`
