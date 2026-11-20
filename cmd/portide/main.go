// portide 是 Portide 的命令行工具：身份、配对、发现（M1a），文件与文本传输（M1b-2），离线发送队列（M1b-3a），加密的本地历史（M1b-3b），设备移除（M1b-4），经中继跨网络连接（M2a），打洞升级为直连（M2b）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"portide/core/i18n"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"portide/core/appdir"
	"portide/core/node"
	"portide/core/pairing"
	"portide/core/store"
	"portide/core/version"
)

// usage 是命令的帮助文字。
func usage() string {
	return i18n.T(`用法：portide [--data 目录] <命令> [参数]

命令：
  init --name 名称          初始化本机设备身份并设置设备名
  whoami                    显示本机设备信息
  daemon [选项]             运行守护进程（接收连接、发现设备、执行配对）
      --listen 地址         监听地址，默认用设置里的端口（7443）
      --peer host:port      额外尝试连接的地址，可重复
      --no-mdns             关闭局域网自动发现
  pair                      邀请新设备加入（需守护进程在运行），显示 8 位配对码
  join 配对码 [--addr host:port]
                            用配对码加入设备圈；不给 --addr 时在局域网里自动寻找
  devices                   列出设备圈成员与在线状态
  devices rm 设备 [--yes]    把一台设备（例如丢失的手机）移出设备圈，同时更换发现密钥；不能移除本机
  devices rename 设备 新名字  给一台设备（可以是本机）改名，同步给设备圈里的所有设备
  relay                     查看中继列表和连接状态（跨网络连接）
  relay add 配置串           添加或更新中继（最多 4 个，配置串由中继服务器的 portide-relay token add 生成）；
                            中继列表会自动同步到设备圈里的其他设备，配对时也会带给新设备
                            （也可以执行 portide relay add - 再粘贴配置串，令牌就不会留在命令历史里）
  relay rm [编号|地址]      删除一个中继（只有一个时可省略参数）；删到空之后只在局域网内连接
  send 设备[,设备…] [--text 文本] [文件或文件夹…]
                            发送给设备圈里的设备；对方离线时加入发送队列，上线后自动发送。
                            只发给一台在线设备时显示进度，Ctrl+C 取消
  queue                     查看发送队列（排队、发送中、等待重试、最近结束的任务）
  queue cancel 任务号        取消一个排队或进行中的任务
  queue pause|resume 任务号  暂停或继续一个排队或进行中的任务（暂停的任务不发送、不过期）
  queue retry 任务号         立即重试一个等待重试的任务，不用等自动重试
  history [-n 条数] [--device 设备]
                            查看本机发出和收到的记录（新的在前；内容在本地加密保存）
  history show 编号          显示一条记录的全文和文件位置
  history rm 编号            删除一条记录
  history clear [--yes]     清空全部记录
  config                    显示保存目录、接收策略、排队和历史的保留天数
  config receive auto|ask|<大小>
                            接收策略：自动接收（默认）、总是询问、超过大小（例如 100MB）时询问
  config save-dir 目录      设置接收文件的保存目录（默认 下载/Portide）
  config queue-days 天数    排队任务的保留天数（默认 7 天），过期仍未送出就放弃
  config history-days 天数  历史记录的保留天数（默认 0：永久保留）
  inbox                     列出等待确认的接收
  accept 编号 / reject 编号 接受或拒绝一个等待确认的接收
  version                   显示版本（也可以用 --version）

环境变量：
  PORTIDE_HOME        数据目录（默认为系统配置目录下的 portide）
  PORTIDE_KEYSTORE    密钥库：auto（默认）| os | file
  PORTIDE_PASSPHRASE  file 密钥库的口令
  PORTIDE_LANG        输出语言：en 或 zh（默认按 LANG，没有设置时用英文）
`)
}

func main() {
	i18n.SetLanguage(cliLanguage())
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, i18n.T("错误："), terminalSafe(err.Error(), true))
		os.Exit(1)
	}
}

// cliLanguage 是命令行输出的语言：PORTIDE_LANG（zh 或 en）优先，否则看 LC_ALL、LC_MESSAGES、LANG；
// 都没有设置（例如 systemd 服务、Docker 容器）时用英文。
func cliLanguage() string {
	if v := os.Getenv("PORTIDE_LANG"); v != "" {
		return v
	}
	return i18n.FromSystem(os.Getenv)
}

