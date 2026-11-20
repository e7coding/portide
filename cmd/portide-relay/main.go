// portide-relay 是 Portide 的中继服务器（设计方案 8）：设备在线登记、上线通知、会话转发。
// 它不在任何设备圈里，只转发加密后的数据，不落盘任何内容。
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"portide/core/i18n"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"portide/core/relay"
	"portide/core/relayserver"
	"portide/core/transfer"
	"portide/core/version"
)

// usage 是命令的帮助文字。
func usage() string {
	return i18n.T(`用法：portide-relay [--data 目录] <命令> [参数]

命令：
  init                      生成中继密钥（已有则保留），显示指纹
  serve [选项]              运行中继；第一次运行时自动生成密钥和一个令牌，并打印配置串
      --listen 地址         监听地址，默认 :7443（UDP）
      --public-addr 地址    对外的地址（host:port），用来拼配置串；也可以用环境变量 PORTIDE_RELAY_ADDR
      --max-clients 数量    同时登记的设备数上限，默认 1000
      --max-sessions 数量   每个令牌同时进行的会话数上限，默认 64
      --session-rate 速率   每个会话每个方向每秒的字节数上限，例如 10MB；默认不限
      --debug               记录每次登记和会话（含 IP 和路由 ID 前缀），默认只记聚合计数
  token add [--name 名称] [--addr host:port]
                            生成一个访问令牌，打印配置串（在设备上用 portide relay add 配置串 设置）
  token list                列出令牌
  token revoke 编号          吊销一个令牌：正在用它的设备几秒内被断开
  fingerprint               显示中继指纹
  version                   显示版本（也可以用 --version）

环境变量：
  PORTIDE_RELAY_DATA  数据目录（默认为系统配置目录下的 portide-relay；Docker 镜像里是 /data）
  PORTIDE_RELAY_ADDR  对外的地址（同 --public-addr）
  PORTIDE_LANG        输出语言：en 或 zh（默认按 LANG，没有设置时用英文）
`)
}

func main() {
	i18n.SetLanguage(cliLanguage())
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, i18n.T("错误："), err)
		os.Exit(1)
	}
}

// cliLanguage 是中继输出和日志的语言：PORTIDE_LANG（zh 或 en）优先，否则看 LC_ALL、LC_MESSAGES、LANG；
// 都没有设置（例如 systemd 服务、Docker 容器）时用英文。
func cliLanguage() string {
	if v := os.Getenv("PORTIDE_LANG"); v != "" {
		return v
	}
	return i18n.FromSystem(os.Getenv)
}

func run(args []string, out io.Writer) error {
	global := flag.NewFlagSet("portide-relay", flag.ContinueOnError)
	global.Usage = func() { fmt.Fprint(os.Stderr, usage()) }
	dataFlag := global.String("data", "", i18n.T("数据目录"))
	versionFlag := global.Bool("version", false, i18n.T("显示版本"))
	if err := global.Parse(args); err != nil {
		return err
	}
	if *versionFlag || global.Arg(0) == "version" {
		fmt.Fprintln(out, "portide-relay", version.Version)
		return nil
	}
	if global.NArg() == 0 {
		global.Usage()
		return i18n.New("缺少命令")
	}
	dir, err := dataDir(*dataFlag)
	if err != nil {
		return err
	}
	cmd, rest := global.Arg(0), global.Args()[1:]
	switch cmd {
	case "init":
		key, created, err := loadOrCreateKey(dir)
		if err != nil {
			return err
		}
		if created {
			fmt.Fprintln(out, i18n.T("已生成中继密钥"))
		}
		fmt.Fprintf(out, i18n.T("中继指纹：%s\n数据目录：%s\n"), fingerprint(key), dir)
		return nil
	case "fingerprint":
		key, err := loadKey(dir)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, fingerprint(key))
		return nil
	case "serve":
		return cmdServe(dir, rest, out)
	case "token":
		return cmdToken(dir, rest, out)
	}
	global.Usage()
	return fmt.Errorf(i18n.T("未知命令 %q"), cmd)
}

