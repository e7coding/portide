// Package app 是界面调用的服务层（M3a 设计 2.3）：在进程内运行 Portide 核心，并把核心的能力整理成界面能直接调用的方法。
// 桌面端（Wails）和 iOS（经 gomobile 的 portide/mobile）共用它（M4 设计 2.2）。这个包不引用任何界面框架，可以单独测试。
package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"portide/core/appdir"
	"portide/core/discovery"
	"portide/core/i18n"
	"portide/core/keystore"
	"portide/core/node"
	"portide/core/store"
)

// 启动阶段（State.Phase）。
const (
	PhaseLocked  = "locked"  // 文件密钥库：等用户输入口令
	PhaseSetup   = "setup"   // 首次启动：还没有设备身份，或者还没有设备名
	PhaseBusy    = "busy"    // 这个数据目录已经有别的守护进程在运行（例如命令行的 portide daemon）
	PhaseRunning = "running" // 核心正在运行
	PhaseError   = "error"   // 无法继续（例如数据目录来自更新版本的 Portide）
	// PhaseSuspended：iOS 退到后台时暂停了核心（Suspend），回到前台 Resume 后继续（M4）。
	PhaseSuspended = "suspended"
)

// joinTimeout 是加入设备圈（局域网里找邀请方 + 配对）的总时限。
const joinTimeout = 2 * time.Minute

// ErrNotRunning 表示核心还没有启动（还在首次启动向导、等待口令，或者启动失败）。
var ErrNotRunning = i18n.New("Portide 还没有启动")

// errJoining 表示上一次 Join 还没有结束：Init、LeaveCircle、再次 Join 都要等它。
var errJoining = i18n.New("正在加入设备圈，请稍候")

// Config 是服务的启动参数。除 DataDir 外都可以留空，留空时用核心的默认值。
type Config struct {
	DataDir      string
	ListenAddr   string        // 守护进程监听地址；为空时用设置里的端口（默认 7443）
	DisableMDNS  bool          // 测试用：关闭局域网发现
	PollInterval time.Duration // 测试用：守护进程检查数据库的间隔
	Logf         func(format string, args ...any)
	// Keys 不为空时直接用它保存秘密（iOS 的 Keychain），不再选择密钥库、也不需要口令（M4）。
	Keys keystore.Store
	// Discovery 是局域网发现的实现，传给 node.Options.Discovery；为空时用 mDNS（M4：iOS 传入 Bonjour）。
	Discovery discovery.Discoverer
	// SaveDir 不为空时，每次打开核心都把保存目录设成它，收到的文件在历史里也按它定位（M4：iOS 应用的
	// 容器路径每次更新都会变，保存目录必须每次启动重新设置，历史里记下的旧绝对路径会失效）。
	SaveDir  string
	OnChange func(topic string)      // 核心的变化信号（store.Change*），见 node.Options.OnChange 的约束
	Open     func(path string) error // 用系统默认程序打开文件（由桌面运行时提供）
	Reveal   func(path string) error // 在文件管理器里显示文件（由桌面运行时提供）
	OnEvent  func(node.Event)        // 值得通知用户的事（M3b 系统通知），约束同 OnChange
}

// State 是界面启动时、以及每一步向导之后读取的状态。
type State struct {
	Phase           string `json:"phase"`
	NeedsName       bool   `json:"needsName"`       // setup：还没有设备身份，先填设备名
	FirstPassphrase bool   `json:"firstPassphrase"` // locked：第一次使用文件密钥库，是在设置口令而不是输入口令
	DefaultName     string `json:"defaultName"`     // 主机名
	// NameSuggestions 是首次启动时建议的设备名，第一个是默认值（M3c："joy 的笔记本"、"joy 的台式机"、主机名）。
	NameSuggestions []string `json:"nameSuggestions"`
	Name            string   `json:"name"`
	KeyID           string   `json:"keyId"`
	Error           string   `json:"error"` // 上一步的错误（例如口令不对），或 error / busy 阶段的原因
	// ErrorKind 是 error 阶段的原因分类（ErrorKind*），界面据此给出对应的操作（M3c 设计 4.9）。
	ErrorKind string `json:"errorKind"`
	// Detail 是 error 阶段的原始错误，界面放在"详细信息"里，方便反馈问题。
	Detail  string `json:"detail"`
	DataDir string `json:"dataDir"` // 数据目录（出错页显示）
}

