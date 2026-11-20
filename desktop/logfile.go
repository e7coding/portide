package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"portide/core/i18n"
)

// logMaxSize 是日志文件的大小上限：启动时超过它就改名为 portide.log.1（覆盖上一份），重新开始写。
const logMaxSize = 5 << 20

// openLogFile 把 log 包的输出（核心的 Logf 默认用它）同时写到数据目录下的 portide.log：
// 从"应用程序"里启动时标准错误没人看，连接走了中继、直连失败这类问题只能靠这个文件排查。打不开时只写标准错误。
func openLogFile(dir string) {
	path := filepath.Join(dir, "portide.log")
	if st, err := os.Stat(path); err == nil && st.Size() > logMaxSize {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf(i18n.T("打开日志文件失败：%v"), err)
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
}
