package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"portide/core/chain"
	"portide/core/discovery"
	"portide/core/i18n"
	"portide/core/memberauth"
	"portide/core/pairing"
	"portide/core/relay"
	"portide/core/store"
	"portide/core/transfer"
	"portide/core/transport"
)

const (
	dialInterval = 3 * time.Second
	dialTimeout  = 5 * time.Second
	// lanFirstTimeout 是"先试局域网再经中继"时每个局域网地址的拨号时限：局域网里握手只要几毫秒，
	// 不通就尽快改经中继，之后由 lanRetry 和候选地址交换继续尝试直连。
	lanFirstTimeout = 2 * time.Second
	// lanFirstAddrs 是先试局域网时最多试几个 mDNS 地址（同网段的排在前面），免得地址多时迟迟不走中继。
	lanFirstAddrs = 2
	// lanRetry 是经中继连着时，按 mDNS 发现的局域网地址再次尝试直连的最短间隔。
	lanRetry      = 20 * time.Second
	pairTimeout   = 60 * time.Second
	codeDuplicate = quic.ApplicationErrorCode(1) // 重复连接被关闭
	codeShutdown  = quic.ApplicationErrorCode(2)
	codeNotMember = quic.ApplicationErrorCode(3) // 对方没能证明自己是成员
	// tieBreakLabel 用于从 TLS 导出双方相同的值，决定同一方发起的重复连接保留哪条。
	tieBreakLabel = "portide-conn-tiebreak-v1"
)

var errAlreadyRunning = errors.New("node: daemon is already running")

// daemon 是 Run 期间的运行时状态。
//
// 锁的顺序：relaySubMu → daemon.mu → Node.mu；反过来不行。
type daemon struct {
	t     *transport.Transport // 监听失败时为 nil
	recv  *transfer.Receiver   // 接收端：把传输流交给对应的接收
	ready chan struct{}
	wg    sync.WaitGroup // Run 期间启动的所有后台任务；退出前全部等待结束

	mu         sync.Mutex
	started    bool
	conns      map[string]*memberConn // KeyID → 当前保留的连接
	candidates map[string][]string    // KeyID → mDNS 发现的地址
	dialing    map[string]bool        // 正在拨号的成员 KeyID，或 "addr:" 加静态地址
	punching   map[string]bool        // 正在进行一轮打洞拨号的成员 KeyID（见 tryDirect）
	lanTried   map[string]time.Time   // 经中继连着的成员 → 上次按 mDNS 发现的地址尝试直连的时间（见 dialMembers）
	sending    map[int64]bool         // 正在执行的发送任务 ID
	lastPrune  time.Time              // 上次清理已结束发送任务的时间（只在 Run 的轮询中访问）
	lastStale  time.Time              // 上次清理过期未完成接收的时间（只在 Run 的轮询中访问）
	rejected   map[string]time.Time   // 远端主机 → 上次记录"拒绝连接"日志的时间（见 logRejected）
	pair       *store.PairSession     // 当前开放的配对会话
	pairBusy   bool                   // 是否有配对连接正在处理
	stopPairAd context.CancelFunc     // 停止发布配对服务
	stopDisc   context.CancelFunc     // 停止成员发现
	circleID   string                 // 当前连接表所属的设备圈；换圈（switchCircle）之后 onChainChanged 据此断开旧圈的连接
	moveTried  map[string]time.Time   // 旧圈连接没成功的设备 → 时间；moveRetry 之内不再对它发起旧圈连接（见 move.go）
	discMove   string                 // 上次启动发现时迁移记录的摘要（见 moveDigest）；变了 syncMove 就重启发现

	// 中继（设计方案 4.2；多个中继见中继设置同步设计第 6 节）。
	relays    map[[32]byte]*relayConn // 中继指纹 → 这个中继的连接循环
	relaySent string                  // 上次广播的中继设置 Raw 的 base64，只在 Run 的轮询里读写
	routes    map[string]string       // 路由 ID（十六进制）→ 成员 KeyID

	// relaySubMu 串行化 relaySubscribe：快照和发送订阅之间不能被另一次订阅插进来。锁顺序：relaySubMu → daemon.mu → Node.mu。
	relaySubMu sync.Mutex
}

type memberConn struct {
	conn     *quic.Conn
	peerID   string
	dialedBy string // 发起连接一方的 KeyID
	tieKey   []byte // 双方从 TLS 导出的相同值
	relayed  bool   // 这条连接经中继（两端对同一条连接的看法一致）
	upgraded bool   // 登记时替换掉了一条经中继的连接（打洞成功，M2b）
}

// spawn 启动一个受 Run 管理的后台任务。
func (d *daemon) spawn(f func()) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		f()
	}()
}

// Ready 在守护进程开始监听（或监听失败）后关闭。
func (n *Node) Ready() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.daemon == nil {
		n.daemon = &daemon{ready: make(chan struct{})}
	}
	return n.daemon.ready
}

// Addr 返回守护进程的监听地址；需在 Ready 之后调用。监听失败时返回空字符串。
func (n *Node) Addr() string {
	<-n.Ready()
	if n.daemon.t == nil {
		return ""
	}
	return n.daemon.t.Addr().String()
}

