package main

import (
	"fmt"
	"os"
	"portide/core/i18n"

	"golang.org/x/term"
)

// askPassphrase 在终端里提示输入文件密钥库的口令（没有设置 PORTIDE_PASSPHRASE 时由 appdir.OpenKeys 调用）。
func askPassphrase() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", i18n.New("文件密钥库需要口令：请设置环境变量 PORTIDE_PASSPHRASE")
	}
	fmt.Fprint(os.Stderr, i18n.T("密钥库口令："))
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
