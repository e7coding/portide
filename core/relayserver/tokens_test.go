package relayserver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"portide/core/relay"
)

func TestTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	f := OpenTokens(path)
	if _, ok := f.Check(make([]byte, relay.TokenSize)); ok {
		t.Fatal("an empty token file must reject every token")
	}
	e1, tok1, err := f.Add("笔记本")
	if err != nil {
		t.Fatal(err)
	}
	_, tok2, _ := f.Add("手机")
	if id, ok := f.Check(tok1); !ok || id != e1.ID {
		t.Fatalf("Check(tok1) = %q %v", id, ok)
	}
	// 文件里只有哈希，没有令牌本身。
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), string(tok1)) {
		t.Fatal("the token itself must not be stored")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v", st.Mode().Perm())
	}

	// 另一个进程（命令行）吊销令牌：正在运行的中继读到变化。
	cli := OpenTokens(path)
	if err := cli.Revoke(e1.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Check(tok1); ok || f.Valid(e1.ID) {
		t.Fatal("a revoked token must stop working")
	}
	if _, ok := f.Check(tok2); !ok {
		t.Fatal("other tokens keep working")
	}
	if err := cli.Revoke("nope"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("revoking an unknown id: err = %v", err)
	}
	list, _ := f.List()
	if len(list) != 1 || list[0].Name != "手机" {
		t.Fatalf("list = %+v", list)
	}
	// 快速吊销 + 添加：大小相同（"手机"和"平板"都是 6 字节 UTF-8）、mtime 也改回原样，
	// 只有文件本身换了（os.Rename）。f 必须按文件身份（os.SameFile）察觉变化，不能沿用旧内容。
	beforeStat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Revoke(list[0].ID); err != nil { // 吊销"手机"，即 tok2
		t.Fatal(err)
	}
	_, tok3, err := cli.Add("平板")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, beforeStat.ModTime(), beforeStat.ModTime()); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Size() != beforeStat.Size() {
		t.Fatalf("size changed (%d → %d): the test no longer isolates the file-identity check", beforeStat.Size(), st.Size())
	}
	if _, ok := f.Check(tok2); ok {
		t.Fatal("tok2 was revoked in a replaced file of the same size and mtime; the relay must not keep using the old content")
	}
	if _, ok := f.Check(tok3); !ok {
		t.Fatal("the token added in the replaced file must work")
	}
}

// 读不了令牌文件时必须按无效处理（宁可断开，也不能让已吊销的令牌继续有效），并通过 Err 报告问题。
func TestTokenFileUnreadableFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受文件权限限制，跳过")
	}
	path := filepath.Join(t.TempDir(), "tokens.json")
	f := OpenTokens(path)
	e, _, err := f.Add("笔记本")
	if err != nil {
		t.Fatal(err)
	}
	if !f.Valid(e.ID) || f.Err() != nil {
		t.Fatalf("before chmod: valid=%v err=%v", f.Valid(e.ID), f.Err())
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if f.Valid(e.ID) {
		t.Fatal("Valid must return false when the token file cannot be read")
	}
	if _, ok := f.Check(make([]byte, relay.TokenSize)); ok {
		t.Fatal("Check must reject every token when the file cannot be read")
	}
	if f.Err() == nil {
		t.Fatal("Err must report the read failure")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if !f.Valid(e.ID) || f.Err() != nil {
		t.Fatalf("after chmod back: valid=%v err=%v", f.Valid(e.ID), f.Err())
	}
}
