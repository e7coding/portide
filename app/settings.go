package app

import (
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"

	"portide/core/i18n"
	"portide/core/node"
	"portide/core/relay"
	"portide/core/relayset"
)

// Settings 是设置页里的接收与保留选项。
type Settings struct {
	SaveDir     string `json:"saveDir"`
	ReceiveMode string `json:"receiveMode"` // "auto" | "ask" | "ask-above"
	AskAbove    int64  `json:"askAbove"`    // ReceiveMode 为 "ask-above" 时的阈值（字节）
	HistoryDays int    `json:"historyDays"` // 0 表示永久保留
	QueueDays   int    `json:"queueDays"`
	Notify      bool   `json:"notify"`     // 收到、发送失败、待确认时发系统通知
	ListenPort  int    `json:"listenPort"` // 监听端口；0 表示自动选择（M3c）
	ActualPort  int    `json:"actualPort"` // 守护进程正在监听的端口
}

// RelayEntry 是中继列表里的一项。
type RelayEntry struct {
	ID          string `json:"id"` // 完整指纹的十六进制，RemoveRelay 用
	Addr        string `json:"addr"`
	Fingerprint string `json:"fingerprint"` // 指纹前 8 字节的十六进制
	Status      string `json:"status"`      // 守护进程未运行或还没状态时为"正在连接"
}

// RelayInfo 是设置页里的中继信息：设备圈共用的中继列表（会同步到所有设备）。
type RelayInfo struct {
	Relays    []RelayEntry `json:"relays"`
	ChangedBy string       `json:"changedBy"` // 最后修改的设备名；没有设置时为空
	ChangedAt int64        `json:"changedAt"` // Unix 毫秒；没有设置时为 0
	Outdated  []string     `json:"outdated"`  // 收不到同步的已连接旧版本设备
}

// Settings 返回当前的接收与保留选项。
func (s *Service) Settings() (Settings, error) {
	n, err := s.node()
	if err != nil {
		return Settings{}, err
	}
	dir, err := n.SaveDir()
	if err != nil {
		return Settings{}, uiError(err)
	}
	p := n.ReceivePolicy()
	st := Settings{SaveDir: dir, ReceiveMode: p.Mode, AskAbove: p.Above, HistoryDays: n.HistoryDays(), QueueDays: n.QueueDays(), Notify: n.Notify(), ListenPort: n.ListenPort()}
	if ap, err := netip.ParseAddrPort(n.Addr()); err == nil {
		st.ActualPort = int(ap.Port())
	}
	return st, nil
}

// OpenSaveDir 用文件管理器打开保存目录（时间线头部的"收到的文件"、设置里的"打开"）。
func (s *Service) OpenSaveDir() error {
	n, err := s.node()
	if err != nil {
		return err
	}
	dir, err := n.SaveDir()
	if err != nil {
		return uiError(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return i18n.New("无法打开保存位置：%v", err)
	}
	if s.cfg.Open == nil {
		return i18n.New("这个平台不支持打开文件夹")
	}
	return s.cfg.Open(dir)
}

// LocalAddresses 返回本机可以被其他设备手动填写的地址（局域网 IP 加实际监听端口），IPv4 在前。
// 设置里"网络"一节显示它们，方便在自动发现不工作时手动填写。
func (s *Service) LocalAddresses() ([]string, error) {
	n, err := s.node()
	if err != nil {
		return nil, err
	}
	ap, err := netip.ParseAddrPort(n.Addr())
	if err != nil {
		return []string{}, nil
	}
	return localAddrs(ap.Port()), nil
}

func localAddrs(port uint16) []string {
	var v4, v6 []string
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			pfx, err := netip.ParsePrefix(a.String())
			if err != nil {
				continue
			}
			ip := pfx.Addr()
			switch {
			case ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast():
			case ip.Is4():
				v4 = append(v4, netip.AddrPortFrom(ip, port).String())
			default:
				v6 = append(v6, netip.AddrPortFrom(ip, port).String())
			}
		}
	}
	return append(append([]string{}, v4...), v6...)
}

// SetListenPort 修改监听端口（0 表示每次启动由系统自动选择），重启 Portide 后生效。
// 局域网发现发布的是实际端口，换了端口其他设备照样能找到；只有手动填写过本机地址的设备需要改。
func (s *Service) SetListenPort(port int) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	if n.SetListenPort(port) != nil {
		return i18n.New("端口应为 0（自动选择）或 1 到 65535 之间的整数")
	}
	return nil
}

