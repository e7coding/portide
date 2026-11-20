package app

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"portide/core/i18n"
	"portide/core/node"
	"portide/core/store"
)

// 设备图标（Device.Icon），对应界面里的 lucide 图标。
const (
	IconLaptop     = "laptop"
	IconMonitor    = "monitor"
	IconSmartphone = "smartphone"
	IconTablet     = "tablet"
)

var deviceIcons = []string{IconLaptop, IconMonitor, IconSmartphone, IconTablet}

// RenameCheck 是 CanRename 的结果。
type RenameCheck struct {
	Allowed bool     `json:"allowed"`
	Missing []string `json:"missing"` // 不能改名时：还没升级到支持改名的版本的设备名
}

// guessIcon 按设备名猜一个图标（M3c 设计 3）：笔记本、平板、手机，都不像时当作台式机。
func guessIcon(name string) string {
	n := strings.ToLower(name)
	has := func(words ...string) bool {
		return slices.ContainsFunc(words, func(w string) bool { return strings.Contains(n, w) })
	}
	switch {
	case has("笔记本", "laptop", "notebook", "macbook", "thinkpad"): // i18n-ignore（按设备名猜图标的关键词）。先认笔记本：ThinkPad 的名字里也有 pad
		return IconLaptop
	case has("平板", "ipad", "tablet", "pad"): // i18n-ignore
		return IconTablet
	case has("手机", "phone", "android", "pixel", "galaxy", "redmi", "huawei", "honor"): // i18n-ignore
		return IconSmartphone
	}
	return IconMonitor
}

func loadIcons(st *store.Store) map[string]string {
	icons := map[string]string{}
	if v, ok, _ := st.GetMeta(store.MetaDeviceIcons); ok {
		_ = json.Unmarshal([]byte(v), &icons)
	}
	return icons
}

// SetDeviceIcon 设置本机显示某台设备时用的图标（只存在本机，不同步）；icon 为空时恢复按名字猜。
func (s *Service) SetDeviceIcon(keyID, icon string) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	if icon != "" && !slices.Contains(deviceIcons, icon) {
		return i18n.New("未知的设备图标")
	}
	icons := loadIcons(n.Store())
	if icon == "" {
		delete(icons, keyID)
	} else {
		icons[keyID] = icon
	}
	b, _ := json.Marshal(icons)
	return uiError(n.Store().SetMeta(store.MetaDeviceIcons, string(b)))
}

// CanRename 报告现在能否改设备名：设备圈里其他设备都升级到支持改名的版本（并且连上过一次）才可以。
func (s *Service) CanRename() (RenameCheck, error) {
	n, err := s.node()
	if err != nil {
		return RenameCheck{}, err
	}
	ok, missing, err := n.CanRename()
	if err != nil {
		return RenameCheck{}, uiError(err)
	}
	return RenameCheck{Allowed: ok, Missing: nonNil(missing)}, nil
}

// RenameDevice 给设备圈里的一台设备（可以是本机）改名，新名字会同步给所有成员。
func (s *Service) RenameDevice(keyID, name string) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if err := validName(name); err != nil {
		return err
	}
	err = n.RenameDevice(keyID, name)
	switch {
	case errors.Is(err, node.ErrRenameUnsupported):
		_, missing, _ := n.CanRename()
		return i18n.New("%s需要先升级到新版本，才能改名", quoteNames(missing))
	case errors.Is(err, store.ErrChainChanged):
		return i18n.New("设备圈刚刚有更新，请重试")
	}
	return uiError(err)
}

// quoteNames 把设备名连成一句：中文是「甲」「乙」，英文是 A, B。
func quoteNames(names []string) string {
	if i18n.Language() == i18n.English {
		return strings.Join(names, ", ")
	}
	return "「" + strings.Join(names, "」「") + "」"
}
