package keystore

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// DefaultIterations 是 PBKDF2-SHA256 的迭代次数（OWASP 2023 建议值）。
const DefaultIterations = 600_000

// File 把秘密用口令派生的密钥（PBKDF2-SHA256 → AES-256-GCM）加密后保存在一个 JSON 文件里。
type File struct {
	path       string
	passphrase string
	iterations int

	mu      sync.Mutex
	key     []byte // 派生后的密钥，首次使用时计算
	keySalt []byte // key 对应的盐；盐变化（例如首次写入失败后重试）时必须重新派生
}

type fileFormat struct {
	Version    int               `json:"version"`
	Iterations int               `json:"iterations"`
	Salt       []byte            `json:"salt"`
	Entries    map[string][]byte `json:"entries"` // nonce ‖ ciphertext
}

// NewFile 返回位于 path 的文件密钥库。iterations 为 0 时使用 DefaultIterations。
func NewFile(path, passphrase string, iterations int) (*File, error) {
	if passphrase == "" {
		return nil, errors.New("keystore: file store requires a passphrase (set PORTIDE_PASSPHRASE)")
	}
	if iterations == 0 {
		iterations = DefaultIterations
	}
	return &File{path: path, passphrase: passphrase, iterations: iterations}, nil
}

func (f *File) load() (*fileFormat, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
		return &fileFormat{Version: 1, Iterations: f.iterations, Salt: salt, Entries: map[string][]byte{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var ff fileFormat
	if err := json.Unmarshal(data, &ff); err != nil {
		return nil, fmt.Errorf("keystore: corrupt file %s: %w", f.path, err)
	}
	if ff.Entries == nil {
		ff.Entries = map[string][]byte{}
	}
	return &ff, nil
}

func (f *File) aead(ff *fileFormat) (cipher.AEAD, error) {
	if f.key == nil || !bytes.Equal(f.keySalt, ff.Salt) {
		key, err := pbkdf2.Key(sha256.New, f.passphrase, ff.Salt, ff.Iterations, 32)
		if err != nil {
			return nil, err
		}
		f.key = key
		f.keySalt = ff.Salt
	}
	block, err := aes.NewCipher(f.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (f *File) Get(name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, err := f.load()
	if err != nil {
		return nil, err
	}
	sealed, ok := ff.Entries[name]
	if !ok {
		return nil, ErrNotFound
	}
	aead, err := f.aead(ff)
	if err != nil {
		return nil, err
	}
	if len(sealed) < aead.NonceSize() {
		return nil, fmt.Errorf("keystore: entry %q is truncated", name)
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(name))
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	return plain, nil
}

func (f *File) Set(name string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, err := f.load()
	if err != nil {
		return err
	}
	aead, err := f.aead(ff)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ff.Entries[name] = aead.Seal(nonce, nonce, value, []byte(name))
	data, err := json.MarshalIndent(ff, "", "  ")
	if err != nil {
		return err
	}
	// 先写临时文件再改名，避免另一个进程读到写了一半的文件；fsync 临时文件、改名之后再 fsync
	// 所在目录，这样掉电也不会出现"数据库已经记下绑定的密钥校验值，密钥库里却是旧文件"的情况——
	// 不然目录重新映射到这把新密钥之前就断电，数据目录会永久绑定一把密钥库里找不到的密钥
	// （ErrKeyMissing，且无法恢复）。
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	tf, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := tf.Write(data); err != nil {
		tf.Close()
		return err
	}
	if err := tf.Sync(); err != nil {
		tf.Close()
		return err
	}
	if err := tf.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// Windows 上目录不能用 os.Open 打开来 fsync；改名本身在 NTFS 上已经是日志化的，
		// 这里就不再多做。
		return nil
	}
	df, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer df.Close()
	return df.Sync()
}
