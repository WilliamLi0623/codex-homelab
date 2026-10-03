package codexsession

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const sshBootstrapHelperMaxBytes = 32 << 20

const (
	sshBootstrapHelperInstallTimeout = 2 * time.Minute
	sshBootstrapHelperObserveTimeout = 30 * time.Second
)

type sshBootstrapHelperLayout struct {
	root     string
	launcher string
}

var productionSSHBootstrapHelperLayout = sshBootstrapHelperLayout{
	root:     "/opt/codex-bootstrap-helper",
	launcher: "/usr/local/libexec/codex-artifact-install",
}

// RunSSHBootstrapHelperInstall uploads and verifies one immutable bootstrap
// helper generation over the existing pinned SSH transport.
func RunSSHBootstrapHelperInstall(ctx context.Context, config SSHAppServerConfig, generationDigest, helperSHA256 string, body []byte) (string, error) {
	return runSSHBootstrapHelperInstallWithLayout(ctx, config, generationDigest, helperSHA256, body, productionSSHBootstrapHelperLayout)
}

// RunSSHBootstrapHelperObserve checks a previously staged helper without
// changing remote filesystem state.
func RunSSHBootstrapHelperObserve(ctx context.Context, config SSHAppServerConfig, generationDigest, helperSHA256 string, size int64) (string, error) {
	return runSSHBootstrapHelperObserveWithLayout(ctx, config, generationDigest, helperSHA256, size, productionSSHBootstrapHelperLayout)
}

func runSSHBootstrapHelperInstallWithLayout(ctx context.Context, config SSHAppServerConfig, generationDigest, helperSHA256 string, body []byte, layout sshBootstrapHelperLayout) (string, error) {
	if runtime.GOOS != "linux" || ctx == nil {
		return "", errors.New("pinned bootstrap helper install requires a Linux Controller and context")
	}
	if !p28ArtifactDigestPattern.MatchString(generationDigest) || !p28ArtifactDigestPattern.MatchString(helperSHA256) || len(body) == 0 || len(body) > sshBootstrapHelperMaxBytes {
		return "", errors.New("pinned bootstrap helper install input is invalid")
	}
	actualHash := sha256.Sum256(body)
	if hex.EncodeToString(actualHash[:]) != helperSHA256 {
		return "", errors.New("pinned bootstrap helper body hash does not match")
	}
	return runSSHBootstrapHelperCommand(ctx, config, generationDigest, helperSHA256, int64(len(body)), bytes.NewReader(body), "install", layout)
}

func runSSHBootstrapHelperObserveWithLayout(ctx context.Context, config SSHAppServerConfig, generationDigest, helperSHA256 string, size int64, layout sshBootstrapHelperLayout) (string, error) {
	if runtime.GOOS != "linux" || ctx == nil {
		return "", errors.New("pinned bootstrap helper observe requires a Linux Controller and context")
	}
	if !p28ArtifactDigestPattern.MatchString(generationDigest) || !p28ArtifactDigestPattern.MatchString(helperSHA256) || size <= 0 || size > sshBootstrapHelperMaxBytes {
		return "", errors.New("pinned bootstrap helper observation input is invalid")
	}
	return runSSHBootstrapHelperCommand(ctx, config, generationDigest, helperSHA256, size, strings.NewReader(""), "observe", layout)
}

