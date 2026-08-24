package minirelay

import (
	"github.com/sagernet/sing/common/logger"
)

// nopLogger 是 sing logger 接口的 no-op 实现。
// sing-vmess 的 vision 协议在 filterTLS 时会调 logger.Trace，
// 如果传 nil 会 panic，所以需要一个空实现。
type nopLogger struct{}

func (nopLogger) Trace(args ...any)                    {}
func (nopLogger) Debug(args ...any)                    {}
func (nopLogger) Info(args ...any)                     {}
func (nopLogger) Warn(args ...any)                     {}
func (nopLogger) Error(args ...any)                    {}
func (nopLogger) Fatal(args ...any)                    {}
func (nopLogger) Panic(args ...any)                    {}
func (nopLogger) Tracef(format string, args ...any)    {}
func (nopLogger) Debugf(format string, args ...any)    {}
func (nopLogger) Infof(format string, args ...any)     {}
func (nopLogger) Warnf(format string, args ...any)      {}
func (nopLogger) Errorf(format string, args ...any)     {}
func (nopLogger) Fatalf(format string, args ...any)     {}
func (nopLogger) Panicf(format string, args ...any)     {}

var _ logger.Logger = nopLogger{}
