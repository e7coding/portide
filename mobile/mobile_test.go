package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"portide/core/discovery"
	"portide/internal/fixtures"
)

// bonjour 是假的局域网：fakePlatform 发布的实例登记在这里，正在浏览的平台定期收到它们（模拟 Swift 的 Bonjour）。
type bonjour struct {
	mu        sync.Mutex
	announced map[int64]announcement // 全局编号 → 发布内容
	nextID    int64
}

type announcement struct {
	service, instance string
	port              int64
}

// fakePlatform 实现 Platform：Keychain 在内存里，Bonjour 用共享的 bonjour。
type fakePlatform struct {
	net *bonjour

	mu       sync.Mutex
	app      *App
	keys     map[string][]byte
	ann      map[int64]int64  // 本平台的发布编号 → 全局编号
	browsing map[int64]string // 浏览编号 → 服务类型
	changes  map[string]int
	events   []Event
}

func newFakePlatform(n *bonjour) *fakePlatform {
	return &fakePlatform{net: n, keys: map[string][]byte{}, ann: map[int64]int64{}, browsing: map[int64]string{}, changes: map[string]int{}}
}

func (p *fakePlatform) KeyGet(name string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.keys[name], nil
}

func (p *fakePlatform) KeySet(name string, value []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys[name] = append([]byte(nil), value...)
	return nil
}

func (p *fakePlatform) StartAnnounce(id int64, service, instance string, port int64) {
	p.net.mu.Lock()
	p.net.nextID++
	g := p.net.nextID
	p.net.announced[g] = announcement{service, instance, port}
	p.net.mu.Unlock()
	p.mu.Lock()
	p.ann[id] = g
	p.mu.Unlock()
}

func (p *fakePlatform) StopAnnounce(id int64) {
	p.mu.Lock()
	g := p.ann[id]
	delete(p.ann, id)
	p.mu.Unlock()
	p.net.mu.Lock()
	delete(p.net.announced, g)
	p.net.mu.Unlock()
}

func (p *fakePlatform) StartBrowse(id int64, service string) {
	p.mu.Lock()
	p.browsing[id] = service
	p.mu.Unlock()
}

func (p *fakePlatform) StopBrowse(id int64) {
	p.mu.Lock()
	delete(p.browsing, id)
	p.mu.Unlock()
}

func (p *fakePlatform) OnChange(topic string) {
	p.mu.Lock()
	p.changes[topic]++
	p.mu.Unlock()
}

func (p *fakePlatform) OnEvent(eventJSON string) {
	var e Event
	if json.Unmarshal([]byte(eventJSON), &e) != nil {
		panic("bad event JSON: " + eventJSON)
	}
	p.mu.Lock()
	p.events = append(p.events, e)
	p.mu.Unlock()
}

func (p *fakePlatform) Log(line string) {}

// deliver 把假局域网上的实例报给正在浏览的平台，像 Swift 解析出实例后调用 ReportFound 那样（地址用回环）。
func (p *fakePlatform) deliver() {
	p.mu.Lock()
	browsing := map[int64]string{}
	for id, s := range p.browsing {
		browsing[id] = s
	}
	a := p.app
	p.mu.Unlock()
	p.net.mu.Lock()
	var anns []announcement
	for _, an := range p.net.announced {
		anns = append(anns, an)
	}
	p.net.mu.Unlock()
	for id, service := range browsing {
		for _, an := range anns {
			if an.service == service {
				a.ReportFound(id, an.instance, `["127.0.0.1"]`, an.port)
			}
		}
	}
}

// phone 是一台测试用的"手机"：App 加上它的平台，后台不断投递发现结果。
type phone struct {
	app      *App
	platform *fakePlatform
	saveDir  string
}

func newPhone(t *testing.T, n *bonjour) *phone {
	t.Helper()
	p := newFakePlatform(n)
	saveDir := filepath.Join(t.TempDir(), "Documents")
	a := newApp(t.TempDir(), saveDir, p, "127.0.0.1:0")
	p.mu.Lock()
	p.app = a
	p.mu.Unlock()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				p.deliver()
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done; a.Stop() })
	return &phone{app: a, platform: p, saveDir: saveDir}
}

