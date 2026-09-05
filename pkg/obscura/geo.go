package obscura

import (
	"encoding/json"
	"io"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// proxyTimezoneCache: proxy URL → IANA timezone of its exit IP. One lookup
// per proxy per process; obscura reads it at startup (TZ env) so Date and
// Intl both report the exit region.
var (
	tzMu    sync.Mutex
	tzCache = map[string]interface{}{}
)

type GeoInfo struct {
	Timezone string
	Locale   string // BCP47 primary tag, e.g. zh-CN
	Langs    []string
}

// ResolveProxyGeo returns the exit IP's timezone and locale, querying
// ip-api.com through the proxy itself (Camoufox's geoip flow). The IANA
// zone's second segment is a CITY, not a country code, so the locale maps
// from ip-api's countryCode instead of string-parsing the zone name.
// Falls back on any failure. Never blocks longer than ~12s.
func ResolveProxyGeo(proxy, fallbackTZ string) GeoInfo {
	if strings.TrimSpace(proxy) == "" {
		return GeoInfo{Timezone: fallbackTZ, Locale: "en-US", Langs: []string{"en-US", "en"}}
	}
	tzMu.Lock()
	defer tzMu.Unlock()
	if tz, ok := tzCache[proxy]; ok {
		return tz.(GeoInfo)
	}
	g := fetchGeoViaProxy(proxy, fallbackTZ)
	tzCache[proxy] = g
	return g
}

// countryLocale maps the exit country to a plausible browser locale.
func countryLocale(cc string) (string, []string) {
	switch strings.ToUpper(cc) {
	case "CN":
		return "zh-CN", []string{"zh-CN", "zh"}
	case "HK":
		return "zh-HK", []string{"zh-HK", "zh"}
	case "TW":
		return "zh-TW", []string{"zh-TW", "zh"}
	case "JP":
		return "ja-JP", []string{"ja-JP", "ja"}
	case "KR":
		return "ko-KR", []string{"ko-KR", "ko"}
	case "SG":
		return "en-SG", []string{"en-SG", "en"}
	case "GB":
		return "en-GB", []string{"en-GB", "en"}
	case "DE":
		return "de-DE", []string{"de-DE", "de"}
	case "FR":
		return "fr-FR", []string{"fr-FR", "fr"}
	case "RU":
		return "ru-RU", []string{"ru-RU", "ru"}
	default:
		return "en-US", []string{"en-US", "en"}
	}
}

func fetchGeoViaProxy(proxy, fallbackTZ string) GeoInfo {
	pu, err := url.Parse(proxy)
	if err != nil {
		return GeoInfo{Timezone: fallbackTZ, Locale: "en-US", Langs: []string{"en-US", "en"}}
	}
	client := &http.Client{
		Timeout: 6 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(pu),
		},
	}
	req, err := http.NewRequest("GET", "http://ip-api.com/json?fields=timezone,countryCode", nil)
	if err != nil {
		return GeoInfo{Timezone: fallbackTZ, Locale: "en-US", Langs: []string{"en-US", "en"}}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		// Plain-HTTP fallback (ip-api.com is http-only on the free tier) —
		// some tunnels refuse port 80, retry https endpoints. ipinfo.io is
		// field-compatible for our needs (country, timezone).
		for _, u := range []string{"https://ipwho.is/", "https://ipinfo.io/json"} {
			req2, err2 := http.NewRequest("GET", u, nil)
			if err2 != nil {
				continue
			}
			req2.Header.Set("User-Agent", req.Header.Get("User-Agent"))
			resp2, err2 := client.Do(req2)
			if err2 != nil {
				fmt.Printf("[obscura] geo fallback %s failed: %v\n", u, err2)
				continue
			}
			return parseGeoBody(resp2, fallbackTZ)
		}
	}
	if err != nil {
		return GeoInfo{Timezone: fallbackTZ, Locale: "en-US", Langs: []string{"en-US", "en"}}
	}
	defer resp.Body.Close()
	return parseGeoBody(resp, fallbackTZ)
}

func parseGeoBody(resp *http.Response, fallbackTZ string) GeoInfo {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var payload struct {
		Timezone    string `json:"timezone"`
		CountryCode string `json:"countryCode"`
		Country     string `json:"country_code"`
		IPInfoCC    string `json:"country"` // ipinfo.io
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return GeoInfo{Timezone: fallbackTZ, Locale: "en-US", Langs: []string{"en-US", "en"}}
	}
	cc := payload.CountryCode
	if cc == "" {
		cc = payload.Country
	}
	if cc == "" {
		cc = payload.IPInfoCC
	}
	tz := strings.TrimSpace(payload.Timezone)
	if tz == "" || !validIANATimezone(tz) {
		return GeoInfo{Timezone: fallbackTZ, Locale: "en-US", Langs: []string{"en-US", "en"}}
	}
	locale, langs := countryLocale(cc)
	fmt.Printf("[obscura] proxy exit geo: tz=%s cc=%s locale=%s\n", tz, cc, locale)
	return GeoInfo{Timezone: tz, Locale: locale, Langs: langs}
}

func validIANATimezone(tz string) bool {
	_, err := time.LoadLocation(tz)
	return err == nil
}
