package sessionruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestExportBootstrapTLSArchiveStreamsFixedBytesAndReturnsIntegrityAndIsolationEvidence(t *testing.T) {
	archive := []byte("mock-tls-archive-bytes")
	options := &bootstrapHostKeyConsoleOptions{}
	options.terminal = func(conn *websocket.Conn, command string) {
		markers := bootstrapTLSArchiveMarkersFromCommand(t, command)
		script := bootstrapTLSArchiveScriptFromCommand(t, command)
		if !strings.Contains(script, bootstrapTLSArchiveKeyGuestPath) || !strings.Contains(script, bootstrapTLSArchiveCertGuestPath) {
			t.Errorf("fixed export command omitted one of the TLS archive paths")
			return
		}
		output := bootstrapTLSArchiveConsoleTranscript(command, markers, options.identity, archive)
		if err := websocket.Message.Send(conn, []byte(output)); err != nil {
			t.Errorf("send archive fixture: %v", err)
		}
	}
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()

	var destination bytes.Buffer
	result, err := runtime.exportBootstrapTLSArchive(context.Background(), options.binding, &destination)
	if err != nil {
		t.Fatalf("exportBootstrapTLSArchive() error = %v", err)
	}
	if !bytes.Equal(destination.Bytes(), archive) {
		t.Fatalf("streamed bytes = %q, want %q", destination.Bytes(), archive)
	}
	if result.Bytes != int64(len(archive)) || result.SHA256 != "77c8270a3a0afdfaee49014baa5062d5f7db6739a3912dd7fdd5423f280dbd08" || result.IsolationSHA256 == "" {
		t.Fatalf("export evidence = %+v; expected nonzero byte count and integrity/isolation digests", result)
	}
	if options.configCalls != 2 || options.consolePosts != 1 || options.websocketHits != 1 {
		t.Fatalf("config reads=%d termproxy POSTs=%d websocket dials=%d; want two fence proofs around one export", options.configCalls, options.consolePosts, options.websocketHits)
	}
	command := bootstrapTLSArchiveScriptFromCommand(t, options.output)
	for _, required := range []string{"bash -o pipefail", "tar", "base64", bootstrapTLSArchiveKeyGuestPath, bootstrapTLSArchiveCertGuestPath} {
		if !strings.Contains(command, required) {
			t.Errorf("fixed export script omitted %q", required)
		}
	}
	if strings.Contains(command, "VM.Backup") || strings.Contains(command, "/root") || strings.Contains(command, "SSH") {
		t.Fatalf("export script included a prohibited backup or host SSH mechanism")
	}
}

func TestBootstrapTLSArchiveCommandMarkersAlwaysMatchArchiveDecoderAlphabet(t *testing.T) {
	for attempt := 0; attempt < 128; attempt++ {
		markers, _, err := makeBootstrapTLSArchiveCommand()
		if err != nil {
			t.Fatalf("makeBootstrapTLSArchiveCommand(): %v", err)
		}
		for _, marker := range markers {
			if !validArchiveMarker(marker) {
				t.Fatalf("generated marker %q is rejected by the archive decoder", marker)
			}
		}
	}
}

