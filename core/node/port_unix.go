//go:build !windows

package node

import (
	"errors"
	"syscall"
)

// isAddrInUse 报告监听失败是不是因为端口已被占用。
func isAddrInUse(err error) bool { return errors.Is(err, syscall.EADDRINUSE) }
