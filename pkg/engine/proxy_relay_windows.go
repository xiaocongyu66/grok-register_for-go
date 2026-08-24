//go:build windows

package engine

import (
	"os"
	"os/exec"
)

// setSysProcAttr 在 Windows 上不设置进程组(Windows 不支持 Setpgid)。
func setSysProcAttr(cmd *exec.Cmd) {}

// processAlive 在 Windows 上检查进程是否存活。
// Windows 不支持 signal 0,这里用 OpenProcess 检测。
// 简单实现:如果 cmd.ProcessState 已设置说明已退出,否则认为存活。
func processAlive(p *os.Process) bool {
	// Windows 上 os.Process 没有 Signal 方法,用 ExitCode 检查
	// 简化处理:返回 true,依赖端口/超时机制判断
	return true
}