// Run 运行守护进程直到 ctx 结束：接受连接、发现并连接成员、执行配对会话、维护在线状态。
// 每个 Node 只能运行一次。
func (n *Node) Run(ctx context.Context) error {
	n.Ready() // 确保 daemon 已创建
	d := n.daemon
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return errAlreadyRunning
	}
	d.started = true
	d.conns = map[string]*memberConn{}
	d.candidates = map[string][]string{}
	d.dialing = map[string]bool{}
	d.punching = map[string]bool{}
	d.lanTried = map[string]time.Time{}
	d.sending = map[int64]bool{}
	d.rejected = map[string]time.Time{}
	d.routes = map[string]string{}
	d.relays = map[[32]byte]*relayConn{}
	d.moveTried = map[string]time.Time{}
	d.circleID = n.CircleID()
	d.mu.Unlock()

	t, err := transport.Listen(n.listenAddr())
	if err != nil {
		close(d.ready)
		if isAddrInUse(err) {
			return fmt.Errorf("%w: %v", ErrPortInUse, err)
		}
		return err
	}
	d.t = t
	recv := n.newReceiver()
	d.mu.Lock()
	d.recv = recv // 加锁：界面随时可能通过 IncomingTransfers 读它
	d.mu.Unlock()
	n.cleanupStaleTransfers()
	_ = n.store.RecoverOutgoing()
	_ = n.store.SetAllOffline()
	_ = n.store.SetMeta(store.MetaDaemonAddr, t.Addr().String())
	n.heartbeat()
	close(d.ready)
	n.opts.Logf(i18n.T("守护进程已启动：%s，设备 %s（%.16s…）"), t.Addr(), n.Name(), n.id.KeyID)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.spawn(func() { n.acceptLoop(ctx) })
	n.restartDiscovery(ctx)
	n.syncRelay(ctx)

	poll := time.NewTicker(n.opts.PollInterval)
	defer poll.Stop()
	dial := time.NewTicker(dialInterval)
	defer dial.Stop()
	progress := time.NewTicker(incomingProgressEvery)
	defer progress.Stop()
	n.dialMembers(ctx)
	for {
		select {
		case <-ctx.Done():
			n.shutdown(cancel)
			return nil
		case <-poll.C:
			n.heartbeat()
			local := n.takeLocalChange()
			if changed, err := n.reloadCircle(); err != nil {
				n.opts.Logf(i18n.T("重新加载设备圈失败：%v"), err)
				if local {
					// 重新加载失败，本进程的改动（RemoveDevice）还没有被推送出去：恢复标记，下次轮询重试。
					n.markLocalChange()
				}
			} else if changed || local {
				n.onChainChanged(ctx, i18n.T("数据库中的设备圈已更新"))
			}
			n.syncMove(ctx)
			n.syncPairSession(ctx)
			n.syncRelay(ctx)
			n.broadcastRelaySettings(ctx)
			n.runOutgoing(ctx)
		case <-dial.C:
			n.dialMembers(ctx)
		case <-progress.C:
			n.reportIncomingProgress()
		}
	}
}

func (n *Node) heartbeat() {
	_ = n.store.SetMeta(store.MetaDaemonHeartbeat, strconv.FormatInt(time.Now().UnixMilli(), 10))
}

// shutdown 先停止并等待所有后台任务，再写入最终状态，保证退出后不再有写库或日志。
func (n *Node) shutdown(cancel context.CancelFunc) {
	d := n.daemon
	cancel()
	d.mu.Lock()
	for _, mc := range d.conns {
		_ = mc.conn.CloseWithError(codeShutdown, "shutdown")
	}
	if d.stopDisc != nil {
		d.stopDisc()
		d.stopDisc = nil
	}
	if d.stopPairAd != nil {
		d.stopPairAd()
		d.stopPairAd = nil
	}
	for fp, rc := range d.relays {
		rc.stop()
		delete(d.relays, fp)
	}
	d.mu.Unlock()
	_ = d.t.Close() // 结束 Accept、进行中的拨号、到中继的连接和其余连接
	d.wg.Wait()
	_ = n.store.SetAllOffline()
	_ = n.store.SetMeta(store.MetaDaemonHeartbeat, "0")
	n.clearRelayStatuses()
}

// memberCount 返回当前成员数（不是签名链的记录数：移除设备之后两者不再相等）。
func (n *Node) memberCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.chain == nil {
		return 0
	}
	return len(n.chain.Members())
}

func (n *Node) acceptLoop(ctx context.Context) {
	d := n.daemon
	for {
		conn, err := d.t.Accept(ctx)
		if err != nil {
			return
		}
		switch transport.ALPN(conn) {
		case transport.ALPNMember:
			d.spawn(func() { n.serveMember(ctx, conn, false, false) })
		case transport.ALPNPair:
			d.spawn(func() { n.servePair(ctx, conn) })
		default:
			_ = conn.CloseWithError(0, "unknown protocol")
		}
	}
}

