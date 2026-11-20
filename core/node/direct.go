package node

import (
	"context"
	"portide/core/i18n"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"portide/core/discovery"
	"portide/core/pb"
	"portide/core/wire"
)

// 打洞与直连升级（设计方案 4.2 第 2、3 条，M2b）：两台设备先经中继建立已认证的成员连接，在里面交换候选地址
// （中继看到的公网地址、本机各网卡的局域网地址），然后同时向对方的每个地址发起直连。两边的握手包各自在 NAT 上
// 打开映射，锥形 NAT 下就能连通；连上之后去重规则保留直连、关掉经中继的那条。打不通（对称 NAT 等）就留在中继上。
// 候选地址只在已经通过成员握手的连接里交换，中继和其他持有令牌的设备拿不到。

const (
	// maxCandidates 是一次交换的候选地址上限（收发两边都按它截断）。
	maxCandidates = 8
	// punchTimeout 是向一个候选地址发起直连的时限：对方可能晚一点才开始向本机发包，比普通拨号留得长一些。
	punchTimeout = 10 * time.Second
	// defaultUpgradeEvery 是经中继时重新交换候选地址、再试一次直连的间隔：换了网络（例如回到同一个局域网）
	// 之后也能自动改回直连。
	defaultUpgradeEvery = 2 * time.Minute
)

// upgradeLoop 在一条经中继的连接还是当前连接期间，先立刻、之后每隔 upgradeEvery 把本机的候选地址发给对方；
// 对方收到后发起直连（双方都这样做）。连接断开或者已经升级为直连时退出。
func (n *Node) upgradeLoop(ctx context.Context, mc *memberConn) {
	every := n.upgradeEvery
	if every <= 0 {
		every = defaultUpgradeEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if !n.isCurrent(mc) {
			return
		}
		n.sendCandidates(ctx, mc.conn)
		select {
		case <-ctx.Done():
			return
		case <-mc.conn.Context().Done():
			return
		case <-t.C:
		}
	}
}

// isCurrent 报告 mc 是否仍是本机为对方保留的那条连接。
func (n *Node) isCurrent(mc *memberConn) bool {
	d := n.daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[mc.peerID] == mc
}

// localCandidates 返回本机的直连候选地址：各中继看到的地址在前（按列表顺序，重复的由 validCandidates 去掉），
// 其后是各网卡的局域网地址（都用守护进程的监听端口）。
func (n *Node) localCandidates() []string {
	d := n.daemon
	var out []string
	for _, rc := range n.relayConnsInOrder() {
		d.mu.Lock()
		cl := rc.client
		d.mu.Unlock()
		if cl != nil {
			if a := cl.ObservedAddr(); a != "" {
				out = append(out, a)
			}
		}
	}
	port := strconv.Itoa(d.t.Addr().Port)
	for _, ip := range orderLocalIPs(discovery.LocalIPs()) {
		out = append(out, net.JoinHostPort(ip, port))
	}
	out = validCandidates(out)
	if n.candidatesHook != nil {
		out = n.candidatesHook(out)
	}
	return out
}

// orderLocalIPs 把私有 IPv4 地址（通常就是真正的局域网地址）排到前面，接着是全局单播 IPv6（公网可达，打洞用得上），
// 其余地址按原顺序跟在后面：虚拟网卡（Hyper-V、WSL、Docker）多的时候，截断到 maxCandidates 也不会把它们挤掉。
func orderLocalIPs(ips []string) []string {
	var private, global6, rest []string
	for _, s := range ips {
		ip := net.ParseIP(s)
		switch {
		case ip != nil && ip.To4() != nil && ip.IsPrivate():
			private = append(private, s)
		case ip != nil && ip.To4() == nil && ip.IsGlobalUnicast() && !ip.IsPrivate():
			global6 = append(global6, s)
		default:
			rest = append(rest, s)
		}
	}
	return append(append(private, global6...), rest...)
}

// validCandidates 只保留格式正确的 IP:端口（不接受域名，不做 DNS 解析），去掉未指定地址和组播地址，去重，
// 最多 maxCandidates 个。对方发来的地址也按这个规则过滤后才会去连接。
func validCandidates(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range in {
		host, port, err := net.SplitHostPort(a)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		p, err := strconv.Atoi(port)
		if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || err != nil || p < 1 || p > 65535 {
			continue
		}
		a = net.JoinHostPort(ip.String(), port)
		if seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
		if len(out) == maxCandidates {
			break
		}
	}
	return out
}

