package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"portide/core/relay"
	"portide/core/relayserver"
)

func runOK(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := run(args, &out); err != nil {
		t.Fatalf("portide-relay %v: %v", args, err)
	}
	return out.String()
}

var configRE = regexp.MustCompile(`portide-relay://\S+`)

func TestInitKeepsTheKey(t *testing.T) {
	dir := t.TempDir()
	first := runOK(t, "--data", dir, "init")
	if !strings.Contains(first, "已生成中继密钥") {
		t.Fatalf("first init = %q", first)
	}
	second := runOK(t, "--data", dir, "init")
	if strings.Contains(second, "已生成") {
		t.Fatal("a second init must keep the existing key")
	}
	fp := strings.TrimSpace(runOK(t, "--data", dir, "fingerprint"))
	if len(fp) != 64 || !strings.Contains(first, fp) {
		t.Fatalf("fingerprint %q not shown by init", fp)
	}
	if st, _ := os.Stat(keyPath(dir)); st.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v", st.Mode().Perm())
	}
}

func TestCorruptKeyIsNotReplaced(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(keyPath(dir), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--data", dir, "init"}, &bytes.Buffer{}); err == nil {
		t.Fatal("a corrupt key file must be reported, not replaced")
	}
	if b, _ := os.ReadFile(keyPath(dir)); string(b) != "short" {
		t.Fatal("the corrupt key file must be left alone")
	}
}

func TestTokenLifecycle(t *testing.T) {
	dir := t.TempDir()
	if err := run([]string{"--data", dir, "token", "add"}, &bytes.Buffer{}); err == nil {
		t.Fatal("token add needs a relay key first")
	}
	runOK(t, "--data", dir, "init")
	out := runOK(t, "--data", dir, "token", "add", "--name", "家里", "--addr", "relay.example.com:7443")
	cfg, err := relay.ParseConfig(configRE.FindString(out))
	if err != nil {
		t.Fatalf("token add printed no valid config string: %q (%v)", out, err)
	}
	if cfg.Addr != "relay.example.com:7443" || strings.Contains(out, "你的服务器地址") {
		t.Fatalf("config = %+v, output %q", cfg, out)
	}
	tokens := relayserver.OpenTokens(tokensPath(dir))
	id, ok := tokens.Check(cfg.Token)
	if !ok {
		t.Fatal("the printed token must be valid")
	}
	if list := runOK(t, "--data", dir, "token", "list"); !strings.Contains(list, id) || !strings.Contains(list, "家里") {
		t.Fatalf("token list = %q", list)
	}
	runOK(t, "--data", dir, "token", "revoke", id)
	if _, ok := tokens.Check(cfg.Token); ok {
		t.Fatal("a revoked token must stop working")
	}
	if err := run([]string{"--data", dir, "token", "revoke", id}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "没有编号") {
		t.Fatalf("revoking twice: err = %v", err)
	}
	// 没有给地址时用占位符并提示。
	if out := runOK(t, "--data", dir, "token", "add"); !strings.Contains(out, "你的服务器地址") {
		t.Fatalf("token add without --addr = %q", out)
	}
}

func TestTCPSettings(t *testing.T) {
	cases := []struct {
		listen, tcpListen string
		set               bool
		tcpPort           string
		wantAddr, wantPub string
	}{
		// 没显式给 --tcp-listen：对外 TCP 端口留空（= 对外 UDP 端口），不从 --listen 推（端口映射时两者不同）
		{":7443", "", false, "", ":7443", ""},
		{"0.0.0.0:9000", "", false, "", "0.0.0.0:9000", ""},
		{":7443", "", false, "443", ":7443", "443"},
		{":7443", "", false, "0443", ":7443", "443"}, // 写成规范形式
		{":7443", "", true, "", "", ""},
		{":7443", "", true, "443", "", "443"},
		{":7443", ":8443", true, "", ":8443", "8443"},
		{":7443", "127.0.0.1:8443", true, "443", "127.0.0.1:8443", "443"},
	}
	for _, c := range cases {
		a, p, err := tcpSettings(c.listen, c.tcpListen, c.set, c.tcpPort)
		if err != nil || a != c.wantAddr || p != c.wantPub {
			t.Errorf("%+v = %q %q %v", c, a, p, err)
		}
	}
	if _, _, err := tcpSettings(":7443", "nonsense", true, ""); err == nil {
		t.Error("an invalid --tcp-listen must be rejected")
	}
	for _, bad := range []string{"0", "65536", "-1", "abc", "443x", " 443"} {
		if _, _, err := tcpSettings(":7443", "", false, bad); err == nil || !strings.Contains(err.Error(), "--tcp-port") {
			t.Errorf("--tcp-port %q: err = %v", bad, err)
		}
	}
}

