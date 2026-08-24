//go:build !windows

package main

import "syscall"

// sysProcAttrSetpgid 让子进程脱离父进程组（Unix 专属）。
var sysProcAttrSetpgid = syscall.SysProcAttr{Setpgid: true}