// SetSaveDir 修改接收文件的保存目录（不存在时创建）。
func (s *Service) SetSaveDir(dir string) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	if strings.TrimSpace(dir) == "" {
		return i18n.New("请选择保存位置")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return i18n.New("无法使用这个文件夹：%v", err)
	}
	return uiError(n.SetSaveDir(dir))
}

// SetReceivePolicy 修改接收策略。mode 为 "ask-above" 时 askAbove 是阈值（字节，必须大于 0）。
func (s *Service) SetReceivePolicy(mode string, askAbove int64) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	p := node.ReceivePolicy{Mode: mode}
	switch mode {
	case node.PolicyAuto, node.PolicyAsk:
	case node.PolicyAskAbove:
		if askAbove <= 0 {
			return i18n.New("请填写大于 0 的大小")
		}
		p.Above = askAbove
	default:
		return i18n.New("未知的接收策略 %q", mode)
	}
	return uiError(n.SetReceivePolicy(p))
}

// SetHistoryDays 修改历史保留天数（0 表示永久保留）。
func (s *Service) SetHistoryDays(days int) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	if n.SetHistoryDays(days) != nil {
		return i18n.New("历史保留天数应为 0（永久保留）到 3650 之间的整数")
	}
	return nil
}

// SetQueueDays 修改离线队列的保留天数。
func (s *Service) SetQueueDays(days int) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	if n.SetQueueDays(days) != nil {
		return i18n.New("排队保留天数应为 1 到 365 之间的整数")
	}
	return nil
}

// SetNotify 打开或关闭系统通知。
func (s *Service) SetNotify(on bool) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	return uiError(n.SetNotify(on))
}

// ClearHistory 清空全部历史，返回清掉的条数。
func (s *Service) ClearHistory() (int64, error) {
	n, err := s.node()
	if err != nil {
		return 0, err
	}
	count, err := n.Store().ClearMessages()
	return count, uiError(err)
}

// Relay 返回中继列表与各中继的连接状态。
func (s *Service) Relay() (RelayInfo, error) {
	n, err := s.node()
	if err != nil {
		return RelayInfo{}, err
	}
	rs, err := n.Relays()
	if err != nil {
		return RelayInfo{}, uiError(err)
	}
	info := RelayInfo{Relays: []RelayEntry{}, Outdated: []string{}}
	statuses := n.RelayStatuses() // 可能含已删除中继的旧记录：只按当前列表查
	for _, cfg := range rs {
		id := hex.EncodeToString(cfg.Fingerprint[:])
		status := statuses[id]
		if status == "" {
			status = i18n.T("正在连接")
		}
		info.Relays = append(info.Relays, RelayEntry{ID: id, Addr: cfg.Addr, Fingerprint: hex.EncodeToString(cfg.Fingerprint[:8]), Status: status})
	}
	if name, at, ok := n.RelaySettingsInfo(); ok {
		info.ChangedBy, info.ChangedAt = name, at.UnixMilli()
	}
	if o := n.RelayOutdatedPeers(); o != nil {
		info.Outdated = o
	}
	return info, nil
}

// AddRelay 按指纹新增或更新中继（配置串来自 portide-relay 首次启动时打印的那一行；带不带前面的 "portide relay add" 都可以）。
func (s *Service) AddRelay(config string) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	config = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(config), "portide relay add"))
	cfg, err := relay.ParseConfig(config)
	if err != nil {
		return i18n.New("配置串无效：%v", err)
	}
	if _, err = n.AddRelay(cfg); errors.Is(err, relayset.ErrFull) {
		return i18n.New("最多 %d 个中继：先删掉一个", relayset.MaxRelays)
	}
	return uiError(err)
}

// RemoveRelay 按 id（RelayEntry.ID，完整指纹的十六进制）删除一个中继；删到空时之后只在局域网内连接。
func (s *Service) RemoveRelay(id string) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	raw, err := hex.DecodeString(id)
	if err != nil || len(raw) != 32 {
		return node.ErrRelayNotFound
	}
	return uiError(n.RemoveRelay([32]byte(raw)))
}