// error 阶段的原因分类（State.ErrorKind）。
const (
	ErrorKindPortInUse = "port-in-use"   // 端口被占用：可以"重试"或"换一个端口启动"
	ErrorKindNewer     = "newer-version" // 数据目录来自更新版本的 Portide
	ErrorKindOther     = "other"
)

// Service 管理核心的生命周期。方法可以被并发调用。
type Service struct {
	cfg Config

	mu       sync.Mutex
	pass     string
	keys     keystore.Store
	n        *node.Node
	phase    string
	errMsg   string
	errKind  string // error 阶段的原因分类
	errRaw   string // error 阶段的原始错误
	autoPort bool   // UseAutomaticPort：下一次打开核心时把端口设置改成自动选择
	cancel   context.CancelFunc
	done     chan struct{}
	runErr   error
	stopped  bool
	starting bool // startLocked 正在跑（可能已经短暂解锁在等 Run 退出）：这期间不能再对同一个 Node 并发启动一次
	joining  bool // Join 正在进行：Init、LeaveCircle、再次 Join 都要排斥它，避免同时改动设备圈的密钥/签名链
	booted   bool // ensureBootedLocked 是否已经跑过第一次 boot

	speed  speedMeter // 传输页的速度估算（有自己的锁）
	thumbs thumbCache // 缩略图缓存（有自己的锁）
	paste  sync.Mutex // SendPasted：清理、写入和建任务一次只做一个，免得清理删掉另一次刚写的文件
}

// New 创建服务；第一次调用任何方法时才开始启动流程（解锁、打开设备、启动核心）——
// 桌面应用的第二个实例在单实例检查里就退出了，不会碰密钥库和数据库。
func New(cfg Config) *Service {
	return &Service{cfg: cfg, keys: cfg.Keys}
}

// ensureBootedLocked 保证 boot 只在第一次被用到时才跑一次：Wails 的单实例检查判定为"已有实例在运行"的
// 第二个进程不会调用任何方法，这个方法也就不会执行，密钥库和数据库都不会被碰。
// 调用方必须持有 s.mu——每个公开方法的统一写法是先 s.mu.Lock()，再立刻调这个方法。
func (s *Service) ensureBootedLocked() {
	if s.booted {
		return
	}
	s.booted = true
	s.boot()
}

// State 返回当前的启动状态。
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureBootedLocked()
	st := State{Phase: s.phase, Error: s.errMsg, DataDir: s.cfg.DataDir}
	if s.phase == PhaseError {
		st.ErrorKind, st.Detail = s.errKind, s.errRaw
	}
	st.DefaultName, _ = os.Hostname()
	st.NameSuggestions = []string{}
	if s.phase == PhaseSetup && s.n == nil {
		st.NameSuggestions = nameSuggestions()
	}
	switch s.phase {
	case PhaseLocked:
		_, err := os.Stat(keysFile(s.cfg.DataDir))
		st.FirstPassphrase = errors.Is(err, os.ErrNotExist)
	case PhaseSetup:
		st.NeedsName = s.needsNameLocked()
	}
	if s.n != nil {
		st.Name, st.KeyID = s.n.Name(), s.n.Identity().KeyID
	}
	return st
}

