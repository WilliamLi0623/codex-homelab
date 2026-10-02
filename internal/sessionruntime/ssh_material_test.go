package sessionruntime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func publicMaterialFixture(t *testing.T) string {
	t.Helper()
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blob := make([]byte, 4+11+4+32)
	binary.BigEndian.PutUint32(blob, 11)
	copy(blob[4:], "ssh-ed25519")
	binary.BigEndian.PutUint32(blob[15:], 32)
	copy(blob[19:], key)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)
}

func materialRegistryFixture(t *testing.T) (*SSHMaterialRegistry, store.SessionRuntimeBinding) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions and installed OpenSSH keygen required; covered by frozen Linux tests")
	}
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Fatal("Linux bootstrap requires installed ssh-keygen")
	}
	keygen, err = filepath.EvalSymlinks(keygen)
	if err != nil {
		t.Fatalf("resolve installed ssh-keygen path: %v", err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	registry, err := NewSSHMaterialRegistry(root, keygen)
	if err != nil {
		t.Fatal(err)
	}
	return registry, store.SessionRuntimeBinding{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-a", VMID: 4002}
}

func TestSSHMaterialPreparePinAndReopen(t *testing.T) {
	registry, binding := materialRegistryFixture(t)
	material, err := registry.Prepare(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Load(binding); err == nil {
		t.Fatal("untrusted first connection accepted before host pin")
	}
	if _, err := registry.Prepare(context.Background(), binding); !errors.Is(err, ErrSSHMaterialExists) {
		t.Fatalf("existing intent allowed key rotation: %v", err)
	}
	hostKey := publicMaterialFixture(t)
	if err := registry.Pin(binding, hostKey); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSSHMaterialRegistry(registry.root, registry.keygen)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Load(binding)
	if err != nil || got.Alias != material.Alias || got.IdentityFile != material.IdentityFile {
		t.Fatalf("material continuity error=%v", err)
	}
	contents, err := os.ReadFile(got.KnownHostsFile)
	if err != nil || string(contents) != got.Alias+" "+hostKey+"\n" {
		t.Fatal("pin not associated with the exact alias/key")
	}
	if err := registry.Pin(binding, publicMaterialFixture(t)); !errors.Is(err, ErrSSHMaterialExists) {
		t.Fatalf("pin replacement allowed: %v", err)
	}
	other := binding
	other.Generation = "gen-b"
	if _, err := registry.Load(other); err == nil {
		t.Fatal("old generation inherited key/pin")
	}
}

func TestSSHMaterialRejectsTamperingAndSymlinks(t *testing.T) {
	for _, kind := range []string{"private replaced", "public replaced", "manifest changed", "pin replaced", "pin insecure", "pin symlink"} {
		t.Run(kind, func(t *testing.T) {
			registry, binding := materialRegistryFixture(t)
			material, err := registry.Prepare(context.Background(), binding)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Pin(binding, publicMaterialFixture(t)); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "private replaced":
				err = os.WriteFile(material.IdentityFile, []byte("synthetic invalid key"), 0600)
			case "public replaced":
				err = os.WriteFile(material.IdentityFile+".pub", []byte(publicMaterialFixture(t)+"\n"), 0600)
			case "manifest changed":
				err = os.WriteFile(filepath.Join(filepath.Dir(material.IdentityFile), "material.json"), []byte("{}"), 0600)
			case "pin replaced":
				err = os.WriteFile(material.KnownHostsFile, []byte(material.Alias+" "+publicMaterialFixture(t)+"\n"), 0600)
			case "pin insecure":
				err = os.Chmod(material.KnownHostsFile, 0644)
			case "pin symlink":
				original := material.KnownHostsFile + ".retained"
				err = os.Rename(material.KnownHostsFile, original)
				if err == nil {
					err = os.Symlink(original, material.KnownHostsFile)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Load(binding); !errors.Is(err, ErrSSHMaterialInvalid) {
				t.Fatalf("tampered material accepted: %v", err)
			}
		})
	}
}

func TestSSHMaterialRejectsInvalidPublicKeys(t *testing.T) {
	registry, binding := materialRegistryFixture(t)
	material, err := registry.Prepare(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "ssh-ed25519 AAAA", publicMaterialFixture(t) + "\nother", material.ClientPublicKey, "ssh-rsa " + strings.Repeat("a", 80)} {
		if err := registry.Pin(binding, key); !errors.Is(err, ErrSSHMaterialInvalid) {
			t.Fatalf("invalid/shared key accepted: %v", err)
		}
	}
}

func TestSSHMaterialRejectsRootSymlink(t *testing.T) {
	registry, _ := materialRegistryFixture(t)
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(registry.root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSSHMaterialRegistry(link, registry.keygen); err == nil {
		t.Fatal("symlink root accepted")
	}
}

func TestSSHMaterialRejectsWritableAncestor(t *testing.T) {
	registry, _ := materialRegistryFixture(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "private")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSSHMaterialRegistry(root, registry.keygen); !errors.Is(err, ErrSSHMaterialInvalid) {
		t.Fatalf("untrusted writable parent accepted: %v", err)
	}
}

func TestSSHMaterialPrepareFailureNeverRetries(t *testing.T) {
	registry, binding := materialRegistryFixture(t)
	failure, err := NewSSHMaterialRegistry(registry.root, "/bin/false")
	if err != nil {
		// /bin may be a symlink on merged-/usr hosts; use the real approved path.
		failure, err = NewSSHMaterialRegistry(registry.root, "/usr/bin/false")
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = failure.Prepare(context.Background(), binding); !errors.Is(err, ErrSSHMaterialUnknown) {
		t.Fatalf("failed keygen not ambiguous: %v", err)
	}
	if _, err = failure.Prepare(context.Background(), binding); !errors.Is(err, ErrSSHMaterialExists) {
		t.Fatalf("failed keygen replay allowed: %v", err)
	}
	if _, err = failure.Load(binding); !errors.Is(err, ErrSSHMaterialInvalid) {
		t.Fatalf("partial key material accepted: %v", err)
	}
}

func TestSSHMaterialPartialPinNeverBecomesUsable(t *testing.T) {
	registry, binding := materialRegistryFixture(t)
	material, err := registry.Prepare(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(filepath.Dir(material.IdentityFile), "pin.sha256")
	if err := os.WriteFile(marker, []byte("synthetic incomplete marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := registry.Pin(binding, publicMaterialFixture(t)); !errors.Is(err, ErrSSHMaterialUnknown) {
		t.Fatalf("incomplete pin reported complete: %v", err)
	}
	if _, err := registry.Load(binding); !errors.Is(err, ErrSSHMaterialInvalid) {
		t.Fatalf("partial pin accepted: %v", err)
	}
	if err := registry.Pin(binding, publicMaterialFixture(t)); !errors.Is(err, ErrSSHMaterialExists) {
		t.Fatalf("partial pin replayed: %v", err)
	}
}
