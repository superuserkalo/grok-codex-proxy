package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func RewriteModel(body []byte) ([]byte, string) {
	start, end, raw, ok := topLevelString(body, "model")
	if !ok {
		return body, ""
	}
	mapped := MapModel(raw)
	if mapped == raw {
		return body, mapped
	}
	quoted, err := json.Marshal(mapped)
	if err != nil {
		return body, mapped
	}
	out := make([]byte, 0, len(body)+len(quoted)-(end-start))
	out = append(out, body[:start]...)
	out = append(out, quoted...)
	out = append(out, body[end:]...)
	return out, mapped
}

func topLevelString(body []byte, field string) (start, end int, raw string, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	t, err := dec.Token()
	if err != nil {
		return 0, 0, "", false
	}
	d, isDelim := t.(json.Delim)
	if !isDelim || d != '{' {
		return 0, 0, "", false
	}
	found := false
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return 0, 0, "", false
		}
		key, isStr := t.(string)
		if !isStr {
			return 0, 0, "", false
		}
		afterKey := dec.InputOffset()
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return 0, 0, "", false
		}
		afterVal := dec.InputOffset()
		if key != field {
			continue
		}
		var s string
		if err := json.Unmarshal(val, &s); err != nil || s == "" {
			continue
		}
		rel := bytes.Index(body[afterKey:afterVal], val)
		if rel < 0 {
			return 0, 0, "", false
		}
		start = int(afterKey) + rel
		end = start + len(val)
		raw = s
		found = true
	}
	return start, end, raw, found
}

func CopyStream(dst http.ResponseWriter, src io.Reader) {
	buf := make([]byte, 32*1024)
	fl, _ := dst.(http.Flusher)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			_, _ = dst.Write(buf[:n])
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func JoinURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}
