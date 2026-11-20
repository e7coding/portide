package node

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"portide/core/i18n"
	"slices"
	"time"

	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

	"portide/core/chain"
	"portide/core/memberauth"
	"portide/core/pairing"
	"portide/core/pb"
	"portide/core/relay"
	"portide/core/wire"
)

var (
	errNotMember = errors.New("node: peer is not a member of this circle")
	errDuplicate = errors.New("node: already connected to this peer")
	errSelf      = errors.New("node: connected to myself")
	// errMoveCooling 表示对方用来源圈的密钥连进来，但对它的旧圈连接刚失败过，还在冷却期内。
	errMoveCooling = errors.New("node: old-circle peer is cooling down")
)

// authenticate 在成员连接的第一条流上完成匿名成员握手（设计方案 4.2 修复，方案 B）：
//
//  1. memberauth.Exchange：圈子证明，然后互相出示身份；在本机出示身份（接收方）或确认连接（发起方）之前，
//     Admit 回调先合并对方的签名链、再核对对方是否为成员——对方的链必须是本机链的合法延伸，
//     这样还没收到推送的老成员也能直接认可新成员。作为接收方时，已被移除的设备拿不到本机的身份；
//     作为发起方时，本机按协议先出示身份，此时靠移除设备时轮换发现密钥（设计方案 3.5）来防止泄露；
//  3. 发起方决定是否保留这条连接（已有存活连接时放弃），确认后发 MemberReady；
//     接收方收到 MemberReady 才登记，所以两端对重复连接的处理一致。
//
// 返回对方身份、本机链是否因此变化（出错时也会返回，调用方据此传播更新），
// 以及握手结束时对方已知的链长度（用于登记后判断是否需要补推）。
//
// 旧圈连接（合并设备圈设计 8.3）：本机合并过、还有设备没跟过来时，作为发起方 old 为 true 时用来源圈的密钥做圈子证明、
// 出示来源圈的链；作为接收方时也接受来源圈的密钥，用它连进来的只能看到来源圈的链。对方用来源圈的密钥通过了
// 圈子证明时 viaOld 为 true，成员核对换成 admitOld（不合并进本机现在的链）；forked 表示两边的来源圈链分叉了。
func (n *Node) authenticate(ctx context.Context, conn *quic.Conn, dialed, old bool) (peer *memberauth.Peer, changed bool, peerLen int, viaOld, forked bool, err error) {
	c, key := n.snapshot()
	if c == nil {
		return nil, false, 0, false, false, ErrNotInCircle
	}
	rec, oldKey := n.activeMove()
	if rec == nil || len(rec.OldChain) == 0 {
		oldKey = nil
	}
	if oldKey == nil {
		old = false // 迁移刚刚结束：照常连
	}
	// 发起方只用当前发现密钥；接收方还接受宽限期内的旧密钥，让移除设备时不在线、还没拿到新密钥的成员能连进来，
	// 握手之后再把新密钥推给它（设计方案 3.5）。
	keys := [][]byte{key}
	if old {
		keys = [][]byte{oldKey}
	} else if !dialed {
		keys = n.circleKeys()
		if oldKey != nil {
			keys = append(keys, oldKey)
		}
	}
	var str *quic.Stream
	if dialed {
		str, err = conn.OpenStreamSync(ctx)
	} else {
		str, err = conn.AcceptStream(ctx)
	}
	if err != nil {
		return nil, false, 0, false, false, err
	}
	defer str.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = str.SetDeadline(dl)
	}
	exp, err := exportKeyingMaterial(conn, memberauth.ExporterLabel, memberauth.ExporterLen)
	if err != nil {
		return nil, false, 0, false, false, err
	}
	hello := c.Records()
	if dialed {
		// 主动连接用的是当前密钥；本机已经合并了新的移除记录、却还没拿到对应的新密钥时，对方可能就是被移除的设备
		// （它也有这把旧密钥）。只出示与当前密钥一致的那段链，真正的成员登记之后会把完整的链和新密钥推过来。
		hello = c.PrefixFor(key)
	}
	// 旧圈连接上只出示来源圈链里本机已经是成员的最短前缀（见 oldHelloChain）。
	var oldChain [][]byte
	if oldKey != nil {
		oldChain = oldHelloChain(rec.OldChain, n.id.KeyID)
	}
	if old {
		hello = oldChain
	}
	peer, err = memberauth.Exchange(str, exp, memberauth.Config{
		Self: n.id, Name: n.Name(), Chain: hello, CircleKeys: keys, Dialer: dialed, Features: n.features,
		// 用来源圈的密钥连进来的只能看到来源圈的链，看不到本机现在的圈。
		ChainFor: func(k []byte) [][]byte {
			if oldKey != nil && bytes.Equal(k, oldKey) {
				return oldChain
			}
			return hello
		},
		// 在本机出示身份（接收方）或确认连接（发起方）之前核对对方：先合并它的签名链，再看它是不是成员。
		Admit: func(p *memberauth.Peer) error {
			if p.KeyID == n.id.KeyID {
				return errSelf
			}
			if oldKey != nil && bytes.Equal(p.GateKey, oldKey) {
				viaOld = true
				// 旧圈连接刚失败过（对方版本太旧、回复了错误）：冷却期内不再接受它，免得每隔几秒重来一遍。
				// 在拿 n.mu 之前查（moveCoolingDown 拿 daemon.mu）。
				if n.moveCoolingDown(p.KeyID) {
					return errMoveCooling
				}
				f, err := n.admitOld(p)
				forked = f
				if err != nil {
					return err
				}
				// 旧版本（没有声明 merge）只交换一次就进冷却期：在这里原子地占住冷却期，
				// 两边同时互拨时只有一条握手能通过（admitOld 已经放开 n.mu，moveFailed 拿 daemon.mu）。
				// 声明了 merge 的不占：两台帮手互拨时两条都要能通过（见 TestTwoHelpersConverge）。
				if !slices.Contains(p.Features, pairing.FeatureMerge) && !n.moveFailed(p.KeyID) {
					return errMoveCooling
				}
				return nil
			}
			ch, err := n.applyChain(p.Chain)
			changed = changed || ch // 出错时内存里的链也可能已经变了（合并前从数据库重新加载），照样要推送
			if err != nil {
				return err
			}
			if !n.isMember(p.KeyID) {
				return errNotMember
			}
			return nil
		},
	})
	if err != nil {
		return nil, changed, 0, viaOld, forked, err
	}
	if dialed {
		// 旧圈连接不登记为成员连接，和已有的连接不算重复。
		if !viaOld && n.hasLiveConn(peer.KeyID, relay.IsRelayed(conn.RemoteAddr())) {
			return nil, changed, 0, false, false, errDuplicate
		}
		if err := wire.Write(str, &pb.MemberReady{}); err != nil {
			return nil, changed, 0, viaOld, forked, err
		}
	} else {
		var ready pb.MemberReady
		if err := wire.Read(str, &ready); err != nil {
			return nil, changed, 0, viaOld, forked, err
		}
	}
	// 双方都已拿到对方的链：本机链因合并而变化时，对方的链就是合并的来源；否则对方会合并本机发出的链
	// （peer 实际看到的是 hello，不一定是完整链）。
	peerLen = len(hello)
	if changed {
		peerLen = len(peer.Chain)
	}
	return peer, changed, peerLen, viaOld, forked, nil
}

