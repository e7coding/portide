// Package node 把身份、签名链、存储、传输、发现和配对组合成一台设备。
// 命令行的一次性命令（init、pair、join、devices）和守护进程（Run）都通过 Node 工作，
// 二者共享同一个数据目录：数据库（store）和密钥库（keystore）。
package node

import (
	"bytes"
	"crypto/rand"
	"errors"
	"portide/core/i18n"
	"fmt"
	"log"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"portide/core/chain"
	"portide/core/discovery"
	"portide/core/identity"
	"portide/core/keystore"
	"portide/core/pairing"
	"portide/core/relay"
	"portide/core/store"
)

// ErrNotInitialized 表示数据目录里还没有设备身份，需要先执行 init。
var ErrNotInitialized = errors.New("node: device not initialized (run `portide init` first)")

// ErrNotInCircle 表示本机还没有加入任何设备圈。
var ErrNotInCircle = errors.New("node: not in a circle yet")

// ErrInCircle 表示本机已经在设备圈里：设备名要用改名记录修改（RenameDevice），不能再直接设置。
var ErrInCircle = errors.New("node: already in a circle")

// ErrNoName 表示本机还没有设备名（例如重装后密钥还在、数据目录是新的），不能加入设备圈。
var ErrNoName = errors.New("node: device name is not set")

// ErrRemoveSelf 表示试图把本机移出设备圈：移除记录必须由另一台成员签名。
var ErrRemoveSelf = errors.New("node: a device cannot remove itself")

// ErrRemovedKeyPending 表示移除记录已经成功写进签名链，但新的发现密钥这次没能启用
// （例如密钥库被另一个进程并发改动）：移除本身是有效的，守护进程下次读取设备圈时会补完。
var ErrRemovedKeyPending = errors.New("node: device removed, but the new discovery key is not active yet")

// heartbeatFresh 是判断守护进程仍在运行的心跳时效。
const heartbeatFresh = 3 * time.Second

// Options 配置一个 Node。
type Options struct {
	DataDir     string
	Keys        keystore.Store
	ListenAddr  string   // 守护进程监听地址；为空时用设置里的端口（ListenPort，默认 7443）
	StaticPeers []string // 额外尝试连接的地址（mDNS 被屏蔽时使用）
	DisableMDNS bool     // 关闭局域网发现（测试或受限网络）
	// Discovery 是局域网发现的实现，为 nil 时用 zeroconf 的 mDNS（discovery.MDNS）；iOS 传入 Swift 的 Bonjour（M4）。
	Discovery    discovery.Discoverer
	PollInterval time.Duration // 守护进程检查数据库的间隔，默认 1s
	RelayAfter   time.Duration // 成员在中继上在线多久、局域网仍没连上时改经中继连接，默认 5s
	// RelayUDPProbe 是经 TCP 连着中继时，多久试一次 UDP 是否恢复（只握手、不登记），默认 10 分钟（defaultUDPProbe）
	RelayUDPProbe time.Duration
	Logf          func(format string, args ...any)
	// OnChange 在数据库里界面关心的某类数据被改写后调用，参数是 store.Change* 主题（见 store.SetOnChange 的约束：
	// 必须很快返回、不能阻塞、不能调用 Node 或 Store 的方法）。为 nil 时不调用。
	// 它可能在守护进程内部持有锁（daemon.mu / Node.mu）的时候被调用：阻塞会卡住守护进程，
	// 回调 Node 或 Store 可能去抢同一把锁而死锁，所以只能记下信号、交给别的 goroutine 处理。
	OnChange func(topic string)
	// OnEvent 在值得通知用户的事发生后调用（收到、发送失败、等待确认，见 Event），通知设置关闭时不调用（M3b）。
	// 约束同 OnChange。为 nil 时不调用。
	OnEvent func(Event)
}

