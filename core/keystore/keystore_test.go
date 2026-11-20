package keystore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func exercise(t *testing.T, s Store) {
	t.Helper()
	if _, err := s.Get(DeviceSeed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on empty store: err = %v, want ErrNotFound", err)
	}
	want := []byte("0123456789abcdef0123456789abcdef")
	if err := s.Set(DeviceSeed, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(DeviceSeed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Get = %x, want %x", got, want)
	}
}

func TestMemory(t *testing.T) { exercise(t, Memory{}) }

func TestFileRoundTripAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	f, err := NewFile(path, "correct horse", 1000)
	if err != nil {
		t.Fatal(err)
	}
	exercise(t, f)

	// 新实例从磁盘读取，验证持久化。
	f2, _ := NewFile(path, "correct horse", 1000)
	if _, err := f2.Get(DeviceSeed); err != nil {
		t.Fatalf("reopened store: %v", err)
	}
	// 文件里不能出现明文。
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "0123456789abcdef") {
		t.Fatal("plaintext secret found in key file")
	}
}

func TestFileWrongPassphrase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	f, _ := NewFile(path, "right", 1000)
	if err := f.Set(DiscoveryKey, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	wrong, _ := NewFile(path, "wrong", 1000)
	if _, err := wrong.Get(DiscoveryKey); err == nil || !strings.Contains(err.Error(), "wrong passphrase") {
		t.Fatalf("expected wrong-passphrase error, got %v", err)
	}
}

func TestFileRequiresPassphrase(t *testing.T) {
	if _, err := NewFile(filepath.Join(t.TempDir(), "k.json"), "", 0); err == nil {
		t.Fatal("expected error for empty passphrase")
	}
}

func TestOSWithMockKeyring(t *testing.T) {
	keyring.MockInit()
	o := NewOS(t.TempDir())
	if !o.Available() {
		t.Fatal("mock keyring should be available")
	}
	exercise(t, o)
	if NewOS("/a").service == NewOS("/b").service {
		t.Fatal("different data dirs must map to different services")
	}
}

// 首次写入失败后在同一个实例上重试，不能用旧盐派生的密钥加密、却把新盐写进文件。
func TestFileRetryAfterFailedFirstWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	f, _ := NewFile(path, "pw", 1000)
	// 让临时文件路径被一个目录占住，第一次 Set 必然失败。
	if err := os.Mkdir(path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(DeviceSeed, []byte("first")); err == nil {
		t.Fatal("expected the first write to fail")
	}
	if err := os.Remove(path + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(DeviceSeed, []byte("second")); err != nil {
		t.Fatal(err)
	}
	reopened, _ := NewFile(path, "pw", 1000)
	got, err := reopened.Get(DeviceSeed)
	if err != nil || string(got) != "second" {
		t.Fatalf("reopened Get = %q, %v", got, err)
	}
}
