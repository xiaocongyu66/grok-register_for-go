package curlcffi

// websocket.go — WebSocket 实现（替代 curl_cffi 的 ws_connect）
// 使用标准库 net/http 的 WebSocket 升级 + gorilla/websocket 兼容

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// WsCloseCode WebSocket 关闭码
type WsCloseCode int

const (
	WsCloseNormal          WsCloseCode = 1000
	WsCloseGoingAway       WsCloseCode = 1001
	WsCloseProtocolError   WsCloseCode = 1002
	WsCloseUnsupportedData WsCloseCode = 1003
	WsCloseNoStatus        WsCloseCode = 1005
	WsCloseAbnormal        WsCloseCode = 1006
	WsCloseInvalidData     WsCloseCode = 1007
	WsClosePolicyViolation WsCloseCode = 1008
	WsCloseTooLarge        WsCloseCode = 1009
	WsCloseExtension       WsCloseCode = 1010
	WsCloseInternalError   WsCloseCode = 1011
)

// WsFlag WebSocket 帧标志
type WsFlag int

const (
	WsFlagText   WsFlag = 0x1
	WsFlagBinary WsFlag = 0x2
	WsFlagClose  WsFlag = 0x8
	WsFlagPing   WsFlag = 0x9
	WsFlagPong   WsFlag = 0xA
)

// WebSocket WebSocket 连接
type WebSocket struct {
	conn       net.Conn
	mu         sync.Mutex
	closed     bool
	closeCode  WsCloseCode
	closeReason string
	proxy      string
	readDeadline time.Duration
}

// SetReadDeadline 设置读取超时
func (ws *WebSocket) SetReadDeadline(d time.Duration) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.readDeadline = d
	if d > 0 {
		ws.conn.SetReadDeadline(time.Now().Add(d))
	}
}

// WebSocketError WebSocket 错误
type WebSocketError struct{ Msg string }

func (e *WebSocketError) Error() string { return "websocket error: " + e.Msg }

// WebSocketClosed 连接已关闭
type WebSocketClosed struct{ Msg string }

func (e *WebSocketClosed) Error() string { return "websocket closed: " + e.Msg }

// WebSocketTimeout 超时
type WebSocketTimeout struct{ Msg string }

func (e *WebSocketTimeout) Error() string { return "websocket timeout: " + e.Msg }

// WebSocketRetryStrategy 重试策略
type WebSocketRetryStrategy struct {
	Count    int
	Backoff  time.Duration
}

// WsConnect 建立 WebSocket 连接
func WsConnect(targetURL string, proxy string, headers map[string]string, timeout time.Duration) (*WebSocket, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}

	// 将 http(s):// 转换为 ws(s)://
	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "https" || u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	// 建立连接
	var conn net.Conn
	dialer := &net.Dialer{Timeout: timeout}
	if proxy != "" {
		conn, err = dialViaProxyWS(dialer, host, proxy)
	} else {
		conn, err = dialer.Dial("tcp", host)
	}
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	// 如果是 TLS，包装为 TLS 连接
	if u.Scheme == "https" || u.Scheme == "wss" {
		// 用标准库 TLS（utls 的 WebSocket 支持复杂，这里用标准库）
		tlsConn := wrapTLS(conn, u.Hostname())
		if tlsConn == nil {
			conn.Close()
			return nil, fmt.Errorf("TLS handshake failed")
		}
		conn = tlsConn
	}

	// 生成 WebSocket 密钥
	keyBytes := make([]byte, 16)
	rand.Read(keyBytes)
	wsKey := base64.StdEncoding.EncodeToString(keyBytes)

	// 构建 WebSocket 握手请求
	path := u.Path
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}

	handshake := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n", path, u.Host, wsKey)
	for k, v := range headers {
		handshake += fmt.Sprintf("%s: %s\r\n", k, v)
	}
	handshake += "\r\n"

	if _, err := conn.Write([]byte(handshake)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write handshake: %w", err)
	}

	// 读取握手响应
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read handshake: %w", err)
	}

	resp := string(buf[:n])
	if !strings.Contains(resp, "101") {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake failed: %s", resp[:min(200, len(resp))])
	}

	return &WebSocket{conn: conn, proxy: proxy}, nil
}

