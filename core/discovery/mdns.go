package discovery

import (
	"context"
	"errors"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/libp2p/zeroconf/v2"
)

const domain = "local."

// txtRecords 是 TXT 记录的全部内容：只有协议版本，不含任何身份信息。
var txtRecords = []string{"v=1"}

// Found 是浏览到的一个服务实例。
type Found struct {
	Instance string
	Addrs    []string // host:port，与本机同网段的 IPv4 在前
}

// record 是发布到局域网的全部内容（设计方案 4.1）：实例名和主机名都用令牌，
// 不能出现真实主机名、设备名或任何身份信息。
type record struct {
	Instance string
	Host     string
	Service  string
	Port     int
	IPs      []string
	Text     []string
}

func announcement(service, instance string, port int, ips []string) record {
	return record{Instance: instance, Host: instance, Service: service, Port: port, IPs: ips, Text: txtRecords}
}

// Announce 以 instance 为实例名和主机名发布服务，直到 ctx 结束。
//
// 使用 zeroconf 而不是 dnssd：在 Windows 上，Go 不支持按包指定组播的发出网卡，
// dnssd 的组播因此只会从系统挑的那张网卡发出（往往是 VirtualBox、Hyper-V 等虚拟网卡），
// 到不了真正的局域网；zeroconf 会逐张网卡设置发出接口再发送。
// 地址列表显式给出：Windows 上收不到查询来自哪张网卡，zeroconf 无法按网卡挑地址。
func Announce(ctx context.Context, service, instance string, port int) error {
	ips := localIPs()
	if len(ips) == 0 {
		return errors.New("discovery: no usable network address")
	}
	r := announcement(service, instance, port, ips)
	srv, err := zeroconf.RegisterProxy(r.Instance, r.Service, domain, r.Port, r.Host, r.IPs, r.Text, lanInterfaces())
	if err != nil {
		return err
	}
	<-ctx.Done()
	// 关闭时 zeroconf 先发告别包再关 socket；组播发不出去时（例如 macOS 没有放行 Portide 的本地网络）
	// 它会一直卡在发送上，连带守护进程和整个应用都退不出去。最多等 shutdownWait，之后放弃等待。
	stopWithin(shutdownWait, srv.Shutdown)
	return nil
}

// shutdownWait 是关闭一次广播最多等待的时间。
const shutdownWait = 2 * time.Second

// stopWithin 在后台执行 stop，最多等 d；stop 按时结束时返回 true。超时的 stop 留在后台继续，不再等它。
func stopWithin(d time.Duration, stop func()) bool {
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// Discoverer 是局域网发现的实现：默认是本包基于 zeroconf 的 mDNS（MDNS），iOS 上由 Swift 用系统的 Bonjour 实现
// （iOS 不允许应用直接收发组播，M4 设计 2.1）。两个方法的约定同包级的 Announce 和 Browse。
type Discoverer interface {
	Announce(ctx context.Context, service, instance string, port int) error
	Browse(ctx context.Context, service string, found func(Found)) error
}

// MDNS 是默认的 Discoverer：本包的 Announce 和 Browse。
type MDNS struct{}

func (MDNS) Announce(ctx context.Context, service, instance string, port int) error {
	return Announce(ctx, service, instance, port)
}

func (MDNS) Browse(ctx context.Context, service string, found func(Found)) error {
	return Browse(ctx, service, found)
}

// AnnounceMember 用 d 以轮换令牌发布成员服务，每到窗口边界换一次名字（同时刷新地址列表），直到 ctx 结束。
func AnnounceMember(ctx context.Context, d Discoverer, key []byte, keyID string, port int) error {
	for ctx.Err() == nil {
		now := time.Now()
		wctx, cancel := context.WithDeadline(ctx, nextRotation(now))
		err := d.Announce(wctx, MemberService, MemberName(key, keyID, now), port)
		cancel()
		if err != nil && ctx.Err() == nil {
			return err
		}
	}
	return nil
}

// Browse 浏览 service，每发现一个实例调用一次 found，直到 ctx 结束。返回时不会再调用 found。
func Browse(ctx context.Context, service string, found func(Found)) error {
	local := localNets()
	entries := make(chan *zeroconf.ServiceEntry, 16)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case e, ok := <-entries:
				if !ok {
					return
				}
				ips := append(append([]net.IP(nil), e.AddrIPv4...), e.AddrIPv6...)
				found(Found{Instance: e.Instance, Addrs: addrs(ips, e.Port, local)})
			case <-stop:
				return
			}
		}
	}()
	err := zeroconf.Browse(ctx, service, domain, entries, zeroconf.SelectIfaces(lanInterfaces()))
	close(stop)
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// OrderAddrs 把别处（例如 iOS 的 Bonjour）解析出的 IP 和端口转成按优先级排好的 host:port，规则同 Browse：
// 和本机同网段的 IPv4 在前，其余 IPv4 其次，可路由的 IPv6 最后；无法解析的、回环和链路本地地址丢掉。
func OrderAddrs(ips []string, port int) []string {
	parsed := make([]net.IP, 0, len(ips))
	for _, s := range ips {
		if ip := net.ParseIP(s); ip != nil {
			parsed = append(parsed, ip)
		}
	}
	return addrs(parsed, port, localNets())
}