func runSSHBootstrapHelperCommand(ctx context.Context, config SSHAppServerConfig, generationDigest, helperSHA256 string, size int64, input io.Reader, operation string, layout sshBootstrapHelperLayout) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", errors.New("pinned bootstrap helper request was cancelled")
	}
	executable, args, environment, err := BuildSSHArtifactInstallCommand(config, generationDigest, os.Environ())
	if err != nil {
		return "", errors.New("pinned bootstrap helper SSH configuration is invalid")
	}
	if len(args) < 2 || args[len(args)-2] != "/usr/local/libexec/codex-artifact-install" || args[len(args)-1] != generationDigest {
		return "", errors.New("pinned bootstrap helper SSH command is invalid")
	}
	script := bootstrapHelperReceiverScript(layout)
	remoteCommand := "/bin/sh -c " + shellQuoteSSHRemote(script) + " codex-bootstrap-helper " + operation + " " + generationDigest + " " + helperSHA256 + " " + fmt.Sprint(size)
	args = append(args[:len(args)-2], remoteCommand)

	timeout := sshBootstrapHelperObserveTimeout
	if operation == "install" {
		timeout = sshBootstrapHelperInstallTimeout
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, executable, args...)
	command.Env = environment
	command.Stdin = input
	command.Stderr = io.Discard
	var stdout boundedSSHBootstrapHelperOutput
	command.Stdout = &stdout
	if err := command.Run(); err != nil {
		return "", errors.New("pinned bootstrap helper request did not complete")
	}
	want := "P28_HELPER_COMPLETE:" + generationDigest + "\n"
	if stdout.overflow || stdout.data.String() != want {
		return "", errors.New("pinned bootstrap helper acknowledgment is invalid")
	}
	return generationDigest, nil
}

type boundedSSHBootstrapHelperOutput struct {
	data     bytes.Buffer
	overflow bool
}

func (w *boundedSSHBootstrapHelperOutput) Write(data []byte) (int, error) {
	if w.overflow || len(data) > 128-w.data.Len() {
		w.overflow = true
		return 0, errors.New("bootstrap helper acknowledgment exceeds limit")
	}
	return w.data.Write(data)
}

