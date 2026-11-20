package node

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

	"portide/core/chain"
	"portide/core/i18n"
	"portide/core/identity"
	"portide/core/pb"
	"portide/core/relay"
	"portide/core/relayset"
	"portide/core/store"
	"portide/core/transport"
	"portide/core/wire"
)

const (
	// defaultRelayAfter 是"局域网优先"的等待时间：成员在中继上在线这么久、局域网还没连上，才经中继连接。
	defaultRelayAfter = 5 * time.Second
	relayBackoffMin   = 2 * time.Second
	relayBackoffMax   = time.Minute
	// defaultUDPProbe 是经 TCP 连着中继时试 UDP 是否恢复的间隔（设计 D6）。
	defaultUDPProbe = 10 * time.Minute
)

// ErrRelayNotFound 表示列表里没有这个指纹的中继。
var ErrRelayNotFound = i18n.New("没有这个中继")

// RelayOutdatedPeers 返回当前在线、并且最近一次声明的功能里没有 relays 的成员名（收不到中继设置的更改），按名字排序。
func (n *Node) RelayOutdatedPeers() []string {
	c, _ := n.snapshot()
	if c == nil {
		return nil
	}
	peers, err := n.store.Peers()
	if err != nil {
		return nil
	}
	feats, err := n.store.PeerFeatures()
	if err != nil {
		return nil
	}
	online := map[string]bool{}
	for _, p := range peers {
		online[p.KeyID] = p.Online
	}
	var out []string
	for _, m := range c.Members() {
		if m.KeyID == n.id.KeyID || !online[m.KeyID] || slices.Contains(feats[m.KeyID], FeatureRelays) {
			continue
		}
		out = append(out, m.Name)
	}
	slices.Sort(out)
	return out
}

// RelaySettingsInfo 返回最后一次修改中继设置的设备名和时间；没有设置时 ok 为 false。
// 签名者不在链里（例如已被移除）时名字为空。
func (n *Node) RelaySettingsInfo() (signerName string, at time.Time, ok bool) {
	s, err := n.loadRelaySettings()
	if s == nil || err != nil {
		return "", time.Time{}, false
	}
	if c, _ := n.snapshot(); c != nil {
		for _, m := range c.Members() {
			if m.PublicKey.Equal(s.Signer) {
				signerName = m.Name
				break
			}
		}
	}
	return signerName, s.Time, true
}

// loadRelaySettings 读出本机保存的中继设置；没有时返回 nil, nil。
// 只校验签名、不核对成员：签名者被移除后设置照样有效（设计 3.4），成员资格由收到设置时的 Parse 把关。
// 不碰 n.mu，持有 n.mu 时也能调用。
func (n *Node) loadRelaySettings() (*relayset.Settings, error) {
	v, ok, err := n.store.GetSecretMeta(store.MetaRelaySettings)
	if err != nil || !ok {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, err
	}
	return relayset.Parse(raw, func(ed25519.PublicKey) bool { return true })
}

// RelaySettings 返回本机保存的中继设置；没有设置时返回 nil, nil。
func (n *Node) RelaySettings() (*relayset.Settings, error) {
	return n.loadRelaySettings()
}

// Relays 返回本机配置的中继列表（没有设置时为空）。
func (n *Node) Relays() ([]relay.Config, error) {
	s, err := n.loadRelaySettings()
	if s == nil || err != nil {
		return nil, err
	}
	return s.Configs, nil
}

// saveRelaySettings 保存一份已签名的设置（加密元数据，值是 Raw 的 base64）。
func (n *Node) saveRelaySettings(s *relayset.Settings) error {
	return n.store.SetSecretMeta(store.MetaRelaySettings, base64.StdEncoding.EncodeToString(s.Raw))
}