func TestBootstrapSessionImageArchiveCommandUsesExactAllowlistedRoots(t *testing.T) {
	policy := bootstrapArchivePolicySessionImageCredentials
	roots, ok := bootstrapArchivePolicyRoots(policy)
	if !ok || len(roots) != 17 {
		t.Fatalf("Session-image archive roots = %d, valid=%v; want exactly 17", len(roots), ok)
	}
	markers, command, err := makeBootstrapArchiveCommand(policy)
	if err != nil {
		t.Fatalf("makeBootstrapArchiveCommand(SessionImage): %v", err)
	}
	startQuote, endQuote := strings.IndexByte(command, '\''), strings.LastIndexByte(command, '\'')
	if startQuote < 0 || endQuote <= startQuote {
		t.Fatalf("generated command lacks encoded script: %q", command)
	}
	scriptBytes, err := base64.StdEncoding.DecodeString(command[startQuote+1 : endQuote])
	if err != nil {
		t.Fatalf("decode generated script: %v", err)
	}
	script := string(scriptBytes)
	start := strings.Index(script, "tar -C / -cf - -- ")
	if start < 0 {
		t.Fatalf("archive command lacks fixed tar invocation: %q", script)
	}
	end := strings.Index(script[start:], " 2>/dev/null")
	if end < 0 {
		t.Fatalf("archive command lacks bounded tar stderr redirection: %q", script)
	}
	got := strings.Fields(script[start+len("tar -C / -cf - -- ") : start+end])
	if len(got) != len(roots) {
		t.Fatalf("tar roots = %d, want %d: %q", len(got), len(roots), got)
	}
	for i, root := range roots {
		if got[i] != "/"+root {
			t.Fatalf("tar root[%d] = %q, want /%s", i, got[i], root)
		}
	}
	for _, marker := range markers {
		if !validArchiveMarker(marker) {
			t.Fatalf("generated marker %q is rejected by the archive decoder", marker)
		}
	}
	if _, _, err := makeBootstrapArchiveCommand(bootstrapArchivePolicy(255)); err == nil {
		t.Fatal("unknown archive policy unexpectedly produced a guest command")
	}
}

func TestExportBootstrapSessionImageArchiveUsesCombinedPolicy(t *testing.T) {
	archive := []byte("mock-session-image-archive")
	options := &bootstrapHostKeyConsoleOptions{}
	options.terminal = func(conn *websocket.Conn, command string) {
		markers := bootstrapTLSArchiveMarkersFromCommand(t, command)
		script := bootstrapTLSArchiveScriptFromCommand(t, command)
		roots, _ := bootstrapArchivePolicyRoots(bootstrapArchivePolicySessionImageCredentials)
		for _, root := range roots {
			if !strings.Contains(script, " /"+root) {
				t.Errorf("session-image export omitted audited path /%s", root)
				return
			}
		}
		if err := websocket.Message.Send(conn, []byte(bootstrapTLSArchiveConsoleTranscript(command, markers, options.identity, archive))); err != nil {
			t.Errorf("send combined archive fixture: %v", err)
		}
	}
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()
	var destination bytes.Buffer
	result, err := runtime.exportBootstrapSessionImageArchive(context.Background(), options.binding, &destination)
	if err != nil {
		t.Fatalf("exportBootstrapSessionImageArchive() error = %v", err)
	}
	if !bytes.Equal(destination.Bytes(), archive) || result.Bytes != int64(len(archive)) || result.SHA256 == "" || result.IsolationSHA256 == "" {
		t.Fatalf("combined export bytes/evidence = %q / %+v", destination.Bytes(), result)
	}
}

func TestBootstrapTLSArchiveFixedCommandRequiresSuccessfulTarPipeline(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux shell characterization; skipped explicitly on non-Linux hosts")
	}
	for _, tc := range []struct {
		name       string
		tarScript  string
		wantEnd    bool
		wantBase64 string
	}{
		{name: "tar succeeds", tarScript: "#!/bin/sh\nprintf '%s' 'fixture-archive'\nprintf invoked > \"$CODEX_TAR_MARKER\"\n", wantEnd: true, wantBase64: "Zml4dHVyZS1hcmNoaXZl"},
		{name: "tar fails while base64 succeeds", tarScript: "#!/bin/sh\nprintf invoked > \"$CODEX_TAR_MARKER\"\nexit 1\n", wantEnd: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shimDir := t.TempDir()
			tarShim := filepath.Join(shimDir, "tar")
			if err := os.WriteFile(tarShim, []byte(tc.tarScript), 0700); err != nil {
				t.Fatalf("write harmless tar shim: %v", err)
			}
			markerFile := filepath.Join(shimDir, "invoked")
			_, command, err := makeBootstrapTLSArchiveCommand()
			if err != nil {
				t.Fatalf("makeBootstrapTLSArchiveCommand(): %v", err)
			}
			script := bootstrapTLSArchiveScriptFromCommand(t, "0:120:40:"+command)
			process := exec.Command("/bin/sh", "-c", script)
			process.Env = []string{
				"PATH=" + shimDir + string(os.PathListSeparator) + "/usr/bin:/bin",
				"HOME=" + shimDir,
				"LANG=C",
				"CODEX_TAR_MARKER=" + markerFile,
			}
			output, err := process.Output()
			if tc.wantEnd && err != nil {
				t.Fatalf("run fixed harmless command: %v", err)
			}
			if !tc.wantEnd && err == nil {
				t.Fatal("fixed command reported success after the tar shim failed")
			}
			if _, err := os.Stat(markerFile); err != nil {
				t.Fatalf("fixed script did not invoke the controlled tar shim: %v", err)
			}
			if gotEnd := strings.Contains(string(output), extractTLSArchiveEndMarker(t, script)); gotEnd != tc.wantEnd {
				t.Fatalf("end marker present = %v, want %v; output=%q", gotEnd, tc.wantEnd, output)
			}
			if tc.wantBase64 != "" && !strings.Contains(string(output), tc.wantBase64) {
				t.Fatalf("successful fixed command omitted base64 fixture %q", tc.wantBase64)
			}
			if !strings.Contains(string(output), extractTLSArchiveBeginMarker(t, script)) {
				t.Fatal("fixed command omitted its begin marker")
			}
		})
	}
}

