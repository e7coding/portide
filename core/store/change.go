package store

// 变化信号（M3a，设计方案 7.1 / M3a 设计 2.1）：数据库里某一类界面关心的数据被改写之后，Store 调用
// SetOnChange 设置的回调，参数是主题名。界面收到后重新读取这一类数据，信号本身不带内容。
// 写操作失败时也可能发出信号（多刷新一次无害），所以回调只能当作"可能变了"。

// 变化主题。
const (
	ChangeDevices   = "devices"   // 设备圈成员、在线状态、地址
	ChangeTransfers = "transfers" // 发送队列的状态与进度
	ChangeHistory   = "history"   // 历史记录
	ChangeInbox     = "inbox"     // 等待确认的接收
	ChangePairing   = "pairing"   // 配对会话
	ChangeRelay     = "relay"     // 中继配置与连接状态
	ChangeSettings  = "settings"  // 保存位置、接收策略、保留天数
)

// metaTopics 把元数据键映射到变化主题；不在表里的键（例如每秒写一次的守护进程心跳）不发信号。
var metaTopics = map[string]string{
	MetaRelaySettings: ChangeRelay,
	MetaRelayConfig:   ChangeRelay,
	MetaRelayStatus:   ChangeRelay,
	MetaSaveDir:       ChangeSettings,
	MetaReceivePolicy: ChangeSettings,
	MetaQueueDays:     ChangeSettings,
	MetaHistoryDays:   ChangeSettings,
	MetaNotify:        ChangeSettings,
	MetaListenPort:    ChangeSettings,
	MetaDeviceIcons:   ChangeDevices,
}

// SetOnChange 设置变化回调（nil 表示不需要）。要在开始使用 Store 之前调用；回调必须很快返回，
// 不能调用 Store 的方法，也不能阻塞（例如只往带缓冲的通道里非阻塞地放一个值）。
func (s *Store) SetOnChange(f func(topic string)) { s.onChange = f }

func (s *Store) changed(topic string) {
	if s.onChange != nil {
		s.onChange(topic)
	}
}

func (s *Store) metaChanged(key string) {
	if topic, ok := metaTopics[key]; ok {
		s.changed(topic)
	}
}