// editRelays 读出当前列表，交给 fn 改，再以版本 + 1 由本机签名保存。
func (n *Node) editRelays(fn func([]relay.Config) ([]relay.Config, error)) error {
	n.relaySetMu.Lock()
	defer n.relaySetMu.Unlock()
	cur, err := n.loadRelaySettings()
	if err != nil {
		return err
	}
	var configs []relay.Config
	var version uint64 = 1
	if cur != nil {
		configs, version = cur.Configs, cur.Version+1
	}
	configs, err = fn(configs)
	if err != nil {
		return err
	}
	s, err := relayset.Sign(n.id.Private, version, configs, time.Now())
	if err != nil {
		return err
	}
	return n.saveRelaySettings(s)
}

// AddRelay 按指纹新增或更新一个中继并由本机签名保存（updated 为 true 表示更新了已有的）；已满返回 relayset.ErrFull。
// 守护进程在下一次轮询时按新列表重连。
func (n *Node) AddRelay(cfg relay.Config) (updated bool, err error) {
	err = n.editRelays(func(cs []relay.Config) ([]relay.Config, error) {
		out, up, err := relayset.Upsert(cs, cfg)
		updated = up
		return out, err
	})
	return updated, err
}

// RemoveRelay 按指纹删除一个中继；没有这个指纹返回 ErrRelayNotFound。删到空列表也保存一份新版本。
func (n *Node) RemoveRelay(fp [32]byte) error {
	return n.editRelays(func(cs []relay.Config) ([]relay.Config, error) {
		out, ok := relayset.Remove(cs, fp)
		if !ok {
			return nil, ErrRelayNotFound
		}
		return out, nil
	})
}

// handleRelaySettings 处理成员发来的中继设置：签名者必须是本机当前链的成员（设计 3.4），再与本机的设置合并。
// 采用对方的就原样保存；两边都有内容需要并集时由本机重新签名。daemon 在下一次轮询里按新列表重连并转发。
func (n *Node) handleRelaySettings(peer streamPeer, raw []byte) {
	c, _ := n.snapshot()
	if c == nil {
		return
	}
	in, err := relayset.Parse(raw, func(pub ed25519.PublicKey) bool { return c.IsMember(identity.KeyIDOf(pub)) })
	if err != nil {
		n.opts.Logf(i18n.T("来自 %s 的中继设置无效：%v"), peer.Name, err)
		return
	}
	// 与 editRelays 共用一把锁：读—合并—写是一个整体。
	n.relaySetMu.Lock()
	defer n.relaySetMu.Unlock()
	local, err := n.loadRelaySettings()
	if err != nil {
		n.opts.Logf(i18n.T("读取中继设置失败：%v"), err)
		return
	}
	act, configs, version := relayset.Combine(local, in)
	switch act {
	case relayset.Adopt:
		err = n.saveRelaySettings(in)
	case relayset.Resign:
		var s *relayset.Settings
		if s, err = relayset.Sign(n.id.Private, version, configs, time.Now()); err == nil {
			err = n.saveRelaySettings(s)
		}
	}
	if err != nil {
		n.opts.Logf(i18n.T("保存来自 %s 的中继设置失败：%v"), peer.Name, err)
	}
}

