// Package appdir 是命令行和桌面应用共用的启动逻辑：解析数据目录、选定并打开密钥库，
// 以及把数据库打开时的常见错误换成用户能看懂的中文提示。
package appdir

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"portide/core/i18n"
	"portide/core/keystore"
	"portide/core/store"
)

// MetaKeystore 记录 init 时选定的密钥库后端，之后每次都用同一个，避免秘密"丢失"在另一个后端里。
const MetaKeystore = "keystore_backend"

// PassphraseFunc 在文件密钥库需要口令、且没有设置 PORTIDE_PASSPHRASE 时被调用，由调用方决定怎么问用户
// （命令行在终端里输入，桌面应用弹出对话框）。
type PassphraseFunc func() (string, error)

// DataDir 解析数据目录：flagValue（命令行的 --data）> PORTIDE_HOME > 系统配置目录/portide，并确保它存在。
func DataDir(flagValue string) (string, error) {
	dir := flagValue
	if dir == "" {
		dir = os.Getenv("PORTIDE_HOME")
	}
	if dir == "" {
		cfg, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(cfg, "portide")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return abs, os.MkdirAll(abs, 0o700)
}

// MapStoreErr 把"数据库版本比本程序新""数据库密钥丢失 / 不匹配"换成用户能看懂的提示；其他错误原样返回。
// OpenKeys 和打开设备（node.Open 内部又一次 store.Open）都会打开数据库，任何一处遇到这些错误都要经过这里，
// 用户才不会看到英文原文。
func MapStoreErr(err error) error {
	switch {
	case errors.Is(err, store.ErrNewerDatabase):
		return i18n.New("数据目录来自更新版本的 Portide，请升级本程序")
	case errors.Is(err, store.ErrKeyMissing):
		return i18n.New("找不到这个数据目录的数据库密钥：数据目录或 PORTIDE_KEYSTORE 是不是换了？")
	case errors.Is(err, store.ErrWrongKey):
		return i18n.New("密钥库里的数据库密钥和这个数据目录不匹配：数据目录或 PORTIDE_KEYSTORE 是不是换了？")
	}
	return err
}

// Backend 返回这个数据目录使用的密钥库后端（"os" 或 "file"）。首次调用时按 PORTIDE_KEYSTORE（auto | os | file，
// 默认 auto）选定并记录，之后一直沿用记录的值。
func Backend(dir string) (string, error) {
	st, err := store.Open(dir)
	if err != nil {
		return "", MapStoreErr(err)
	}
	defer st.Close()
	backend, ok, err := st.GetMeta(MetaKeystore)
	if err != nil {
		return "", err
	}
	if ok {
		return backend, nil
	}
	backend, err = chooseBackend(dir)
	if err != nil {
		return "", err
	}
	return backend, st.SetMeta(MetaKeystore, backend)
}

// NeedsPassphrase 报告打开这个数据目录的密钥库时是否要向用户要口令（文件密钥库且没有设置 PORTIDE_PASSPHRASE）。
func NeedsPassphrase(dir string) (bool, error) {
	backend, err := Backend(dir)
	if err != nil {
		return false, err
	}
	return backend == "file" && os.Getenv("PORTIDE_PASSPHRASE") == "", nil
}

// OpenKeys 打开数据目录的密钥库。文件密钥库的口令优先取 PORTIDE_PASSPHRASE，没有时调用 ask。
func OpenKeys(dir string, ask PassphraseFunc) (keystore.Store, error) {
	backend, err := Backend(dir)
	if err != nil {
		return nil, err
	}
	switch backend {
	case "os":
		return keystore.NewOS(dir), nil
	case "file":
		pass := os.Getenv("PORTIDE_PASSPHRASE")
		if pass == "" {
			if pass, err = ask(); err != nil {
				return nil, err
			}
		}
		if pass == "" {
			return nil, i18n.New("口令不能为空")
		}
		return keystore.NewFile(filepath.Join(dir, "keys.json"), pass, 0)
	default:
		return nil, i18n.New("未知的密钥库类型 %q", backend)
	}
}

func chooseBackend(dir string) (string, error) {
	switch v := strings.ToLower(os.Getenv("PORTIDE_KEYSTORE")); v {
	case "os", "file":
		return v, nil
	case "", "auto":
		// 设置了口令就说明用户想用文件密钥库，避免在无图形桌面的 Linux 上探测系统密钥库。
		if os.Getenv("PORTIDE_PASSPHRASE") != "" {
			return "file", nil
		}
		if keystore.NewOS(dir).Available() {
			return "os", nil
		}
		return "file", nil
	default:
		return "", i18n.New("PORTIDE_KEYSTORE 只能是 auto、os 或 file，当前为 %q", v)
	}
}