// ---------- 成员发现与连接 ----------

// restartDiscovery 按当前设备圈状态（重新）启动 mDNS 发布与浏览。
// 整个替换过程持有 daemon.mu，并发调用时最后一个安装的总是最新的成员列表，旧的一定被停止。
func (n *Node) restartDiscovery(ctx context.Context) {
	d := n.daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopDisc != nil {
		d.stopDisc()
		d.stopDisc = nil
	}
	c, key := n.snapshot()
	rec, oldKey := n.activeMove()
	d.discMove = moveDigest(rec, oldKey)
	if c == nil || n.opts.DisableMDNS || ctx.Err() != nil {
		return
	}
	// 合并之后还有设备没跟过来：再用来源圈的密钥发布一次、浏览时也用它认还在等的设备（合并设备圈设计 8.2）。
	// 认出来的地址照样记进 candidates，dialMembers 对 Pending 里的设备本来就走旧圈连接。
	var pending []string
	if oldKey != nil {
		for _, id := range rec.Pending {
			if id != n.id.KeyID {
				pending = append(pending, id)
			}
		}
	}
	dctx, cancel := context.WithCancel(ctx)
	d.stopDisc = cancel
	port := d.t.Addr().Port
	d.spawn(func() {
		if err := discovery.AnnounceMember(dctx, n.discoverer(), key, n.id.KeyID, port); err != nil {
			n.opts.Logf(i18n.T("mDNS 发布失败：%v"), err)
		}
	})
	if oldKey != nil {
		d.spawn(func() {
			if err := discovery.AnnounceMember(dctx, n.discoverer(), oldKey, n.id.KeyID, port); err != nil {
				n.opts.Logf(i18n.T("用原设备圈的身份发布 mDNS 失败：%v"), err)
			}
		})
	}
	d.spawn(func() {
		err := n.discoverer().Browse(dctx, discovery.MemberService, func(f discovery.Found) {
			c, key := n.snapshot()
			if c == nil {
				return
			}
			var others []string
			for _, m := range c.Members() {
				if m.KeyID != n.id.KeyID {
					others = append(others, m.KeyID)
				}
			}
			if len(f.Addrs) == 0 {
				return
			}
			id, ok := discovery.Identify(key, others, f.Instance, time.Now())
			if !ok && oldKey != nil {
				id, ok = discovery.Identify(oldKey, pending, f.Instance, time.Now())
			}
			if ok {
				d.mu.Lock()
				d.candidates[id] = f.Addrs
				d.mu.Unlock()
			}
		})
		if err != nil {
			n.opts.Logf(i18n.T("mDNS 浏览失败：%v"), err)
		}
	})
}

