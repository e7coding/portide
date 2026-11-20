package main

import (
	"context"
	"errors"
	"portide/core/i18n"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"portide/core/node"
	"portide/core/pb"
	"portide/core/store"
	"portide/core/transfer"
)

// parseSendArgs 解析设备名之后的参数："--text 文本"（也可写成 -text、--text=文本）可以出现在文件路径之前、之后或之间；
// 字面的 "--" 之后不再识别选项，全部当作路径（例如文件名正好是 "-note.txt"）。
func parseSendArgs(args []string) (text string, paths []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return text, append(paths, args[i+1:]...), nil
		case a == "--text" || a == "-text":
			if i+1 >= len(args) {
				return "", nil, i18n.New("--text 后面缺少文本")
			}
			i++
			text = args[i]
		case strings.HasPrefix(a, "--text=") || strings.HasPrefix(a, "-text="):
			_, text, _ = strings.Cut(a, "=")
		case a != "-" && strings.HasPrefix(a, "-"):
			return "", nil, fmt.Errorf(i18n.T("未知选项 %s（文件名以 - 开头时，请写在 -- 之后）"), a)
		default:
			paths = append(paths, a)
		}
	}
	return text, paths, nil
}

// splitTargets 把 "台式机,手机" 这样的目标列表拆开（中英文逗号都可以），去掉空项。
func splitTargets(s string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '，' }) {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func cmdSend(dir string, args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return i18n.New("用法：portide send 设备名[,设备名…] [--text 文本] [文件或文件夹…]")
	}
	targets := splitTargets(args[0])
	text, paths, err := parseSendArgs(args[1:])
	if err != nil {
		return err
	}
	return withNode(dir, node.Options{}, func(n *node.Node) error {
		var devs []node.DeviceStatus
		seen := map[string]bool{}
		for _, t := range targets {
			dev, err := n.FindDevice(t)
			switch {
			case errors.Is(err, node.ErrNoSuchDevice):
				return fmt.Errorf(i18n.T("设备圈里没有「%s」（用 portide devices 查看）"), t)
			case errors.Is(err, node.ErrAmbiguousDevice):
				return fmt.Errorf(i18n.T("「%s」匹配到多台设备，请改用 KeyID 前缀"), t)
			case err != nil:
				return err
			}
			if !seen[dev.KeyID] {
				seen[dev.KeyID] = true
				devs = append(devs, dev)
			}
		}
		if len(devs) == 0 {
			return i18n.New("没有给出目标设备")
		}

		var p transfer.Plan
		for _, path := range paths {
			if err := p.AddPath(path); errors.Is(err, transfer.ErrTooManyItems) {
				return fmt.Errorf(i18n.T("一次最多发送 %d 个条目，请分批发送或先打包"), transfer.MaxItems)
			} else if err != nil {
				return fmt.Errorf(i18n.T("无法发送 %s：%v"), path, err)
			}
		}
		if text != "" {
			if err := p.AddText(text, time.Now()); err != nil {
				return err
			}
		}
		if len(p.Items) == 0 {
			return i18n.New("没有要发送的内容：请给出文件、文件夹或 --text")
		}
		for _, s := range p.Skipped {
			fmt.Printf(i18n.T("跳过：%s（符号链接、特殊文件或无法读取）\n"), s)
		}
		desc, _ := transfer.Describe(&pb.Offer{Items: p.Items})

		// 每个目标一个任务，互不影响（设计方案 5.3）。中途出错时撤销这次已经建好的任务，
		// 避免"提示失败却仍有一部分在发送"（守护进程会把被删除的任务当作取消）。
		ids := make([]int64, 0, len(devs))
		for _, dev := range devs {
			id, err := n.QueueSend(dev.KeyID, &p)
			if err != nil {
				for _, created := range ids {
					_ = n.Store().DeleteOutgoing(created)
				}
				if errors.Is(err, transfer.ErrBadOffer) {
					return i18n.New("有文件名或路径太长、或目录层级太深，对方无法接收")
				}
				return err
			}
			ids = append(ids, id)
		}
		daemon := n.DaemonRunning()
		if len(devs) == 1 && devs[0].Online && daemon {
			fmt.Printf(i18n.T("正在发送给「%s」：%s\n"), terminalSafe(devs[0].Name, false), terminalSafe(desc, false))
			return watchSend(n.Store(), ids[0])
		}
		fmt.Printf(i18n.T("要发送的内容：%s\n"), desc)
		hasOffline := false
		for i, dev := range devs {
			state := i18n.T("离线，已加入发送队列")
			switch {
			case !daemon:
				// 守护进程没在运行，不知道对方是否在线：所有目标都只是排队，不说"离线"。
				state = i18n.T("已加入发送队列")
			case dev.Online:
				state = i18n.T("在线，开始发送")
			}
			if state != i18n.T("在线，开始发送") {
				hasOffline = true
			}
			fmt.Printf(i18n.T("  →「%s」：%s（任务 %d）\n"), terminalSafe(dev.Name, false), state, ids[i])
		}
		if !daemon {
			fmt.Println(i18n.T("守护进程未运行：任务已排队，运行 portide daemon 后开始发送"))
		}
		if hasOffline {
			fmt.Printf(i18n.T("离线的设备在 %d 天内上线就会自动收到。"), n.QueueDays())
		}
		fmt.Println(i18n.T("用 portide queue 查看进度，portide queue cancel 任务号 取消"))
		return nil
	})
}

