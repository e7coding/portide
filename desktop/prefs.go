package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"portide/core/update"
)

// prefsFile 是桌面端自己的偏好（M3c 设计 4.4），放在数据目录里；和核心无关，所以不进数据库。
const prefsFile = "desktop-prefs.json"

// DesktopPrefs 是设置页"通用"和"通知与启动"里的桌面端选项。
type DesktopPrefs struct {
	CloseToTray bool   `json:"closeToTray"` // 关闭窗口时缩到托盘（没有托盘时不起作用）；关掉时关闭窗口即退出
	HideHint    bool   `json:"hideHint"`    // 每次缩到托盘时都发一条提示（第一次总会提示）
	Language    string `json:"language"`    // 界面语言："zh"、"en"，空表示跟随系统
	UpdateCheck bool   `json:"updateCheck"` // 每天检查一次有没有新版本（查 GitHub 上的发布）
	HasTray     bool   `json:"hasTray"`     // 只读：这台机器的桌面有没有托盘
	UILanguage  string `json:"uiLanguage"`  // 只读：实际使用的界面语言（"zh" 或 "en"）
	CanUpdate   bool   `json:"canUpdate"`   // 只读：这个构建能检查更新（发布版才有；PORTIDE_NO_UPDATE_CHECK 可以关掉）
}

// prefsStore 读写偏好文件；方法可以并发调用（界面和关闭窗口的钩子）。
type prefsStore struct {
	mu      sync.Mutex
	path    string
	hasTray bool
}

func (p *prefsStore) load() DesktopPrefs {
	p.mu.Lock()
	defer p.mu.Unlock()
	prefs := DesktopPrefs{CloseToTray: true, UpdateCheck: true}
	if b, err := os.ReadFile(p.path); err == nil {
		_ = json.Unmarshal(b, &prefs)
	}
	prefs.HasTray = p.hasTray
	prefs.UILanguage = resolveLanguage(prefs.Language)
	prefs.CanUpdate = update.Enabled()
	return prefs
}

func (p *prefsStore) update(f func(*DesktopPrefs)) error {
	prefs := p.load()
	f(&prefs)
	p.mu.Lock()
	defer p.mu.Unlock()
	b, _ := json.Marshal(struct {
		CloseToTray bool   `json:"closeToTray"`
		HideHint    bool   `json:"hideHint"`
		Language    string `json:"language,omitempty"`
		UpdateCheck bool   `json:"updateCheck"`
	}{prefs.CloseToTray, prefs.HideHint, prefs.Language, prefs.UpdateCheck})
	return os.WriteFile(p.path, b, 0o600)
}

func newPrefsStore(dataDir string, hasTray bool) *prefsStore {
	return &prefsStore{path: filepath.Join(dataDir, prefsFile), hasTray: hasTray}
}