// dialMembers 尝试连接所有尚未连上的成员：mDNS 发现的地址、上次连通的地址、静态地址。
// 每个成员同一时间只有一个拨号任务，它按顺序尝试该成员的地址，第一次成功就停止，
// 避免同一方并发拨出多条重复连接。
func (n *Node) dialMembers(ctx context.Context) {
	c, _ := n.snapshot()
	if c == nil {
		return
	}
	d := n.daemon
	// 合并之后还有设备没跟过来：对它们用旧圈连接（来源圈的密钥和链）。旧圈连接刚失败过的，冷却期内照常连。
	waiting := map[string]bool{}
	if rec, oldKey := n.activeMove(); oldKey != nil {
		for _, id := range rec.Pending {
			waiting[id] = true
		}
	}
	peers, _ := n.store.Peers()
	lastAddr := map[string]string{}
	for _, p := range peers {
		lastAddr[p.KeyID] = p.Addr
	}

	type job struct {
		key   string
		addrs []string
		lan   []string // relay 为 true 时：mDNS 刚发现的局域网地址，先用短时限试，再经中继
		relay bool     // 经中继连接成员 key
		old   bool     // 旧圈连接
	}
	var jobs []job
	var upgrades []upgradeJob
	d.mu.Lock()
	remote := map[string]bool{}
	for _, mc := range d.conns {
		remote[mc.conn.RemoteAddr().String()] = true
	}
	now := time.Now()
	for id, at := range d.moveTried {
		if now.Sub(at) >= moveRetry {
			delete(d.moveTried, id)
		}
	}
	for _, m := range c.Members() {
		if m.KeyID == n.id.KeyID {
			continue
		}
		// 先看已有连接：本机拨出的连接在断开之前一直占着 dialing（拨号任务就在服务这条连接）。
		if mc := d.conns[m.KeyID]; mc != nil {
			// 已经经中继连着、mDNS 又在局域网里找到了它：不等下一轮候选地址交换，直接试这些地址（M2b）。
			if lan := d.candidates[m.KeyID]; mc.relayed && len(lan) > 0 && !d.punching[m.KeyID] &&
				now.Sub(d.lanTried[m.KeyID]) >= lanRetry {
				d.lanTried[m.KeyID] = now
				upgrades = append(upgrades, upgradeJob{peer: streamPeer{KeyID: m.KeyID, Name: m.Name}, addrs: lan})
			}
			continue
		}
		if d.dialing[m.KeyID] {
			continue
		}
		var addrs []string
		seen := map[string]bool{}
		for _, a := range append(append([]string(nil), d.candidates[m.KeyID]...), lastAddr[m.KeyID]) {
			// 上次是经中继连上的，记下的地址是 "relay:中继地址"，不能拿来直接拨号。
			if _, via := relay.RelayedVia(a); a != "" && !via && !seen[a] && !remote[a] {
				seen[a] = true
				addrs = append(addrs, a)
			}
		}
		_, tried := d.moveTried[m.KeyID]
		old := waiting[m.KeyID] && !tried
		// 局域网优先：对方在中继上在线已经超过 relayAfter、局域网还没连上，才经中继连接（设计方案 4.2 第 3 条）。
		if since, ok := d.relayOnlineSince(m.KeyID); ok && time.Since(since) >= n.relayAfter() {
			d.dialing[m.KeyID] = true
			lan := d.candidates[m.KeyID]
			lan = lan[:min(len(lan), lanFirstAddrs)]
			jobs = append(jobs, job{key: m.KeyID, addrs: addrs, lan: lan, relay: true, old: old})
			continue
		}
		if len(addrs) > 0 {
			d.dialing[m.KeyID] = true
			jobs = append(jobs, job{key: m.KeyID, addrs: addrs, old: old})
		}
	}
	// 还有成员没连上时才拨静态地址（按成员数比较：移除设备之后签名链的记录数比成员数多）。
	if len(d.conns) < len(c.Members())-1 {
		for _, a := range n.opts.StaticPeers {
			k := "addr:" + a
			if !remote[a] && !d.dialing[k] {
				d.dialing[k] = true
				jobs = append(jobs, job{key: k, addrs: []string{a}})
			}
		}
	}
	d.mu.Unlock()

	for _, u := range upgrades {
		n.tryDirect(ctx, u.peer, u.addrs, i18n.T("局域网发现"))
	}
	for _, j := range jobs {
		d.spawn(func() {
			defer func() {
				d.mu.Lock()
				delete(d.dialing, j.key)
				d.mu.Unlock()
			}()
			// 局域网优先：mDNS 找到了对方时，先用短时限试一下局域网，不通再经中继。
			tried := map[string]bool{}
			if j.relay {
				for _, addr := range j.lan {
					tried[addr] = true
					if ctx.Err() != nil || n.dialOne(ctx, addr, j.old, lanFirstTimeout) {
						return
					}
				}
				if n.dialRelayed(ctx, j.key, j.old) {
					return
				}
			}
			// 中继这条路没通时照样试局域网地址
			for _, addr := range j.addrs {
				if tried[addr] {
					continue
				}
				if ctx.Err() != nil || n.dialOne(ctx, addr, j.old, dialTimeout) {
					return
				}
			}
		})
	}
}

// dialOne 拨号一个地址，最多等 timeout；连上时服务这条连接直到断开并返回 true。old 为 true 时是旧圈连接（见 serveMember）。
func (n *Node) dialOne(ctx context.Context, addr string, old bool, timeout time.Duration) bool {
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := n.daemon.t.DialMember(dctx, addr)
	if err != nil {
		return false
	}
	n.serveMember(ctx, conn, true, old)
	return true
}