// sendCandidates 在经中继的连接 conn 上把本机的候选地址发给对方。
func (n *Node) sendCandidates(ctx context.Context, conn *quic.Conn) {
	addrs := n.localCandidates()
	if len(addrs) == 0 {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	str, err := conn.OpenStreamSync(sctx)
	if err != nil {
		return
	}
	_ = wire.Write(str, &pb.Envelope{Msg: &pb.Envelope_Candidates{Candidates: &pb.Candidates{Addrs: addrs}}})
	_ = str.Close()
}

// handleCandidates 处理对方发来的候选地址：本机和它之间目前只有经中继的连接时，同时向每个地址发起直连。
func (n *Node) handleCandidates(ctx context.Context, peer streamPeer, c *pb.Candidates) {
	n.tryDirect(ctx, peer, validCandidates(c.Addrs), i18n.T("候选地址交换"))
}

// upgradeJob 是 dialMembers 发现的一次直连尝试：对方经中继连着，mDNS 又在局域网里找到了它。
type upgradeJob struct {
	peer  streamPeer
	addrs []string
}

// tryDirect 在本机和 peer 之间目前只有经中继的连接时，同时向 addrs 的每个地址发起直连；source 只用于日志。
// 对同一台设备同时只进行一轮（为一组地址发起的那组直连）：上一轮的拨号还没全部结束时再来的地址直接丢弃，
// 这样出错或者被攻破的成员反复发候选地址也不会让拨号无限堆积。整轮都没连上时记一条日志。
func (n *Node) tryDirect(ctx context.Context, peer streamPeer, addrs []string, source string) {
	if len(addrs) == 0 || !n.relayedOnly(peer.KeyID) {
		return
	}
	d := n.daemon
	d.mu.Lock()
	if d.punching[peer.KeyID] {
		d.mu.Unlock()
		return
	}
	d.punching[peer.KeyID] = true
	d.mu.Unlock()
	// 这一轮最后一个拨号结束（不是连接断开：连上的那条要服务到断开）时放开，之后的地址才会开始新的一轮。
	var left, ok atomic.Int32
	left.Store(int32(len(addrs)))
	errs := make([]string, len(addrs))
	dialed := func(i int, err error) {
		if err == nil {
			ok.Add(1)
		} else {
			errs[i] = i18n.Tf("%s：%s", addrs[i], err.Error())
		}
		if left.Add(-1) == 0 {
			d.mu.Lock()
			delete(d.punching, peer.KeyID)
			d.mu.Unlock()
			// 拨号期间已经从别处连上（例如对方先连了过来）的不算失败。
			if ok.Load() == 0 && n.relayedOnly(peer.KeyID) && ctx.Err() == nil {
				n.opts.Logf(i18n.T("没能直连 %s（%s），继续经中继：%s"), peer.Name, source, strings.Join(errs, "；"))
			}
		}
	}
	for i, a := range addrs {
		d.spawn(func() { n.dialDirect(ctx, peer.KeyID, a, func(err error) { dialed(i, err) }) })
	}
}

// relayedOnly 报告本机和 peerID 之间当前保留的连接是否经中继（没有连接时返回 false）。
func (n *Node) relayedOnly(peerID string) bool {
	d := n.daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	mc := d.conns[peerID]
	return mc != nil && mc.conn.Context().Err() == nil && mc.relayed
}

// dialDirect 直连对方的一个候选地址（对方同时也在向本机的候选地址发起连接）。拨号结束时调用 dialed，
// 没连上时带上原因（已经不需要再连时为 nil）。连上并通过成员握手之后，去重规则保留这条直连、关掉经中继的那条；
// 连接断开后才返回。
func (n *Node) dialDirect(ctx context.Context, peerID, addr string, dialed func(error)) {
	conn, err := n.punch(ctx, peerID, addr)
	dialed(err)
	if conn != nil {
		n.serveMember(ctx, conn, true, false)
	}
}

// punch 向一个候选地址拨号，返回连上的连接；没连上时返回原因，已经不需要再连时两者都是 nil。
func (n *Node) punch(ctx context.Context, peerID, addr string) (*quic.Conn, error) {
	if !n.relayedOnly(peerID) {
		return nil, nil // 已经从别的地址连上了
	}
	if n.punchHook != nil {
		if err := n.punchHook(addr); err != nil {
			return nil, err
		}
	}
	dctx, cancel := context.WithTimeout(ctx, punchTimeout)
	defer cancel()
	return n.daemon.t.DialMember(dctx, addr)
}