// multiFlag 收集可重复的参数。
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func run(args []string) error {
	global := flag.NewFlagSet("portide", flag.ContinueOnError)
	global.Usage = func() { fmt.Fprint(os.Stderr, usage()) }
	dataFlag := global.String("data", "", i18n.T("数据目录"))
	versionFlag := global.Bool("version", false, i18n.T("显示版本"))
	if err := global.Parse(args); err != nil {
		return err
	}
	if *versionFlag || global.Arg(0) == "version" {
		fmt.Println("portide", version.Version)
		if dir, err := appdir.DataDir(*dataFlag); err == nil {
			printUpdate(dir)
		}
		return nil
	}
	if global.NArg() == 0 {
		global.Usage()
		return i18n.New("缺少命令")
	}
	cmd, rest := global.Arg(0), global.Args()[1:]
	dir, err := appdir.DataDir(*dataFlag)
	if err != nil {
		return err
	}

	switch cmd {
	case "init":
		return cmdInit(dir, rest)
	case "whoami":
		return withNode(dir, node.Options{}, cmdWhoami)
	case "daemon":
		return cmdDaemon(dir, rest)
	case "pair":
		return withNode(dir, node.Options{}, cmdPair)
	case "join":
		return cmdJoin(dir, rest)
	case "devices":
		return cmdDevicesCommand(dir, rest)
	case "send":
		return cmdSend(dir, rest)
	case "config":
		return cmdConfig(dir, rest)
	case "queue":
		return cmdQueue(dir, rest)
	case "relay":
		return cmdRelay(dir, rest)
	case "history":
		return cmdHistory(dir, rest)
	case "inbox":
		return withNode(dir, node.Options{}, cmdInbox)
	case "accept":
		return cmdDecide(dir, rest, true)
	case "reject":
		return cmdDecide(dir, rest, false)
	case "help", "-h", "--help":
		global.Usage()
		return nil
	default:
		global.Usage()
		return fmt.Errorf(i18n.T("未知命令 %q"), cmd)
	}
}

// withNode 打开本机设备并执行 f。
func withNode(dir string, opts node.Options, f func(*node.Node) error) error {
	keys, err := appdir.OpenKeys(dir, askPassphrase)
	if err != nil {
		return err
	}
	opts.DataDir, opts.Keys = dir, keys
	n, err := node.Open(opts)
	if err != nil {
		return appdir.MapStoreErr(err)
	}
	defer n.Close()
	return f(n)
}

func cmdInit(dir string, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	name := fs.String("name", "", i18n.T("设备名，例如：笔记本"))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		host, _ := os.Hostname()
		*name = host
	}
	keys, err := appdir.OpenKeys(dir, askPassphrase)
	if err != nil {
		return err
	}
	keyID, err := node.Init(dir, keys, *name)
	if err != nil {
		return err
	}
	fmt.Printf(i18n.T("已初始化设备「%s」\nKeyID：%s\n数据目录：%s\n"), *name, keyID, dir)
	return nil
}

func cmdWhoami(n *node.Node) error {
	fmt.Printf(i18n.T("设备名：%s\nKeyID：%s\n"), n.Name(), n.Identity().KeyID)
	if id := n.CircleID(); id != "" {
		fmt.Printf(i18n.T("设备圈：%.16s…\n"), id)
	} else {
		fmt.Println(i18n.T("设备圈：尚未加入（运行 portide pair 或 portide join）"))
	}
	fmt.Printf(i18n.T("守护进程：%s\n"), map[bool]string{true: i18n.T("运行中"), false: i18n.T("未运行")}[n.DaemonRunning()])
	return nil
}