func bootstrapHelperReceiverScript(layout sshBootstrapHelperLayout) string {
	root := shellQuoteSSHRemote(layout.root)
	launcher := shellQuoteSSHRemote(layout.launcher)
	return `set -eu
umask 077
die() { exit 1; }
uid=$(/usr/bin/id -u) || die
[ "$uid" = 0 ] || die
operation=$1
generation_digest=$2
helper_sha256=$3
expected_size=$4
case "$operation" in install|observe) ;; *) die ;; esac
case "$generation_digest" in *[!0-9a-f]*|'') die ;; esac
[ "${#generation_digest}" -eq 64 ] || die
case "$helper_sha256" in *[!0-9a-f]*|'') die ;; esac
[ "${#helper_sha256}" -eq 64 ] || die
case "$expected_size" in *[!0-9]*|'') die ;; esac
[ "${#expected_size}" -le 8 ] || die
[ "$expected_size" -gt 0 ] && [ "$expected_size" -le 33554432 ] || die
root=` + root + `
launcher=` + launcher + `
generation="$root/$generation_digest"
helper="$generation/codex-artifact-install"
launcher_parent=${launcher%/*}
check_directory() {
  check_path=$1
  allow_sticky=$2
  [ ! -L "$check_path" ] || die
  [ -d "$check_path" ] || die
  check_stat=$(/usr/bin/stat -c '%u %a' -- "$check_path") || die
  check_owner=${check_stat%% *}
  check_mode=${check_stat#* }
  [ "$check_owner" = 0 ] || die
  check_perm=$((0$check_mode))
  if [ $((check_perm & 0022)) -ne 0 ]; then
    [ "$allow_sticky" = yes ] || die
    [ "$check_path" = /tmp ] || [ "$check_path" = /var/tmp ] || die
    [ $((check_perm & 01000)) -ne 0 ] || die
  fi
}
check_ancestors() {
  check_rest=${1#/}
  check_current=
  check_directory / no
  while [ -n "$check_rest" ]; do
    check_part=${check_rest%%/*}
    check_current="$check_current/$check_part"
    check_allow=no
    if [ "$check_current" = /tmp ] || [ "$check_current" = /var/tmp ]; then
      check_allow=yes
    fi
    check_directory "$check_current" "$check_allow"
    if [ "$check_rest" = "$check_part" ]; then
      check_rest=
    else
      check_rest=${check_rest#*/}
    fi
  done
}
ensure_directory_path() {
  ensure_rest=${1#/}
  ensure_current=
  check_directory / no
  while [ -n "$ensure_rest" ]; do
    ensure_part=${ensure_rest%%/*}
    ensure_current="$ensure_current/$ensure_part"
    ensure_allow=no
    if [ "$ensure_current" = /tmp ] || [ "$ensure_current" = /var/tmp ]; then
      ensure_allow=yes
    fi
    [ ! -L "$ensure_current" ] || die
    if [ -d "$ensure_current" ]; then
      check_directory "$ensure_current" "$ensure_allow"
    else
      [ ! -e "$ensure_current" ] || die
      /usr/bin/mkdir -m 0700 -- "$ensure_current" || die
      ensure_parent=${ensure_current%/*}
      [ -n "$ensure_parent" ] || ensure_parent=/
      /usr/bin/sync -f "$ensure_parent" || die
      /usr/bin/sync -f "$ensure_current" || die
      check_directory "$ensure_current" no
    fi
    if [ "$ensure_rest" = "$ensure_part" ]; then
      ensure_rest=
    else
      ensure_rest=${ensure_rest#*/}
    fi
  done
}
case "$operation" in
  install)
    ensure_directory_path "$root"
    ensure_directory_path "$launcher_parent"
    ;;
  observe)
    check_ancestors "$root"
    check_ancestors "$launcher_parent"
    ;;
esac
verify_hash() {
  verify_file=$1
  verify_expected=$2
  verify_line=$(/usr/bin/sha256sum < "$verify_file") || die
  verify_actual=${verify_line%% *}
  [ "$verify_actual" = "$verify_expected" ] || die
}
verify_file_metadata() {
  verify_file=$1
  verify_mode=$2
  [ ! -L "$verify_file" ] || die
  [ -f "$verify_file" ] || die
  verify_stat=$(/usr/bin/stat -c '%u %a %s' -- "$verify_file") || die
  set -- $verify_stat
  [ "$1" = 0 ] && [ "$2" = "$verify_mode" ] && [ "$3" = "$expected_size" ] || die
}
case "$operation" in
  install)
    [ ! -e "$generation" ] && [ ! -L "$generation" ] || die
    /usr/bin/mkdir -m 700 -- "$generation" || die
    /usr/bin/sync -f "$root" || die
    [ ! -e "$helper" ] && [ ! -L "$helper" ] || die
    (set -C; : > "$helper") || die
    /usr/bin/head -c "$((expected_size + 1))" > "$helper" || die
    written_size=$(/usr/bin/wc -c < "$helper") || die
    written_size=$(printf '%s' "$written_size" | /usr/bin/tr -d '[:space:]')
    [ "$written_size" = "$expected_size" ] || die
    verify_hash "$helper" "$helper_sha256"
    /usr/bin/sync -f "$helper" || die
    /usr/bin/chmod 0700 -- "$helper" || die
    /usr/bin/sync -f "$helper" || die
    verify_file_metadata "$helper" 700
    /usr/bin/ln -s -- "$helper" "$launcher" || die
    /usr/bin/sync -f "$generation" || die
    /usr/bin/sync -f "$launcher_parent" || die
    [ -L "$launcher" ] || die
    [ "$(/usr/bin/readlink -- "$launcher")" = "$helper" ] || die
    ;;
  observe)
    [ -d "$generation" ] && [ ! -L "$generation" ] || die
    generation_stat=$(stat -c '%u %a' -- "$generation") || die
    [ "$generation_stat" = "0 700" ] || die
    verify_file_metadata "$helper" 700
    verify_hash "$helper" "$helper_sha256"
    [ -L "$launcher" ] || die
    [ "$(/usr/bin/readlink -- "$launcher")" = "$helper" ] || die
    ;;
esac
printf 'P28_HELPER_COMPLETE:%s\n' "$generation_digest"
`
}

func shellQuoteSSHRemote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