func TestExportBootstrapTLSArchiveRejectsUnfencedIdentityAndChangedConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*bootstrapHostKeyConsoleOptions)
		want error
	}{
		{name: "unfenced before console", edit: func(o *bootstrapHostKeyConsoleOptions) { o.unfenced = true }, want: ErrBootstrapHostKeyIsolation},
		{name: "fence lost after console", edit: func(o *bootstrapHostKeyConsoleOptions) { o.loseFenceAfterConsole = true }, want: ErrBootstrapHostKeyIsolation},
		{name: "network configuration changed", edit: func(o *bootstrapHostKeyConsoleOptions) { o.changeConfigAfterConsole = true }, want: ErrBootstrapHostKeyIsolation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := &bootstrapHostKeyConsoleOptions{}
			tc.edit(options)
			options.terminal = bootstrapTLSArchiveSuccessfulTerminal(t, options, []byte("archive"))
			runtime, server := newBootstrapHostKeyRuntime(t, options)
			defer server.Close()
			var destination bytes.Buffer
			_, err := runtime.exportBootstrapTLSArchive(context.Background(), options.binding, &destination)
			if !errors.Is(err, tc.want) {
				t.Fatalf("export error = %v; want %v", err, tc.want)
			}
		})
	}
}

func TestExportBootstrapTLSArchiveRejectsGuestIdentityMismatchBeforePayload(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{identity: [3]string{"0", "Linux", "wrong-host"}}
	options.terminal = bootstrapTLSArchiveSuccessfulTerminal(t, options, []byte("archive"))
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()
	var destination bytes.Buffer
	_, err := runtime.exportBootstrapTLSArchive(context.Background(), options.binding, &destination)
	if !errors.Is(err, ErrConsoleIdentityMismatch) || destination.Len() != 0 {
		t.Fatalf("identity mismatch error=%v bytes written=%d; want rejection before archive payload", err, destination.Len())
	}
}

func TestExportBootstrapTLSArchiveRejectsGenerationChangeAfterConsole(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{}
	options.terminal = func(conn *websocket.Conn, command string) {
		markers := bootstrapTLSArchiveMarkersFromCommand(t, command)
		options.binding.Generation = "rotated-generation"
		_ = websocket.Message.Send(conn, []byte(bootstrapTLSArchiveConsoleTranscript(command, markers, options.identity, []byte("archive"))))
	}
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()
	var destination bytes.Buffer
	_, err := runtime.exportBootstrapTLSArchive(context.Background(), options.binding, &destination)
	if !errors.Is(err, ErrBootstrapHostKeyIsolation) {
		t.Fatalf("generation change error = %v; want isolation rejection", err)
	}
}