func dataDir(flagValue string) (string, error) {
	dir := flagValue
	if dir == "" {
		dir = os.Getenv("PORTIDE_RELAY_DATA")
	}
	if dir == "" {
		cfg, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(cfg, "portide-relay")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return abs, os.MkdirAll(abs, 0o700)
}

func keyPath(dir string) string    { return filepath.Join(dir, "relay.key") }
func tokensPath(dir string) string { return filepath.Join(dir, "tokens.json") }

// loadKey 读取中继密钥（文件里是 32 字节的 Ed25519 种子）。
func loadKey(dir string) (ed25519.PrivateKey, error) {
	seed, err := os.ReadFile(keyPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, i18n.New("还没有中继密钥：先运行 portide-relay init（或直接 portide-relay serve）")
	}
	if err != nil {
		return nil, err
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf(i18n.T("中继密钥文件 %s 已损坏"), keyPath(dir))
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// loadOrCreateKey 读取中继密钥，没有时生成一把。换了密钥指纹就变了，所有设备都要重新配置，所以已有的绝不覆盖。
func loadOrCreateKey(dir string) (ed25519.PrivateKey, bool, error) {
	if key, err := loadKey(dir); err == nil {
		return key, false, nil
	} else if _, statErr := os.Stat(keyPath(dir)); statErr == nil {
		return nil, false, err // 文件在但读不了或已损坏：不能悄悄换一把
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(keyPath(dir), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, false, err
	}
	if _, err := f.Write(seed); err != nil {
		f.Close()
		return nil, false, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, false, err
	}
	if err := f.Close(); err != nil {
		return nil, false, err
	}
	return ed25519.NewKeyFromSeed(seed), true, nil
}

func fingerprint(key ed25519.PrivateKey) string {
	fp := relay.Fingerprint(key.Public().(ed25519.PublicKey))
	return hex.EncodeToString(fp[:])
}

// configString 拼出设备要用的配置串；不知道对外地址时用占位符并提示。
func configString(key ed25519.PrivateKey, addr, tcpPort string, token []byte) string {
	if addr == "" {
		addr = i18n.T("你的服务器地址") + ":" + relay.DefaultPort
	}
	return relay.Config{Addr: addr, Fingerprint: relay.Fingerprint(key.Public().(ed25519.PublicKey)), Token: token, TCPPort: tcpPort}.ConfigString()
}

func cmdServe(dir string, args []string, out io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, dir, args, out)
}

// tcpSettings 由 --listen、--tcp-listen、--tcp-port 算出 TCP 的监听地址（空表示不监听）
// 和写进配置串的对外 TCP 端口（空表示和对外的 UDP 端口相同）。
// 没显式给 --tcp-listen 时 TCP 跟着 --listen 监听，但对外端口留空：监听端口不一定是对外端口
// （例如 Docker 的 -p 17443:7443），而 TCP 和 UDP 用同一个端口号映射出去时，"和 UDP 相同"才是对的。
// 只有显式给了 --tcp-listen（且没给 --tcp-port）时，才用它的端口当对外端口。
func tcpSettings(listen, tcpListen string, tcpListenSet bool, tcpPort string) (string, string, error) {
	if tcpPort != "" {
		p, err := parsePort(tcpPort)
		if err != nil {
			return "", "", err
		}
		tcpPort = p
	}
	if !tcpListenSet {
		return listen, tcpPort, nil
	}
	if tcpPort == "" && tcpListen != "" {
		_, p, err := net.SplitHostPort(tcpListen)
		if err != nil {
			return "", "", fmt.Errorf(i18n.T("--tcp-listen 的值 %q 无效：%v"), tcpListen, err)
		}
		tcpPort = p
	}
	return tcpListen, tcpPort, nil
}

// parsePort 检查 --tcp-port 的值是 1–65535 的端口号，返回它的规范写法。
func parsePort(s string) (string, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf(i18n.T("--tcp-port 的值 %q 无效，应是 1–65535 的端口号"), s)
	}
	return strconv.Itoa(n), nil
}

func serve(ctx context.Context, dir string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", ":"+relay.DefaultPort, i18n.T("监听地址"))
	publicAddr := fs.String("public-addr", os.Getenv("PORTIDE_RELAY_ADDR"), i18n.T("对外的地址"))
	maxClients := fs.Int("max-clients", 0, i18n.T("同时登记的设备数上限"))
	maxSessions := fs.Int("max-sessions", 0, i18n.T("每个令牌同时进行的会话数上限"))
	rateFlag := fs.String("session-rate", "", i18n.T("每个会话每个方向每秒的字节数上限"))
	tcpListenFlag := fs.String("tcp-listen", "", i18n.T("TCP 备用通道的监听地址（默认和 --listen 相同；设为空字符串则不监听 TCP）"))
	tcpPortFlag := fs.String("tcp-port", "", i18n.T("写进配置串的对外 TCP 端口（默认和对外的 UDP 端口相同；显式给了 --tcp-listen 时取它的端口）"))
	debug := fs.Bool("debug", false, i18n.T("记录每次登记和会话"))
	if err := fs.Parse(args); err != nil {
		return err
	}
	tcpListenSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "tcp-listen" {
			tcpListenSet = true
		}
	})
	tcpAddr, pubTCPPort, err := tcpSettings(*listen, *tcpListenFlag, tcpListenSet, *tcpPortFlag)
	if err != nil {
		return err
	}
	var rate int64
	if *rateFlag != "" {
		r, err := transfer.ParseSize(*rateFlag)
		if err != nil || r <= 0 {
			return fmt.Errorf(i18n.T("--session-rate 的值 %q 无效，例如 10MB"), *rateFlag)
		}
		rate = r
	}
	key, created, err := loadOrCreateKey(dir)
	if err != nil {
		return err
	}
	tokens := relayserver.OpenTokens(tokensPath(dir))
	list, err := tokens.List()
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintln(out, i18n.T("已生成中继密钥"))
	}
	fmt.Fprintf(out, i18n.T("中继指纹：%s\n"), fingerprint(key))
	if len(list) == 0 {
		_, token, err := tokens.Add(i18n.T("默认"))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, i18n.T("已生成第一个访问令牌，在每台设备上执行：\n  portide relay add %s\n"), configString(key, *publicAddr, pubTCPPort, token))
		fmt.Fprintln(out, i18n.T("（也可以执行 portide relay add - 再粘贴配置串，令牌就不会留在命令历史里）"))
		if *publicAddr == "" {
			fmt.Fprintln(out, i18n.T("（把其中的\"你的服务器地址\"换成这台服务器的公网 IP 或域名；也可以用 --public-addr 或 PORTIDE_RELAY_ADDR 指定）"))
		}
		fmt.Fprintln(out, i18n.T("这个配置串只显示这一次；以后用 portide-relay token add 生成新的"))
	}
	pc, err := net.ListenPacket("udp", *listen)
	if err != nil {
		return err
	}
	defer pc.Close()
	var ln net.Listener
	if tcpAddr != "" {
		ln, err = net.Listen("tcp", tcpAddr)
		if err != nil {
			return err
		}
		defer ln.Close()
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	srv := &relayserver.Server{
		Key:    key,
		Tokens: tokens,
		Limits: relayserver.Limits{MaxClients: *maxClients, MaxSessionsPerToken: *maxSessions, SessionRate: rate},
		Logf:   logger.Printf,
		Debug:  *debug,
	}
	if ln != nil {
		logger.Printf(i18n.T("中继已启动：%s（UDP）、%s（TCP）"), pc.LocalAddr(), ln.Addr())
		return srv.ServeAll(ctx, pc, ln)
	}
	logger.Printf(i18n.T("中继已启动：%s（UDP）"), pc.LocalAddr())
	return srv.Serve(ctx, pc)
}

