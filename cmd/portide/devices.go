package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"portide/core/i18n"
	"strings"

	"golang.org/x/term"

	"portide/core/chain"
	"portide/core/node"
	"portide/core/store"
)

// cmdDevicesCommand 实现 `portide devices`（列表）、`portide devices rm 设备 [--yes]`（设计方案 3.5）
// 和 `portide devices rename 设备 新名字`（M3c）。
func cmdDevicesCommand(dir string, args []string) error {
	if len(args) == 0 {
		return withNode(dir, node.Options{}, cmdDevices)
	}
	switch args[0] {
	case "rm":
	case "rename":
		return cmdRename(dir, args[1:])
	default:
		return fmt.Errorf(i18n.T("未知的 devices 子命令 %q（可用：rm、rename）"), args[0])
	}
	query, yes, force, err := parseRemoveArgs(args[1:])
	if err != nil {
		return err
	}
	return withNode(dir, node.Options{}, func(n *node.Node) error {
		dev, err := n.FindDevice(query) // FindDevice 不会匹配本机
		switch {
		case errors.Is(err, node.ErrNoSuchDevice):
			// 只有在其他设备里找不到时才判断是不是本机：设备名可以重复，别的成员可能和本机同名。
			self := n.Identity().KeyID
			if strings.EqualFold(query, n.Name()) || (len(query) >= 4 && strings.HasPrefix(self, strings.ToUpper(query))) {
				return i18n.New("不能移除本机：请在设备圈里的另一台设备上执行 portide devices rm")
			}
			return fmt.Errorf(i18n.T("设备圈里没有「%s」（用 portide devices 查看）"), query)
		case errors.Is(err, node.ErrAmbiguousDevice):
			return fmt.Errorf(i18n.T("「%s」匹配到多台设备，请改用 KeyID 前缀"), query)
		case err != nil:
			return err
		}
		name := terminalSafe(dev.Name, false)
		if !force {
			devs, err := n.Devices()
			if err != nil {
				return err
			}
			if node.RemovalMayBeStale(devs, n.DaemonRunning(), dev.KeyID) {
				return fmt.Errorf(i18n.T("暂时确认不了设备圈是否已同步到最新（守护进程未运行，或没有其他设备在线）。")+
					i18n.T("如果已经在别的设备上移除过「%s」，请不要重复移除——重复移除会让设备圈分裂；确定要移除请加 --force"), name)
			}
		}
		ok, err := confirm(fmt.Sprintf(i18n.T("确定把「%s」（%.12s…）移出设备圈？它将无法再连接设备圈里的设备 [y/N] "), name, dev.KeyID),
			i18n.T("移除设备需要确认：请加 --yes"), yes)
		if err != nil || !ok {
			return err
		}
		rmErr := n.RemoveDevice(dev.KeyID)
		pending := errors.Is(rmErr, node.ErrRemovedKeyPending)
		if rmErr != nil && !pending {
			if errors.Is(rmErr, store.ErrChainChanged) {
				return i18n.New("设备圈刚刚有更新，请重新执行这条命令")
			}
			return rmErr
		}
		fmt.Printf(i18n.T("✓ 已把「%s」移出设备圈，并更换了发现密钥\n"), name)
		if pending {
			fmt.Println(i18n.T("（新的发现密钥还没有启用，守护进程下次读取设备圈时会补完）"))
		}
		if rs, err := n.Relays(); err == nil && len(rs) > 0 {
			fmt.Println(i18n.T("已配置中继：请在中继服务器上换一个新令牌（portide-relay token add，再 token revoke 旧令牌），") +
				i18n.T("然后在任意一台设备上 portide relay add 新配置串，会自动同步到其余设备"))
		}
		if n.DaemonRunning() {
			fmt.Println(i18n.T("守护进程会断开它，并把这次移除同步给其他设备"))
		} else {
			fmt.Println(i18n.T("守护进程未运行：运行 portide daemon 后才会把这次移除同步给其他设备"))
		}
		fmt.Println(i18n.T("移除时不在线的设备，下次连上任意一台已同步的设备时自动更新"))
		return nil
	})
}