func TestExportBootstrapTLSArchiveRequiresGuestToRemainRunning(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{}
	options.terminal = func(conn *websocket.Conn, command string) {
		markers := bootstrapTLSArchiveMarkersFromCommand(t, command)
		options.state = "stopped"
		_ = websocket.Message.Send(conn, []byte(bootstrapTLSArchiveConsoleTranscript(command, markers, options.identity, []byte("archive"))))
	}
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()
	var destination bytes.Buffer
	_, err := runtime.exportBootstrapTLSArchive(context.Background(), options.binding, &destination)
	if !errors.Is(err, ErrConsoleNotRunning) {
		t.Fatalf("post-export guest state error = %v; want running-state rejection", err)
	}
}

func TestExportBootstrapTLSArchiveRejectsDisconnectAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal func(*websocket.Conn, string)
	}{
		{name: "disconnect", terminal: func(conn *websocket.Conn, _ string) { _ = conn.Close() }},
		{name: "truncated base64 stream", terminal: func(conn *websocket.Conn, command string) {
			markers := bootstrapTLSArchiveMarkersFromCommand(t, command)
			partial := bootstrapTLSArchiveConsoleOutput(markers, [3]string{"0", "Linux", sessionHostname("session-a", "epoch-a", "gen-1")}, []byte("archive"))
			partial = strings.TrimSuffix(partial, markers[4]+"\n")
			partial = strings.TrimSuffix(partial, "ZQ==\n")
			_ = websocket.Message.Send(conn, []byte(partial))
			_ = conn.Close()
		}},
		{name: "failed remote command has no success marker", terminal: func(conn *websocket.Conn, command string) {
			markers := bootstrapTLSArchiveMarkersFromCommand(t, command)
			partial := strings.Join([]string{markers[0], "0", markers[1], "Linux", markers[2], sessionHostname("session-a", "epoch-a", "gen-1"), markers[3], ""}, "\n")
			_ = websocket.Message.Send(conn, []byte(partial))
			_ = conn.Close()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := &bootstrapHostKeyConsoleOptions{terminal: tc.terminal}
			runtime, server := newBootstrapHostKeyRuntime(t, options)
			defer server.Close()
			var destination bytes.Buffer
			_, err := runtime.exportBootstrapTLSArchive(context.Background(), options.binding, &destination)
			if err == nil {
				t.Fatal("export unexpectedly accepted incomplete console output")
			}
			for _, secret := range []string{bootstrapHostKeyTestToken, "ticket-secret", "OUTPUT_SECRET", "vncticket=", "writer contains sensitive detail"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked sensitive value %q", secret)
				}
			}
		})
	}
}

func TestExportBootstrapTLSArchiveBoundsConsoleFramesAndClosesOnCancellation(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		term func(*websocket.Conn, string)
		want error
	}{
		{name: "oversized websocket frame", ctx: func() (context.Context, context.CancelFunc) { return context.Background(), func() {} }, term: func(conn *websocket.Conn, _ string) {
			_ = websocket.Message.Send(conn, []byte(strings.Repeat("x", consoleFrameLimit+1)))
		}, want: ErrConsoleOutputLimit},
		{name: "canceled blocked read", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 30*time.Millisecond)
		}, term: func(_ *websocket.Conn, _ string) { time.Sleep(100 * time.Millisecond) }, want: ErrConsoleCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := &bootstrapHostKeyConsoleOptions{terminal: tc.term}
			runtime, server := newBootstrapHostKeyRuntime(t, options)
			defer server.Close()
			ctx, cancel := tc.ctx()
			defer cancel()
			_, err := runtime.exportBootstrapTLSArchive(ctx, options.binding, &bytes.Buffer{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("export error = %v; want %v", err, tc.want)
			}
		})
	}
}

func TestExportBootstrapTLSArchiveRejectsWriterError(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{}
	options.terminal = bootstrapTLSArchiveSuccessfulTerminal(t, options, []byte("archive"))
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()
	_, err := runtime.exportBootstrapTLSArchive(context.Background(), options.binding, failingArchiveWriter{})
	if err == nil {
		t.Fatal("export unexpectedly accepted a failing destination writer")
	}
}

type failingArchiveWriter struct{}