// watchSend 显示发送进度直到任务结束；Ctrl+C 取消任务。结束后删除任务记录。
// 直接关闭窗口时任务留在队列里，由守护进程继续发送。
func watchSend(st *store.Store, id int64) error {
	defer st.DeleteOutgoing(id)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	interrupt := ctx.Done()
	cancelling := false
	var cancelDeadline time.Time
	shownInterrupted, shownProgress := false, false
	for {
		select {
		case <-interrupt:
			interrupt = nil // 只处理一次 Ctrl+C，之后继续按间隔轮询
			cancelling = true
			cancelDeadline = time.Now().Add(10 * time.Second)
			_ = st.CancelOutgoing(id)
			fmt.Println(i18n.T("\n正在取消…"))
		case <-tick.C:
		}
		o, err := st.GetOutgoing(id)
		if err != nil {
			return err
		}
		if o.BytesTotal > 0 && (o.Status == store.SendActive || o.Status == store.SendDone) {
			fmt.Printf(i18n.T("\r  %s / %s（%d%%）"), transfer.FormatSize(o.BytesDone), transfer.FormatSize(o.BytesTotal), o.BytesDone*100/o.BytesTotal)
			shownProgress = true
		}
		if shownProgress && (o.Status == store.SendDone || o.Status == store.SendFailed || o.Status == store.SendCancelled) {
			fmt.Println() // 结束进度行
		}
		switch o.Status {
		case store.SendDone:
			fmt.Println(i18n.T("✓ 已发送"))
			return nil
		case store.SendFailed:
			return errors.New(o.Error)
		case store.SendCancelled:
			return i18n.New("已取消")
		case store.SendInterrupted:
			if !shownInterrupted {
				shownInterrupted = true
				fmt.Println(i18n.T("\n连接中断，稍后自动续传…（Ctrl+C 取消；直接关闭窗口则任务留在队列里继续）"))
			}
		case store.SendActive:
			shownInterrupted = false
		}
		if cancelling && time.Now().After(cancelDeadline) {
			return i18n.New("取消请求已发出，但守护进程没有响应")
		}
	}
}