func cmdToken(dir string, args []string, out io.Writer) error {
	use := i18n.T("用法：portide-relay token add [--name 名称] [--addr host:port] [--tcp-port 端口] | token list | token revoke 编号")
	if len(args) == 0 {
		return errors.New(use)
	}
	tokens := relayserver.OpenTokens(tokensPath(dir))
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("token add", flag.ContinueOnError)
		name := fs.String("name", "", i18n.T("名称（方便以后辨认，例如 家里的设备）"))
		addr := fs.String("addr", os.Getenv("PORTIDE_RELAY_ADDR"), i18n.T("中继对外的地址"))
		tcpPortFlag := fs.String("tcp-port", "", i18n.T("对外的 TCP 端口（默认和 UDP 相同）"))
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New(use)
		}
		tcpPort := ""
		if *tcpPortFlag != "" {
			p, err := parsePort(*tcpPortFlag)
			if err != nil {
				return err
			}
			tcpPort = p
		}
		key, err := loadKey(dir)
		if err != nil {
			return err
		}
		e, token, err := tokens.Add(*name)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, i18n.T("已生成令牌 %s。在每台设备上执行：\n  portide relay add %s\n"), e.ID, configString(key, *addr, tcpPort, token))
		fmt.Fprintln(out, i18n.T("（也可以执行 portide relay add - 再粘贴配置串，令牌就不会留在命令历史里）"))
		if *addr == "" {
			fmt.Fprintln(out, i18n.T("（把其中的\"你的服务器地址\"换成这台服务器的公网 IP 或域名；也可以用 --addr 指定）"))
		}
		fmt.Fprintln(out, i18n.T("这个配置串只显示这一次。"))
		return nil
	case "list":
		if len(args) != 1 {
			return errors.New(use)
		}
		list, err := tokens.List()
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Fprintln(out, i18n.T("没有令牌（用 portide-relay token add 生成）"))
			return nil
		}
		w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, i18n.T("编号\t名称\t生成时间"))
		for _, e := range list {
			fmt.Fprintf(w, "%s\t%s\t%s\n", e.ID, e.Name, time.Unix(e.Created, 0).Format("2006-01-02 15:04"))
		}
		return w.Flush()
	case "revoke":
		if len(args) != 2 {
			return errors.New(use)
		}
		if err := tokens.Revoke(args[1]); errors.Is(err, relayserver.ErrNoToken) {
			return fmt.Errorf(i18n.T("没有编号为 %s 的令牌（用 portide-relay token list 查看）"), args[1])
		} else if err != nil {
			return err
		}
		fmt.Fprintf(out, i18n.T("已吊销令牌 %s：正在用它的设备几秒内会被断开\n"), args[1])
		return nil
	}
	return errors.New(use)
}
