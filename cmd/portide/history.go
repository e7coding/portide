package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"portide/core/i18n"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"portide/core/node"
	"portide/core/store"
	"portide/core/transfer"
)

// cmdHistory 实现 `portide history`（列表）、`history show 编号`、`history rm 编号`、`history clear [--yes]`（设计方案 6.3）。
func cmdHistory(dir string, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "show", "rm":
			if len(args) != 2 {
				return fmt.Errorf(i18n.T("用法：portide history %s 编号"), args[0])
			}
			id, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return fmt.Errorf(i18n.T("编号应为数字，当前为 %q"), args[1])
			}
			return withNode(dir, node.Options{}, func(n *node.Node) error {
				if args[0] == "rm" {
					if err := n.Store().DeleteMessage(id); errors.Is(err, store.ErrNoMessage) {
						return fmt.Errorf(i18n.T("没有编号 %d 的历史记录"), id)
					} else if err != nil {
						return err
					}
					fmt.Printf(i18n.T("已删除第 %d 条历史记录\n"), id)
					return nil
				}
				e, err := n.HistoryEntry(id)
				if errors.Is(err, store.ErrNoMessage) {
					return fmt.Errorf(i18n.T("没有编号 %d 的历史记录"), id)
				} else if err != nil {
					return err
				}
				useCurrentName(memberNames(n), e)
				showHistory(os.Stdout, e)
				return nil
			})
		case "clear":
			return clearHistory(dir, args[1:])
		}
	}
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	limit := fs.Int("n", 20, i18n.T("显示最近多少条"))
	device := fs.String("device", "", i18n.T("只看和这台设备之间的记录"))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 || *limit < 1 {
		return i18n.New("用法：portide history [-n 条数] [--device 设备] | show 编号 | rm 编号 | clear [--yes]")
	}
	return withNode(dir, node.Options{}, func(n *node.Node) error {
		peer := ""
		if *device != "" {
			dev, err := n.FindDevice(*device)
			if err != nil {
				return fmt.Errorf(i18n.T("设备圈里没有「%s」（用 portide devices 查看）"), *device)
			}
			peer = dev.KeyID
		}
		entries, err := n.History(peer, *limit)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println(i18n.T("还没有历史记录"))
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, i18n.T("编号\t时间\t方向\t设备\t内容\t结果"))
		names := memberNames(n)
		for _, e := range entries {
			useCurrentName(names, &e)
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", e.ID, e.CreatedAt.Format("01-02 15:04"), historyDirection(e), terminalSafe(e.Body.PeerName, false), historySummary(e), historyStatus(e))
		}
		if err := w.Flush(); err != nil {
			return err
		}
		fmt.Println(i18n.T("用 portide history show 编号 查看全文和文件位置"))
		return nil
	})
}

func historyDirection(e node.HistoryEntry) string {
	if e.Direction == store.MessageOut {
		return i18n.T("→ 发给")
	}
	return i18n.T("← 来自")
}

func historyStatus(e node.HistoryEntry) string {
	switch e.Status {
	case store.MessageDone:
		if e.Direction == store.MessageOut {
			return i18n.T("已送达")
		}
		return i18n.T("已收到")
	case store.MessagePartial:
		return i18n.T("部分送达")
	case store.MessageFailed:
		return i18n.T("失败")
	case store.MessageCancelled:
		return i18n.T("已取消")
	}
	return e.Status
}

// historySummary 用一句话概括一条历史：只有文本时显示开头，有文件时显示第一个文件和总数。
func historySummary(e node.HistoryEntry) string {
	b := e.Body
	if len(b.Files) == 0 {
		if len(b.Texts) == 0 {
			return i18n.T("（无内容）")
		}
		s := i18n.Tf("文本「%s」", preview(b.Texts[0], 30))
		if len(b.Texts) > 1 {
			s += fmt.Sprintf(i18n.T(" 等 %d 段"), len(b.Texts))
		}
		return s
	}
	s := terminalSafe(b.Files[0].Path, false)
	if len(b.Files) > 1 {
		s += fmt.Sprintf(i18n.T(" 等 %d 个文件"), len(b.Files))
	}
	s += i18n.Tf("（%s）", transfer.FormatSize(e.Total))
	if len(b.Texts) > 0 {
		s += fmt.Sprintf(i18n.T(" 和 %d 段文本"), len(b.Texts))
	}
	return s
}

// preview 把文本压成一行，最多 n 个字。
func preview(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return terminalSafe(s, false)
	}
	return terminalSafe(string([]rune(s)[:n])+"…", false)
}

// memberNames 返回设备圈成员现在的名字（KeyID → 名字）。
func memberNames(n *node.Node) map[string]string {
	names := map[string]string{}
	if devs, err := n.Devices(); err == nil {
		for _, d := range devs {
			names[d.KeyID] = d.Name
		}
	}
	return names
}

// useCurrentName 把历史里记下的设备名换成它现在的名字（对方改过名时）；已经不在设备圈里的设备不变。
func useCurrentName(names map[string]string, e *node.HistoryEntry) {
	if name, ok := names[e.PeerKey]; ok {
		e.Body.PeerName = name
	}
}

// showHistory 打印一条历史的全部内容。
func showHistory(w io.Writer, e *node.HistoryEntry) {
	who := i18n.T("来自")
	if e.Direction == store.MessageOut {
		who = i18n.T("发给")
	}
	fmt.Fprintf(w, i18n.T("编号：%d\n时间：%s\n%s：%s\n结果：%s\n"), e.ID, e.CreatedAt.Format("2006-01-02 15:04:05"), who, terminalSafe(e.Body.PeerName, false), historyStatus(*e))
	if e.Body.Error != "" {
		fmt.Fprintf(w, i18n.T("原因：%s\n"), terminalSafe(e.Body.Error, false))
	}
	for i, t := range e.Body.Texts {
		fmt.Fprintf(w, i18n.T("文本 %d：\n%s\n"), i+1, terminalSafe(t, true))
	}
	if len(e.Body.Files) > 0 {
		fmt.Fprintf(w, i18n.T("文件（%d 个，%s）：\n"), len(e.Body.Files), transfer.FormatSize(e.Total))
		for _, f := range e.Body.Files {
			fmt.Fprintf(w, "  %s\n", terminalSafe(e.LocalPath(f), false))
		}
	}
}

func clearHistory(dir string, args []string) error {
	yes := len(args) == 1 && args[0] == "--yes"
	if len(args) > 1 || (len(args) == 1 && !yes) {
		return i18n.New("用法：portide history clear [--yes]")
	}
	return withNode(dir, node.Options{}, func(n *node.Node) error {
		ok, err := confirm(i18n.T("确定清空全部历史记录？此操作无法撤销 [y/N] "), i18n.T("清空历史需要确认：请加 --yes"), yes)
		if err != nil || !ok {
			return err
		}
		count, err := n.Store().ClearMessages()
		if err != nil {
			return err
		}
		fmt.Printf(i18n.T("已清空 %d 条历史记录\n"), count)
		return nil
	})
}