// Unlock 用口令打开文件密钥库（第一次使用时就是设置口令）。
func (s *Service) Unlock(passphrase string) error {
	if passphrase == "" {
		return i18n.New("请输入口令")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureBootedLocked()
	if s.phase != PhaseLocked {
		return nil
	}
	s.pass = passphrase
	s.boot()
	if s.phase == PhaseLocked {
		return errors.New(s.errMsg)
	}
	return s.bootError()
}

// Init 用设备名创建本机身份（首次启动的第一步）。
func (s *Service) Init(name string) error {
	name = strings.TrimSpace(name)
	if err := validName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureBootedLocked()
	if s.joining {
		return errJoining
	}
	if s.phase != PhaseSetup || !s.needsNameLocked() {
		return i18n.New("这台设备已经初始化过了")
	}
	if s.n != nil {
		// 已有设备密钥、只是没有名字（重装后数据目录是新的）：补上名字即可。
		if err := s.n.SetName(name); err != nil {
			return uiError(err)
		}
		s.boot() // 有名字了：自动建好单机圈并启动
		return s.bootError()
	}
	if _, err := node.Init(s.cfg.DataDir, s.keys, name); err != nil {
		return uiError(err)
	}
	s.boot()
	return s.bootError()
}

// needsNameLocked 报告首次启动时是否还要起名字：还没有设备身份，或者有身份、不在设备圈里而且没有名字
// （iOS 删掉应用后 Keychain 里的密钥还在，重装后的数据目录里却没有名字）。调用方持有 s.mu。
func (s *Service) needsNameLocked() bool {
	return s.n == nil || (!s.n.InCircle() && s.n.Name() == "")
}

// JoinResult 是 Join 的结果。
type JoinResult struct {
	// Mode 是 "join"（本机换到了对方的设备圈）、"adopt"（对方换到了本机的设备圈）或 "merge"（两个设备圈合并，本机换到了对方的圈）。
	Mode     string `json:"mode"`
	PeerName string `json:"peerName"` // 对方的设备名
	Devices  int    `json:"devices"`  // 配对之后本机设备圈里的设备数
	// Waiting 是合并之后还没跟过来的设备名；Outdated 是其中需要先升级的设备名，每台一项（同名的设备各出现一次）。
	Waiting  []string `json:"waiting"`
	Outdated []string `json:"outdated"`
}

// Join 用配对码和对方配对，只能在核心运行时调用（刚装好的设备也已经有一个只有本机的设备圈）。addr 为空时在局域网里自动寻找邀请方。
// 本机是单机时加入对方的设备圈；本机圈里有其他设备、对方是单机时，对方加入本机的设备圈；
// 两边都有其他设备时是合并，界面总是在输入前就说明了，所以传 mergeOK。
// 同一时间只能有一个 Join 在进行：它会在局域网里找几秒到几十秒，这期间 Init、LeaveCircle、
// 另一个 Join 都可能和它一起改动设备圈的密钥和签名链，所以用 joining 标记互斥。
// 换圈由核心在进程内完成，这里不停止、也不重启核心。
func (s *Service) Join(code, addr string) (JoinResult, error) {
	s.mu.Lock()
	s.ensureBootedLocked()
	if s.joining {
		s.mu.Unlock()
		return JoinResult{}, errJoining
	}
	n := s.n
	if s.phase != PhaseRunning || n == nil {
		s.mu.Unlock()
		return JoinResult{}, ErrNotRunning
	}
	s.joining = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.joining = false
		s.mu.Unlock()
	}()

	var addrs []string
	if addr = strings.TrimSpace(addr); addr != "" {
		addrs = []string{addr}
	}
	// 配对要几秒到几十秒，不能一直占着锁（界面同时还会读状态）。
	ctx, cancel := context.WithTimeout(context.Background(), joinTimeout)
	defer cancel()
	res, err := n.Join(ctx, code, addrs, true)
	if err != nil {
		return JoinResult{}, uiError(err)
	}
	devs, err := n.Devices()
	if err != nil {
		return JoinResult{}, uiError(err)
	}
	out := JoinResult{Mode: string(res.Mode), PeerName: res.Peer, Devices: len(devs), Waiting: []string{}, Outdated: []string{}}
	out.Waiting = append(out.Waiting, res.Waiting...)
	out.Outdated = append(out.Outdated, res.Outdated...)
	return out, nil
}