func TestTokenAddBadTCPPort(t *testing.T) {
	dir := t.TempDir()
	runOK(t, "--data", dir, "init")
	for _, bad := range []string{"0", "70000", "x"} {
		var out bytes.Buffer
		err := run([]string{"--data", dir, "token", "add", "--addr", "relay.example.com:7443", "--tcp-port", bad}, &out)
		if err == nil || !strings.Contains(err.Error(), "--tcp-port") {
			t.Errorf("--tcp-port %q: err = %v out = %q", bad, err, out.String())
		}
	}
	out := runOK(t, "--data", dir, "token", "add", "--addr", "relay.example.com:7443", "--tcp-port", "00443")
	if !strings.Contains(out, "tcp=443") {
		t.Fatalf("--tcp-port must be written canonically: %q", out)
	}
}

func TestTokenAddTCPPort(t *testing.T) {
	dir := t.TempDir()
	runOK(t, "--data", dir, "init")
	out := runOK(t, "--data", dir, "token", "add", "--addr", "relay.example.com:7443", "--tcp-port", "443")
	cfg, err := relay.ParseConfig(configRE.FindString(out))
	if err != nil || cfg.TCPPort != "443" || !strings.Contains(out, "tcp=443") {
		t.Fatalf("cfg = %+v err = %v out = %q", cfg, err, out)
	}
	out = runOK(t, "--data", dir, "token", "add", "--addr", "relay.example.com:7443")
	if strings.Contains(out, "tcp=") {
		t.Fatalf("default token add must not print tcp=: %q", out)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, p, _ := net.SplitHostPort(l.Addr().String())
	return p
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func startServe(t *testing.T, dir string, args ...string) (*syncBuf, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := &syncBuf{}
	done := make(chan error, 1)
	go func() { done <- serve(ctx, dir, args, out) }()
	return out, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("serve did not stop")
		}
	}
}

func waitDial(t *testing.T, network, addr string, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout(network, addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
		}
		if (err == nil) == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s %s: listening = %v expected", network, addr, want)
}

func TestServeListensOnTCP(t *testing.T) {
	port := freePort(t)
	addr := "127.0.0.1:" + port
	out, stop := startServe(t, t.TempDir(), "--listen", addr, "--public-addr", "relay.example.com:"+port, "--tcp-port", "443")
	waitDial(t, "tcp", addr, true)
	stop()
	s := out.String()
	if !strings.Contains(s, "tcp=443") {
		t.Fatalf("first-run config string must carry tcp=443: %q", s)
	}
}

func TestServeTCPDisabled(t *testing.T) {
	port := freePort(t)
	addr := "127.0.0.1:" + port
	out, stop := startServe(t, t.TempDir(), "--listen", addr, "--public-addr", "relay.example.com:"+port, "--tcp-listen", "")
	time.Sleep(500 * time.Millisecond)
	waitDial(t, "tcp", addr, false)
	stop()
	if strings.Contains(out.String(), "tcp=") {
		t.Fatalf("no tcp= when TCP is off: %q", out.String())
	}
}

// Docker 端口映射：容器里监听 7443，对外是 17443（-p 17443:7443/udp -p 17443:7443/tcp）。
// 配置串不能带 tcp=<监听端口>，应和 token add 一样不带 tcp=（TCP 与 UDP 对外端口相同）。
func TestServePortMappingNoTCPParam(t *testing.T) {
	port := freePort(t)
	addr := "127.0.0.1:" + port
	out, stop := startServe(t, t.TempDir(), "--listen", addr, "--public-addr", "relay.example.com:17443")
	waitDial(t, "tcp", addr, true)
	stop()
	s := out.String()
	if strings.Contains(s, "tcp=") {
		t.Fatalf("with port mapping the config string must not carry tcp=<listen port>: %q", s)
	}
	cfg, err := relay.ParseConfig(configRE.FindString(s))
	if err != nil || cfg.TCPAddr() != "relay.example.com:17443" {
		t.Fatalf("cfg = %+v err = %v", cfg, err)
	}
}

func TestServeDefaultConfigHasNoTCPParam(t *testing.T) {
	port := freePort(t)
	addr := "127.0.0.1:" + port
	out, stop := startServe(t, t.TempDir(), "--listen", addr, "--public-addr", "relay.example.com:"+port)
	waitDial(t, "tcp", addr, true)
	stop()
	if strings.Contains(out.String(), "tcp=") {
		t.Fatalf("default config string must not have tcp=: %q", out.String())
	}
}