// SendStr 发送文本消息
func (ws *WebSocket) SendStr(msg string) error {
	return ws.sendFrame(WsFlagText, []byte(msg))
}

// SendBytes 发送二进制消息
func (ws *WebSocket) SendBytes(data []byte) error {
	return ws.sendFrame(WsFlagBinary, data)
}

// SendBinary 发送二进制消息（别名）
func (ws *WebSocket) SendBinary(data []byte) error {
	return ws.SendBytes(data)
}

// Recv 接收消息
func (ws *WebSocket) Recv() ([]byte, WsFlag, error) {
	return ws.readFrame()
}

// RecvStr 接收文本消息
func (ws *WebSocket) RecvStr() (string, error) {
	data, _, err := ws.Recv()
	return string(data), err
}

// RecvJSON 接收 JSON 消息
func (ws *WebSocket) RecvJSON(v interface{}) error {
	data, _, err := ws.Recv()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// RecvFragment 接收消息片段
func (ws *WebSocket) RecvFragment() ([]byte, WsFlag, error) {
	return ws.readFrame()
}

// Ping 发送 Ping 帧
func (ws *WebSocket) Ping() error {
	return ws.sendFrame(WsFlagPing, nil)
}

// Flush 刷新发送缓冲区（Go 的 net.Conn 自动刷新）
func (ws *WebSocket) Flush() error {
	return nil
}

// IsAlive 检查连接是否存活
func (ws *WebSocket) IsAlive() bool {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return !ws.closed
}

// CloseCode 返回关闭码
func (ws *WebSocket) CloseCode() WsCloseCode {
	return ws.closeCode
}

// CloseReason 返回关闭原因
func (ws *WebSocket) CloseReason() string {
	return ws.closeReason
}

// SendQueueSize 返回发送队列大小（Go 无队列，返回 0）
func (ws *WebSocket) SendQueueSize() int {
	return 0
}

// Close 关闭连接
func (ws *WebSocket) Close() error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.closed {
		return nil
	}
	ws.closed = true
	// 发送 Close 帧
	ws.sendFrameRaw(WsFlagClose, []byte{0x03, 0xe8}) // 1000
	return ws.conn.Close()
}

// Terminate 强制终止连接
func (ws *WebSocket) Terminate() error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.closed = true
	return ws.conn.Close()
}

