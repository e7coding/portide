package app

import (
	"errors"
	"net/netip"
	"time"

	"portide/core/i18n"
	"portide/core/node"
	"portide/core/pairing"
	"portide/core/relay"
	"portide/core/store"
)

// 连接路径（Device.Path）。
const (
	PathLAN    = "lan"    // 局域网
	PathDirect = "direct" // 跨网络直连（打洞或公网地址）
	PathRelay  = "relay"  // 经中继
)

// 配对状态（Pairing.Status）。
const (
	PairWaiting = "waiting" // 等新设备输入配对码
	PairDone    = "done"
	PairFailed  = "failed"
	PairExpired = "expired"
)

// Device 是设备圈里的一台设备。
type Device struct {
	KeyID    string `json:"keyId"`
	Name     string `json:"name"`
	Icon     string `json:"icon"` // 设备图标（Icon*）：用户选过的，或按名字猜的
	Self     bool   `json:"self"`
	Online   bool   `json:"online"`
	Path     string `json:"path"` // 在线时的连接路径，见 Path*；本机或离线时为空
	Addr     string `json:"addr"` // 对方地址；经中继时是中继的地址（不带 relay: 前缀，不知道时为空）
	LastSeen int64  `json:"lastSeen"`
	JoinedAt int64  `json:"joinedAt"` // 加入设备圈的时间（Unix 毫秒）
	// Move 是合并之后这台设备的迁移状态（取自 node.DeviceStatus.Move）："" 表示没有，
	// "waiting"（还没跟过来）、"outdated"（需要先升级）、"left"（等了 30 天没有跟过来）、"elsewhere"（去了别的设备圈）。
	Move      string `json:"move"`
	MoveUntil int64  `json:"moveUntil"` // 等待到什么时候（Unix 毫秒）；Move 为空时是 0
}

// Pairing 是一次"添加设备"。
type Pairing struct {
	ID         int64  `json:"id"`
	Code       string `json:"code"` // 显示给用户的配对码，例如 "1234-5678"
	ExpiresAt  int64  `json:"expiresAt"`
	Status     string `json:"status"`
	JoinerName string `json:"joinerName"` // 加入成功时新设备的名字
	Error      string `json:"error"`
	Mode       string `json:"mode"`    // status 为 done 时是 "join"（新设备加入了本机的圈）、"adopt"（本机换到了新设备的圈）或 "merge"（两个圈合并了）
	Brought    int    `json:"brought"` // Mode 为 "merge" 时，随新设备一起加进来的其他设备数
}

// RemoveResult 是移除设备的结果。
type RemoveResult struct {
	// NeedsForce 为真时没有移除：暂时确认不了设备圈是否已同步到最新（没有其他设备在线），
	// 界面要提醒用户"如果已经在别的设备上移除过，不要重复移除"，确认后带 force 再调一次。
	NeedsForce bool `json:"needsForce"`
	// RelayWarning 为真时提醒用户在中继服务器上换一个新令牌。
	RelayWarning bool `json:"relayWarning"`
}

// Devices 返回设备圈成员，本机排在第一个。
func (s *Service) Devices() ([]Device, error) {
	n, err := s.node()
	if err != nil {
		return nil, err
	}
	devs, err := n.Devices()
	if err != nil {
		return nil, uiError(err)
	}
	icons := loadIcons(n.Store())
	out := make([]Device, 0, len(devs))
	for _, d := range devs {
		dev := Device{KeyID: d.KeyID, Name: d.Name, Icon: icons[d.KeyID], Self: d.Self, Online: d.Online, LastSeen: unixMilli(d.LastSeen), JoinedAt: unixMilli(d.JoinedAt), Move: d.Move, MoveUntil: unixMilli(d.MoveUntil)}
		if dev.Icon == "" {
			dev.Icon = guessIcon(d.Name)
		}
		if !d.Self && d.Online {
			dev.Path = connPath(d.Addr)
			if via, ok := relay.RelayedVia(d.Addr); ok {
				dev.Addr = via
			} else {
				dev.Addr = d.Addr
			}
		}
		if d.Self {
			out = append([]Device{dev}, out...)
		} else {
			out = append(out, dev)
		}
	}
	return out, nil
}