// Node 是一台设备。
type Node struct {
	opts  Options
	store *store.Store
	id    *identity.Identity
	name  string

	relaySetMu sync.Mutex // 串行化中继设置的读改写。可以在持有 mu 时拿（mu → relaySetMu），持有它时绝不拿 mu

	mu sync.Mutex
	// chain 指向的对象一旦发布就不再修改：更新时整体替换指针，
	// 这样其他 goroutine 拿到的旧指针可以安全地在锁外读取。尚未加入设备圈时为 nil。
	chain   *chain.Chain
	discKey []byte      // 当前发现密钥：发布 mDNS 令牌、主动连接时的圈子证明都用它
	retired []ringEntry // 轮换下来的旧发现密钥：宽限期内作为接收方仍然接受（见 retiredKeyGrace）
	// localChange 表示本进程自己改了签名链（RemoveDevice）：守护进程的下一次轮询据此推送，
	// 因为这种变化不会让数据库里的链比内存里的长，reloadCircle 发现不了。
	localChange bool
	// candidatesHook 只给测试用：改写本机发给对方的直连候选地址（例如模拟打不通的网络）。
	candidatesHook func([]string) []string
	// relayUDPDial 只给测试用：包装连中继时 UDP 那条路的拨号（参数是守护进程端点的 DialRelay），例如模拟 UDP 被拦。
	relayUDPDial func(relay.DialFunc) relay.DialFunc
	// relayProbeDial 只给测试用：同 relayUDPDial，但只用于经 TCP 连着时探测 UDP（relay.ProbeUDP）。
	relayProbeDial func(relay.DialFunc) relay.DialFunc
	// relayTCPDelay 只给测试用：relay.Dialer.TCPDelay，为 0 时用 relay.TCPDelay。
	relayTCPDelay time.Duration
	// upgradeEvery 只给测试用：经中继时重新交换候选地址的间隔，默认 defaultUpgradeEvery。
	upgradeEvery time.Duration
	// punchHook 只给测试用：每次向候选地址发起直连之前调用；返回错误时当作这次直连失败、不再真的拨号。
	punchHook func(addr string) error
	// features 是本机在配对和成员握手里声明的功能，Open 时设为包级的 features；测试可以在启动前换掉它来模拟旧版本。
	features []string
	// move 是迁移记录和来源圈发现密钥的缓存（见 move.go）。
	move moveState
	// moveMu 让收到的迁移消息一条一条处理（先到先得）。在 n.mu 之前拿；持有时不拿 daemon.mu。
	moveMu sync.Mutex

	// 以下字段只在守护进程（Run）中使用。
	daemon *daemon
}

// DeviceStatus 是 Devices 返回的一行。
type DeviceStatus struct {
	KeyID    string
	Name     string
	Self     bool
	Online   bool
	Addr     string
	LastSeen time.Time
	JoinedAt time.Time // 加入设备圈的时间（M3c：设备详情里显示）
	// Move 是合并之后还没跟过来的设备的状态（Move* 常量之一，合并设备圈设计 5.3），其余设备为空。
	Move string
	// MoveUntil 是迁移等待期结束的时间（Move 不为空时才有）。
	MoveUntil time.Time
}

// DeviceStatus.Move 的取值。
const (
	MoveWaiting   = "waiting"   // 还在等它跟过来
	MoveOutdated  = "outdated"  // 它的 Portide 版本不支持合并设备圈，需要先升级
	MoveLeft      = "left"      // 等待期过了，没有跟过来
	MoveElsewhere = "elsewhere" // 它已经合并到了别的设备圈
)

// Init 在 dataDir 中创建设备身份（已存在则保留），并设置设备名。返回 KeyID。
func Init(dataDir string, keys keystore.Store, name string) (string, error) {
	if err := chain.ValidName(name); err != nil {
		return "", fmt.Errorf("node: %w", err)
	}
	st, err := store.Open(dataDir)
	if err != nil {
		return "", err
	}
	defer st.Close()
	recorded, hasRecorded, err := st.GetMeta(store.MetaKeyID)
	if err != nil {
		return "", err
	}
	seed, err := keys.Get(keystore.DeviceSeed)
	var id *identity.Identity
	switch {
	case errors.Is(err, keystore.ErrNotFound):
		if hasRecorded {
			return "", fmt.Errorf("node: data dir was initialized as device %.16s… but its key is missing from this keystore (did the data dir path or PORTIDE_KEYSTORE change?)", recorded)
		}
		if id, err = identity.Generate(); err != nil {
			return "", err
		}
		if err := keys.Set(keystore.DeviceSeed, id.Seed()); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	default:
		if id, err = identity.FromSeed(seed); err != nil {
			return "", err
		}
	}
	if hasRecorded && recorded != id.KeyID {
		return "", fmt.Errorf("node: keystore holds device %.16s… but the data dir belongs to %.16s…", id.KeyID, recorded)
	}
	if err := st.SetMeta(store.MetaKeyID, id.KeyID); err != nil {
		return "", err
	}
	if err := st.SetMeta(store.MetaDeviceName, name); err != nil {
		return "", err
	}
	return id.KeyID, nil
}

