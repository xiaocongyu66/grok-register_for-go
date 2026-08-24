package curlcffi

// ja3_profile.go — 用 tls-client 的 NewClientProfile + GetSpecFactoryFromJa3String
// 实现自定义 JA3 指纹，同时保留 tls-client 的 HTTP/2 支持

import (
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	utls "github.com/bogdanfinn/utls"
	http2 "github.com/bogdanfinn/fhttp/http2"
)

// ProfileFromJA3 根据自定义 JA3 字符串创建 tls-client ClientProfile
// 使用 tls-client 内置的 GetSpecFactoryFromJa3String
func ProfileFromJA3(ja3 string) (profiles.ClientProfile, error) {
	specFactory, err := tls_client.GetSpecFactoryFromJa3String(
		ja3,
		// 默认签名算法
		[]string{"ecdsa_secp256r1_sha256", "rsa_pss_rsae_sha256", "rsa_pkcs1_sha256",
			"ecdsa_secp384r1_sha384", "rsa_pss_rsae_sha384", "rsa_pkcs1_sha384",
			"rsa_pss_rsae_sha512", "rsa_pkcs1_sha512"},
		// 委托凭证算法
		[]string{},
		// 支持的 TLS 版本
		[]string{"0x0304", "0x0303"},
		// key share curves
		[]string{"GREASE", "x25519"},
		// ALPN
		[]string{"h2", "http/1.1"},
		// ALPS
		[]string{},
		// ECH cipher suites
		nil,
		// candidate payloads
		nil,
		// cert compression
		[]string{"brotli"},
		// record size limit
		16385,
	)
	if err != nil {
		return profiles.ClientProfile{}, err
	}

	// 创建自定义 ClientHelloID
	// 用 HelloChrome_Auto 作为壳（utls 需要已知 ID），但 SpecFactory 会覆盖实际 spec
	helloID := utls.ClientHelloID{
		Client:      utls.HelloChrome_Auto.Client,
		Version:     utls.HelloChrome_Auto.Version,
		SpecFactory: specFactory,
	}

	// 用 Chrome 131 的 HTTP/2 设置（Akamai 指纹）
	profile := profiles.NewClientProfile(
		helloID,
		// HTTP/2 settings（Chrome 默认）
		map[http2.SettingID]uint32{
			http2.SettingHeaderTableSize:      65536,
			http2.SettingEnablePush:           0,
			http2.SettingMaxConcurrentStreams:  1000,
			http2.SettingInitialWindowSize:    6291456,
			http2.SettingMaxHeaderListSize:    262144,
		},
		// settings order
		[]http2.SettingID{
			http2.SettingHeaderTableSize,
			http2.SettingEnablePush,
			http2.SettingMaxConcurrentStreams,
			http2.SettingInitialWindowSize,
			http2.SettingMaxHeaderListSize,
		},
		// pseudo header order
		[]string{":method", ":authority", ":scheme", ":path"},
		// connection flow
		15663105,
		// priorities
		nil,
		// header priority
		nil,
	)

	return profile, nil
}

// ProfileFromJA3AndAkamai 根据自定义 JA3 + Akamai 创建 ClientProfile
func ProfileFromJA3AndAkamai(ja3, akamai string) (profiles.ClientProfile, error) {
	profile, err := ProfileFromJA3(ja3)
	if err != nil {
		return profiles.ClientProfile{}, err
	}

	// 如果有 Akamai 指纹，解析并覆盖 HTTP/2 设置
	if akamai != "" {
		fp, err := ParseAkamai(akamai)
		if err == nil && fp != nil {
			// 用 Akamai 的 settings 覆盖
			settings := make(map[http2.SettingID]uint32)
			var order []http2.SettingID
			for _, s := range fp.Settings {
				settings[http2.SettingID(s.ID)] = s.Value
				order = append(order, http2.SettingID(s.ID))
			}
			if len(order) == 0 {
				order = profile.GetSettingsOrder()
				settings = profile.GetSettings()
			}
			profile = profiles.NewClientProfile(
				profile.GetClientHelloId(),
				settings,
				order,
				fp.HeaderOrder,
				fp.WindowUpdate,
				toPriorities(fp.Priority),
				nil,
			)
		}
	}

	return profile, nil
}

func toPriorities(ps []H2Priority) []http2.Priority {
	var result []http2.Priority
	for _, p := range ps {
		result = append(result, http2.Priority{
			StreamID: p.StreamID,
			PriorityParam: http2.PriorityParam{
				Weight: p.Weight,
			},
		})
	}
	return result
}

// WithJA3Profile 在 Session 上设置自定义 JA3 profile
func WithJA3Profile(ja3 string) func(*Session) {
	return func(s *Session) {
		s.ja3Profile = ja3
	}
}

// WithJA3AkamaiProfile 同时设置 JA3 + Akamai
func WithJA3AkamaiProfile(ja3, akamai string) func(*Session) {
	return func(s *Session) {
		s.ja3Profile = ja3
		s.akamaiProfile = akamai
	}
}
