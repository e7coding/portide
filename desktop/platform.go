package main

import (
	"context"
	"errors"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"portide/core/i18n"
	"portide/core/update"
)

// Platform 是绑定给界面的系统功能：文件和文件夹选择对话框（选完之后界面再调用服务层，例如 Service.SendFiles）、
// 开机时启动、用浏览器打开链接。
type Platform struct {
	app     *application.App
	prefs   *prefsStore
	updates *update.Checker
	// onLanguage 在界面语言改变后调用（重写托盘菜单）；托盘建好之后才设置，所以用原子指针。
	onLanguage atomic.Pointer[func()]
}

// Prefs 返回桌面端的偏好（关闭窗口时缩到托盘等）。
func (p *Platform) Prefs() DesktopPrefs { return p.prefs.load() }

// SetCloseToTray 设置关闭窗口时是缩到托盘还是退出。
func (p *Platform) SetCloseToTray(on bool) error {
	return p.prefs.update(func(d *DesktopPrefs) { d.CloseToTray = on })
}

// SetHideHint 设置是否每次缩到托盘时都提示。
func (p *Platform) SetHideHint(on bool) error {
	return p.prefs.update(func(d *DesktopPrefs) { d.HideHint = on })
}

// SetLanguage 设置界面语言："zh"、"en"，空串表示跟随系统。托盘、通知和服务层的提示立即换成新语言，
// 界面自己按返回的偏好（UILanguage）切换。
func (p *Platform) SetLanguage(lang string) (DesktopPrefs, error) {
	if lang != "" && lang != i18n.Chinese && lang != i18n.English {
		return DesktopPrefs{}, i18n.New("不支持的界面语言 %q", lang)
	}
	if err := p.prefs.update(func(d *DesktopPrefs) { d.Language = lang }); err != nil {
		return DesktopPrefs{}, err
	}
	prefs := p.prefs.load()
	i18n.SetLanguage(prefs.UILanguage)
	if f := p.onLanguage.Load(); f != nil {
		(*f)()
	}
	return prefs, nil
}

// SetUpdateCheck 打开或关闭每天的更新检查。
func (p *Platform) SetUpdateCheck(on bool) error {
	return p.prefs.update(func(d *DesktopPrefs) { d.UpdateCheck = on })
}

// CheckUpdate 返回有没有新版本（只提示，不下载）。force 为 false 时是启动时的自动检查：设置里关掉了就只返回上次的结果，
// 否则一天最多联网查一次；force 为 true 是用户点了"检查更新"，总是联网查。
func (p *Platform) CheckUpdate(force bool) (update.Info, error) {
	if !force && !p.prefs.load().UpdateCheck {
		return p.updates.Cached(), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return p.updates.Check(ctx, force)
}

// PickFiles 弹出文件选择对话框（可多选）。用户取消时返回空列表。
func (p *Platform) PickFiles() ([]string, error) {
	paths, err := p.app.Dialog.OpenFile().
		SetTitle(i18n.T("选择要发送的文件")).
		CanChooseFiles(true).
		PromptForMultipleSelection()
	if err != nil || paths == nil {
		return []string{}, err
	}
	return paths, nil
}

// PickFolder 弹出文件夹选择对话框（发送一个文件夹，或选择保存位置）。用户取消时返回空字符串。
func (p *Platform) PickFolder(title string) (string, error) {
	return p.app.Dialog.OpenFile().
		SetTitle(title).
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		PromptForSingleSelection()
}

// autostartID 是开机启动登记的名字（Windows 注册表里的值名、Linux 的 .desktop 文件名）。
const autostartID = "portide-desktop"

// Autostart 报告是否已登记开机时启动。
func (p *Platform) Autostart() (bool, error) {
	on, err := p.app.Autostart.IsEnabled()
	if errors.Is(err, application.ErrAutostartNotSupported) {
		return false, nil
	}
	return on, err
}

// SetAutostart 登记或取消开机时启动（M3b 设计 3.5）：开机启动时带 --hidden，只在托盘里启动。
func (p *Platform) SetAutostart(on bool) error {
	var err error
	if on {
		err = p.app.Autostart.EnableWithOptions(application.AutostartOptions{Identifier: autostartID, Arguments: []string{hiddenFlag}})
	} else {
		err = p.app.Autostart.Disable()
	}
	if errors.Is(err, application.ErrAutostartNotSupported) {
		return i18n.New("这个平台不支持开机时启动")
	}
	return err
}

// OpenURL 用系统浏览器打开消息里的链接（M3c 设计 5.4）：只允许 http 和 https，别的协议（file:、javascript: 等）一律拒绝。
func (p *Platform) OpenURL(link string) error {
	if !allowedURL(link) {
		return i18n.New("只能打开 http 或 https 链接")
	}
	return p.app.Browser.OpenURL(link)
}

func allowedURL(link string) bool {
	u, err := url.Parse(link)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