// RunForever 持续接收消息直到连接关闭
func (ws *WebSocket) RunForever(handler func(data []byte, flag WsFlag) error) error {
	for ws.IsAlive() {
		data, flag, err := ws.Recv()
		if err != nil {
			return err
		}
		if flag == WsFlagClose {
			break
		}
		if handler != nil {
			if err := handler(data, flag); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- 内部帧处理 ---

func (ws *WebSocket) sendFrame(opcode WsFlag, data []byte) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.closed {
		return &WebSocketClosed{}
	}
	return ws.sendFrameRaw(opcode, data)
}

func (ws *WebSocket) sendFrameRaw(opcode WsFlag, data []byte) error {
	// WebSocket 帧格式: FIN(1) + RSV(3) + opcode(4) | MASK(1) + payload_len(7) | mask_key(4) | data
	var frame []byte
	fin := byte(0x80) // FIN=1
	mask := byte(0x80) // MASK=1
	frame = append(frame, fin|byte(opcode))

	length := len(data)
	if length < 126 {
		frame = append(frame, mask|byte(length))
	} else if length < 65536 {
		frame = append(frame, mask|126)
		frame = append(frame, byte(length>>8), byte(length))
	} else {
		frame = append(frame, mask|127)
		frame = append(frame, 0, 0, 0, 0, byte(length>>24), byte(length>>16), byte(length>>8), byte(length))
	}

	// 生成 mask key
	maskKey := make([]byte, 4)
	rand.Read(maskKey)
	frame = append(frame, maskKey...)

	// 掩码数据
	masked := make([]byte, len(data))
	for i, b := range data {
		masked[i] = b ^ maskKey[i%4]
	}
	frame = append(frame, masked...)

	_, err := ws.conn.Write(frame)
	return err
}

func (ws *WebSocket) readFrame() ([]byte, WsFlag, error) {
	if ws.closed {
		return nil, 0, &WebSocketClosed{}
	}

	// 读取帧头 (2 bytes)
	header := make([]byte, 2)
	if _, err := io.ReadFull(ws.conn, header); err != nil {
		return nil, 0, err
	}

	opcode := WsFlag(header[0] & 0x0F)
	masked := header[1]&0x80 != 0
	length := int(header[1] & 0x7F)

	// 扩展长度
	if length == 126 {
		extLen := make([]byte, 2)
		if _, err := io.ReadFull(ws.conn, extLen); err != nil {
			return nil, 0, err
		}
		length = int(extLen[0])<<8 | int(extLen[1])
	} else if length == 127 {
		extLen := make([]byte, 8)
		if _, err := io.ReadFull(ws.conn, extLen); err != nil {
			return nil, 0, err
		}
		length = int(extLen[6])<<8 | int(extLen[7])
	}

	// 读取 mask key
	var maskKey []byte
	if masked {
		maskKey = make([]byte, 4)
		if _, err := io.ReadFull(ws.conn, maskKey); err != nil {
			return nil, 0, err
		}
	}

	// 读取 payload
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(ws.conn, payload); err != nil {
			return nil, 0, err
		}
	}

	// 解除掩码
	if masked {
		for i, b := range payload {
			payload[i] = b ^ maskKey[i%4]
		}
	}

	// 处理控制帧
	switch opcode {
	case WsFlagClose:
		if len(payload) >= 2 {
			ws.closeCode = WsCloseCode(int(payload[0])<<8 | int(payload[1]))
		}
		ws.mu.Lock()
		ws.closed = true
		ws.mu.Unlock()
		return payload, WsFlagClose, &WebSocketClosed{}
	case WsFlagPing:
		// 自动回复 Pong
		ws.sendFrame(WsFlagPong, payload)
		return payload, WsFlagPing, nil
	case WsFlagPong:
		return payload, WsFlagPong, nil
	}

	return payload, opcode, nil
}

// --- 辅助 ---

func dialViaProxyWS(dialer *net.Dialer, targetAddr, proxyURL string) (net.Conn, error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	conn, err := dialer.Dial("tcp", u.Host)
	if err != nil {
		return nil, err
	}
	host, port, _ := net.SplitHostPort(targetAddr)
	connectReq := fmt.Sprintf("CONNECT %s:%s HTTP/1.1\r\nHost: %s:%s\r\n", host, port, host, port)
	if u.User != nil {
		user := u.User.Username()
		pass, _ := u.User.Password()
		connectReq += fmt.Sprintf("Proxy-Authorization: Basic %s\r\n", BasicAuth(user, pass))
	}
	connectReq += "\r\n"
	if _, err := conn.Write([]byte(connectReq)); err != nil {
		conn.Close()
		return nil, err
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil || n < 12 || !strings.HasPrefix(string(buf[:12]), "HTTP/1.1 200") {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT failed")
	}
	return conn, nil
}

func wrapTLS(conn net.Conn, hostname string) net.Conn {
	// 用标准库 TLS 包装（不使用 utls，避免类型冲突）
	tlsConn, err := tlsDial(conn, hostname)
	if err != nil {
		return nil
	}
	return tlsConn
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// WsConnectQuick 快速建立 WebSocket 连接
func WsConnectQuick(targetURL string, proxy string) (*WebSocket, error) {
	return WsConnect(targetURL, proxy, nil, 30*time.Second)
}