func cmdDaemon(dir string, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	listen := fs.String("listen", "", i18n.T("监听地址（默认用设置里的端口，7443）"))
	noMDNS := fs.Bool("no-mdns", false, i18n.T("关闭局域网自动发现"))
	var peers multiFlag
	fs.Var(&peers, "peer", i18n.T("额外尝试连接的地址（可重复）"))
	if err := fs.Parse(args); err != nil {
		return err
	}
	logger := log.New(os.Stderr, "", log.Ltime)
	// 日志里可能带对方的设备名、文件名或拒绝原因：先转义再写到终端，换行也转义成 \x0a，
	// 不然对方能在拒绝原因里塞换行，伪造出额外的日志行。
	logf := func(format string, a ...any) { logger.Print(terminalSafe(fmt.Sprintf(format, a...), false)) }
	opts := node.Options{ListenAddr: *listen, StaticPeers: peers, DisableMDNS: *noMDNS, Logf: logf}
	return withNode(dir, opts, func(n *node.Node) error {
		if n.DaemonRunning() {
			return i18n.New("这个数据目录的守护进程已在运行")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go watchUpdates(ctx, dir, logf)
		return n.Run(ctx)
	})
}

func cmdPair(n *node.Node) error {
	if !n.DaemonRunning() {
		return i18n.New("守护进程未运行：请先在另一个终端执行 `portide daemon`")
	}
	if err := n.EnsureCircle(); err != nil {
		return err
	}
	code, err := pairing.NewCode()
	if err != nil {
		return err
	}
	st := n.Store()
	id, err := st.CreatePairSession(code, pairing.CodeTTL)
	if err != nil {
		return err
	}
	fmt.Printf(i18n.T("配对码：%s（%d 分钟内有效）\n在新设备上运行：portide join %s\n等待新设备加入…（Ctrl+C 取消）\n"),
		pairing.FormatCode(code), int(pairing.CodeTTL.Minutes()), pairing.FormatCode(code))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = st.FinishPairSession(id, store.PairFailed, "", store.PairErrCancelled)
			return i18n.New("已取消")
		case <-tick.C:
			ps, err := st.GetPairSession(id)
			if err != nil {
				return err
			}
			switch ps.Status {
			case store.PairDone:
				fmt.Print(pairDoneText(ps.Mode, ps.JoinerName, ps.Brought))
				return nil
			case store.PairFailed:
				return fmt.Errorf(i18n.T("配对失败：%s"), node.PairFailureText(ps.Error))
			}
		}
	}
}

func cmdJoin(dir string, args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	var addrs multiFlag
	fs.Var(&addrs, "addr", i18n.T("邀请方地址 host:port（可重复）；不给时在局域网里自动寻找"))
	merge := fs.Bool("merge", false, i18n.T("两边的设备圈里都有其他设备时，同意合并两个设备圈"))
	// 允许 "join 1234-5678 --addr x" 这种参数顺序：先取出配对码。
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return i18n.New("用法：portide join 配对码 [--addr host:port] [--merge]")
	}
	code := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	return withNode(dir, node.Options{}, func(n *node.Node) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if len(addrs) == 0 {
			fmt.Println(i18n.T("正在局域网里寻找邀请方…"))
		}
		res, err := n.Join(ctx, code, addrs, *merge)
		switch {
		case errors.Is(err, pairing.ErrWrongCode):
			return i18n.New("配对码不正确")
		case errors.Is(err, pairing.ErrMergeNotConfirmed):
			return i18n.New("对方也有其他设备：加 --merge 合并两个设备圈")
		case errors.Is(err, pairing.ErrMergeUnsupported):
			return i18n.New("对方的 Portide 版本不支持合并设备圈，请先在那台设备上升级")
		case errors.Is(err, pairing.ErrSameCircle):
			return i18n.New("这两台设备已经在同一个设备圈里")
		case errors.Is(err, pairing.ErrInviterOutdated):
			return i18n.New("对方的 Portide 版本太旧，请先在那台设备上升级")
		case errors.Is(err, pairing.ErrUnexpectedJoin):
			return i18n.New("对方要求这台设备离开现在的设备圈，已拒绝")
		case errors.Is(err, pairing.ErrStaleCircle):
			return i18n.New("这台设备记下的设备圈比对方新，请让对方先和圈里其他设备同步后再试")
		case err != nil:
			return err
		}
		var safeMembers []string
		for _, m := range res.Members {
			safeMembers = append(safeMembers, terminalSafe(m, false))
		}
		peer := terminalSafe(res.Peer, false)
		if res.Mode == pairing.ModeMerge {
			fmt.Print(mergeResultText(res.Members, res.Waiting, res.Outdated))
		} else if res.Mode == pairing.ModeAdopt {
			fmt.Printf(i18n.T("✓「%s」已加入本机的设备圈，成员：%s\n"), peer, i18n.List(safeMembers))
		} else {
			fmt.Printf(i18n.T("✓ 已加入「%s」的设备圈，成员：%s\n"), peer, i18n.List(safeMembers))
		}
		if !n.DaemonRunning() {
			fmt.Println(i18n.T("下一步：运行 portide daemon，让设备之间保持连接"))
		}
		return nil
	})
}

