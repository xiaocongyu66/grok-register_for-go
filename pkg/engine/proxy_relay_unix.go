//go:build !windows

package engine

import (
	"os"
	"os/exec"
	"syscall"
)

// setSysProcAttr 在 Unix 系统上设置进程组(Setpgid),便于整组 kill。
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// processAlive 用 signal 0 检查进程是否存活(Unix 方式)。
func processAlive(p *os.Process) bool {
	return p.Signal(syscall.Signal(0)) == nil
}
