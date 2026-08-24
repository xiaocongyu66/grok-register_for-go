package engine

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	projectRoot string
	once        sync.Once
)

func initProjectRoot() {
	once.Do(func() {
		exe, _ := os.Executable()
		root := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
		if _, err := os.Stat(filepath.Join(root, "代理.txt")); err != nil {
			root, _ = os.Getwd()
			for {
				if _, err := os.Stat(filepath.Join(root, "代理.txt")); err == nil {
					break
				}
				parent := filepath.Dir(root)
				if parent == root {
					break
				}
				root = parent
			}
		}
		projectRoot = root
		_ = os.MkdirAll(filepath.Join(projectRoot, "keys"), 0755)
		_ = os.MkdirAll(filepath.Join(projectRoot, "logs"), 0755)
	})
}

func ProjectRoot() string { initProjectRoot(); return projectRoot }

func KeysDir() string { return filepath.Join(ProjectRoot(), "keys") }

func ProxyFile() string { return filepath.Join(ProjectRoot(), "代理.txt") }

func SSOFile() string { return filepath.Join(KeysDir(), "sso.txt") }

func envFirst(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		n := 0
		for _, c := range v {
			if c < '0' || c > '9' {
				return def
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

// DebugMode returns true if GROK_DEBUG is set.
func DebugMode() bool { return os.Getenv("GROK_DEBUG") != "" }

// nowUTC returns current UTC time string.
func nowUTC() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }
