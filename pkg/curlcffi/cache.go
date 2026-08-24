package curlcffi

// cache.go — 响应缓存（替代 Python curl_cffi 的 cache 参数）
// 支持 FileCacheBackend：按 URL+方法 哈希缓存响应到内存/文件

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CacheEntry 缓存条目
type CacheEntry struct {
	Response  *Response
	ExpiresAt time.Time
}

// FileCacheBackend 文件+内存混合缓存
type FileCacheBackend struct {
	mu       sync.RWMutex
	memory   map[string]*CacheEntry
	cacheDir string
	ttl      time.Duration
}

// NewFileCacheBackend 创建缓存后端
func NewFileCacheBackend(cacheDir string, ttl time.Duration) *FileCacheBackend {
	if cacheDir != "" {
		os.MkdirAll(cacheDir, 0755)
	}
	return &FileCacheBackend{
		memory:   make(map[string]*CacheEntry),
		cacheDir: cacheDir,
		ttl:      ttl,
	}
}

// cacheKey 生成缓存键
func cacheKey(method, url string) string {
	h := sha256.Sum256([]byte(method + ":" + url))
	return hex.EncodeToString(h[:])
}

// Get 从缓存获取
func (c *FileCacheBackend) Get(method, url string) *Response {
	key := cacheKey(method, url)

	// 先查内存
	c.mu.RLock()
	if entry, ok := c.memory[key]; ok {
		if time.Now().Before(entry.ExpiresAt) {
			c.mu.RUnlock()
			return entry.Response
		}
		c.mu.RUnlock()
		c.mu.Lock()
		delete(c.memory, key)
		c.mu.Unlock()
		return nil
	}
	c.mu.RUnlock()

	// 再查文件
	if c.cacheDir == "" {
		return nil
	}
	path := filepath.Join(c.cacheDir, key+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if c.ttl > 0 && time.Since(info.ModTime()) > c.ttl {
		os.Remove(path)
		return nil
	}
	// 反序列化
	resp := &Response{}
	if err := jsonUnmarshal(data, resp); err != nil {
		return nil
	}
	// 放入内存
	c.mu.Lock()
	c.memory[key] = &CacheEntry{Response: resp, ExpiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
	return resp
}

// Set 写入缓存
func (c *FileCacheBackend) Set(method, url string, resp *Response) {
	key := cacheKey(method, url)

	// 写内存
	c.mu.Lock()
	c.memory[key] = &CacheEntry{Response: resp, ExpiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()

	// 写文件
	if c.cacheDir == "" {
		return
	}
	path := filepath.Join(c.cacheDir, key+".json")
	data, err := jsonMarshal(resp)
	if err == nil {
		os.WriteFile(path, data, 0644)
	}
}

// Clear 清除缓存
func (c *FileCacheBackend) Clear() {
	c.mu.Lock()
	c.memory = make(map[string]*CacheEntry)
	c.mu.Unlock()
	if c.cacheDir != "" {
		os.RemoveAll(c.cacheDir)
		os.MkdirAll(c.cacheDir, 0755)
	}
}

// Size 返回缓存条目数
func (c *FileCacheBackend) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.memory)
}

// WithCache 设置 Session 缓存
func WithCache(cacheDir string, ttl time.Duration) func(*Session) {
	return func(s *Session) {
		s.cache = NewFileCacheBackend(cacheDir, ttl)
	}
}

// jsonMarshal/jsonUnmarshal 辅助
func jsonMarshal(v interface{}) ([]byte, error) {
	return jsonMarshalImpl(v)
}

func jsonUnmarshal(data []byte, v interface{}) error {
	return jsonUnmarshalImpl(data, v)
}
