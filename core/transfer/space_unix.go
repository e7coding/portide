//go:build linux || darwin

package transfer

import "syscall"

// freeSpace 返回 dir 所在文件系统对当前用户可用的字节数。
func freeSpace(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
