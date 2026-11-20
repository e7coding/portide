package main

import (
	"bufio"
	"encoding/hex"
	"errors"
	"portide/core/i18n"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"portide/core/node"
	"portide/core/relay"
	"portide/core/relayset"
)

// cmdRelay 实现 `portide relay`（查看）、`portide relay add 配置串`、`portide relay rm [编号|地址]`（设计方案 4.2、8）。
// 中继列表是整个设备圈共用的，改动会自动同步到其他设备。
// `portide relay add -` 从标准输入读配置串（第一行，去掉首尾空白），这样令牌不会留在 shell 历史或 ps 里。
func cmdRelay(dir string, args []string) error {
	use := i18n.T("用法：portide relay [add 配置串 | rm [编号|地址]]")
	switch {
	case len(args) == 0:
		return withNode(dir, node.Options{}, showRelay)
	case args[0] == "add" && len(args) == 2:
		arg := args[1]
		if arg == "-" {
			line, err := readConfigLineFromStdin(os.Stdin)
			if err != nil {
				return i18n.New("配置串格式不对：应为 portide-relay://地址:端口?fp=…&token=…（在中继服务器上用 portide-relay token add 生成）")
			}
			arg = line
		}
		cfg, err := relay.ParseConfig(arg)
		if err != nil {
			return i18n.New("配置串格式不对：应为 portide-relay://地址:端口?fp=…&token=…（在中继服务器上用 portide-relay token add 生成）")
		}
		return withNode(dir, node.Options{}, func(n *node.Node) error {
			updated, err := n.AddRelay(cfg)
			if errors.Is(err, relayset.ErrFull) {
				return fmt.Errorf(i18n.T("最多 %d 个中继：先用 portide relay rm 删掉一个"), relayset.MaxRelays)
			}
			if err != nil {
				return err
			}
			if updated {
				fmt.Printf(i18n.T("已更新中继 %s\n"), cfg.Addr)
			} else {
				fmt.Printf(i18n.T("已添加中继 %s\n"), cfg.Addr)
			}
			printRelayNextStep(n)
			return nil
		})
	case args[0] == "rm" && len(args) <= 2:
		arg := ""
		if len(args) == 2 {
			arg = args[1]
		}
		return withNode(dir, node.Options{}, func(n *node.Node) error {
			rs, err := n.Relays()
			if err != nil {
				return err
			}
			i, err := pickRelay(rs, arg)
			if err != nil {
				return err
			}
			if err := n.RemoveRelay(rs[i].Fingerprint); err != nil {
				return err
			}
			if len(rs) == 1 {
				fmt.Println(i18n.T("已删除中继配置：之后只在局域网内连接"))
			} else {
				fmt.Printf(i18n.T("已删除中继 %s\n"), rs[i].Addr)
			}
			return nil
		})
	}
	return errors.New(use)
}

// pickRelay 按编号（从 1 开始）或地址选出 rm 要删的中继；只有一个中继时可以省略参数。
func pickRelay(rs []relay.Config, arg string) (int, error) {
	if arg == "" {
		switch len(rs) {
		case 0:
			return 0, node.ErrRelayNotFound
		case 1:
			return 0, nil
		}
		var b strings.Builder
		b.WriteString(i18n.T("有多个中继，请指定编号或地址："))
		for i, cfg := range rs {
			fmt.Fprintf(&b, "\n%d. %s", i+1, cfg.Addr)
		}
		return 0, errors.New(b.String())
	}
	if k, err := strconv.Atoi(arg); err == nil && k >= 1 && k <= len(rs) {
		return k - 1, nil
	}
	for i, cfg := range rs {
		if cfg.Addr == arg {
			return i, nil
		}
	}
	return 0, node.ErrRelayNotFound
}

func showRelay(n *node.Node) error {
	rs, err := n.Relays()
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		fmt.Println(i18n.T("没有配置中继：只在局域网内连接（用 portide relay add 配置串 设置）"))
		return nil
	}
	statuses := n.RelayStatuses() // 可能含已删除中继的旧记录：只按当前列表查
	printRelayList(os.Stdout, rs, func(fp [32]byte) string { return statuses[hex.EncodeToString(fp[:])] }, !n.DaemonRunning())
	if name, at, ok := n.RelaySettingsInfo(); ok {
		fmt.Printf(i18n.T("最后修改：「%s」 %s（会自动同步到设备圈里的其他设备）\n"), terminalSafe(name, false), at.Local().Format("01-02 15:04"))
	}
	for _, name := range n.RelayOutdatedPeers() {
		fmt.Printf(i18n.T("「%s」的 Portide 版本较旧，收不到中继设置的更改，需要在它上面手动设置\n"), terminalSafe(name, false))
	}
	return nil
}

// printRelayList 每行打印一个中继：编号、地址、指纹前 8 字节和状态。
// 守护进程没运行时状态一律是"守护进程未运行"；运行但还没有状态时是"正在连接"。
func printRelayList(w io.Writer, rs []relay.Config, status func([32]byte) string, daemonDown bool) {
	for i, cfg := range rs {
		st := status(cfg.Fingerprint)
		switch {
		case daemonDown:
			st = i18n.T("守护进程未运行")
		case st == "":
			st = i18n.T("正在连接")
		default:
			st = terminalSafe(st, false)
		}
		fmt.Fprintf(w, i18n.T("%d. %s   指纹 %s…   %s\n"), i+1, cfg.Addr, hex.EncodeToString(cfg.Fingerprint[:8]), st)
	}
}

// readConfigLineFromStdin 读标准输入的第一行并去掉首尾空白，用于 `portide relay add -`。
func readConfigLineFromStdin(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", i18n.New("标准输入是空的")
	}
	return line, nil
}

func printRelayNextStep(n *node.Node) {
	if n.DaemonRunning() {
		fmt.Println(i18n.T("守护进程会在几秒内连接中继；用 portide relay 查看状态"))
	} else {
		fmt.Println(i18n.T("下一步：运行 portide daemon，守护进程会连接中继"))
	}
}

// deviceAddr 返回 portide devices 里显示的地址：经中继的连接显示"经中继（中继地址）"，不知道中继地址时只显示"经中继"。
func deviceAddr(d node.DeviceStatus) string {
	if d.Self || d.Addr == "" {
		return "-"
	}
	if via, ok := relay.RelayedVia(d.Addr); ok {
		if via == "" {
			return i18n.T("经中继")
		}
		return i18n.Tf("经中继（%s）", via)
	}
	return d.Addr
}