// addrs 把 IP 列表转成 host:port：与本机某个网段相同的 IPv4 在前，其余 IPv4 其次，可路由的 IPv6 最后；
// 跳过链路本地地址（IPv4 169.254/16 不可靠，IPv6 fe80::/10 需要接口区域标识）。
func addrs(ips []net.IP, port int, local []*net.IPNet) []string {
	type cand struct {
		addr string
		rank int
	}
	var cs []cand
	p := strconv.Itoa(port)
	for _, ip := range ips {
		if ip.IsLinkLocalUnicast() || ip.IsLoopback() {
			continue
		}
		rank := 2 // IPv6
		if ip.To4() != nil {
			rank = 1
			for _, n := range local {
				if n.Contains(ip) {
					rank = 0
					break
				}
			}
		}
		cs = append(cs, cand{net.JoinHostPort(ip.String(), p), rank})
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].rank < cs[j].rank })
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.addr)
	}
	return out
}

// usableIPs 从网卡地址中挑出可以直接拨号的：去掉回环和链路本地地址。
func usableIPs(as []net.Addr) []string {
	var out []string
	for _, a := range as {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, n.IP.String())
	}
	return out
}

// multicastInterfaces 返回已启用、支持组播的非回环网卡。
func multicastInterfaces() []net.Interface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.Interface
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagMulticast != 0 && ifi.Flags&net.FlagLoopback == 0 {
			out = append(out, ifi)
		}
	}
	return out
}

// lanInterfaces 返回收发 mDNS 用的网卡：multicastInterfaces 里去掉点对点网卡（VPN 等的 utun），
// 以及没有可用地址的网卡（例如 macOS 上没联网的 awdl0、llw0、雷雳网桥成员）。
// zeroconf 默认在所有支持组播的网卡上收发；macOS 上这类网卡有十几张，往它们发组播可能一直卡住，
// 关闭广播时的告别包发不完，旧的广播就关不掉、一直留在局域网上（见 Announce 的 stopWithin）。
// 一张都不剩时返回 nil，由 zeroconf 用它的默认网卡列表。
func lanInterfaces() []net.Interface {
	var out []net.Interface
	for _, ifi := range multicastInterfaces() {
		if ifi.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		if as, err := ifi.Addrs(); err == nil && len(usableIPs(as)) > 0 {
			out = append(out, ifi)
		}
	}
	return out
}

// LocalIPs 返回本机各网卡上可以给别的设备直连的地址（不含回环和链路本地地址）。
func LocalIPs() []string { return localIPs() }

// localIPs 返回本机所有可发布的地址。
func localIPs() []string {
	var out []string
	for _, ifi := range multicastInterfaces() {
		as, err := ifi.Addrs()
		if err != nil {
			continue
		}
		out = append(out, usableIPs(as)...)
	}
	return out
}

// localNets 返回本机各网卡的 IPv4 网段，用于给对端地址排序。
func localNets() []*net.IPNet {
	var out []*net.IPNet
	for _, ifi := range multicastInterfaces() {
		as, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range as {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLinkLocalUnicast() {
				out = append(out, n)
			}
		}
	}
	return out
}
