// Package minirelay 实现最小化的多协议代理中继。
// 目标：替代 sing-box 二进制，只做"本地 mixed inbound + 远端协议 outbound"，
// 支持 socks5/http/vless/vmess/trojan/ss/hysteria2，占用更低。
//
// 架构：
//   本地 mixed listener (socks5 + http)
//     → 解析目标地址
//     → 按 proxy URL scheme 选择 outbound
//     → outbound 建立到远端节点的连接
//     → 双向转发
package minirelay

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Relay 管理一个本地 mixed 代理监听器 + 一个上游代理。
type Relay struct {
	listen   string
	upstream string // socks5:// / http:// / vless:// 等
	user     string
	pass     string

	ln     net.Listener
	closed bool
	mu     sync.Mutex
}

// New 创建中继。listen 是本地监听地址（如 127.0.0.1:19210），
// upstream 是上游代理 URL（如 socks5://user:pass@host:port 或 vless://uuid@host:port?...）。
func New(listen, upstream string) (*Relay, error) {
	return &Relay{
		listen:   listen,
		upstream: upstream,
	}, nil
}

// Start 启动监听（非阻塞）。
func (r *Relay) Start() error {
	ln, err := net.Listen("tcp", r.listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", r.listen, err)
	}
	r.ln = ln
	go r.acceptLoop()
	return nil
}

// ListenAddr 返回实际监听地址。
func (r *Relay) ListenAddr() string {
	if r.ln == nil {
		return ""
	}
	return r.ln.Addr().String()
}

// Close 停止中继。
func (r *Relay) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.ln != nil {
		return r.ln.Close()
	}
	return nil
}

func (r *Relay) acceptLoop() {
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			r.mu.Lock()
			closed := r.closed
			r.mu.Unlock()
			if closed {
				return
			}
			continue
		}
		go r.handleConn(conn)
	}
}

// handleConn 处理本地连接：识别 socks5/http，解析目标，建立上游连接，双向转发。
func (r *Relay) handleConn(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	// 读第一个字节判断协议
	buf := make([]byte, 1)
	_, err := io.ReadFull(conn, buf)
	if err != nil {
		return
	}
	conn.SetDeadline(time.Time{}) // 清除 deadline

	var target string
	if buf[0] == 0x05 {
		// socks5
		target, err = r.handleSocks5(conn)
	} else {
		// http
		target, err = r.handleHTTP(conn, buf[0])
	}
	if err != nil {
		fmt.Printf("[minirelay] inbound error: %v\n", err)
		return
	}
	if target == "" {
		return
	}

	// 建立上游连接
	upstream, err := r.dialUpstream(target)
	if err != nil {
		fmt.Printf("[minirelay] upstream error (%s): %v\n", target, err)
		return
	}
	defer upstream.Close()

	// 双向转发
	pipe(conn, upstream)
}

// pipe 双向转发两个连接。
// 一端 EOF 后给另一端短暂时间完成读取（1 秒），然后关闭。
// 避免立即关闭导致客户端 read: connection reset。
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(b, a)
		// a→b 结束，等 1 秒让 b→a 完成读取，然后关闭
		time.AfterFunc(1*time.Second, func() { b.Close() })
		done <- struct{}{}
	}()
	go func() {
		io.Copy(a, b)
		// b→a 结束，等 1 秒让 a→b 完成读取，然后关闭
		time.AfterFunc(1*time.Second, func() { a.Close() })
		done <- struct{}{}
	}()
	<-done
	<-done
}

// dialUpstream 根据上游代理 scheme 建立到目标的连接。
// 这是协议分发的核心——根据 upstream URL 选择对应协议实现。
func (r *Relay) dialUpstream(target string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dialer, err := newOutboundDialer(r.upstream)
	if err != nil {
		return nil, err
	}
	return dialer.Dial(ctx, target)
}
