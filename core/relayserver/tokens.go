package relayserver

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"portide/core/relay"
)

// ErrNoToken 表示没有这个令牌编号。
var ErrNoToken = errors.New("relay: no such token")

// TokenEntry 是一个访问令牌的记录。文件里只保存令牌的 SHA-256，令牌本身只在添加时显示一次。
type TokenEntry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Hash    string `json:"hash"`
	Created int64  `json:"created"` // Unix 秒
}

// TokenFile 是保存在 JSON 文件里的访问令牌，可以生成多个、分别吊销（设计方案 8）。
// 中继进程运行期间，命令行可以随时增删令牌：Check 和 Valid 发现文件变了就重新读取。
type TokenFile struct {
	path    string
	mu      sync.Mutex
	sum     [sha256.Size]byte // 上次解析的文件内容的哈希
	loaded  bool              // entries 是否来自上次成功的解析
	entries []TokenEntry
	lastErr error // 最近一次 reload 的错误；成功或文件不存在时为 nil
}

// OpenTokens 打开令牌文件（不存在时视为没有令牌）。
func OpenTokens(path string) *TokenFile { return &TokenFile{path: path} }

func hashToken(token []byte) string {
	sum := sha256.Sum256(token)
	return hex.EncodeToString(sum[:])
}

// reload 在文件内容变化时重新解析；调用方持有 f.mu。记录结果供 Err 查询。
//
// 每次都读一遍文件（只有几百字节），按内容的哈希判断有没有变。不能按 inode、mtime、大小判断：
// 命令行增删令牌是写临时文件再改名替换，Linux 上刚释放的 inode 号会马上被新文件复用，
// 大小也可能相同，按元数据会误以为没变，已吊销的令牌就还能用。每次读取也顺带发现权限变化
// （例如改成了中继读不了的 0600）。
func (f *TokenFile) reload() error {
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		f.entries, f.loaded = nil, false
		f.lastErr = nil
		return nil
	}
	if err != nil {
		f.lastErr = err
		return err
	}
	sum := sha256.Sum256(b)
	if f.loaded && sum == f.sum {
		f.lastErr = nil
		return nil
	}
	var entries []TokenEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		f.lastErr = err
		return err
	}
	if entries == nil {
		entries = []TokenEntry{}
	}
	f.entries, f.sum, f.loaded = entries, sum, true
	f.lastErr = nil
	return nil
}

// Err 返回最近一次重新读取令牌文件的错误；上次读取成功或文件本来就不存在时为 nil。
func (f *TokenFile) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastErr
}

// save 先写临时文件再改名，并 fsync，避免中继读到写了一半的文件；调用方持有 f.mu。
func (f *TokenFile) save(entries []TokenEntry) error {
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	tf, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := tf.Write(b); err != nil {
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
	f.entries, f.loaded = nil, false // 下次读取时按新文件重新加载
	return f.reload()
}

// Add 生成一个新令牌，返回它的记录和令牌本身（只此一次）。
func (f *TokenFile) Add(name string) (TokenEntry, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.reload(); err != nil {
		return TokenEntry{}, nil, err
	}
	token := make([]byte, relay.TokenSize)
	if _, err := rand.Read(token); err != nil {
		return TokenEntry{}, nil, err
	}
	id := make([]byte, 3)
	if _, err := rand.Read(id); err != nil {
		return TokenEntry{}, nil, err
	}
	e := TokenEntry{ID: hex.EncodeToString(id), Name: name, Hash: hashToken(token), Created: time.Now().Unix()}
	if err := f.save(append(append([]TokenEntry(nil), f.entries...), e)); err != nil {
		return TokenEntry{}, nil, err
	}
	return e, token, nil
}

// List 返回所有令牌记录（按添加顺序）。
func (f *TokenFile) List() ([]TokenEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.reload(); err != nil {
		return nil, err
	}
	return append([]TokenEntry(nil), f.entries...), nil
}

// Revoke 吊销一个令牌：正在用它的设备会在几秒内被断开（见 Server）。
func (f *TokenFile) Revoke(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.reload(); err != nil {
		return err
	}
	var kept []TokenEntry
	for _, e := range f.entries {
		if e.ID != id {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(f.entries) {
		return ErrNoToken
	}
	if kept == nil {
		kept = []TokenEntry{}
	}
	return f.save(kept)
}

// Check 判断令牌是否有效，返回它的编号。
func (f *TokenFile) Check(token []byte) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reload() != nil {
		return "", false
	}
	h := []byte(hashToken(token))
	for _, e := range f.entries {
		if subtle.ConstantTimeCompare(h, []byte(e.Hash)) == 1 {
			return e.ID, true
		}
	}
	return "", false
}

// Valid 判断编号为 id 的令牌是否仍然有效（没有被吊销）。
// 读不了令牌文件时按无效处理（宁可断开，也不能让已吊销的令牌继续有效）。
func (f *TokenFile) Valid(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reload() != nil {
		return false
	}
	for _, e := range f.entries {
		if e.ID == id {
			return true
		}
	}
	return false
}
