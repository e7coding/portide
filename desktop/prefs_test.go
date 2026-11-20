package main

import "testing"

func TestPrefs(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8") // 跟随系统时按英文
	t.Setenv("PORTIDE_NO_UPDATE_CHECK", "1")
	dir := t.TempDir()
	p := newPrefsStore(dir, true)
	if got := p.load(); got != (DesktopPrefs{CloseToTray: true, UpdateCheck: true, HasTray: true, UILanguage: "en"}) {
		t.Fatalf("defaults = %+v", got)
	}
	if err := p.update(func(d *DesktopPrefs) { d.CloseToTray, d.HideHint, d.Language, d.UpdateCheck = false, true, "zh", false }); err != nil {
		t.Fatal(err)
	}
	again := newPrefsStore(dir, false)
	if got := again.load(); got != (DesktopPrefs{CloseToTray: false, HideHint: true, Language: "zh", HasTray: false, UILanguage: "zh"}) {
		t.Fatalf("reloaded = %+v", got)
	}
}

func TestResolveLanguage(t *testing.T) {
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	if got := resolveLanguage(""); got != "zh" {
		t.Errorf("跟随系统（zh_CN）= %q", got)
	}
	if got := resolveLanguage("en"); got != "en" {
		t.Errorf("选了英文 = %q", got)
	}
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	if got := resolveLanguage(""); got != "en" {
		t.Errorf("跟随系统（de_DE）应当用英文：%q", got)
	}
}