// serveMember 在一条成员连接上完成匿名成员握手，登记在线状态，处理对端新开的流，并在连接断开时注销。
// old 只对发起方有意义：用来源圈的密钥和链去连还没跟过来的设备（合并设备圈设计 8.3）。握手走了来源圈的密钥时
// （本机发起的旧圈连接，或者对方用来源圈的密钥连进来）不登记，交给 serveOldMember。
func (n *Node) serveMember(ctx context.Context, conn *quic.Conn, dialed, old bool) {
	tie, err := exportKeyingMaterial(conn, tieBreakLabel, 16)
	if err != nil {
		_ = conn.CloseWithError(0, "no keying material")
		return
	}
	hctx, cancel := context.WithTimeout(ctx, dialTimeout)
	peer, changed, peerLen, viaOld, forked, err := n.authenticate(hctx, conn, dialed, old)
	cancel()
	if changed {
		// 握手中合并到了新记录：不管这条连接最终是否保留（可能因去重被放弃），都要传播给其他已连接成员。
		n.onChainChanged(ctx, i18n.T("握手时同步了设备圈更新"))
	}
	if err != nil {
		n.rejectMember(conn, err)
		return
	}
	if viaOld {
		n.serveOldMember(ctx, conn, peer, forked)
		return
	}
	dialer := peer.KeyID
	if dialed {
		dialer = n.id.KeyID
	}
	mc := &memberConn{conn: conn, peerID: peer.KeyID, dialedBy: dialer, tieKey: tie, relayed: relay.IsRelayed(conn.RemoteAddr())}
	if !n.register(mc) {
		return
	}
	// 对方以本圈成员的身份连上了：它已经跟过来，不用再等。
	n.movedPeer(peer.KeyID)
	// 登记之后再写数据库：放在握手和登记之间会拉长同时互拨时的去重窗口。
	if err := n.store.SetPeerFeatures(peer.KeyID, peer.Features); err != nil {
		n.opts.Logf(i18n.T("记录设备功能失败：%v"), err)
	}
	if mc.upgraded {
		n.opts.Logf(i18n.T("已切换到直连 %s（%s）"), peer.Name, describeAddr(conn))
	} else {
		n.opts.Logf(i18n.T("已连接 %s（%s）"), peer.Name, describeAddr(conn))
	}
	// 经中继连上的：在加密通道里把本机的直连候选地址发给对方，双方同时打洞；之后定期重试（M2b）。
	if mc.relayed {
		d := n.daemon
		d.spawn(func() { n.upgradeLoop(ctx, mc) })
	}
	// 握手之后本机链可能又有变化（例如刚配对了新设备），而这条连接当时还没登记、错过了推送：补推一次。
	// 链里有移除记录时也推：对方可能是移除时不在线的成员，还没拿到新的发现密钥（pushChain 会一并下发）。
	if c, _ := n.snapshot(); c != nil && (c.Len() != peerLen || c.KeyCheck() != nil) {
		d := n.daemon
		d.spawn(func() { n.pushChain(ctx, peer.KeyID, conn) })
	}
	// 中继设置不随链走：每次连上都发一次，对方按版本取舍（对方没声明 relays 或本机没有设置时 pushRelaySettings 什么都不发）。
	{
		d := n.daemon
		d.spawn(func() { n.pushRelaySettings(ctx, peer.KeyID, conn) })
	}

	n.serveStreams(ctx, conn, streamPeer{KeyID: peer.KeyID, Name: peer.Name}) // 阻塞到连接断开

	d := n.daemon
	d.mu.Lock()
	if d.conns[peer.KeyID] == mc {
		delete(d.conns, peer.KeyID)
		// 连接刚断开（例如网络抖了一下）：重新给局域网 relayAfter 的时间，不立刻改走中继。
		d.restartRelayWait(peer.KeyID)
		n.opts.Logf(i18n.T("已断开 %s"), peer.Name)
		// 在锁内写离线状态，避免覆盖随后登记的新连接写入的在线状态。
		_ = n.store.UpsertPeer(store.Peer{KeyID: peer.KeyID, Addr: conn.RemoteAddr().String(), LastSeen: time.Now(), Online: false})
	}
	d.mu.Unlock()
}

// rejectMember 关闭一条没能完成成员握手的连接，并按原因决定是否记录日志。
func (n *Node) rejectMember(conn *quic.Conn, err error) {
	var ae *quic.ApplicationError
	switch {
	case errors.Is(err, errSelf):
		_ = conn.CloseWithError(0, "self")
	case errors.Is(err, errMoveCooling):
		// 冷却期内的旧圈连接（moveFailed 已经记过一次日志）：直接关闭，不再记录。
		_ = conn.CloseWithError(codeNotMember, "not now")
	case errors.Is(err, errDuplicate):
		_ = conn.CloseWithError(codeDuplicate, "duplicate")
	case errors.As(err, &ae) && ae.ErrorCode == codeDuplicate:
		// 对端发现是重复连接后放弃了它，不用记录。
	case errors.As(err, &ae) && ae.ErrorCode == codeNotMember:
		// 对端认定本机不是它的成员（例如上次连通的地址已换成别的设备圈的设备）。拨号每 3 秒重试，不重复记录。
	case errors.Is(err, memberauth.ErrNotInCircle):
		n.logRejected(conn.RemoteAddr(), i18n.T("对方不属于本设备圈"))
		_ = conn.CloseWithError(codeNotMember, "not a member")
	case errors.Is(err, errNotMember):
		n.logRejected(conn.RemoteAddr(), i18n.T("对方已不是本设备圈成员"))
		_ = conn.CloseWithError(codeNotMember, "not a member")
	default:
		// 失败原因可能和对方的版本或状态有关（例如旧版本读不懂移除记录），对方通常每隔几秒重试：
		// 限流日志，不能让它刷屏。
		n.logRejected(conn.RemoteAddr(), i18n.Tf("成员握手失败：%s", err.Error()))
		_ = conn.CloseWithError(0, "handshake failed")
	}
}

// rejectLogEvery 是同一台主机的"拒绝连接"日志最短间隔：被移除的设备和圈外设备每 3 秒重试一次，不能刷屏。
const rejectLogEvery = 10 * time.Minute

// logRejected 记录拒绝了一条成员连接；同一台主机在 rejectLogEvery 内只记一次。
func (n *Node) logRejected(addr net.Addr, why string) {
	host := addr.String()
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	d := n.daemon
	d.mu.Lock()
	now := time.Now()
	last, seen := d.rejected[host]
	if !seen || now.Sub(last) >= rejectLogEvery {
		// 顺便清掉过期条目，避免 d.rejected 无限增长（圈外设备和移除设备换着端口重试）。
		for h, t := range d.rejected {
			if now.Sub(t) >= rejectLogEvery {
				delete(d.rejected, h)
			}
		}
		d.rejected[host] = now
	} else {
		host = ""
	}
	d.mu.Unlock()
	if host != "" {
		n.opts.Logf(i18n.T("拒绝了来自 %s 的连接：%s（10 分钟内不再重复提示）"), addr, why)
	}
}

