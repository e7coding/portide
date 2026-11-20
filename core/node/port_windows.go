package node

import (
	"errors"
	"syscall"
)

// wsaeAddrInUse 是 Windows 的 WSAEADDRINUSE（syscall.EADDRINUSE 在 Windows 上是 Go 自己的编号，对不上系统返回的错误）。
const wsaeAddrInUse = syscall.Errno(10048)

// isAddrInUse 报告监听失败是不是因为端口已被占用。
func isAddrInUse(err error) bool {
	return errors.Is(err, wsaeAddrInUse) || errors.Is(err, syscall.EADDRINUSE)
}
