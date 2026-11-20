package appdir

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"portide/core/keystore"
	"portide/core/store"
)

func noAsk() (string, error) { return "", errors.New("must not ask") }

func TestDataDirPrecedence(t *testing.T) {
	base := t.TempDir()
	t.Setenv("PORTIDE_HOME", filepath.Join(base, "env"))
	if got, _ := DataDir(filepath.Join(base, "flag")); got != filepath.Join(base, "flag") {
		t.Fatalf("flag should win, got %s", got)
	}
	if got, _ := DataDir(""); got != filepath.Join(base, "env") {
		t.Fatalf("PORTIDE_HOME should be used, got %s", got)
	}
}

func TestFileBackendIsRecordedAndReused(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PORTIDE_KEYSTORE", "file")
	t.Setenv("PORTIDE_PASSPHRASE", "pw")
	ks, err := OpenKeys(dir, noAsk)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ks.(*keystore.File); !ok {
		t.Fatalf("backend = %T, want *keystore.File", ks)
	}
	// 之后即使环境变量改了，也沿用 init 时记录的后端。
	t.Setenv("PORTIDE_KEYSTORE", "os")
	ks2, err := OpenKeys(dir, noAsk)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ks2.(*keystore.File); !ok {
		t.Fatalf("recorded backend not reused, got %T", ks2)
	}
	st, _ := store.Open(dir)
	defer st.Close()
	if v, _, _ := st.GetMeta(MetaKeystore); v != "file" {
		t.Fatalf("recorded backend = %q", v)
	}
}

func TestPassphraseImpliesFileBackend(t *testing.T) {
	t.Setenv("PORTIDE_KEYSTORE", "")
	t.Setenv("PORTIDE_PASSPHRASE", "pw")
	ks, err := OpenKeys(t.TempDir(), noAsk)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ks.(*keystore.File); !ok {
		t.Fatalf("backend = %T, want *keystore.File", ks)
	}
}

// 文件密钥库、没有设置 PORTIDE_PASSPHRASE 时：NeedsPassphrase 为真，OpenKeys 向调用方要口令；空口令被拒绝。
func TestFileBackendAsksForPassphrase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PORTIDE_KEYSTORE", "file")
	t.Setenv("PORTIDE_PASSPHRASE", "")
	need, err := NeedsPassphrase(dir)
	if err != nil || !need {
		t.Fatalf("NeedsPassphrase = %v, %v; want true", need, err)
	}
	asked := 0
	ks, err := OpenKeys(dir, func() (string, error) { asked++; return "pw", nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ks.(*keystore.File); !ok || asked != 1 {
		t.Fatalf("backend = %T, asked = %d", ks, asked)
	}
	if _, err := OpenKeys(dir, func() (string, error) { return "", nil }); err == nil {
		t.Fatal("an empty passphrase must be rejected")
	}
	t.Setenv("PORTIDE_PASSPHRASE", "pw")
	if need, _ := NeedsPassphrase(dir); need {
		t.Fatal("PORTIDE_PASSPHRASE set: no passphrase needed")
	}
}

func TestInvalidKeystoreEnv(t *testing.T) {
	t.Setenv("PORTIDE_KEYSTORE", "bogus")
	if _, err := OpenKeys(t.TempDir(), noAsk); err == nil {
		t.Fatal("expected error for an invalid PORTIDE_KEYSTORE")
	}
}

// 数据目录是用更新版本的程序创建的：OpenKeys 直接调用 store.Open，store.ErrNewerDatabase
// 的英文原文会在这里先冒出来，到不了打开设备之后的那层映射。OpenKeys 自己就要把它换成用户能看懂的提示。
func TestOpenKeysRejectsNewerDatabase(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "portide.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := OpenKeys(dir, noAsk); err == nil || err.Error() != "数据目录来自更新版本的 Portide，请升级本程序" {
		t.Fatalf("err = %v", err)
	}
}

func TestMapStoreErrForKeyProblems(t *testing.T) {
	for err, want := range map[error]string{
		store.ErrKeyMissing: "找不到这个数据目录的数据库密钥：数据目录或 PORTIDE_KEYSTORE 是不是换了？",
		store.ErrWrongKey:   "密钥库里的数据库密钥和这个数据目录不匹配：数据目录或 PORTIDE_KEYSTORE 是不是换了？",
	} {
		if got := MapStoreErr(fmt.Errorf("open: %w", err)); got.Error() != want {
			t.Errorf("MapStoreErr(%v) = %q", err, got)
		}
	}
}
