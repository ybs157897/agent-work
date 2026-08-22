package sqlstore

import (
	"encoding/json"
	"time"
)

func timeNow() time.Time { return time.Now().UTC() }

// jsonText 序列化为 JSON 文本参数；两种方言统一存 TEXT/JSONB。
func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// jsonInto 反序列化 JSON 文本/字节列。
func jsonInto(raw any, dst any) error {
	switch v := raw.(type) {
	case nil:
		return nil
	case []byte:
		return json.Unmarshal(v, dst)
	case string:
		return json.Unmarshal([]byte(v), dst)
	}
	return nil
}

// nullString 把空字符串映射为 SQL NULL。
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