// NearbyInviters 报告局域网里是否有设备正在开放配对（浏览 4 秒）；界面据此提示"附近有设备在等你输入配对码"。
// 本机自己正在开放配对时返回 false：那是本机发布的，不是别的设备。
func (s *Service) NearbyInviters() bool {
	n, err := s.node()
	if err != nil {
		return false
	}
	if ps, err := n.Store().PendingPairSession(time.Now()); err != nil || ps != nil {
		return false
	}
	return n.NearbyInviters(context.Background())
}

// LeaveResult 是 LeaveCircle 的结果。
type LeaveResult struct {
	// NeedsForce 为真时没有离开：设备圈里还有别的设备，但没能让它们把本机移出去（都不在线、版本太旧，
	// 或者请求失败，原因在 Reason）。界面要提醒用户"它们会一直显示这台设备，需要在它们上面手动移出"，
	// 确认后带 force 再调一次，只在本机离开。
	NeedsForce bool   `json:"needsForce"`
	Reason     string `json:"reason"`
	// RemovedBy 是把本机移出设备圈的设备名；设备圈里只有本机、或者带 force 离开时为空。
	RemovedBy string `json:"removedBy"`
}

// LeaveCircle 离开当前的设备圈，本机成为一个新的单机圈并继续运行（boot 会自动建圈）。
// 设备圈里还有别的设备时，先请一台在线的设备把本机移出去（它换新的发现密钥、通知其他设备）；
// 没有能做到的设备时不离开，返回 NeedsForce，除非 force 为真。设备名、历史和设置都保留。
func (s *Service) LeaveCircle(force bool) (LeaveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureBootedLocked()
	if s.joining {
		return LeaveResult{}, errJoining
	}
	n := s.n
	if s.phase != PhaseRunning || n == nil {
		return LeaveResult{}, ErrNotRunning
	}
	devs, err := n.Devices()
	if err != nil {
		return LeaveResult{}, uiError(err)
	}
	// joining 挡住整个过程中（会解锁：请求移出要走网络，停止核心也要等）并发的 Init、Join
	// 和再一次 LeaveCircle。
	s.joining = true
	defer func() { s.joining = false }()

	var res LeaveResult
	if len(devs) > 1 && !force {
		s.mu.Unlock()
		res.RemovedBy, err = n.RequestLeave(context.Background())
		s.mu.Lock()
		if s.stopped || s.n != n || s.phase != PhaseRunning { // 解锁期间服务被停止，或者核心被换掉了（例如 iOS 的 Suspend）
			return LeaveResult{}, ErrNotRunning
		}
		if err != nil {
			reason := uiError(err).Error()
			if !errors.Is(err, node.ErrNoLeaveHelper) {
				reason = i18n.Tf("请其他设备把这台移出设备圈时出错：%s", reason)
			}
			return LeaveResult{NeedsForce: true, Reason: reason}, nil
		}
	}
	s.stopLocked()
	if s.stopped || s.n != n {
		return LeaveResult{}, ErrNotRunning
	}
	// Node 只能 Run 一次：关掉运行过的这个，另开一个来改设备圈，之后由 boot 再打开。
	_ = n.Close()
	s.n = nil
	fresh, err := node.Open(s.nodeOptions())
	if err == nil {
		err = fresh.LeaveCircle()
		_ = fresh.Close()
	}
	s.boot() // 成功时自动建一个新的单机圈并启动；失败时照常重新启动核心
	if err != nil {
		return LeaveResult{}, uiError(err)
	}
	return res, s.bootError()
}

// Retry 在 busy 或 error 阶段重新尝试启动（例如用户停掉了命令行的守护进程之后）。
func (s *Service) Retry() State {
	s.mu.Lock()
	s.ensureBootedLocked()
	if s.phase == PhaseBusy || s.phase == PhaseError {
		s.boot()
	}
	s.mu.Unlock()
	return s.State()
}

