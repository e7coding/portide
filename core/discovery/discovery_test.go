package discovery

import (
	"context"
	"net"
	"os"
	"reflect"
	"regexp"
	"testing"
	"time"
)

var (
	key = []byte("discovery-key-for-tests-32-bytes")
	now = time.Unix(1_800_000_000, 0)
)

func TestMemberNameShapeAndRotation(t *testing.T) {
	name := MemberName(key, "LAPTOP", now)
	if !regexp.MustCompile(`^[0-9a-f]{24}$`).MatchString(name) {
		t.Fatalf("name %q is not 24 lowercase hex chars", name)
	}
	if name == MemberName(key, "LAPTOP", now.Add(Window)) {
		t.Fatal("name must change in the next window")
	}
	if name == MemberName(key, "DESKTOP", now) || name == MemberName([]byte("another-key-another-key-32-bytes"), "LAPTOP", now) {
		t.Fatal("name must depend on device and key")
	}
}

func TestIdentify(t *testing.T) {
	members := []string{"LAPTOP", "DESKTOP"}
	if id, ok := Identify(key, members, MemberName(key, "DESKTOP", now.Add(-Window)), now); !ok || id != "DESKTOP" {
		t.Fatalf("previous window: %q %v", id, ok)
	}
	if _, ok := Identify(key, members, MemberName(key, "DESKTOP", now.Add(-2*Window)), now); ok {
		t.Fatal("two windows ago must not match")
	}
	if _, ok := Identify(key, members, MemberName([]byte("outsider-key-outsider-key-32byte"), "DESKTOP", now), now); ok {
		t.Fatal("outsider key must not match")
	}
}

func TestNextRotation(t *testing.T) {
	next := nextRotation(now)
	if !next.After(now) || windowOf(next) != windowOf(now)+1 || next.Sub(now) > Window {
		t.Fatalf("nextRotation(%v) = %v", now, next)
	}
}

// 同网段的地址排在前面：Windows 发布时会带上 VirtualBox、Hyper-V 等虚拟网卡的地址，对端要先试能直接到达的那个。
func TestAddrsPreferLocalSubnet(t *testing.T) {
	_, onLink, _ := net.ParseCIDR("172.16.15.0/24")
	ips := []net.IP{
		net.ParseIP("192.168.56.1"),  // 对端的虚拟网卡
		net.ParseIP("172.16.15.118"), // 与本机同网段
		net.ParseIP("169.254.1.2"),   // IPv4 链路本地：跳过
		net.ParseIP("fe80::1"),       // IPv6 链路本地（需要区域标识）：跳过
		net.ParseIP("2001:db8::5"),
	}
	got := addrs(ips, 7443, []*net.IPNet{onLink})
	want := []string{"172.16.15.118:7443", "192.168.56.1:7443", "[2001:db8::5]:7443"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("addrs = %v, want %v", got, want)
	}
}

// 只发布可以直接拨号的地址：去掉回环和链路本地地址。
func TestUsableIPs(t *testing.T) {
	var in []net.Addr
	for _, c := range []string{"127.0.0.1/8", "169.254.3.4/16", "192.168.1.5/24", "fe80::1/64", "2001:db8::5/64"} {
		ip, n, _ := net.ParseCIDR(c)
		n.IP = ip
		in = append(in, n)
	}
	got := usableIPs(in)
	want := []string{"192.168.1.5", "2001:db8::5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("usableIPs = %v, want %v", got, want)
	}
}

// 发布内容只能有令牌和协议版本：M0 曾因主机名留空、被 dnssd 填成真实主机名而泄露身份。
func TestServiceConfigRevealsNothing(t *testing.T) {
	name := MemberName(key, "LAPTOP", now)
	r := announcement(MemberService, name, 7443, []string{"192.168.1.5"})
	if r.Instance != name || r.Host != name {
		t.Fatalf("instance and host must both be the token: %+v", r)
	}
	if !reflect.DeepEqual(r.Text, []string{"v=1"}) {
		t.Fatalf("TXT must only carry the protocol version: %v", r.Text)
	}
	if r.Service != MemberService || r.Port != 7443 || !reflect.DeepEqual(r.IPs, []string{"192.168.1.5"}) {
		t.Fatalf("unexpected fields: %+v", r)
	}
}

// 需要真实网卡上的组播，默认跳过。
func TestAnnounceBrowse(t *testing.T) {
	if os.Getenv("PORTIDE_MDNS_TEST") != "1" {
		t.Skip("set PORTIDE_MDNS_TEST=1 to run (needs multicast on a real interface)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	name := MemberName(key, "LAPTOP", time.Now())
	go func() { _ = Announce(ctx, MemberService, name, 7443) }()
	found := make(chan Found, 16)
	go func() {
		_ = Browse(ctx, MemberService, func(f Found) {
			select {
			case found <- f:
			default:
			}
		})
	}()
	for {
		select {
		case f := <-found:
			if f.Instance == name {
				if len(f.Addrs) == 0 {
					t.Fatal("no addresses for discovered instance")
				}
				t.Logf("found %s at %v", f.Instance, f.Addrs)
				return
			}
		case <-ctx.Done():
			t.Fatal("not discovered within 10s")
		}
	}
}

func TestLocalIPsExcludeLoopback(t *testing.T) {
	for _, s := range LocalIPs() {
		ip := net.ParseIP(s)
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			t.Fatalf("LocalIPs returned %q", s)
		}
	}
}

// 关闭广播最多等一小会儿：macOS 没有放行组播时，告别包永远发不出去，关闭会一直卡住（退出时整个应用无响应）。
func TestStopWithin(t *testing.T) {
	if !stopWithin(time.Second, func() {}) {
		t.Fatal("a quick stop must report done")
	}
	block := make(chan struct{})
	defer close(block)
	start := time.Now()
	if stopWithin(50*time.Millisecond, func() { <-block }) {
		t.Fatal("a stuck stop must report not done")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("stopWithin waited %v for a stuck stop", d)
	}
}