// pushRelaySettings 在 conn 上新开一条单向流，发送本机的中继设置。对方不是当前链的成员（换圈时 d.conns 里
// 还留着的旧圈连接）、没声明 relays、或本机没有设置时不发。
func (n *Node) pushRelaySettings(ctx context.Context, peerID string, conn *quic.Conn) {
	if c, _ := n.snapshot(); c == nil || !c.IsMember(peerID) {
		return
	}
	known, err := n.store.PeerFeatures()
	if err != nil || !slices.Contains(known[peerID], FeatureRelays) {
		return
	}
	s, err := n.loadRelaySettings()
	if err != nil || s == nil {
		return
	}
	var m pb.RelaySettings
	if proto.Unmarshal(s.Raw, &m) != nil {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	str, err := conn.OpenStreamSync(sctx)
	if err != nil {
		return
	}
	_ = wire.Write(str, &pb.Envelope{Msg: &pb.Envelope_RelaySettings{RelaySettings: &m}})
	_ = str.Close()
}

// broadcastRelaySettings 在本机设置的 Raw 与上次广播的不同时（本机改了、别的进程改了、或采用了别人的），
// 推给所有已连接的成员。守护进程轮询时调用。
func (n *Node) broadcastRelaySettings(ctx context.Context) {
	s, err := n.loadRelaySettings()
	if err != nil || s == nil {
		return
	}
	d := n.daemon
	cur := base64.StdEncoding.EncodeToString(s.Raw)
	if cur == d.relaySent {
		return
	}
	d.relaySent = cur
	d.mu.Lock()
	conns := make([]*memberConn, 0, len(d.conns))
	for _, mc := range d.conns {
		conns = append(conns, mc)
	}
	d.mu.Unlock()
	for _, mc := range conns {
		d.spawn(func() { n.pushRelaySettings(ctx, mc.peerID, mc.conn) })
	}
}

// firstRelayString 返回第一个中继的配置串（配对和换圈时交给对方）；没有时为空。
func (n *Node) firstRelayString() string {
	if rs, _ := n.Relays(); len(rs) > 0 {
		return rs[0].ConfigString()
	}
	return ""
}

// relaySettingsForWire 返回配对和换圈时交给对方的中继设置（原样的 Raw）；本机没有设置时为空。
// 本机没有声明 relays 时也不发（测试里据此模拟旧版本）。
func (n *Node) relaySettingsForWire() []byte {
	if !slices.Contains(n.features, FeatureRelays) {
		return nil
	}
	if s, _ := n.loadRelaySettings(); s != nil {
		return s.Raw
	}
	return nil
}

// combineRelaysOnSwitch 在换到链 c 的圈时合并中继设置（中继设置同步设计第 5 节）。本机原来的列表当作未签名的版本 0；
// 收到的 raw 按新链核对签名者，无效或为空时把 legacy（旧版本只发的 relay_config）当作版本 0、只有一项的设置。
// relayset.Combine 给出采用时原样保存收到的设置；否则由本机把并集（新圈的在前）签成收到的版本 + 1（至少 1），
// 收到了东西就不再停在版本 0。什么都没收到时才把本机原来的列表签成版本 0。
// 保存之后守护进程的轮询会把它推给新圈的成员。调用方持有 n.mu（锁顺序 n.mu → relaySetMu，同 resignOrphanedRelaySettings）。
// 出错只记日志：中继设置不该挡住换圈。
func (n *Node) combineRelaysOnSwitch(c *chain.Chain, raw []byte, legacy string) {
	n.relaySetMu.Lock()
	defer n.relaySetMu.Unlock()
	cur, err := n.loadRelaySettings()
	if err != nil {
		n.opts.Logf(i18n.T("读取中继设置失败，按没有中继处理：%v"), err)
		cur = nil
	}
	var in *relayset.Settings
	if len(raw) > 0 {
		in, err = relayset.Parse(raw, func(pub ed25519.PublicKey) bool { return c.IsMember(identity.KeyIDOf(pub)) })
		if err != nil {
			n.opts.Logf(i18n.T("新设备圈的中继设置无效：%v"), err)
			in = nil
		}
	}
	if in == nil && legacy != "" {
		if cfg, err := relay.ParseConfig(legacy); err == nil {
			in = &relayset.Settings{Configs: []relay.Config{cfg}}
		}
	}
	if cur == nil && in == nil {
		return
	}
	local := &relayset.Settings{}
	if cur != nil {
		local.Configs = cur.Configs
	}
	configs, version := local.Configs, uint64(0)
	if in != nil {
		if act, _, _ := relayset.Combine(local, in); act == relayset.Adopt {
			// 本机是版本 0，只有收到的是已签名、版本 >0、并且就是并集时才会采用。
			n.logDroppedRelays(in.Configs, local.Configs)
			if err := n.saveRelaySettings(in); err != nil {
				n.opts.Logf(i18n.T("保存中继设置失败：%v"), err)
			}
			return
		}
		// 新圈的在前：收到旧版本的 legacy（版本 0）时 Preferred 分不出谁新，这里固定让新圈的中继排前面。
		configs, version = relayset.Union(in.Configs, local.Configs), in.Version+1
		n.logDroppedRelays(configs, local.Configs, in.Configs)
	}
	s, err := relayset.Sign(n.id.Private, version, configs, time.Now())
	if err == nil {
		err = n.saveRelaySettings(s)
	}
	if err != nil {
		n.opts.Logf(i18n.T("保存中继设置失败：%v"), err)
	}
}

// logDroppedRelays 记下两份列表里因为超过 relayset.MaxRelays 个而没能放进 kept 的中继。
func (n *Node) logDroppedRelays(kept []relay.Config, lists ...[]relay.Config) {
	in := make(map[[32]byte]bool, len(kept))
	for _, c := range kept {
		in[c.Fingerprint] = true
	}
	var dropped []string
	for _, list := range lists {
		for _, c := range list {
			if !in[c.Fingerprint] {
				in[c.Fingerprint] = true
				dropped = append(dropped, c.Addr)
			}
		}
	}
	if len(dropped) > 0 {
		n.opts.Logf(i18n.T("中继超过 %d 个，没有加入：%s"), relayset.MaxRelays, i18n.List(dropped))
	}
}

// migrateLegacyRelay 把升级前的单个中继配置（relay_config）迁移成版本 0、由本机签名的设置。
// 旧键只读这一次，之后不读不写也不删。
func (n *Node) migrateLegacyRelay() error {
	if _, ok, err := n.store.GetSecretMeta(store.MetaRelaySettings); err != nil || ok {
		return err
	}
	old, ok, err := n.store.GetSecretMeta(store.MetaRelayConfig)
	if err != nil || !ok {
		return err
	}
	cfg, err := relay.ParseConfig(old)
	if err != nil {
		n.opts.Logf(i18n.T("旧的中继配置无法解析，没有迁移：%v"), err)
		return nil
	}
	s, err := relayset.Sign(n.id.Private, 0, []relay.Config{cfg}, time.Now())
	if err != nil {
		return err
	}
	return n.saveRelaySettings(s)
}

// resignOrphanedRelaySettings 在链变化后调用：设置的签名者已经不是成员（被移除了）时，
// 用同样的内容、同样的版本号由本机重新签名，否则其他设备会因签名者不是成员而拒收（设计 3.4）。
// 不加一：本机可能还没收到别的成员在同一版本号上（或更高版本上）的修改，加一会让这份旧内容盖过它；
// 版本相同时按签名者分胜负，内容相同的几份由不同成员重签也会收敛到一份。
// c 是刚换上的链；只用数据库和 c，不取 n.mu 和 daemon.mu，持有 n.mu 时也能调用。出错只记日志。
func (n *Node) resignOrphanedRelaySettings(c *chain.Chain) {
	if c == nil || !c.IsMember(n.id.KeyID) {
		return
	}
	n.relaySetMu.Lock()
	defer n.relaySetMu.Unlock()
	s, err := n.loadRelaySettings()
	if err != nil || s == nil || c.IsMember(identity.KeyIDOf(s.Signer)) {
		return
	}
	ns, err := relayset.Sign(n.id.Private, s.Version, s.Configs, time.Now())
	if err == nil {
		err = n.saveRelaySettings(ns)
	}
	if err != nil {
		n.opts.Logf(i18n.T("重新签名中继设置失败：%v"), err)
	}
}

// RelayStatuses 返回守护进程最近写下的各中继的连接状态：指纹（十六进制）→ 状态文本（例如"已连接"）。
// 守护进程没在运行、或还没写下某个中继的状态时，对应的项不存在。
func (n *Node) RelayStatuses() map[string]string {
	v, ok, _ := n.store.GetMeta(store.MetaRelayStatus)
	out := map[string]string{}
	if ok {
		_ = json.Unmarshal([]byte(v), &out) // 升级前写下的是单个字符串：解析不了就当没有
	}
	return out
}

// setRelayStatus 写下指纹为 fp 的中继的状态；s 为空时删掉这一项。读—改—写，调用方持有 daemon.mu 来串行化。
func (n *Node) setRelayStatus(fp [32]byte, s string) {
	all := n.RelayStatuses()
	key := hex.EncodeToString(fp[:])
	if s == "" {
		if _, ok := all[key]; !ok {
			return
		}
		delete(all, key)
	} else {
		all[key] = s
	}
	if len(all) == 0 {
		_ = n.store.DeleteMeta(store.MetaRelayStatus)
		return
	}
	b, _ := json.Marshal(all)
	_ = n.store.SetMeta(store.MetaRelayStatus, string(b))
}

// clearRelayStatuses 清空所有中继的状态（守护进程退出时）。
func (n *Node) clearRelayStatuses() {
	_ = n.store.DeleteMeta(store.MetaRelayStatus)
}

// udpProbeEvery 是经 TCP 连着中继时探测 UDP 的间隔（Options.RelayUDPProbe，默认 defaultUDPProbe）。
func (n *Node) udpProbeEvery() time.Duration {
	if n.opts.RelayUDPProbe > 0 {
		return n.opts.RelayUDPProbe
	}
	return defaultUDPProbe
}

// relayDialer 返回连中继用的 Dialer：UDP 走守护进程自己的端点（测试可经 relayUDPDial 包装）。
func (n *Node) relayDialer() relay.Dialer {
	udp := relay.DialFunc(n.daemon.t.DialRelay)
	if n.relayUDPDial != nil {
		udp = n.relayUDPDial(udp)
	}
	return relay.Dialer{UDP: udp, TCPDelay: n.relayTCPDelay}
}

// probeDial 返回探测 UDP 用的拨号：守护进程自己的端点（测试可经 relayProbeDial 包装）。
func (n *Node) probeDial() relay.DialFunc {
	udp := relay.DialFunc(n.daemon.t.DialRelay)
	if n.relayProbeDial != nil {
		udp = n.relayProbeDial(udp)
	}
	return udp
}

func (n *Node) relayAfter() time.Duration {
	if n.opts.RelayAfter > 0 {
		return n.opts.RelayAfter
	}
	return defaultRelayAfter
}

// relayConn 是到一个中继的连接循环（relayLoop）的状态。除 cfg、raw、stop 外的字段由 daemon.mu 保护。
type relayConn struct {
	cfg    relay.Config
	raw    string               // cfg.ConfigString()：指纹相同但地址或令牌变了时据此重启
	stop   context.CancelFunc   // 停止这个 relayLoop
	client *relay.Client        // 当前到这个中继的连接；没连上时为 nil
	online map[string]time.Time // 这个中继报告在线的成员 KeyID → 第一次看到它在线的时间
}

// relayOnlineSince 报告成员 keyID 是否在任意一个已连上的中继上在线，以及最早从什么时候开始。调用方持有 d.mu。
func (d *daemon) relayOnlineSince(keyID string) (since time.Time, ok bool) {
	for _, rc := range d.relays {
		if t, on := rc.online[keyID]; on && rc.client != nil && (!ok || t.Before(since)) {
			since, ok = t, true
		}
	}
	return since, ok
}

// restartRelayWait 把成员 keyID 在各中继上的在线起始时间改成现在：经中继的路刚失败或连接刚断开，
// 重新给局域网 relayAfter 的时间。调用方持有 d.mu。
func (d *daemon) restartRelayWait(keyID string) {
	now := time.Now()
	for _, rc := range d.relays {
		if _, ok := rc.online[keyID]; ok {
			rc.online[keyID] = now
		}
	}
}

// relayConnsInOrder 按 Relays() 的顺序（即拨号的优先顺序）返回正在运行的连接循环。不能持有 d.mu 调用。
func (n *Node) relayConnsInOrder() []*relayConn {
	cfgs, _ := n.Relays()
	d := n.daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []*relayConn
	for _, c := range cfgs {
		if rc := d.relays[c.Fingerprint]; rc != nil {
			out = append(out, rc)
		}
	}
	return out
}

// syncRelay 让中继连接与数据库里的列表保持一致：按指纹增删连接循环，指纹相同但配置串变了（换了地址或令牌）
// 的只重启这一个。守护进程启动时和每次轮询时调用。
func (n *Node) syncRelay(ctx context.Context) {
	cfgs, err := n.Relays()
	if err != nil {
		return
	}
	want := make(map[[32]byte]relay.Config, len(cfgs))
	for _, c := range cfgs {
		want[c.Fingerprint] = c
	}
	d := n.daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	for fp, rc := range d.relays {
		c, keep := want[fp]
		if keep && c.ConfigString() == rc.raw {
			continue
		}
		// 删掉（或换了配置）的中继：停掉循环，它告诉我们的在线状态随 rc 一起作废，不会再对它发起经中继的拨号。
		rc.stop()
		delete(d.relays, fp)
		if !keep {
			n.setRelayStatus(fp, "")
		}
	}
	for _, c := range cfgs {
		if d.relays[c.Fingerprint] != nil {
			continue
		}
		rctx, cancel := context.WithCancel(context.Background())
		rc := &relayConn{cfg: c, raw: c.ConfigString(), stop: cancel, online: map[string]time.Time{}}
		d.relays[c.Fingerprint] = rc
		d.spawn(func() { n.relayLoop(ctx, rctx, rc) })
	}
}

// relayLoop 保持与一个中继的连接（设计方案 4.2 第 1 条）：登记、订阅成员上线、接收经中继连过来的会话；断开后退避重连。
// runCtx 是守护进程的主上下文，经中继的成员连接用它，和局域网连接一样随守护进程退出，不随中继配置变化而中断其上的发现和推送。
// ctx 控制这一轮中继连接（这个中继被删掉或换了配置就取消）。
func (n *Node) relayLoop(runCtx, ctx context.Context, rc *relayConn) {
	d := n.daemon
	cfg := rc.cfg
	backoff := relayBackoffMin
	// 只在这一轮还没被停掉时写状态：中继刚被删除或替换时，不能用旧连接的状态覆盖 syncRelay 写下的。
	status := func(s string) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if ctx.Err() == nil {
			n.setRelayStatus(cfg.Fingerprint, s)
		}
	}
	for ctx.Err() == nil {
		cctx, cancel := context.WithTimeout(ctx, 2*dialTimeout)
		cl, err := relay.Connect(cctx, n.relayDialer(), cfg, n.id)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			status(i18n.Tf("连接失败：%s", relayErrText(err, cfg)))
			n.opts.Logf(i18n.T("连接中继 %s 失败：%s，%v 后重试"), cfg.Addr, relayErrText(err, cfg), backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(2*backoff, relayBackoffMax)
			continue
		}
		backoff = relayBackoffMin
		viaTCP := cl.Transport() == "tcp"
		if viaTCP {
			n.opts.Logf(i18n.T("已连接中继 %s（TCP）"), cfg.Addr)
			status(i18n.T("已连接（TCP）"))
		} else {
			n.opts.Logf(i18n.T("已连接中继 %s"), cfg.Addr)
			status(i18n.T("已连接"))
		}
		d.mu.Lock()
		if ctx.Err() != nil {
			d.mu.Unlock()
			_ = cl.Close()
			return
		}
		rc.client = cl
		d.mu.Unlock()
		n.relaySubscribe()
		d.spawn(func() {
			select {
			case <-ctx.Done():
				_ = cl.Close()
			case <-cl.Done():
			}
		})
		d.spawn(func() { n.relayAccept(runCtx, ctx, cl) })
		var switched atomic.Bool // 探测发现 UDP 恢复、主动关掉了这个经 TCP 的客户端
		if viaTCP {
			d.spawn(func() {
				if n.probeUDP(ctx, cl, cfg) {
					switched.Store(true)
					_ = cl.Close()
				}
			})
		}
		n.relayEvents(rc, cl) // 阻塞到与中继的连接断开
		d.mu.Lock()
		if rc.client == cl {
			rc.client = nil
		}
		rc.online = map[string]time.Time{}
		d.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if switched.Load() {
			// 主动改回 UDP：马上重连（先拨 UDP），不等退避，也不写"连接已断开"。
			// UDP 要是又不通，会再落到 TCP，新的探测从头等满一个间隔（不会反复切换）。
			continue
		}
		status(i18n.T("连接已断开，正在重连"))
		n.opts.Logf(i18n.T("与中继 %s 的连接断开，%v 后重连"), cfg.Addr, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// probeUDP 在经 TCP 连着中继时，每隔 udpProbeEvery 只经 UDP 和中继握手一次（不登记，设计 D6）。
// 握手成功时记日志并返回 true，由调用方关掉这个客户端、重新连接；ctx 结束或 cl 断开时返回 false。
// 每个经 TCP 的客户端各自从头计时：重连后又落到 TCP 时，要等满一个间隔才再探测（Review Focus 4）。
func (n *Node) probeUDP(ctx context.Context, cl *relay.Client, cfg relay.Config) bool {
	every := n.udpProbeEvery()
	timer := time.NewTimer(every)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-cl.Done():
			return false
		case <-timer.C:
		}
		pctx, cancel := context.WithTimeout(ctx, dialTimeout)
		err := relay.ProbeUDP(pctx, n.probeDial(), cfg)
		cancel()
		if err == nil {
			select {
			case <-ctx.Done():
				return false
			case <-cl.Done():
				return false
			default:
			}
			n.opts.Logf(i18n.T("中继 %s 的 UDP 恢复了，改回 UDP"), cfg.Addr)
			return true
		}
		timer.Reset(every)
	}
}

// relayErrText 把中继错误换成简短的中文说明。没有回应时用 cfg 的 UDP 和 TCP 端口提示要放行哪些端口。
func relayErrText(err error, cfg relay.Config) string {
	var hto *quic.HandshakeTimeoutError
	var ito *quic.IdleTimeoutError
	switch {
	case errors.Is(err, relay.ErrNoResponse), errors.Is(err, context.DeadlineExceeded), errors.As(err, &hto), errors.As(err, &ito):
		// 包发出去了却没有任何回应：多半是服务器的防火墙或云安全组没放行 UDP 和 TCP 端口。
		udpPort, tcpPort := relay.DefaultPort, relay.DefaultPort
		if _, p, err := net.SplitHostPort(cfg.Addr); err == nil {
			udpPort, tcpPort = p, p
		}
		if _, p, err := net.SplitHostPort(cfg.TCPAddr()); err == nil {
			tcpPort = p
		}
		return i18n.Tf("中继没有回应（检查地址是否正确，以及服务器的防火墙和云服务商的安全组是否放行了 UDP %s 或 TCP %s 端口）", udpPort, tcpPort)
	case errors.Is(err, relay.ErrFingerprint):
		return i18n.T("中继指纹不对（配置串里的地址或指纹有误）")
	case errors.Is(err, relay.ErrRejected):
		// 只显示中继的拒绝原因
		reason := strings.TrimPrefix(err.Error(), relay.ErrRejected.Error()+": ")
		return i18n.Tf("中继拒绝了登记（%s）", reason)
	}
	return err.Error()
}

// relaySubscribe 按当前成员名单（除本机外）向每个已连上的中继订阅上线通知。签名链变化时、连上一个中继时调用。
func (n *Node) relaySubscribe() {
	d := n.daemon
	d.relaySubMu.Lock()
	defer d.relaySubMu.Unlock()
	c, _ := n.snapshot()
	if c == nil {
		return
	}
	routes := map[string]string{}
	var list [][]byte
	for _, m := range c.Members() {
		if m.KeyID == n.id.KeyID {
			continue
		}
		r := relay.RouteID(m.PublicKey)
		routes[hex.EncodeToString(r)] = m.KeyID
		list = append(list, r)
	}
	d.mu.Lock()
	d.routes = routes
	var clients []*relay.Client
	for _, rc := range d.relays {
		if rc.client != nil {
			clients = append(clients, rc.client)
		}
		// 已经不是成员的设备不再关注。
		for id := range rc.online {
			if !c.IsMember(id) {
				delete(rc.online, id)
			}
		}
	}
	d.mu.Unlock()
	for _, cl := range clients {
		_ = cl.Subscribe(list)
	}
}

// relayEvents 处理中继 rc 的上线 / 下线通知，直到连接 cl 断开。上线的成员在 relayAfter 之后仍没有从局域网连上时，
// dialMembers 会经中继连接它（局域网优先，设计方案 4.2 第 3 条）。
func (n *Node) relayEvents(rc *relayConn, cl *relay.Client) {
	d := n.daemon
	for {
		e, err := cl.NextEvent()
		if err != nil {
			return
		}
		d.mu.Lock()
		if id, ok := d.routes[hex.EncodeToString(e.Route)]; ok && rc.client == cl {
			if e.Online {
				if _, seen := rc.online[id]; !seen {
					rc.online[id] = time.Now()
				}
			} else {
				delete(rc.online, id)
			}
		}
		d.mu.Unlock()
	}
}

// relayAccept 接收经中继连过来的会话，在上面完成成员握手并服务这条连接。
// runCtx 是守护进程的主上下文，经中继的成员连接用它，不随中继配置变化而中断。
// ctx 控制这一轮中继连接（配置变了就取消）。
func (n *Node) relayAccept(runCtx, ctx context.Context, cl *relay.Client) {
	d := n.daemon
	for {
		pc, err := cl.Accept(ctx)
		if err != nil {
			return
		}
		d.spawn(func() {
			actx, cancel := context.WithTimeout(ctx, dialTimeout)
			conn, closer, err := transport.AcceptMemberOver(actx, pc)
			cancel()
			if err != nil {
				return
			}
			defer closer()
			n.serveMember(runCtx, conn, false, false)
		})
	}
}

// dialRelayed 经中继连接成员 keyID：在对方在线的中继里按列表顺序逐个尝试，一个失败就试下一个。
// 连上时服务这条连接直到断开并返回 true。old 为 true 时是旧圈连接（见 serveMember）。
func (n *Node) dialRelayed(ctx context.Context, keyID string, old bool) bool {
	c, _ := n.snapshot()
	m, ok := c.Member(keyID)
	if !ok {
		return false
	}
	d := n.daemon
	for _, rc := range n.relayConnsInOrder() {
		d.mu.Lock()
		cl := rc.client
		_, on := rc.online[keyID]
		d.mu.Unlock()
		if cl == nil || !on || ctx.Err() != nil {
			continue
		}
		pc, err := cl.Open(ctx, relay.RouteID(m.PublicKey))
		if err != nil {
			d.mu.Lock()
			if errors.Is(err, relay.ErrOffline) {
				delete(rc.online, keyID)
			} else if _, ok := rc.online[keyID]; ok {
				// 这个中继这条路失败：等 relayAfter 之后再试它，期间照常拨局域网
				rc.online[keyID] = time.Now()
			}
			d.mu.Unlock()
			continue
		}
		dctx, cancel := context.WithTimeout(ctx, dialTimeout)
		conn, closer, err := transport.DialMemberOver(dctx, pc)
		cancel()
		if err != nil {
			d.mu.Lock()
			if _, ok := rc.online[keyID]; ok {
				rc.online[keyID] = time.Now()
			}
			d.mu.Unlock()
			continue
		}
		defer closer()
		n.serveMember(ctx, conn, true, old)
		return true
	}
	return false
}
