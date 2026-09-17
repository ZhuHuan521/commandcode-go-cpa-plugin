package plugin

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func decodeObject(data []byte) map[string]any {
	var out map[string]any
	if len(data) == 0 || json.Unmarshal(data, &out) != nil {
		return map[string]any{}
	}
	return out
}

func encodeJSON(v any) []byte {
	out, _ := json.Marshal(v)
	return out
}

func mustJSON(v any) string {
	return string(encodeJSON(v))
}

func cloneMap(v map[string]any) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	raw := encodeJSON(v)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func asSlice(v any) []any {
	if list, ok := v.([]any); ok {
		return list
	}
	return nil
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return ""
	default:
		return strings.TrimSpace(mustJSON(x))
	}
}

func asNumber(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	default:
		return 0
	}
}

func asBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true") || x == "1"
	case float64:
		return x != 0
	default:
		return false
	}
}

func trimSpaceBytes(b []byte) []byte { return bytes.TrimSpace(b) }

func timeNowUnix() int64 {
	return time.Now().Unix()
}
