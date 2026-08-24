package curlcffi

// headers_extra.go — 补齐 Headers 的 copy/items/keys/raw 方法
// Go 用 http.Header (map[string][]string) 作为 Headers 类型

import (
	http "github.com/bogdanfinn/fhttp"
)

// HeadersType 是 Headers 的类型别名（兼容 Python 的 Headers 类）
type HeadersType = http.Header

// NewHeaders 创建 Headers
func NewHeaders() HeadersType {
	return make(http.Header)
}

// CopyHeaders 复制 Headers
func CopyHeaders(h http.Header) http.Header {
	result := make(http.Header)
	for k, vs := range h {
		copyVals := make([]string, len(vs))
		copy(copyVals, vs)
		result[k] = copyVals
	}
	return result
}

// HeadersItems 返回 headers 的 (key, value) 列表（取每个 key 的第一个值）
func HeadersItems(h http.Header) [][2]string {
	var items [][2]string
	for k, vs := range h {
		if len(vs) > 0 {
			items = append(items, [2]string{k, vs[0]})
		}
	}
	return items
}

// HeadersKeys 返回所有 header key
func HeadersKeys(h http.Header) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	return keys
}

// HeadersValues 返回所有 header value（取每个 key 的第一个值）
func HeadersValues(h http.Header) []string {
	values := make([]string, 0, len(h))
	for _, vs := range h {
		if len(vs) > 0 {
			values = append(values, vs[0])
		}
	}
	return values
}

// HeadersMultiItems 返回 headers 的 (key, value) 列表（包含重复值）
func HeadersMultiItems(h http.Header) [][2]string {
	var items [][2]string
	for k, vs := range h {
		for _, v := range vs {
			items = append(items, [2]string{k, v})
		}
	}
	return items
}

// HeadersGetList 返回指定 key 的所有值
func HeadersGetList(h http.Header, key string) []string {
	return h.Values(key)
}

// HeadersRaw 返回 raw headers（map[string][]string）
func HeadersRaw(h http.Header) map[string][]string {
	return h
}

// HeadersUpdate 用另一个 Headers 更新
func HeadersUpdate(target, source http.Header) {
	for k, vs := range source {
		for _, v := range vs {
			target.Add(k, v)
		}
	}
}

// HeadersContains 检查是否包含某个 key
func HeadersContains(h http.Header, key string) bool {
	_, ok := h[http.CanonicalHeaderKey(key)]
	return ok
}