func call[T any](t *testing.T, ph *phone, method string, args ...any) T {
	t.Helper()
	argsJSON := ""
	if len(args) > 0 {
		b, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		argsJSON = string(b)
	}
	out, err := ph.app.call(method, argsJSON)
	if err != nil {
		t.Fatalf("Call(%s, %s): %v", method, argsJSON, err)
	}
	var v T
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("Call(%s) result %q: %v", method, out, err)
	}
	return v
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

type state struct {
	Phase     string `json:"phase"`
	NeedsName bool   `json:"needsName"`
	Name      string `json:"name"`
	KeyID     string `json:"keyId"`
}

type device struct {
	KeyID  string `json:"keyId"`
	Name   string `json:"name"`
	Self   bool   `json:"self"`
	Online bool   `json:"online"`
}

type timelineItem struct {
	Kind      string   `json:"kind"`
	Direction string   `json:"direction"`
	Texts     []string `json:"texts"`
}

// 两台"手机"：经 Call 完成首次启动、用配对码（不给地址，靠 Bonjour 桥找到对方）组成设备圈、互发文本。
func TestTwoPhonesPairAndSendThroughTheBridge(t *testing.T) {
	orderAddrs = func(ips []string, port int) []string {
		var out []string
		for _, ip := range ips {
			out = append(out, net.JoinHostPort(ip, strconv.Itoa(port)))
		}
		return out
	}
	t.Cleanup(func() { orderAddrs = discovery.OrderAddrs })
	n := &bonjour{announced: map[int64]announcement{}}
	a, b := newPhone(t, n), newPhone(t, n)

	if st := call[state](t, a, "State"); st.Phase != "setup" || !st.NeedsName {
		t.Fatalf("first State = %+v (an injected Keychain must skip the passphrase step)", st)
	}
	if out := a.app.Call("Init", `["iPhone"]`); out != `{"result":null}` {
		t.Fatalf("Init = %s", out)
	}
	if st := call[state](t, a, "State"); st.Phase != "running" || st.Name != "iPhone" {
		t.Fatalf("State after Init = %+v (Init must create a solo circle and start the core)", st)
	}
	a.platform.mu.Lock()
	stored := len(a.platform.keys)
	a.platform.mu.Unlock()
	if stored == 0 {
		t.Fatal("secrets were not stored through the platform Keychain")
	}

	pairing := call[struct {
		Code string `json:"code"`
	}](t, a, "StartPairing")
	call[any](t, b, "Init", "iPad")
	if out := b.app.Call("Join", fmt.Sprintf(`[%q, ""]`, pairing.Code)); !strings.HasPrefix(out, `{"result":{"mode":"join",`) { // 不给地址：经桥浏览 _portide-pair._udp 找到 a
		t.Fatalf("Join = %s", out)
	}
	eventually(t, "both online through the bridge", func() bool {
		da := call[[]device](t, a, "Devices")
		db := call[[]device](t, b, "Devices")
		return len(da) == 2 && len(db) == 2 && (da[0].Online || da[1].Online) && (db[0].Online || db[1].Online)
	})

	var target string
	for _, d := range call[[]device](t, a, "Devices") {
		if !d.Self {
			target = d.KeyID
		}
	}
	call[any](t, a, "SendText", []string{target}, "你好，iPad")
	eventually(t, "text arrives on b", func() bool {
		for _, it := range call[[]timelineItem](t, b, "Timeline", "", 50) {
			if it.Direction == "in" && len(it.Texts) == 1 && it.Texts[0] == "你好，iPad" {
				return true
			}
		}
		return false
	})
	eventually(t, "b is notified through the platform", func() bool {
		b.platform.mu.Lock()
		defer b.platform.mu.Unlock()
		for _, e := range b.platform.events {
			if e.Kind == "received" && e.PeerName == "iPhone" && e.Summary == "你好，iPad" && e.HistoryID > 0 {
				return true
			}
		}
		return false
	})
	eventually(t, "change signals reach the platform", func() bool {
		b.platform.mu.Lock()
		defer b.platform.mu.Unlock()
		return b.platform.changes["history"] > 0 && b.platform.changes["devices"] > 0
	})
	if got := call[struct {
		SaveDir string `json:"saveDir"`
	}](t, b, "Settings"); got.SaveDir != b.saveDir {
		t.Fatalf("save dir = %q, want %q", got.SaveDir, b.saveDir)
	}

	// 退到后台再回来：核心重新启动，设备圈还在。
	b.app.Suspend()
	if _, err := b.app.call("Devices", ""); err == nil {
		t.Fatal("Devices must fail while suspended")
	}
	var st state
	if err := json.Unmarshal([]byte(b.app.Resume()), &st); err != nil || st.Phase != "running" {
		t.Fatalf("Resume = %+v, %v", st, err)
	}
	eventually(t, "online again after resume", func() bool {
		for _, d := range call[[]device](t, b, "Devices") {
			if !d.Self && d.Online {
				return true
			}
		}
		return false
	})
}