// applyChain 用对端的签名链更新本机；本机链发生变化时返回 true。
func (n *Node) applyChain(records [][]byte) (bool, error) {
	changed, err := n.mergeChain(records)
	if errors.Is(err, chain.ErrFork) {
		return changed, fmt.Errorf("node: membership chains have diverged, resolve on one device: %w", err)
	}
	return changed, err
}

// streamPeer 是流处理函数看到的对端：一台已通过成员握手的设备。
type streamPeer struct {
	KeyID string
	Name  string
}

// serveStreams 是成员连接上的流路由：对端新开的每条流以一条 Envelope 开头，按类型交给对应的处理函数。
// 阻塞到连接断开。
func (n *Node) serveStreams(ctx context.Context, conn *quic.Conn, peer streamPeer) {
	for {
		str, err := conn.AcceptStream(conn.Context())
		if err != nil {
			return
		}
		n.daemon.spawn(func() { n.routeStream(ctx, peer, str) })
	}
}

// routeStream 读出流的第一条 Envelope 并分发。读 Envelope 有超时；之后清除超时，
// 流的超时和关闭都由处理函数负责（传输的流可能持续很久）。
func (n *Node) routeStream(ctx context.Context, peer streamPeer, str *quic.Stream) {
	if !n.isMember(peer.KeyID) {
		// 对方刚被移出设备圈、连接还没来得及断开：不再处理它的任何请求。
		str.CancelRead(0)
		_ = str.Close()
		return
	}
	_ = str.SetReadDeadline(time.Now().Add(dialTimeout))
	var env pb.Envelope
	if err := wire.Read(str, &env); err != nil {
		str.CancelRead(0)
		_ = str.Close()
		return
	}
	_ = str.SetReadDeadline(time.Time{})
	switch msg := env.Msg.(type) {
	case *pb.Envelope_ChainUpdate:
		_ = str.Close()
		n.handleChainUpdate(ctx, peer, msg.ChainUpdate)
	case *pb.Envelope_Offer:
		n.daemon.recv.HandleOffer(ctx, peer.KeyID, str, msg.Offer)
	case *pb.Envelope_FileData:
		n.daemon.recv.HandleFileData(peer.KeyID, str, msg.FileData)
	case *pb.Envelope_RelaySettings:
		_ = str.Close()
		if raw, err := proto.Marshal(msg.RelaySettings); err == nil {
			n.handleRelaySettings(peer, raw)
		}
	case *pb.Envelope_Candidates:
		_ = str.Close()
		n.handleCandidates(ctx, peer, msg.Candidates)
	case *pb.Envelope_LeaveRequest:
		n.handleLeaveRequest(ctx, peer, str)
	case *pb.Envelope_CircleMove:
		n.handleCircleMove(ctx, peer, str, msg.CircleMove)
	default:
		// 不认识的消息类型：直接丢弃这条流
		str.CancelRead(0)
		_ = str.Close()
	}
}

