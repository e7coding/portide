package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"portide/core/i18n"
	"portide/core/update"
)

// printUpdate 在 `portide version` 之后提示有没有新版本：一天最多联网查一次，查不到时什么都不说。
// 写到标准错误，标准输出仍然只有版本号那一行，脚本照样能解析。
func printUpdate(dir string) {
	if !update.Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := update.NewChecker(dir).Check(ctx, false)
	if err == nil && info.Newer {
		fmt.Fprintln(os.Stderr, updateText(info))
	}
}

// watchUpdates 在守护进程运行期间每天检查一次新版本，有新版本时记一行日志（每个版本只记一次）。
// 设置环境变量 PORTIDE_NO_UPDATE_CHECK=1 可以关掉。
func watchUpdates(ctx context.Context, dir string, logf func(string, ...any)) {
	if !update.Enabled() {
		return
	}
	c := update.NewChecker(dir)
	told := ""
	for {
		check, cancel := context.WithTimeout(ctx, 30*time.Second)
		info, err := c.Check(check, false)
		cancel()
		if err == nil && info.Newer && info.Latest != told {
			logf("%s", updateText(info))
			told = info.Latest
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(update.Interval):
		}
	}
}

func updateText(info update.Info) string {
	return i18n.Tf("有新版本 %s（当前 %s）：%s", info.Latest, info.Current, info.URL)
}
