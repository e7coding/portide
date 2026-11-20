package version

import (
	"regexp"
	"testing"
)

// 构建脚本把 VERSION 拆成数字部分（Windows 文件版本、macOS 的 CFBundleShortVersionString 只接受数字）
// 和预发布标记，所以格式必须是 X.Y.Z 或 X.Y.Z-标记。
func TestVersionFormat(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`).MatchString(Version) {
		t.Fatalf("VERSION = %q，应为 X.Y.Z 或 X.Y.Z-预发布标记", Version)
	}
}