// UseAutomaticPort 在端口被占用时把监听端口改成由系统自动选择，然后重新启动（出错页的"换一个端口启动"）。
func (s *Service) UseAutomaticPort() State {
	s.mu.Lock()
	s.ensureBootedLocked()
	if s.phase == PhaseError && s.errKind == ErrorKindPortInUse {
		s.autoPort = true
		s.boot()
	}
	s.mu.Unlock()
	return s.State()
}

// Stop 停止核心并关闭数据库，之后服务不能再使用；可以重复调用。它是包级函数而不是方法：
// Wails 只把服务的导出方法生成为界面绑定，界面不应该能关掉核心。Stop 不会触发启动流程。
func Stop(s *Service) { s.shutdown() }

func (s *Service) shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	s.stopLocked()
	if s.n != nil {
		_ = s.n.Close()
		s.n = nil
	}
	// 停止之后界面可能还会调用方法（例如窗口关闭时的最后一次刷新）：让它们拿到 ErrNotRunning。
	s.phase, s.errMsg, s.errKind = PhaseError, i18n.T("Portide 已停止"), ErrorKindOther
}

// node 返回正在运行的核心；没有运行时返回 ErrNotRunning。
func (s *Service) node() (*node.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureBootedLocked()
	if s.phase != PhaseRunning || s.n == nil {
		return nil, ErrNotRunning
	}
	return s.n, nil
}

// boot 从当前状态尽量往前推进：解锁 → 打开设备 → 在设备圈里 → 没有别的守护进程 → 启动核心。调用方持有 s.mu。
func (s *Service) boot() {
	if s.stopped || s.starting {
		// starting 为真时说明已经有一个 startLocked 在跑（可能正短暂解锁等 Run 退出）：
		// 这时再并发启动同一个 Node 会撞上"只能 Run 一次"的限制，所以原样保留当前阶段，什么也不做。
		return
	}
	s.errMsg, s.errKind, s.errRaw = "", "", ""
	if s.keys == nil {
		need, err := appdir.NeedsPassphrase(s.cfg.DataDir)
		if err != nil {
			s.fail(PhaseError, err)
			return
		}
		if need && s.pass == "" {
			s.phase = PhaseLocked
			return
		}
		keys, err := appdir.OpenKeys(s.cfg.DataDir, func() (string, error) { return s.pass, nil })
		if err != nil {
			s.fail(PhaseError, err)
			return
		}
		s.keys = keys
	}
	if s.n == nil {
		n, err := node.Open(s.nodeOptions())
		switch {
		case errors.Is(err, node.ErrNotInitialized):
			s.phase = PhaseSetup
			return
		case errors.Is(err, keystore.ErrWrongPassphrase) && os.Getenv("PORTIDE_PASSPHRASE") != "":
			// 口令来自环境变量：OpenKeys 总是优先用它，界面里再输入也没用，停在输入口令只会一直循环。
			s.keys = nil
			s.phase, s.errMsg, s.errKind = PhaseError, i18n.T("环境变量 PORTIDE_PASSPHRASE 的口令不对"), ErrorKindOther
			return
		case errors.Is(err, keystore.ErrWrongPassphrase):
			s.keys, s.pass = nil, ""
			s.phase, s.errMsg = PhaseLocked, i18n.T("口令不对")
			return
		case err != nil:
			s.fail(PhaseError, err)
			return
		}
		s.n = n
		if s.autoPort {
			s.autoPort = false
			if err := n.SetListenPort(0); err != nil {
				s.fail(PhaseError, err)
				return
			}
		}
		if s.cfg.SaveDir != "" {
			if err := n.SetSaveDir(s.cfg.SaveDir); err != nil {
				s.fail(PhaseError, err)
				return
			}
		}
	}
	if !s.n.InCircle() {
		if s.n.Name() == "" { // 还没有设备名：等 Init
			s.phase = PhaseSetup
			return
		}
		// 有名字但还没有设备圈（Init 刚完成、离开设备圈之后，或旧版本留下的数据目录）：自动建一个只有本机的设备圈。
		if err := s.n.EnsureCircle(); err != nil {
			s.fail(PhaseError, err)
			return
		}
	}
	if s.done != nil {
		s.phase = PhaseRunning
		return
	}
	if s.n.DaemonRunning() {
		s.phase = PhaseBusy
		s.errMsg = i18n.T("这个数据目录已经有守护进程在运行（例如 portide daemon），请先停止它，然后点\"重试\"")
		return
	}
	s.startLocked()
}