// connPath 由守护进程记下的对方地址判断连接路径：经中继时地址是 relay.Addr 的字符串形式；私有、回环、链路本地地址算局域网。
func connPath(addr string) string {
	if _, ok := relay.RelayedVia(addr); ok {
		return PathRelay
	}
	ap, err := netip.ParseAddrPort(addr) // 支持链路本地 IPv6 的作用域（例如 [fe80::1%3]:7443）
	if err != nil {
		return PathDirect
	}
	if ip := ap.Addr(); ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return PathLAN
	}
	return PathDirect
}

func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// StartPairing 开放配对，返回给新设备输入的配对码。
func (s *Service) StartPairing() (Pairing, error) {
	n, err := s.node()
	if err != nil {
		return Pairing{}, err
	}
	code, err := pairing.NewCode()
	if err != nil {
		return Pairing{}, err
	}
	id, err := n.Store().CreatePairSession(code, pairing.CodeTTL)
	if err != nil {
		return Pairing{}, uiError(err)
	}
	return s.PairingStatus(id)
}

// PairingStatus 返回一次配对的当前状态。
func (s *Service) PairingStatus(id int64) (Pairing, error) {
	n, err := s.node()
	if err != nil {
		return Pairing{}, err
	}
	ps, err := n.Store().GetPairSession(id)
	if err != nil {
		return Pairing{}, i18n.New("没有这次配对")
	}
	p := Pairing{ID: ps.ID, Code: pairing.FormatCode(ps.Code), ExpiresAt: ps.ExpiresAt.UnixMilli(), JoinerName: ps.JoinerName, Error: node.PairFailureText(ps.Error)}
	switch {
	case ps.Status == store.PairDone:
		p.Status = PairDone
		p.Mode = ps.Mode
		p.Brought = ps.Brought
	case ps.Status == store.PairFailed:
		p.Status = PairFailed
	case time.Now().After(ps.ExpiresAt):
		p.Status = PairExpired
	default:
		p.Status = PairWaiting
	}
	return p, nil
}

// CancelPairing 结束一次还在等待的配对（关闭"添加设备"对话框时调用）。
func (s *Service) CancelPairing(id int64) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	ps, err := n.Store().GetPairSession(id)
	if err != nil || ps.Status != store.PairPending {
		return nil
	}
	return uiError(n.Store().FinishPairSession(id, store.PairFailed, "", store.PairErrCancelled))
}

// RemoveDevice 把一台设备移出设备圈。force 为假且暂时确认不了设备圈已同步时不移除，返回 NeedsForce。
func (s *Service) RemoveDevice(keyID string, force bool) (RemoveResult, error) {
	n, err := s.node()
	if err != nil {
		return RemoveResult{}, err
	}
	if keyID == n.Identity().KeyID {
		return RemoveResult{}, i18n.New("不能移除本机：请在设备圈里的另一台设备上移除它")
	}
	devs, err := n.Devices()
	if err != nil {
		return RemoveResult{}, uiError(err)
	}
	found := false
	for _, d := range devs {
		found = found || d.KeyID == keyID
	}
	if !found {
		return RemoveResult{}, i18n.New("设备圈里没有这台设备")
	}
	if !force && node.RemovalMayBeStale(devs, n.DaemonRunning(), keyID) {
		return RemoveResult{NeedsForce: true}, nil
	}
	err = n.RemoveDevice(keyID)
	switch {
	case errors.Is(err, store.ErrChainChanged):
		return RemoveResult{}, i18n.New("设备圈刚刚有更新，请重试")
	case err != nil && !errors.Is(err, node.ErrRemovedKeyPending):
		return RemoveResult{}, uiError(err)
	}
	rs, _ := n.Relays()
	return RemoveResult{RelayWarning: len(rs) > 0}, nil
}
