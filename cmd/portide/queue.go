package main

import (
	"errors"
	"portide/core/i18n"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"google.golang.org/protobuf/proto"

	"portide/core/node"
	"portide/core/pb"
	"portide/core/store"
	"portide/core/transfer"
)

// cmdQueue 实现 `portide queue`（列出发送任务）和 `portide queue cancel|pause|resume|retry 任务号`。
func cmdQueue(dir string, args []string) error {
	if len(args) == 0 {
		return withNode(dir, node.Options{}, listQueue)
	}
	if len(args) == 2 {
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf(i18n.T("任务号应为数字，当前为 %q"), args[1])
		}
		switch args[0] {
		case "cancel":
			return withNode(dir, node.Options{}, func(n *node.Node) error { return cancelQueued(n, id) })
		case "pause", "resume":
			return withNode(dir, node.Options{}, func(n *node.Node) error { return pauseQueued(n, id, args[0] == "pause") })
		case "retry":
			return withNode(dir, node.Options{}, func(n *node.Node) error { return retryQueued(n, id) })
		}
	}
	return i18n.New("用法：portide queue [cancel|pause|resume|retry 任务号]")
}

// pauseQueued 暂停或继续一个还没结束的任务（M3b）。
func pauseQueued(n *node.Node, id int64, pause bool) error {
	var err error
	if pause {
		err = n.Store().PauseOutgoing(id)
	} else {
		err = n.Store().ResumeOutgoing(id)
	}
	if errors.Is(err, store.ErrTaskFinished) {
		return fmt.Errorf(i18n.T("没有任务 %d，或者它已经结束（用 portide queue 查看）"), id)
	}
	if err != nil {
		return err
	}
	if pause {
		fmt.Printf(i18n.T("已暂停任务 %d\n"), id)
	} else {
		fmt.Printf(i18n.T("已继续任务 %d\n"), id)
	}
	return nil
}

func listQueue(n *node.Node) error {
	tasks, err := n.Store().ListOutgoing()
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		fmt.Println(i18n.T("发送队列是空的"))
		return nil
	}
	names := map[string]string{}
	if devs, err := n.Devices(); err == nil {
		for _, d := range devs {
			names[d.KeyID] = d.Name
		}
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, i18n.T("任务\t发给\t内容\t状态\t进度\t说明"))
	for _, t := range tasks {
		name := names[t.PeerKey]
		if name == "" {
			name = fmt.Sprintf("%.12s…", t.PeerKey)
		}
		desc := "?"
		var o pb.Offer
		if proto.Unmarshal(t.Offer, &o) == nil {
			desc, _ = transfer.Describe(&o)
		}
		progress := "-"
		if t.BytesTotal > 0 {
			progress = fmt.Sprintf("%d%%", t.BytesDone*100/t.BytesTotal)
		}
		status, note := describeTask(t, n.QueueDays())
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", t.ID, terminalSafe(name, false), desc, status, progress, terminalSafe(note, false))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if !n.DaemonRunning() {
		fmt.Println(i18n.T("（守护进程未运行：排队的任务要等 portide daemon 启动后才会发送）"))
	}
	return nil
}

// describeTask 返回任务状态的中文说明和备注。
func describeTask(t store.Outgoing, queueDays int) (status, note string) {
	expires := t.QueuedSince().Add(time.Duration(queueDays) * 24 * time.Hour).Format("01-02 15:04")
	switch t.Status {
	case store.SendPending:
		status, note = i18n.T("排队中"), i18n.Tf("等对方上线，%s 前有效", expires)
	case store.SendActive:
		status = i18n.T("发送中")
	case store.SendInterrupted:
		if t.Attempts == 0 {
			note = i18n.T("等待续传")
		} else {
			note = fmt.Sprintf(i18n.T("已重试 %d 次：%s"), t.Attempts, t.Error)
		}
		if !t.NextAt.IsZero() && t.NextAt.After(time.Now()) {
			note += i18n.Tf("；下次 %s", t.NextAt.Format("15:04:05"))
		}
		status = i18n.T("等待重试")
	case store.SendDone:
		status = i18n.T("已发送")
	case store.SendFailed:
		status, note = i18n.T("失败"), t.Error
	case store.SendCancelled:
		status = i18n.T("已取消")
	default:
		status = t.Status
	}
	open := t.Status == store.SendPending || t.Status == store.SendInterrupted || t.Status == store.SendActive
	switch {
	// 命令行已经请求取消，但守护进程还没来得及把任务标成已取消：只覆盖状态文字，备注照常显示。
	case t.Cancel && open:
		status = i18n.T("取消中")
	case t.Paused && open:
		status, note = i18n.T("已暂停"), i18n.T("用 portide queue resume 任务号 继续")
	}
	return status, note
}

// retryQueued 让一个等待重试（或排队）的任务立即重试，不用等退避时间。真正的发送仍由守护进程执行，
// 它保证同一个任务同一时间只发一次，所以和自动重试同时发生也不会重复发送。
func retryQueued(n *node.Node, id int64) error {
	err := n.Store().RetryOutgoingNow(id)
	if errors.Is(err, store.ErrTaskFinished) {
		t, gerr := n.Store().GetOutgoing(id)
		switch {
		case gerr != nil:
			return fmt.Errorf(i18n.T("没有任务 %d（用 portide queue 查看）"), id)
		case t.Status == store.SendActive:
			return fmt.Errorf(i18n.T("任务 %d 正在发送"), id)
		case t.Paused && t.Status != store.SendDone && t.Status != store.SendFailed && t.Status != store.SendCancelled:
			return fmt.Errorf(i18n.T("任务 %d 已暂停，用 portide queue resume %d 继续"), id, id)
		}
		return fmt.Errorf(i18n.T("任务 %d 已经结束，不能重试（失败的发送请用 portide send 重新发送）"), id)
	}
	if err != nil {
		return err
	}
	t, err := n.Store().GetOutgoing(id)
	if err != nil {
		return err
	}
	online := false
	if devs, err := n.Devices(); err == nil {
		for _, d := range devs {
			if d.KeyID == t.PeerKey {
				online = d.Online
			}
		}
	}
	switch {
	case !n.DaemonRunning():
		fmt.Printf(i18n.T("已标记立即重试任务 %d（守护进程启动后生效）\n"), id)
	case !online:
		fmt.Printf(i18n.T("已标记立即重试任务 %d：对方现在不在线，上线后自动发送\n"), id)
	default:
		fmt.Printf(i18n.T("正在重试任务 %d\n"), id)
	}
	return nil
}

func cancelQueued(n *node.Node, id int64) error {
	t, err := n.Store().GetOutgoing(id)
	if err != nil {
		return fmt.Errorf(i18n.T("没有任务 %d（用 portide queue 查看）"), id)
	}
	switch t.Status {
	case store.SendDone, store.SendFailed, store.SendCancelled:
		return fmt.Errorf(i18n.T("任务 %d 已经结束，无需取消"), id)
	}
	if err := n.Store().CancelOutgoing(id); err != nil {
		return err
	}
	if n.DaemonRunning() {
		fmt.Printf(i18n.T("已请求取消任务 %d\n"), id)
	} else {
		fmt.Printf(i18n.T("已标记取消任务 %d（守护进程启动后生效）\n"), id)
	}
	return nil
}
