package curlcffi

// json_helpers.go — JSON 辅助函数

import "encoding/json"

func jsonMarshalImpl(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func jsonUnmarshalImpl(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
