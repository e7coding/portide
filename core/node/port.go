package node

import (
	"errors"
	"fmt"
	"strconv"

	"portide/core/store"
)

// DefaultListenPort 是守护进程默认监听的 UDP 端口。
const DefaultListenPort = 7443

// ErrPortInUse 表示监听端口已被别的程序（或另一个 Portide）占用（M3c 设计 5.3）。
var ErrPortInUse = errors.New("node: listen port is already in use")

// ListenPort 返回设置里的监听端口；0 表示由系统自动选择（每次启动可能不同）。
func (n *Node) ListenPort() int {
	v, ok, _ := n.store.GetMeta(store.MetaListenPort)
	p, err := strconv.Atoi(v)
	if !ok || err != nil || p < 0 || p > 65535 {
		return DefaultListenPort
	}
	return p
}

// SetListenPort 设置监听端口（0 表示自动选择），守护进程下次启动时生效。
func (n *Node) SetListenPort(port int) error {
	if port < 0 || port > 65535 {
		return fmt.Errorf("node: listen port must be between 0 and 65535, got %d", port)
	}
	return n.store.SetMeta(store.MetaListenPort, strconv.Itoa(port))
}

// listenAddr 返回守护进程的监听地址：Options.ListenAddr 优先（命令行 --listen、测试），否则用设置里的端口。
func (n *Node) listenAddr() string {
	if n.opts.ListenAddr != "" {
		return n.opts.ListenAddr
	}
	return ":" + strconv.Itoa(n.ListenPort())
}
