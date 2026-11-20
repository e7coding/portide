package app

import (
	"os"
	"path/filepath"
)

// 这里的函数只给 iOS 用（M4 设计 2.2、3.3）。它们是包级函数而不是方法：Wails 只把 Service 的导出方法
// 生成为桌面界面的绑定，桌面界面用不到它们。

// Suspend 停止核心并关闭数据库（iOS 退到后台、延长的时间用完时调用），之后 Resume 可以重新打开。
// 没有运行时什么也不做。
func Suspend(s *Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.n == nil {
		return
	}
	s.stopLocked()
	if s.n != nil {
		_ = s.n.Close()
		s.n = nil
	}
	s.phase, s.errMsg = PhaseSuspended, ""
}

// Resume 在 Suspend 之后重新打开并启动核心（iOS 回到前台时调用），返回启动后的状态。
// 未完成的发送和接收由核心从断点继续。
func Resume(s *Service) State {
	s.mu.Lock()
	s.ensureBootedLocked()
	if s.phase == PhaseSuspended {
		s.boot()
	}
	s.mu.Unlock()
	return s.State()
}

// CleanOutbox 删掉发件箱 dir 里不再被未结束的发送任务引用的文件和空目录（iOS 应用启动时调用）。
// iOS 上从相册或"文件"选出的内容先复制到发件箱再发送（访问权限会过期），任务结束后副本就没用了。
// 核心没有运行时什么也不删，返回 ErrNotRunning。
func CleanOutbox(s *Service, dir string) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	paths, err := n.PendingSourcePaths()
	if err != nil {
		return uiError(err)
	}
	keep := map[string]bool{}
	for _, p := range paths {
		// 文件夹整个发送时来源是其中的每个文件：保留它们以及到发件箱根目录之间的每一级目录。
		for p = filepath.Clean(p); p != dir && p != "." && p != string(filepath.Separator); p = filepath.Dir(p) {
			keep[p] = true
		}
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if !keep[p] {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
	}
	return nil
}