func cmdConfig(dir string, args []string) error {
	return withNode(dir, node.Options{}, func(n *node.Node) error {
		switch {
		case len(args) == 0:
			saveDir, err := n.SaveDir()
			if err != nil {
				return err
			}
			fmt.Printf(i18n.T("保存目录：%s\n接收策略：%s\n排队保留：%d 天\n历史保留：%s\n"), saveDir, describePolicy(n.ReceivePolicy()), n.QueueDays(), describeHistoryDays(n.HistoryDays()))
			return nil
		case len(args) == 2 && args[0] == "history-days":
			days, err := strconv.Atoi(args[1])
			if err != nil || n.SetHistoryDays(days) != nil {
				return fmt.Errorf(i18n.T("历史保留天数应为 0（永久保留）到 3650 之间的整数，当前为 %q"), args[1])
			}
			fmt.Printf(i18n.T("历史保留：%s\n"), describeHistoryDays(days))
			return nil
		case len(args) == 2 && args[0] == "queue-days":
			days, err := strconv.Atoi(args[1])
			if err != nil || n.SetQueueDays(days) != nil {
				return fmt.Errorf(i18n.T("排队保留天数应为 1 到 365 之间的整数，当前为 %q"), args[1])
			}
			fmt.Printf(i18n.T("排队保留：%d 天\n"), days)
			return nil
		case len(args) == 2 && args[0] == "receive":
			p, err := parsePolicy(args[1])
			if err != nil {
				return err
			}
			if err := n.SetReceivePolicy(p); err != nil {
				return err
			}
			fmt.Printf(i18n.T("接收策略：%s\n"), describePolicy(p))
			return nil
		case len(args) == 2 && args[0] == "save-dir":
			if err := os.MkdirAll(args[1], 0o755); err != nil {
				return err
			}
			if err := n.SetSaveDir(args[1]); err != nil {
				return err
			}
			saveDir, _ := n.SaveDir()
			fmt.Printf(i18n.T("保存目录：%s\n"), saveDir)
			return nil
		}
		return i18n.New("用法：portide config [receive auto|ask|<大小，例如 100MB>] [save-dir 目录] [queue-days 天数] [history-days 天数]")
	})
}

// describeHistoryDays 把历史保留天数写成提示文字。
func describeHistoryDays(days int) string {
	if days == 0 {
		return i18n.T("永久")
	}
	return fmt.Sprintf(i18n.T("%d 天（更早的自动删除）"), days)
}

// parsePolicy 解析命令行给出的接收策略：auto、ask，或一个大小（超过时询问）。
func parsePolicy(s string) (node.ReceivePolicy, error) {
	switch strings.ToLower(s) {
	case "auto":
		return node.ReceivePolicy{Mode: node.PolicyAuto}, nil
	case "ask":
		return node.ReceivePolicy{Mode: node.PolicyAsk}, nil
	}
	n, err := parseSize(s)
	if err != nil {
		return node.ReceivePolicy{}, fmt.Errorf(i18n.T("接收策略只能是 auto、ask 或一个大小（例如 100MB），当前为 %q"), s)
	}
	return node.ReceivePolicy{Mode: node.PolicyAskAbove, Above: n}, nil
}

func describePolicy(p node.ReceivePolicy) string {
	switch p.Mode {
	case node.PolicyAsk:
		return i18n.T("总是询问")
	case node.PolicyAskAbove:
		return i18n.Tf("超过 %s 时询问", transfer.FormatSize(p.Above))
	default:
		return i18n.T("自动接收")
	}
}

// parseSize 解析 "100MB"、"1.5G"、"512k"、"2048" 这样的大小（按 1024 进位，见 transfer.ParseSize）。
func parseSize(s string) (int64, error) { return transfer.ParseSize(s) }

func cmdInbox(n *node.Node) error {
	entries, err := n.Store().PendingInbox(time.Now())
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println(i18n.T("没有等待确认的接收"))
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, i18n.T("编号\t来自\t内容\t剩余时间"))
	for _, e := range entries {
		left := time.Until(e.ExpiresAt).Round(time.Second)
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", e.ID, terminalSafe(e.PeerName, false), terminalSafe(e.Summary, false), left)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Println(i18n.T("用 portide accept 编号 接收，portide reject 编号 拒绝"))
	return nil
}

func cmdDecide(dir string, args []string, accept bool) error {
	verb := map[bool]string{true: "accept", false: "reject"}[accept]
	if len(args) != 1 {
		return fmt.Errorf(i18n.T("用法：portide %s 编号（用 portide inbox 查看）"), verb)
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf(i18n.T("编号应为数字，当前为 %q"), args[0])
	}
	return withNode(dir, node.Options{}, func(n *node.Node) error {
		err := n.Store().DecideInbox(id, accept)
		if errors.Is(err, store.ErrNotPending) {
			return fmt.Errorf(i18n.T("编号 %d 不存在、已处理或已过期"), id)
		}
		if err != nil {
			return err
		}
		if accept {
			fmt.Println(i18n.T("已接受，进度见守护进程的输出"))
		} else {
			fmt.Println(i18n.T("已拒绝"))
		}
		return nil
	})
}
