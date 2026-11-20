//go:build !linux && !darwin && !windows

package transfer

import "errors"

// freeSpace 在不支持的平台上返回错误，调用方跳过磁盘空间预检。
func freeSpace(string) (uint64, error) { return 0, errors.New("transfer: free space unknown") }