func TestCallErrors(t *testing.T) {
	ph := newPhone(t, &bonjour{announced: map[int64]announcement{}})
	cases := map[string][2]string{
		"unknown method":   {"NoSuchMethod", ""},
		"not an array":     {"Init", `{"name":"x"}`},
		"wrong arg count":  {"Init", `[]`},
		"wrong arg type":   {"Init", `[42]`},
		"service error":    {"Init", `[""]`},
		"before init join": {"Join", `["1234-5678", ""]`}, // 还没启动：ErrNotRunning
	}
	for name, c := range cases {
		var env struct {
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if err := json.Unmarshal([]byte(ph.app.Call(c[0], c[1])), &env); err != nil || env.Error == "" || env.Result != nil {
			t.Errorf("%s: envelope = %+v, %v; want an error", name, env, err)
		}
	}
	// 服务层的错误原样返回（给用户看的中文）。
	if out := ph.app.Call("Join", `["1234-5678", ""]`); out != `{"error":"Portide 还没有启动"}` {
		t.Fatalf("join before running = %s", out)
	}
	if out := ph.app.Call("Init", `[""]`); out != `{"error":"请填写设备名"}` {
		t.Fatalf("service error = %s", out)
	}
}

// 浏览结束后 Swift 迟到的报告被丢掉，没有可用地址的也丢掉。
func TestBridgeDropsLateAndUnusableReports(t *testing.T) {
	p := newFakePlatform(&bonjour{announced: map[int64]announcement{}})
	b := newBridge(p)
	b.found(99, "x", []string{"127.0.0.1"}, 7443) // 没有这个浏览：什么也不发生
	got := make(chan string, 4)
	browsing := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		close(browsing)
		_ = b.Browse(ctx, "_portide._udp", func(f discovery.Found) { got <- f.Addrs[0] })
	}()
	<-browsing
	eventually(t, "browse registered", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.browsing) == 1
	})
	var id int64
	p.mu.Lock()
	for k := range p.browsing {
		id = k
	}
	p.mu.Unlock()
	b.found(id, "x", []string{"fe80::1", "nonsense"}, 7443) // 没有可用地址
	b.found(id, "x", []string{"127.0.0.1"}, 7443)           // 回环地址也会被 OrderAddrs 丢掉
	b.found(id, "x", []string{"192.0.2.7"}, 7443)
	if a := <-got; a != "192.0.2.7:7443" {
		t.Fatalf("delivered %q", a)
	}
	cancel()
	eventually(t, "browse stopped", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.browsing) == 0
	})
	b.found(id, "x", []string{"192.0.2.8"}, 7443)
	select {
	case a := <-got:
		t.Fatalf("report after the browse ended was delivered: %q", a)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestEncodeResult(t *testing.T) {
	var nilSlice []string
	for _, c := range []struct {
		in   any
		want string
	}{
		{nilSlice, `[]`},
		{[]int{1}, `[1]`},
		{nil, `null`},
		{struct {
			A int `json:"a"`
		}{2}, `{"a":2}`},
	} {
		got, err := encodeResult(c.in)
		if err != nil || string(got) != c.want {
			t.Errorf("encodeResult(%#v) = %s, %v; want %s", c.in, got, err, c.want)
		}
	}
}

func TestIOSEventFixtureMatches(t *testing.T) {
	fixtures.Check(t, "../ios/PortideTests/Fixtures.json", map[string]any{"Event": Event{}})
}