// Open 打开一台已初始化的设备。
func Open(opts Options) (*Node, error) {
	if opts.PollInterval == 0 {
		opts.PollInterval = time.Second
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	seed, err := opts.Keys.Get(keystore.DeviceSeed)
	if errors.Is(err, keystore.ErrNotFound) {
		return nil, ErrNotInitialized
	}
	if err != nil {
		return nil, err
	}
	id, err := identity.FromSeed(seed)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(opts.DataDir)
	if err != nil {
		return nil, err
	}
	st.SetOnChange(opts.OnChange)
	name, _, err := st.GetMeta(store.MetaDeviceName)
	if err != nil {
		st.Close()
		return nil, err
	}
	if recorded, hasRecorded, err := st.GetMeta(store.MetaKeyID); err != nil {
		st.Close()
		return nil, err
	} else if hasRecorded && recorded != id.KeyID {
		st.Close()
		return nil, fmt.Errorf("node: keystore holds device %.16s… but the data dir belongs to %.16s…", id.KeyID, recorded)
	}
	if err := openDatabaseKey(opts.Keys, st); err != nil {
		st.Close()
		return nil, err
	}
	n := &Node{opts: opts, store: st, id: id, name: name, features: features}
	if _, err := n.reloadCircle(); err != nil {
		st.Close()
		return nil, err
	}
	if err := n.loadMoveLocked(); err != nil {
		st.Close()
		return nil, err
	}
	if err := n.migrateLegacyRelay(); err != nil {
		st.Close()
		return nil, err
	}
	return n, nil
}

// openDatabaseKey 从密钥库取出数据库密钥（第一次时生成并保存），交给数据库，
// 再把升级前以明文写入的敏感字段改写成密文（设计方案 6.2）。
func openDatabaseKey(keys keystore.Store, st *store.Store) error {
	err := st.InitKey(func() ([]byte, bool, error) {
		key, err := keys.Get(keystore.DatabaseKey)
		if errors.Is(err, keystore.ErrNotFound) {
			return nil, false, nil
		}
		return key, err == nil, err
	}, func() ([]byte, error) {
		key := make([]byte, store.DBKeySize)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		return key, keys.Set(keystore.DatabaseKey, key)
	})
	if err != nil {
		return err
	}
	return st.SealLegacy()
}

// Close 释放数据库。
func (n *Node) Close() error { return n.store.Close() }

// Identity 返回本机身份。
func (n *Node) Identity() *identity.Identity { return n.id }

// Name 返回本机设备名：在设备圈里时以链上的名字为准（别的成员可能改过它），否则用初始化时的名字。
func (n *Node) Name() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.chain != nil {
		if m, ok := n.chain.Member(n.id.KeyID); ok {
			return m.Name
		}
	}
	return n.name
}

// SetName 在加入设备圈之前设置本机的设备名。iOS 删掉应用后 Keychain 里的密钥还在，重装后的数据目录里却没有名字，
// 这时由首次启动引导补上。已经在设备圈里时返回 ErrInCircle（改名要追加改名记录）。
func (n *Node) SetName(name string) error {
	if err := chain.ValidName(name); err != nil {
		return fmt.Errorf("node: %w", err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.chain != nil {
		return ErrInCircle
	}
	if err := n.store.SetMeta(store.MetaDeviceName, name); err != nil {
		return err
	}
	n.name = name
	return nil
}

// Store 返回本机数据库（命令行用来创建配对会话、查询状态）。
func (n *Node) Store() *store.Store { return n.store }

// InCircle 报告本机是否已加入设备圈。
func (n *Node) InCircle() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.chain != nil
}

// CircleID 返回设备圈 ID；未加入时为空。
func (n *Node) CircleID() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.chain == nil {
		return ""
	}
	return n.chain.CircleID()
}