func (failingArchiveWriter) Write([]byte) (int, error) {
	return 0, errors.New("writer contains sensitive detail")
}

func bootstrapTLSArchiveSuccessfulTerminal(t *testing.T, options *bootstrapHostKeyConsoleOptions, archive []byte) func(*websocket.Conn, string) {
	t.Helper()
	return func(conn *websocket.Conn, command string) {
		markers := bootstrapTLSArchiveMarkersFromCommand(t, command)
		_ = websocket.Message.Send(conn, []byte(bootstrapTLSArchiveConsoleTranscript(command, markers, options.identity, archive)))
	}
}

func bootstrapTLSArchiveConsoleTranscript(command string, markers [5]string, identity [3]string, archive []byte) string {
	input := extractTLSArchiveConsoleInputUnchecked(command)
	return "root@guest:~# " + input + "\r\n" + bootstrapTLSArchiveConsoleOutput(markers, identity, archive)
}

func extractTLSArchiveConsoleInputUnchecked(command string) string {
	frame := strings.TrimPrefix(command, "0:")
	_, remaining, ok := strings.Cut(frame, ":")
	if !ok {
		return ""
	}
	_, input, ok := strings.Cut(remaining, ":")
	if !ok {
		return ""
	}
	return strings.TrimSuffix(input, "\n")
}

func extractTLSArchiveBeginMarker(t *testing.T, script string) string {
	t.Helper()
	return bootstrapTLSArchiveMarkersFromScript(t, script)[3]
}

func extractTLSArchiveEndMarker(t *testing.T, script string) string {
	t.Helper()
	return bootstrapTLSArchiveMarkersFromScript(t, script)[4]
}

func bootstrapTLSArchiveMarkersFromScript(t *testing.T, script string) [5]string {
	t.Helper()
	re := regexp.MustCompile(`__CODEX_ARCHIVE_[A-Za-z0-9_-]+__`)
	matches := re.FindAllString(script, -1)
	var markers [5]string
	if len(matches) != len(markers) {
		t.Fatalf("script contains %d archive markers; want %d", len(matches), len(markers))
	}
	copy(markers[:], matches)
	return markers
}

func bootstrapTLSArchiveConsoleOutput(markers [5]string, identity [3]string, archive []byte) string {
	var output strings.Builder
	output.WriteString(markers[0] + "\n" + identity[0] + "\n" + markers[1] + "\n" + identity[1] + "\n" + markers[2] + "\n" + identity[2] + "\n" + markers[3] + "\n")
	encoded := base64.StdEncoding.EncodeToString(archive)
	for len(encoded) > 76 {
		output.WriteString(encoded[:76])
		output.WriteByte('\n')
		encoded = encoded[76:]
	}
	output.WriteString(encoded + "\n" + markers[4] + "\n")
	return output.String()
}

func bootstrapTLSArchiveMarkersFromCommand(t *testing.T, command string) [5]string {
	t.Helper()
	script := bootstrapTLSArchiveScriptFromCommand(t, command)
	re := regexp.MustCompile(`__CODEX_ARCHIVE_[A-Za-z0-9_-]+__`)
	matches := re.FindAllString(script, -1)
	var markers [5]string
	if len(matches) != len(markers) {
		t.Fatalf("export command contains %d archive markers; want %d", len(matches), len(markers))
	}
	copy(markers[:], matches)
	return markers
}

func bootstrapTLSArchiveScriptFromCommand(t *testing.T, command string) string {
	t.Helper()
	frame := strings.TrimPrefix(command, "0:")
	_, encoded, ok := strings.Cut(frame, ":")
	if !ok {
		t.Fatal("console command is not a termproxy input frame")
	}
	start, end := strings.IndexByte(encoded, '\''), strings.LastIndexByte(encoded, '\'')
	if start < 0 || end <= start {
		t.Fatal("console command lacks its fixed base64 script")
	}
	script, err := base64.StdEncoding.DecodeString(encoded[start+1 : end])
	if err != nil {
		t.Fatalf("decode console script: %v", err)
	}
	return string(script)
}
