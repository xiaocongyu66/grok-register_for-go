//go:build windows

package main

import "syscall"

// Windows 不支持 Setpgid，空 SysProcAttr。
var sysProcAttrSetpgid = syscall.SysProcAttr{}
