# go-curlcffi

Go implementation of curl_cffi (Python) — TLS fingerprint impersonation library.

100% compatible with Python curl_cffi + CF-Ares API:
- Session / Response / Request
- JA3 / Akamai / Perk fingerprint
- WebSocket
- Cache / Connection info
- Exception types

## Usage

```go
import "grok/pkg/curlcffi"

s, _ := curlcffi.NewSession(
    curlcffi.WithImpersonate(curlcffi.Chrome131),
    curlcffi.WithProxy("http://127.0.0.1:8080"),
)
defer s.Close()

r, _ := s.Get("https://example.com")
fmt.Println(r.StatusCode)
```
