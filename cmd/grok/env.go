package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// loadEnvFile 加载 .env 文件到环境变量。
// 自动查找顺序：当前目录 → 上级目录(最多 5 级) → 可执行文件目录及上级。
func loadEnvFile(path string) {
	// 1. 直接用指定路径
	if loadEnvFromFile(path) {
		return
	}
	// 2. 当前工作目录及上级目录（最多 5 级）
	if cwd, err := os.Getwd(); err == nil {
		dir := cwd
		for i := 0; i < 5; i++ {
			if loadEnvFromFile(filepath.Join(dir, ".env")) {
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	// 3. 可执行文件所在目录及上级
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		if loadEnvFromFile(filepath.Join(exeDir, ".env")) {
			return
		}
		parent := filepath.Dir(exeDir)
		if loadEnvFromFile(filepath.Join(parent, ".env")) {
			return
		}
	}
}

// loadEnvFromFile 从指定路径加载 .env，成功返回 true。
func loadEnvFromFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
	return true
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}
