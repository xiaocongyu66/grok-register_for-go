package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// cmdInit 检测并安装 grok 运行所需的外部依赖：
//   - Chrome/Chromium 浏览器（Turnstile 求解需要）
//   - Xvfb（虚拟显示，headed 浏览器需要）
//   - playwright 运行时（chromium-1234）
//
// 用法：grok init [--install]
//   --install  自动安装缺失的依赖（需要 root + apt）
func cmdInit(args []string) {
	install := false
	for _, a := range args {
		if a == "--install" || a == "-i" {
			install = true
		}
	}

	fmt.Println("=== grok init: 检测运行环境 ===")
	fmt.Printf("平台: %s/%s\n\n", runtime.GOOS, runtime.GOARCH)

	allOK := true

	// 1. Chrome/Chromium
	fmt.Println("[1/3] 检测 Chrome/Chromium 浏览器...")
	chromePath := findChromeForInit()
	if chromePath != "" {
		fmt.Printf("  ✓ 找到: %s\n", chromePath)
	} else {
		fmt.Println("  ✗ 未找到 Chrome/Chromium")
		if install {
			installChrome()
			chromePath = findChromeForInit()
			if chromePath != "" {
				fmt.Printf("  ✓ 安装后找到: %s\n", chromePath)
			} else {
				fmt.Println("  ✗ 安装后仍未找到，请手动安装 chromium")
				allOK = false
			}
		} else {
			fmt.Println("  提示: 运行 'grok init --install' 自动安装，或手动安装 chromium-browser")
			fmt.Println("  或设置 CHROME_PATH 环境变量指向 Chrome 可执行文件")
			allOK = false
		}
	}

	// 2. Xvfb
	fmt.Println()
	fmt.Println("[2/3] 检测 Xvfb（虚拟显示）...")
	xvfbPath := findXvfb()
	if xvfbPath != "" {
		fmt.Printf("  ✓ 找到: %s\n", xvfbPath)
		// 检查 Xvfb 是否在运行
		if os.Getenv("DISPLAY") == "" {
			fmt.Println("  启动 Xvfb（DISPLAY 未设置）...")
			startXvfb()
		} else {
			fmt.Printf("  ✓ DISPLAY=%s 已设置\n", os.Getenv("DISPLAY"))
		}
	} else {
		fmt.Println("  ✗ 未找到 Xvfb")
		if install {
			installXvfb()
			xvfbPath = findXvfb()
			if xvfbPath != "" {
				fmt.Printf("  ✓ 安装后找到: %s\n", xvfbPath)
				startXvfb()
			} else {
				fmt.Println("  ✗ 安装失败")
				allOK = false
			}
		} else {
			fmt.Println("  提示: 运行 'grok init --install' 自动安装，或 apt install xvfb")
			fmt.Println("  没有 Xvfb 时浏览器会用 headless 模式（Turnstile 通过率较低）")
		}
	}

	// 3. playwright 运行时
	fmt.Println()
	fmt.Println("[3/3] 检测 playwright 运行时（chromium-1234）...")
	pwChrome := findPlaywrightChrome()
	if pwChrome != "" {
		fmt.Printf("  ✓ 找到: %s\n", pwChrome)
	} else {
		fmt.Println("  ! 未找到 playwright 运行时")
		fmt.Println("  提示: cloakbrowser 已够用，playwright 运行时可选")
		fmt.Println("  如需安装: npx playwright install chromium")
	}

	// 4. 设置环境变量提示
	fmt.Println()
	fmt.Println("=== 环境变量配置 ===")
	if chromePath != "" {
		fmt.Printf("export CHROME_PATH=%s\n", chromePath)
		fmt.Printf("export SOLVER_CHROME_PATH=%s\n", chromePath)
	}
	if os.Getenv("DISPLAY") != "" {
		fmt.Printf("export DISPLAY=%s\n", os.Getenv("DISPLAY"))
	}

	fmt.Println()
	if allOK {
		fmt.Println("✓ 环境就绪！可以运行:")
		fmt.Println("  ./grok scrape          # 抓取代理")
		fmt.Println("  ./grok register -t 5   # 注册账号")
	} else {
		fmt.Println("✗ 部分依赖缺失，运行 'grok init --install' 自动安装")
		os.Exit(1)
	}
}

// findChromeForInit 查找 Chrome/Chromium 可执行文件。
func findChromeForInit() string {
	// 1. 环境变量
	for _, key := range []string{"CHROME_PATH", "SOLVER_CHROME_PATH", "CHROME_BIN"} {
		if p := os.Getenv(key); p != "" {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	// 2. cloakbrowser
	home, _ := os.UserHomeDir()
	matches, _ := filepath.Glob(filepath.Join(home, ".cloakbrowser", "chromium-*", "chrome"))
	if len(matches) > 0 {
		return matches[len(matches)-1]
	}
	// 3. 系统路径
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// findXvfb 查找 Xvfb。
func findXvfb() string {
	if p, err := exec.LookPath("Xvfb"); err == nil {
		return p
	}
	if p, err := exec.LookPath("xvfb-run"); err == nil {
		return p
	}
	return ""
}

// findPlaywrightChrome 查找 playwright 运行时。
func findPlaywrightChrome() string {
	home, _ := os.UserHomeDir()
	// ms-playwright 缓存
	matches, _ := filepath.Glob(filepath.Join(home, ".cache", "ms-playwright", "chromium-*", "chrome-linux", "chrome"))
	if len(matches) > 0 {
		return matches[0]
	}
	matches, _ = filepath.Glob(filepath.Join(home, ".cache", "ms-playwright", "chromium-*", "chrome-linux", "headless_shell"))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// installChrome 用 apt 安装 chromium。
func installChrome() {
	fmt.Println("  尝试 apt install chromium...")
	if _, err := exec.LookPath("apt-get"); err != nil {
		fmt.Println("  ✗ 未找到 apt-get，请手动安装 chromium")
		return
	}
	cmd := exec.Command("apt-get", "install", "-y", "chromium")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("  安装出错: %v\n", err)
	}
}

// installXvfb 用 apt 安装 xvfb。
func installXvfb() {
	fmt.Println("  尝试 apt install xvfb...")
	if _, err := exec.LookPath("apt-get"); err != nil {
		fmt.Println("  ✗ 未找到 apt-get，请手动安装 xvfb")
		return
	}
	cmd := exec.Command("apt-get", "install", "-y", "xvfb")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("  安装出错: %v\n", err)
	}
}

// startXvfb 启动 Xvfb 虚拟显示。
func startXvfb() {
	display := ":2"
	os.Setenv("DISPLAY", display)
	// 清理旧的锁文件
	os.Remove(fmt.Sprintf("/tmp/.X%s-lock", strings.TrimPrefix(display, ":")))
	os.Remove(fmt.Sprintf("/tmp/.X11-unix/X%s", strings.TrimPrefix(display, ":")))
	// 启动 Xvfb
	cmd := exec.Command("Xvfb", display, "-screen", "0", "1280x720x24")
	cmd.SysProcAttr = &sysProcAttrSetpgid
	if err := cmd.Start(); err != nil {
		fmt.Printf("  启动 Xvfb 失败: %v\n", err)
		return
	}
	fmt.Printf("  ✓ Xvfb 启动 (PID=%d, DISPLAY=%s)\n", cmd.Process.Pid, display)
}