// nodeOptions 是打开核心用的参数。调用方持有 s.mu（s.keys 已经打开）。
func (s *Service) nodeOptions() node.Options {
	return node.Options{
		DataDir: s.cfg.DataDir, Keys: s.keys, ListenAddr: s.cfg.ListenAddr, DisableMDNS: s.cfg.DisableMDNS, PollInterval: s.cfg.PollInterval,
		Discovery: s.cfg.Discovery, Logf: s.cfg.Logf, OnChange: s.cfg.OnChange, OnEvent: s.cfg.OnEvent,
	}
}

// startLocked 在后台运行核心，等它开始监听。调用方持有 s.mu。
func (s *Service) startLocked() {
	// starting 全程挡住并发的 boot：不然解锁等 Run 退出的那一小段时间里，另一个 Retry/CreateCircle
	// 可能看到 s.n 还在、s.done 已经被清空，就又调一次 startLocked，撞上"只能 Run 一次"的限制。
	s.starting = true
	defer func() { s.starting = false }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	n := s.n
	s.cancel, s.done, s.runErr = cancel, done, nil
	go func() {
		err := n.Run(ctx)
		s.mu.Lock()
		s.runErr = err
		s.mu.Unlock()
		close(done)
	}()
	<-n.Ready()
	if n.Addr() == "" { // 监听失败（例如端口被占用）：Run 马上返回错误
		// 在解锁之前就清空 s.cancel/s.done：不然解锁期间并发的 boot 会看到 s.done 不为空，
		// 以为核心已经在运行。先留一份本地副本，解锁之后再用它们等 Run 真正退出。
		s.cancel, s.done = nil, nil
		s.mu.Unlock()
		cancel()
		<-done
		s.mu.Lock()
		err := s.runErr
		// Node 只能 Run 一次（启动时就标记了已启动），监听失败之后这个 Node 不能再用：关掉并丢弃它，
		// 下一次 boot 会用 node.Open 重新打开一个。用进入时记下的本地指针 n 去关——解锁期间
		// Stop 可能已经把 s.n 关掉并清空（甚至换成了别的 Node），这里不能再碰 s.n 本身，
		// 否则可能对着 nil 解引用，或者把别人新开的 Node 误清空。Close 允许重复调用。
		_ = n.Close()
		if s.n == n {
			s.n = nil
		}
		s.fail(PhaseError, err)
		return
	}
	s.phase = PhaseRunning
}

// stopLocked 停止正在运行的核心并等它退出。调用方持有 s.mu。
func (s *Service) stopLocked() {
	if s.done == nil {
		return
	}
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	cancel()
	s.mu.Unlock()
	<-done
	s.mu.Lock()
}

func (s *Service) fail(phase string, err error) {
	s.phase = phase
	s.errMsg, s.errRaw = uiError(err).Error(), err.Error()
	switch {
	case errors.Is(err, node.ErrPortInUse):
		s.errKind = ErrorKindPortInUse
	case errors.Is(err, store.ErrNewerDatabase):
		s.errKind = ErrorKindNewer
	default:
		s.errKind = ErrorKindOther
	}
}

// bootError 把 boot 之后仍未成功的状态变成错误返回给调用方（setup 阶段不算错误：向导还有下一步）。
func (s *Service) bootError() error {
	if s.phase == PhaseError || s.phase == PhaseBusy {
		return errors.New(s.errMsg)
	}
	return nil
}