// EnsureCircle 在尚未加入设备圈时创建一个新的（本机为第一个成员）。
func (n *Node) EnsureCircle() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.chain != nil {
		return nil
	}
	c, err := chain.New(n.id, n.name)
	if err != nil {
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	// 先保存发现密钥再保存链：有链就一定有密钥。
	if err := n.opts.Keys.Set(keystore.DiscoveryKey, key); err != nil {
		return err
	}
	if err := n.store.SaveChain(c.Records()); err != nil {
		return err
	}
	n.chain, n.discKey = c, key
	return nil
}

// DaemonRunning 根据心跳判断本数据目录的守护进程是否在运行。
func (n *Node) DaemonRunning() bool {
	v, ok, err := n.store.GetMeta(store.MetaDaemonHeartbeat)
	if err != nil || !ok {
		return false
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	return err == nil && time.Since(time.UnixMilli(ms)) < heartbeatFresh
}

// Devices 返回设备圈成员及其在线状态（在线状态由守护进程写入数据库）。
func (n *Node) Devices() ([]DeviceStatus, error) {
	n.mu.Lock()
	c := n.chain
	n.mu.Unlock()
	if c == nil {
		return nil, ErrNotInCircle
	}
	peers, err := n.store.Peers()
	if err != nil {
		return nil, err
	}
	byID := map[string]store.Peer{}
	for _, p := range peers {
		byID[p.KeyID] = p
	}
	// 迁移记录按数据库读，不按缓存：界面所在的进程不运行守护进程，缓存跟不上守护进程对记录的修改。
	mv, err := n.store.CircleMove()
	if err != nil {
		return nil, err
	}
	if mv != nil && mv.OldCircle == c.CircleID() {
		mv = nil // 换圈中途失败留下的，守护进程会删掉它
	}
	var feats map[string][]string
	if mv != nil && len(mv.Pending) > 0 {
		if feats, err = n.store.PeerFeatures(); err != nil {
			return nil, err
		}
	}
	var out []DeviceStatus
	for _, m := range c.Members() {
		d := DeviceStatus{KeyID: m.KeyID, Name: m.Name, Self: m.KeyID == n.id.KeyID, JoinedAt: m.AddedAt}
		if p, ok := byID[m.KeyID]; ok {
			d.Online, d.Addr, d.LastSeen = p.Online, p.Addr, p.LastSeen
		}
		if mv != nil && slices.Contains(mv.Pending, m.KeyID) {
			d.Move, d.MoveUntil = moveStatus(mv, m.KeyID, feats[m.KeyID]), mv.ExpiresAt
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Self && !out[j].Self })
	return out, nil
}

// moveStatus 返回还没跟过来的设备 keyID 在界面上的状态（合并设备圈设计 5.3）。features 是它最近一次声明的功能。
func moveStatus(mv *store.CircleMove, keyID string, features []string) string {
	switch {
	case slices.Contains(mv.Elsewhere, keyID):
		return MoveElsewhere
	case !time.Now().Before(mv.ExpiresAt):
		return MoveLeft
	case len(features) > 0 && !slices.Contains(features, pairing.FeatureMerge):
		return MoveOutdated
	default:
		return MoveWaiting
	}
}

// isMember 判断 KeyID 是否为当前成员（成员握手合并对方签名链之后调用）。
func (n *Node) isMember(keyID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.chain != nil && n.chain.IsMember(keyID)
}

// reloadCircle 从数据库重新加载签名链和发现密钥；链发生变化时返回 true。
func (n *Node) reloadCircle() (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.reloadCircleLocked()
}

// reloadCircleLocked 是持有 n.mu 时的 reloadCircle。
func (n *Node) reloadCircleLocked() (bool, error) {
	recs, err := n.store.LoadChain()
	if err != nil {
		return false, err
	}
	// 数据库里的链是内存链的前缀（包括相同）时不换：读库与合并新链并发时，读到的可能比内存里的旧。
	// 其他情况都以数据库为准，包括更短或一样长、内容不同的链（收敛改名分叉时换掉了后缀，内存可能没跟上）。
	if len(recs) == 0 || (n.chain != nil && isPrefix(recs, n.chain.Records())) {
		return false, nil
	}
	c, err := chain.Load(recs)
	if err != nil {
		return false, fmt.Errorf("node: stored chain is invalid: %w", err)
	}
	switched := n.chain == nil || n.chain.CircleID() != c.CircleID()
	if err := n.useChainLocked(c); err != nil {
		return false, err
	}
	if switched {
		// 别的进程（例如命令行的 portide join）换了圈：迁移记录和旧密钥也是它写的（或清掉的），一起重新读。
		// 读不出来时清空缓存（宁可这次不带旧设备过来，也不能拿旧密钥去找人），链照常换上。
		if err := n.loadMoveLocked(); err != nil {
			n.move = moveState{}
			n.opts.Logf(i18n.T("读取迁移记录失败：%v"), err)
		}
	}
	return true, nil
}

// useChainLocked 换上新的签名链，并按它确定当前发现密钥（见 resolveDiscoveryKeys）。调用方持有 n.mu。
func (n *Node) useChainLocked(c *chain.Chain) error {
	keys, err := resolveDiscoveryKeys(n.opts.Keys, n.store.WithWriteLock, c, time.Now())
	if err != nil {
		return err
	}
	n.chain, n.discKey, n.retired = c, keys.current, keys.retired
	n.syncOwnNameLocked()
	n.resignOrphanedRelaySettings(c)
	return nil
}

// syncOwnNameLocked 让本机记下的设备名跟着链走（别的设备可能给本机改了名）：被移除后重新配对时用的是它。
// 写数据库失败只记日志，不影响换链。调用方持有 n.mu。
func (n *Node) syncOwnNameLocked() {
	m, ok := n.chain.Member(n.id.KeyID)
	if !ok || m.Name == n.name {
		return
	}
	n.name = m.Name
	if err := n.store.SetMeta(store.MetaDeviceName, m.Name); err != nil {
		n.opts.Logf(i18n.T("保存本机设备名失败：%v"), err)
	}
}

// mergeChain 用对端的链更新本机链，变化时持久化。返回值表示内存里的链是否变了（包括为了合并而从数据库重新加载到的变化），
// 出错时也可能为 true，调用方据此推送。
func (n *Node) mergeChain(records [][]byte) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.chain == nil {
		return false, ErrNotInCircle
	}
	reloaded := false
	for {
		next, err := chain.Load(n.chain.Records())
		if err != nil {
			return reloaded, err
		}
		// 补签会给胜出的链加上改名记录：链上已经有改名记录时总是安全的（旧版本早已读不了它，配对也会拒绝旧版本）；
		// 还没有时，要其他成员都声明过支持改名，否则可能有一台刚配对的旧版本设备从此读不了链。
		changed, skipped, err := next.MergeAs(n.id, records, func(winner *chain.Chain) bool {
			if winner.HasRenames() {
				return true
			}
			missing, err := n.renameUnsupported(winner)
			return err == nil && len(missing) == 0
		})
		if err != nil {
			return reloaded, err
		}
		if len(skipped) > 0 {
			n.opts.Logf(i18n.T("这些改名没有同步到设备圈：%s（设备圈里有设备可能还不支持改名，或者改名次数已达上限），需要的话请重新改名"),
				i18n.List(skipped))
		}
		if !changed {
			return reloaded, nil
		}
		// 只在数据库里的链还是内存里这一条时写入：命令行可能刚刚追加了记录（例如移除设备），
		// 整体替换会把它悄悄覆盖。数据库变了就先重新加载，再合并一次（两边各有新的加入或移除记录时得到 ErrFork）。
		// 自动收敛改名分叉时，本机分歧的后缀被换掉，不是单纯追加。
		if isPrefix(n.chain.Records(), next.Records()) {
			err = n.store.AppendChain(n.chain.Len(), next.Records())
		} else {
			err = n.store.ReplaceChain(n.chain.Records(), next.Records())
		}
		if errors.Is(err, store.ErrChainChanged) {
			reloadedThisRound, rerr := n.reloadCircleLocked()
			if rerr != nil {
				return reloaded, rerr
			}
			reloaded = reloaded || reloadedThisRound
			if !reloadedThisRound {
				return reloaded, err // 数据库里的链没有变长（不应发生）：不重试，以免空转
			}
			continue
		}
		if err != nil {
			return reloaded, err
		}
		return true, n.useChainLocked(next)
	}
}

