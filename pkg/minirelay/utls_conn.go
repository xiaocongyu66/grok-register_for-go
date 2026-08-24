package minirelay

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"reflect"
	"time"
	"unsafe"

	utls "github.com/metacubex/utls"
	"golang.org/x/crypto/hkdf"
)

// utlsConn 封装 utls UConn，支持 reality TLS 握手。
type utlsConn struct {
	raw           net.Conn
	uConn         *utls.UConn
	sni           string
	fingerprint   string
	pubKey        []byte // reality public key (32 bytes)
	shortID       [8]byte
	verified      bool
	authKeyVerify []byte // 保存 authKey 用于证书验证
}

func newUTlsConn(raw net.Conn, sni, fingerprint string, pubKey []byte, shortID string) *utlsConn {
	c := &utlsConn{
		raw:         raw,
		sni:         sni,
		fingerprint: fingerprint,
		pubKey:      pubKey,
	}
	if shortID != "" {
		hex.Decode(c.shortID[:], []byte(shortID))
	}
	return c
}

// handshake 执行 reality TLS 握手。
func (c *utlsConn) handshake() error {
	fmt.Printf("[reality] starting handshake sni=%s fp=%s pubKeyLen=%d\n", c.sni, c.fingerprint, len(c.pubKey))

	var spec utls.ClientHelloID
	switch c.fingerprint {
	case "chrome":
		spec = utls.HelloChrome_Auto
	case "firefox":
		spec = utls.HelloFirefox_Auto
	case "safari":
		spec = utls.HelloSafari_Auto
	case "ios":
		spec = utls.HelloIOS_Auto
	case "edge":
		spec = utls.HelloEdge_Auto
	default:
		spec = utls.HelloChrome_Auto
	}

	config := &utls.Config{
		ServerName:            c.sni,
		InsecureSkipVerify:    true,
		SessionTicketsDisabled: true,
		// vision 依赖 TLS 1.3 + ALPN h2 协商
		NextProtos: []string{"h2", "http/1.1"},
	}
	c.uConn = utls.UClient(c.raw, config, spec)

	if err := c.uConn.BuildHandshakeState(); err != nil {
		return fmt.Errorf("reality: build handshake state: %w", err)
	}

	// 过滤掉 X25519MLKEM768（reality 不支持）
	for _, ext := range c.uConn.Extensions {
		if ce, ok := ext.(*utls.SupportedCurvesExtension); ok {
			filtered := ce.Curves[:0]
			for _, cid := range ce.Curves {
				if cid != utls.X25519MLKEM768 {
					filtered = append(filtered, cid)
				}
			}
			ce.Curves = filtered
		}
		if ks, ok := ext.(*utls.KeyShareExtension); ok {
			filtered := ks.KeyShares[:0]
			for _, share := range ks.KeyShares {
				if share.Group != utls.X25519MLKEM768 {
					filtered = append(filtered, share)
				}
			}
			ks.KeyShares = filtered
		}
	}
	if err := c.uConn.BuildHandshakeState(); err != nil {
		return fmt.Errorf("reality: rebuild handshake state: %w", err)
	}

	hello := c.uConn.HandshakeState.Hello
	hello.SessionId = make([]byte, 32)
	hello.SessionId[0] = 1
	hello.SessionId[1] = 8
	hello.SessionId[2] = 1
	binary.BigEndian.PutUint32(hello.SessionId[4:], uint32(time.Now().Unix()))
	copy(hello.SessionId[8:], c.shortID[:])
	fmt.Printf("[reality] session_id set, shortID=%x\n", c.shortID[:])

	if len(c.pubKey) != 32 {
		return fmt.Errorf("reality: invalid public key length %d", len(c.pubKey))
	}
	serverPub, err := ecdh.X25519().NewPublicKey(c.pubKey)
	if err != nil {
		return fmt.Errorf("reality: parse server public key: %w", err)
	}

	keyShareKeys := c.uConn.HandshakeState.State13.KeyShareKeys
	if keyShareKeys == nil || keyShareKeys.Ecdhe == nil {
		return fmt.Errorf("reality: nil key share keys (keyShareKeys=%v)", keyShareKeys)
	}
	fmt.Printf("[reality] keyShareKeys OK, ecdhe type=%T\n", keyShareKeys.Ecdhe)
	authKey, err := keyShareKeys.Ecdhe.ECDH(serverPub)
	if err != nil {
		return fmt.Errorf("reality: ECDH: %w", err)
	}
	if authKey == nil {
		return fmt.Errorf("reality: nil auth key")
	}
	fmt.Printf("[reality] authKey computed, len=%d\n", len(authKey))
	// 保存 authKey 用于后续证书验证
	c.authKeyVerify = make([]byte, len(authKey))
	copy(c.authKeyVerify, authKey)

	// HKDF 派生 auth_key
	_, err = hkdf.New(sha256.New, authKey, hello.Random[:20], []byte("REALITY")).Read(authKey)
	if err != nil {
		return fmt.Errorf("reality: HKDF: %w", err)
	}

	// AES-GCM 加密 session_id 前 16 字节，注入到 ClientHello
	aesBlock, _ := aes.NewCipher(authKey)
	aesGcm, _ := cipher.NewGCM(aesBlock)
	aesGcm.Seal(hello.SessionId[:0], hello.Random[20:], hello.SessionId[:16], hello.Raw)
	copy(hello.Raw[39:], hello.SessionId)
	fmt.Printf("[reality] session_id encrypted, calling TLS handshake...\n")

	// 执行 TLS 握手
	if err := c.uConn.HandshakeContext(context.Background()); err != nil {
		return fmt.Errorf("reality: handshake: %w", err)
	}
	fmt.Printf("[reality] TLS handshake done, verifying cert...\n")

	// 验证服务端证书
	if err := c.verifyCertificate(); err != nil {
		return fmt.Errorf("reality: verify: %w", err)
	}
	if !c.verified {
		return fmt.Errorf("reality: verification failed")
	}
	fmt.Printf("[reality] verified OK\n")
	return nil
}