// mergeResultText 是 portide join 合并成功后的输出：成员行，有没跟过来的设备时再加一行；outdated 里的名字后面加说明。
func mergeResultText(members, waiting, outdated []string) string {
	var safe []string
	for _, m := range members {
		safe = append(safe, terminalSafe(m, false))
	}
	out := fmt.Sprintf(i18n.T("✓ 已合并，设备圈里现在有 %d 台设备：%s\n"), len(members), i18n.List(safe))
	if len(waiting) == 0 {
		return out
	}
	var parts []string
	for _, name := range waiting {
		part := i18n.Tf("「%s」", terminalSafe(name, false))
		if i := slices.Index(outdated, name); i >= 0 {
			outdated = slices.Delete(slices.Clone(outdated), i, i+1) // 同名的设备各用掉一个，重名时只有被标记的那几台带说明
			part += i18n.T("（需要先升级）")
		}
		parts = append(parts, part)
	}
	return out + fmt.Sprintf(i18n.T("还没跟过来：%s——它们下次和这里任意一台合并过来的设备连上时会自动跟过来（30 天内）\n"), i18n.Concat(parts))
}

// pairDoneText 是 portide pair 配对成功后的输出。
func pairDoneText(mode, joinerName string, brought int) string {
	name := terminalSafe(joinerName, false)
	switch mode {
	case string(pairing.ModeMerge):
		return fmt.Sprintf(i18n.T("✓ 已合并：「%s」和它设备圈里的 %d 台设备加入了设备圈\n"), name, brought)
	case string(pairing.ModeAdopt):
		// 对方是单机：本机的守护进程已经把本机换到了对方的设备圈。
		return fmt.Sprintf(i18n.T("✓ 本机已加入「%s」的设备圈\n"), name)
	}
	return fmt.Sprintf(i18n.T("✓「%s」已加入设备圈\n"), name)
}

func cmdDevices(n *node.Node) error {
	devs, err := n.Devices()
	if errors.Is(err, node.ErrNotInCircle) {
		fmt.Println(i18n.T("尚未加入设备圈（运行 portide pair 或 portide join）"))
		return nil
	}
	if err != nil {
		return err
	}
	daemonRunning := n.DaemonRunning()
	if !daemonRunning {
		fmt.Println(i18n.T("（守护进程未运行，无法确定其他设备是否在线）"))
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, i18n.T("设备\tKeyID\t状态\t地址\t最近在线"))
	for _, d := range devs {
		status := deviceStatusText(d, daemonRunning)
		last := "-"
		if !d.LastSeen.IsZero() && !d.Self {
			last = d.LastSeen.Format("01-02 15:04:05")
		}
		fmt.Fprintf(w, "%s\t%.12s…\t%s\t%s\t%s\n", terminalSafe(d.Name, false), d.KeyID, status, deviceAddr(d), last)
	}
	return w.Flush()
}

// deviceStatusText 是 portide devices 状态列的文字：有迁移状态的设备显示迁移状态，取代"离线"。
func deviceStatusText(d node.DeviceStatus, daemonRunning bool) string {
	status := i18n.T("离线")
	switch {
	case d.Self:
		return i18n.T("本机")
	case !daemonRunning:
		status = i18n.T("未知")
	case d.Online:
		status = i18n.T("在线")
	}
	if t := moveText(d.Move); t != "" {
		return t
	}
	return status
}

// moveText 是合并之后设备迁移状态的显示文字；没有迁移状态时返回空串。
func moveText(move string) string {
	switch move {
	case node.MoveWaiting:
		return i18n.T("等待合并")
	case node.MoveOutdated:
		return i18n.T("需要升级")
	case node.MoveLeft:
		return i18n.T("没有跟过来")
	case node.MoveElsewhere:
		return i18n.T("去了别的设备圈")
	}
	return ""
}