// isPrefix 报告 a 是不是 b 的前缀。
func isPrefix(a, b [][]byte) bool {
	return len(a) <= len(b) && slices.EqualFunc(a, b[:len(a)], bytes.Equal)
}

// RemovalMayBeStale 报告本机是否可能还没同步到最新的设备圈：守护进程没在运行，或者此刻没有任何其他成员在线。
// 这时候移除，如果别的设备已经移除过同一台设备，两边会各自换成不同的发现密钥、从此互相连不上。
// target（本次要移除的设备）在线不算：被移除的设备不知道自己已被移除，它在线不能说明设备圈已同步。
func RemovalMayBeStale(devs []DeviceStatus, daemonRunning bool, target string) bool {
	if !daemonRunning {
		return true
	}
	for _, d := range devs {
		if !d.Self && d.KeyID != target && d.Online {
			return false
		}
	}
	return true
}

// RemoveDevice 把 keyID 移出设备圈（设计方案 3.5）：生成新的发现密钥，追加由本机签名、带新密钥校验值的移除记录。
// 守护进程（无论是本进程还是另一个进程）随后把新链和新密钥推送给其他成员，并断开被移除的设备。
func (n *Node) RemoveDevice(keyID string) error {
	if keyID == n.id.KeyID {
		return ErrRemoveSelf
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.chain == nil {
		return ErrNotInCircle
	}
	// 以数据库里的链为准：守护进程可能刚合并了别的更新，本进程内存里的链可能是旧的。
	if _, err := n.reloadCircleLocked(); err != nil {
		return err
	}
	next, err := chain.Load(n.chain.Records())
	if err != nil {
		return err
	}
	key, err := newDiscoveryKey()
	if err != nil {
		return err
	}
	if err := next.RemoveDevice(n.id, keyID, key); err != nil {
		return fmt.Errorf("node: %w", err)
	}
	// 先把新密钥存进密钥库再写链：有链就一定有密钥。
	if err := stageDiscoveryKey(n.opts.Keys, n.store.WithWriteLock, key); err != nil {
		return err
	}
	if err := n.store.AppendChain(n.chain.Len(), next.Records()); err != nil {
		return err
	}
	n.localChange = true
	if err := n.useChainLocked(next); err != nil {
		return fmt.Errorf("%w: %v", ErrRemovedKeyPending, err)
	}
	if !bytes.Equal(n.discKey, key) {
		return fmt.Errorf("%w: the new key was not activated (keystore changed concurrently); run the removal check again", ErrRemovedKeyPending)
	}
	return nil
}

// takeLocalChange 报告并清除"本进程改过签名链"的标记。
func (n *Node) takeLocalChange() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch := n.localChange
	n.localChange = false
	return ch
}

// markLocalChange 重新设置"本进程改过签名链、还没有成功推送"的标记，让下一次轮询重试。
func (n *Node) markLocalChange() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.localChange = true
}

// circleKeys 返回作为接收方时接受的发现密钥（当前密钥在前）。
func (n *Node) circleKeys() [][]byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	return circleKeySet{current: n.discKey, retired: n.retired}.accepted(time.Now())
}

// discoverer 返回局域网发现的实现（Options.Discovery，默认 zeroconf 的 mDNS）。
func (n *Node) discoverer() discovery.Discoverer {
	if n.opts.Discovery != nil {
		return n.opts.Discovery
	}
	return discovery.MDNS{}
}

// snapshot 返回当前链（只读）和发现密钥。需要修改链时，调用方必须先用 chain.Load 复制一份。
func (n *Node) snapshot() (*chain.Chain, []byte) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.chain == nil {
		return nil, nil
	}
	return n.chain, append([]byte(nil), n.discKey...)
}