func (n *Node) handleChainUpdate(ctx context.Context, peer streamPeer, u *pb.ChainUpdate) {
	changed, err := n.applyChain(u.Chain)
	if err != nil {
		n.opts.Logf(i18n.T("来自 %s 的设备圈更新无效：%v"), peer.Name, err)
		if changed {
			n.onChainChanged(ctx, i18n.Tf("从 %s 收到设备圈更新", peer.Name))
		}
		return
	}
	// 先合并链、再核对密钥：新密钥必须对得上（合并后）最近一条移除记录的校验值。
	newKey := false
	if len(u.DiscoveryKey) > 0 {
		if newKey, err = n.adoptDiscoveryKey(u.DiscoveryKey); err != nil {
			n.opts.Logf(i18n.T("保存 %s 下发的发现密钥失败：%v"), peer.Name, err)
		}
	}
	switch {
	case changed:
		n.onChainChanged(ctx, i18n.Tf("从 %s 收到设备圈更新", peer.Name))
	case newKey:
		// 链没变、只是拿到了新密钥：同样重启发现（换用新令牌），并转发给其他可能也还没拿到的成员。
		n.onChainChanged(ctx, i18n.Tf("从 %s 收到新的发现密钥", peer.Name))
	}
}

// adoptDiscoveryKey 采用成员下发的发现密钥：只有它对得上本机签名链最近一条移除记录的校验值、
// 且和当前密钥不同时才采用，返回 true。
func (n *Node) adoptDiscoveryKey(key []byte) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.chain == nil {
		return false, nil
	}
	check := n.chain.KeyCheck()
	if check == nil || !bytes.Equal(chain.DiscoveryKeyCheck(key), check) || bytes.Equal(key, n.discKey) {
		return false, nil
	}
	if err := stageDiscoveryKey(n.opts.Keys, n.store.WithWriteLock, key); err != nil {
		return false, err
	}
	return true, n.useChainLocked(n.chain)
}

// broadcastChain 把本机当前签名链推送给每个已连接成员。
func (n *Node) broadcastChain(ctx context.Context) {
	d := n.daemon
	d.mu.Lock()
	conns := make([]*memberConn, 0, len(d.conns))
	for _, mc := range d.conns {
		conns = append(conns, mc)
	}
	d.mu.Unlock()
	for _, mc := range conns {
		d.spawn(func() { n.pushChain(ctx, mc.peerID, mc.conn) })
	}
}

// pushChain 在 conn 上新开一条流，发送本机当前签名链；本机拿着最近一条移除记录对应的发现密钥时一并下发。
// 对方（peerID）已经不是成员时什么都不发：被移除的设备不能拿到新链，更不能拿到新密钥。
func (n *Node) pushChain(ctx context.Context, peerID string, conn *quic.Conn) {
	c, key := n.snapshot()
	if c == nil || !c.IsMember(peerID) {
		return
	}
	u := &pb.ChainUpdate{Chain: c.Records()}
	if check := c.KeyCheck(); check != nil && bytes.Equal(chain.DiscoveryKeyCheck(key), check) {
		u.DiscoveryKey = key
	}
	sctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	str, err := conn.OpenStreamSync(sctx)
	if err != nil {
		return
	}
	_ = wire.Write(str, &pb.Envelope{Msg: &pb.Envelope_ChainUpdate{ChainUpdate: u}})
	_ = str.Close()
}