// verifyCertificate 验证服务端证书（reality 的 ed25519 签名校验）。
func (c *utlsConn) verifyCertificate() error {
	v := reflect.ValueOf(c.uConn).Elem()
	peerField := v.FieldByName("peerCertificates")
	if !peerField.IsValid() {
		return fmt.Errorf("reality: cannot find peerCertificates field")
	}
	certs := *(*[]*x509.Certificate)(unsafe.Pointer(peerField.UnsafeAddr()))
	if len(certs) == 0 {
		return fmt.Errorf("reality: no peer certificates")
	}
	if pub, ok := certs[0].PublicKey.(ed25519.PublicKey); ok {
		h := hmac.New(sha512.New, c.authKeyVerify)
		h.Write(pub)
		expectedSig := h.Sum(nil)
		if hmac.Equal(expectedSig, certs[0].Signature) {
			c.verified = true
			return nil
		}
	}
	opts := x509.VerifyOptions{DNSName: c.sni, Intermediates: x509.NewCertPool()}
	for _, cert := range certs[1:] {
		opts.Intermediates.AddCert(cert)
	}
	if _, err := certs[0].Verify(opts); err != nil {
		return err
	}
	c.verified = true
	return nil
}

// UConn 返回底层 *utls.UConn（vision 协议需要这个类型）。
func (c *utlsConn) UConn() *utls.UConn {
	return c.uConn
}

// Read/Write/Close 转发到底层 uConn
func (c *utlsConn) Read(b []byte) (int, error)                  { return c.uConn.Read(b) }
func (c *utlsConn) Write(b []byte) (int, error)               { return c.uConn.Write(b) }
func (c *utlsConn) Close() error                              { return c.uConn.Close() }
func (c *utlsConn) LocalAddr() net.Addr                       { return c.raw.LocalAddr() }
func (c *utlsConn) RemoteAddr() net.Addr                      { return c.raw.RemoteAddr() }
func (c *utlsConn) SetDeadline(t time.Time) error            { return c.raw.SetDeadline(t) }
func (c *utlsConn) SetReadDeadline(t time.Time) error        { return c.raw.SetReadDeadline(t) }
func (c *utlsConn) SetWriteDeadline(t time.Time) error       { return c.raw.SetWriteDeadline(t) }