// cmdRename 实现 `portide devices rename 设备 新名字`：设备可以是本机（设备名或 KeyID 前缀）。
func cmdRename(dir string, args []string) error {
	if len(args) != 2 {
		return i18n.New("用法：portide devices rename 设备名或KeyID前缀 新名字")
	}
	query, name := args[0], strings.TrimSpace(args[1])
	if chain.ValidName(name) != nil {
		return fmt.Errorf(i18n.T("新名字不能为空、不能含控制字符，且不超过 %d 字节"), chain.MaxNameBytes)
	}
	return withNode(dir, node.Options{}, func(n *node.Node) error {
		if !n.InCircle() {
			return i18n.New("尚未加入设备圈（运行 portide pair 或 portide join）")
		}
		keyID, old := "", ""
		dev, err := n.FindDevice(query) // FindDevice 不会匹配本机
		switch {
		case errors.Is(err, node.ErrNoSuchDevice):
			self := n.Identity().KeyID
			if !strings.EqualFold(query, n.Name()) && (len(query) < 4 || !strings.HasPrefix(self, strings.ToUpper(query))) {
				return fmt.Errorf(i18n.T("设备圈里没有「%s」（用 portide devices 查看）"), query)
			}
			keyID, old = self, n.Name()
		case errors.Is(err, node.ErrAmbiguousDevice):
			return fmt.Errorf(i18n.T("「%s」匹配到多台设备，请改用 KeyID 前缀"), query)
		case err != nil:
			return err
		default:
			keyID, old = dev.KeyID, dev.Name
		}
		err = n.RenameDevice(keyID, name)
		switch {
		case errors.Is(err, node.ErrRenameUnsupported):
			_, missing, _ := n.CanRename()
			return fmt.Errorf(i18n.T("这些设备还不支持改名，请先把它们升级到最新版本并连上一次：%s"), terminalSafe(i18n.List(missing), false))
		case errors.Is(err, store.ErrChainChanged):
			return i18n.New("设备圈刚刚有更新，请重新执行这条命令")
		case errors.Is(err, chain.ErrTooManyRenames):
			return fmt.Errorf(i18n.T("改名次数已达上限（一个设备圈最多改名 %d 次）"), chain.MaxRenames)
		case err != nil:
			return err
		}
		fmt.Printf(i18n.T("✓ 已把「%s」改名为「%s」\n"), terminalSafe(old, false), terminalSafe(name, false))
		if !n.DaemonRunning() {
			fmt.Println(i18n.T("守护进程未运行：运行 portide daemon 后才会把新名字同步给其他设备"))
		}
		return nil
	})
}

// parseRemoveArgs 解析 `devices rm` 的参数：一个设备名或 KeyID 前缀，可选 --yes、--force（位置不限）。
func parseRemoveArgs(args []string) (query string, yes bool, force bool, err error) {
	use := i18n.T("用法：portide devices rm 设备名或KeyID前缀 [--yes] [--force]")
	for _, a := range args {
		switch {
		case a == "--yes" || a == "-yes":
			yes = true
		case a == "--force" || a == "-force":
			force = true
		case strings.HasPrefix(a, "-"):
			return "", false, false, fmt.Errorf(i18n.T("未知选项 %q\n%s"), a, use)
		case query != "":
			return "", false, false, errors.New(use)
		default:
			query = a
		}
	}
	if query == "" {
		return "", false, false, errors.New(use)
	}
	return query, yes, force, nil
}

// confirm 让用户确认一个不可撤销的操作。yes 为 true（命令行给了 --yes）时直接通过；
// 不是终端时不能询问，返回 needYes 作为错误；否则提示 prompt，输入 y 或 yes 才通过。
func confirm(prompt, needYes string, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, errors.New(needYes)
	}
	fmt.Print(prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		fmt.Println(i18n.T("已取消"))
		return false, nil
	}
	return true, nil
}