// dropRemovedMembers 断开已经不是成员的设备的连接（签名链里刚刚出现了移除记录）。
// 必须在推送新链之前调用：推送只发给成员，这里先把连接断开，被移除的设备就收不到任何东西。
func (n *Node) dropRemovedMembers() {
	c, _ := n.snapshot()
	if c == nil {
		return
	}
	d := n.daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, mc := range d.conns {
		if c.IsMember(id) {
			continue
		}
		delete(d.conns, id)
		_ = mc.conn.CloseWithError(codeNotMember, "removed from the circle")
		_ = n.store.UpsertPeer(store.Peer{KeyID: id, Addr: mc.conn.RemoteAddr().String(), LastSeen: time.Now(), Online: false})
		n.opts.Logf(i18n.T("「%s」已被移出设备圈，已断开连接"), n.memberName(id))
	}
}

// dropOldCircle 在本机换了设备圈（switchCircle）之后断开所有成员连接、清空连接表和发现到的地址：
// 它们都属于旧圈，即使对方也是新圈的成员，连接时用的也是旧圈的发现密钥。圈没变时什么都不做，返回 false。
func (n *Node) dropOldCircle() bool {
	id := n.CircleID()
	d := n.daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	if id == d.circleID {
		return false
	}
	if len(d.conns) > 0 {
		n.opts.Logf(i18n.T("已换到新的设备圈，断开旧设备圈的 %d 条连接"), len(d.conns))
	}
	d.circleID = id
	for peerID, mc := range d.conns {
		_ = mc.conn.CloseWithError(codeNotMember, "circle changed")
		_ = n.store.UpsertPeer(store.Peer{KeyID: peerID, Addr: mc.conn.RemoteAddr().String(), LastSeen: time.Now(), Online: false})
	}
	d.conns = map[string]*memberConn{}
	d.candidates = map[string][]string{}
	return true
}

// hasLiveConn 报告是否已有一条到 peerID 的存活连接，而且新连接（是否经中继由 newRelayed 说明）不比它好：
// 经中继的新连接遇到任何存活连接都算重复；直连的新连接只有遇到存活的直连才算重复，可以替换经中继的那条（M2b）。
func (n *Node) hasLiveConn(peerID string, newRelayed bool) bool {
	d := n.daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	mc := d.conns[peerID]
	return mc != nil && mc.conn.Context().Err() == nil && (newRelayed || !relay.IsRelayed(mc.conn.RemoteAddr()))
}

// preferred 报告重复连接中 a 是否应优先于 b 保留。两端对同一对连接得出相同结论：
// 直连优先于经中继的连接（两端都知道哪条经中继）；都是直连或都经中继时，
// 先比较发起方 KeyID（较小者优先），同一方发起时比较双方从 TLS 导出的相同值。
func preferred(a, b *memberConn) bool {
	if a.relayed != b.relayed {
		return b.relayed
	}
	if a.dialedBy != b.dialedBy {
		return a.dialedBy < b.dialedBy
	}
	return bytes.Compare(a.tieKey, b.tieKey) < 0
}

// register 登记连接并写入在线状态；与仍然存活的已有连接重复时，按 preferred 保留其中一条。
func (n *Node) register(mc *memberConn) bool {
	d := n.daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	// 握手通过之后、登记之前，对方可能刚被移出设备圈（dropRemovedMembers 那时还看不到这条连接）：不登记。
	if !n.isMember(mc.peerID) {
		_ = mc.conn.CloseWithError(codeNotMember, "removed from the circle")
		return false
	}
	old, ok := d.conns[mc.peerID]
	// 对方可能已经先关掉了经中继的那条（它那边先完成了升级），所以不管旧连接是否还活着都算升级。
	mc.upgraded = ok && old.relayed && !mc.relayed
	if ok && old.conn.Context().Err() == nil {
		if preferred(old, mc) {
			_ = mc.conn.CloseWithError(codeDuplicate, "duplicate")
			return false
		}
		_ = old.conn.CloseWithError(codeDuplicate, "duplicate")
	}
	d.conns[mc.peerID] = mc
	_ = n.store.UpsertPeer(store.Peer{KeyID: mc.peerID, Addr: mc.conn.RemoteAddr().String(), LastSeen: time.Now(), Online: true})
	_ = n.store.RetryNow(mc.peerID) // 对方刚连上：排队和等待重试的任务立即发送
	return true
}

// ---------- 配对（邀请方） ----------

