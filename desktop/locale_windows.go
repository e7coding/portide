package main

import "golang.org/x/sys/windows"

// platformLanguage 读 Windows 的显示语言。
func platformLanguage() string {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil || len(langs) == 0 {
		return ""
	}
	return langs[0]
}