// syncPairSession 让传输层与数据库中的配对会话保持一致：有待处理会话就开放配对，否则关闭。
func (n *Node) syncPairSession(ctx context.Context) {
	ps, err := n.store.PendingPairSession(time.Now())
	if err != nil {
		n.opts.Logf(i18n.T("读取配对会话失败：%v"), err)
		return
	}
	d := n.daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case ps != nil && (d.pair == nil || d.pair.ID != ps.ID):
		d.pair = ps
		d.t.SetPairing(true)
		n.opts.Logf(i18n.T("配对已开放，有效期至 %s"), ps.ExpiresAt.Format("15:04:05"))
		if d.stopPairAd != nil {
			d.stopPairAd()
			d.stopPairAd = nil
		}
		if !n.opts.DisableMDNS {
			actx, cancel := context.WithDeadline(ctx, ps.ExpiresAt)
			d.stopPairAd = cancel
			port := d.t.Addr().Port
			d.spawn(func() { _ = n.discoverer().Announce(actx, discovery.PairService, randomInstance(), port) })
		}
	case ps == nil && d.pair != nil:
		n.closePairingLocked()
	}
}

func (n *Node) closePairingLocked() {
	d := n.daemon
	d.pair = nil
	d.t.SetPairing(false)
	if d.stopPairAd != nil {
		d.stopPairAd()
		d.stopPairAd = nil
	}
}

// servePair 处理一条配对连接。同一时间只处理一条，其余直接关闭。
func (n *Node) servePair(ctx context.Context, conn *quic.Conn) {
	defer conn.CloseWithError(0, "")
	d := n.daemon
	d.mu.Lock()
	ps := d.pair
	if ps == nil || d.pairBusy {
		d.mu.Unlock()
		return
	}
	d.pairBusy = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.pairBusy = false
		d.mu.Unlock()
	}()

	// 会话可能在上次轮询之后已经结束（成功，或失败满次数），重新确认后再接受这次尝试，
	// 避免已作废的配对码被多猜一次。
	cur, err := n.store.GetPairSession(ps.ID)
	if err != nil || cur.Status != store.PairPending || cur.Attempts >= pairing.MaxAttempts {
		return
	}
	current, key := n.snapshot()
	if current == nil {
		return
	}
	c, err := chain.Load(current.Records()) // Serve 会追加记录，所以用副本
	if err != nil {
		return
	}
	chainChanged := false
	// 本机配置了中继时，把中继设置和第一个中继的配置串（给旧版本，含令牌）交给新设备：
	// 配对通道已经过配对码认证并加密（设计方案 3.3）。
	relayConf := n.firstRelayString()
	res, err := n.runInvite(ctx, conn, pairing.Invite{
		Code: cur.Code, Self: n.id, Chain: c, DiscoveryKey: key, RelayConfig: relayConf,
		RelaySettings: n.relaySettingsForWire(), Features: n.features,
		Commit: func(records [][]byte) error {
			ch, err := n.mergeChain(records)
			chainChanged = chainChanged || ch // 合并前可能从数据库重新加载到别的变化（例如命令行刚移除了设备），失败时也要推送
			return err
		},
		// 反向加入：本机单机，对方圈里有其他设备，本机丢弃单机圈、换到对方的圈。这个配对会话随后记为完成，不作废。
		Adopt: func(a *pairing.Adopted) error {
			return n.switchCircle(a.Chain.Records(), a.DiscoveryKey, a.RelayConfig, a.RelaySettings, ps.ID, nil)
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			return // 守护进程正在退出，不计入失败次数
		}
		if chainChanged {
			n.onChainChanged(ctx, i18n.T("数据库中的设备圈已更新"))
		}
		if errors.Is(err, pairing.ErrJoinerOutdated) {
			// 不是输错配对码：让邀请方马上看到原因，配对码作废（对方升级后重新生成一个）。
			n.opts.Logf("%s", i18n.T("配对失败：对方的 Portide 版本太旧，读不了改过名的设备圈"))
			_ = n.store.FinishPairSession(ps.ID, store.PairFailed, "", store.PairErrOutdated)
			d.mu.Lock()
			n.closePairingLocked()
			d.mu.Unlock()
			return
		}
		if why := pairRefusalText(err); why != "" {
			// 配对码已经得到证明，只是两台设备的圈子不适合这样配对：不算输错，也不作废配对码，
			// 用户可以换一台设备再用同一个配对码。
			n.opts.Logf(i18n.T("配对没有完成：%s"), why)
			return
		}
		attempts, ierr := n.store.IncPairAttempts(ps.ID)
		if ierr != nil {
			attempts = pairing.MaxAttempts // 无法记录次数时直接作废，宁可让用户重新邀请
		}
		n.opts.Logf(i18n.T("配对失败（第 %d 次）：%v"), attempts, err)
		if attempts >= pairing.MaxAttempts {
			_ = n.store.FinishPairSession(ps.ID, store.PairFailed, "", store.PairErrTooManyAttempts)
			d.mu.Lock()
			n.closePairingLocked()
			d.mu.Unlock()
		}
		return
	}
	// brought 是跟着加入方一起签进来的其他设备数（只有合并时有；重配时 Added 为空）。
	_ = n.store.CompletePairSession(ps.ID, res.JoinerName, string(res.Mode), max(len(res.Added)-1, 0))
	d.mu.Lock()
	n.closePairingLocked()
	d.mu.Unlock()
	if res.Mode == pairing.ModeAdopt {
		// 不记对方这次拨过来的地址：那是它 Join 用的临时端口，之后连不上。它记下了本机的地址，会主动连过来。
		_ = n.store.UpsertPeer(store.Peer{KeyID: res.JoinerKeyID, Addr: "", LastSeen: time.Now(), Online: false})
		n.onChainChanged(ctx, i18n.Tf("配对成功：已加入「%s」的设备圈", res.JoinerName))
		return
	}
	_ = n.store.UpsertPeer(store.Peer{KeyID: res.JoinerKeyID, Addr: "", LastSeen: time.Now(), Online: false})
	if brought := len(res.Added) - 1; res.Mode == pairing.ModeMerge && brought > 0 {
		// 合并：加入方把它设备圈里的其他设备一起带了进来（重配时 Added 为空，按普通加入记日志）。
		n.onChainChanged(ctx, fmt.Sprintf(i18n.T("配对成功：「%s」和它设备圈里的 %d 台设备加入了设备圈"), res.JoinerName, brought))
		return
	}
	n.onChainChanged(ctx, i18n.Tf("配对成功：%s 已加入设备圈", res.JoinerName))
}

// PairFailureText 把配对会话记下的失败原因换成给用户看的中文（命令行和界面共用）。
func PairFailureText(reason string) string {
	switch reason {
	case store.PairErrOutdated:
		return i18n.T("对方的 Portide 版本太旧，请先在那台设备上升级，再重新添加")
	case store.PairErrTooManyAttempts:
		return i18n.T("配对码输错的次数太多，已经作废，请重新生成")
	case store.PairErrCancelled, "superseded":
		return i18n.T("这次配对已经取消")
	}
	return reason
}

// pairRefusalText 返回邀请方在配对码得到证明之后、因为两边的圈子而拒绝时的说明；err 不是这类拒绝时返回空字符串。
func pairRefusalText(err error) string {
	switch {
	case errors.Is(err, pairing.ErrSameCircle):
		return i18n.T("这两台设备已经在同一个设备圈里")
	case errors.Is(err, pairing.ErrMergeUnsupported):
		return i18n.T("对方的 Portide 版本不支持合并设备圈，请先在那台设备上升级")
	case errors.Is(err, pairing.ErrMergeNotConfirmed):
		return i18n.T("对方也有其他设备，需要合并两个设备圈")
	}
	return ""
}

// runInvite 在配对连接上接受加入方打开的流，并在其上执行邀请方协议。
func (n *Node) runInvite(ctx context.Context, conn *quic.Conn, inv pairing.Invite) (*pairing.Result, error) {
	pctx, cancel := context.WithTimeout(ctx, pairTimeout)
	defer cancel()
	str, err := conn.AcceptStream(pctx)
	if err != nil {
		return nil, err
	}
	defer str.Close()
	_ = str.SetDeadline(time.Now().Add(pairTimeout))
	exp, err := exportKeyingMaterial(conn, pairing.ExporterLabel, pairing.ExporterLen)
	if err != nil {
		return nil, err
	}
	res, err := pairing.Serve(str, exp, inv)
	if err != nil {
		// 拒绝消息（输错配对码、同一个圈等）写出之后，等加入方读完、关掉流再返回：调用方随后会关闭连接，
		// 马上关闭的话拒绝消息可能还没送到，加入方只看到连接断开。最多等 refusalDrain。
		_ = str.SetReadDeadline(time.Now().Add(refusalDrain))
		_, _ = io.Copy(io.Discard, str)
	}
	return res, err
}

// refusalDrain 是邀请方拒绝之后等加入方读完回复的最长时间。
const refusalDrain = 2 * time.Second

// exportKeyingMaterial 从连接的 TLS 状态导出双方相同的密钥材料（RFC 8446 7.5）。
func exportKeyingMaterial(conn *quic.Conn, label string, n int) ([]byte, error) {
	st := conn.ConnectionState().TLS
	return st.ExportKeyingMaterial(label, nil, n)
}

// describeAddr 返回日志里显示的对方地址：经中继的连接显示"经中继"。
func describeAddr(conn *quic.Conn) string {
	if relay.IsRelayed(conn.RemoteAddr()) {
		return i18n.T("经中继")
	}
	return conn.RemoteAddr().String()
}

// onChainChanged 在本机签名链变化后调用：重启发现（成员列表变了），并把新链推送给已连接的成员，
// 让没有参与这次变化的设备也尽快认识新成员。对端只在自己的链也发生变化时才继续转发，因此不会无限扩散。
func (n *Node) onChainChanged(ctx context.Context, reason string) {
	n.opts.Logf(i18n.T("%s（成员数 %d）"), reason, n.memberCount())
	switched := n.dropOldCircle()
	n.dropRemovedMembers()
	n.restartDiscovery(ctx)
	n.relaySubscribe()
	n.broadcastChain(ctx)
	if switched {
		// 换了圈：旧连接全部断开了，马上去连新圈的成员（例如刚配对的邀请方），不等下一次定时拨号。
		n.dialMembers(ctx)
	}
}

func randomInstance() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
