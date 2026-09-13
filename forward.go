package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func rewriteModel(body []byte) ([]byte, string) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, ""
	}
	raw, _ := m["model"].(string)
	if raw == "" {
		return body, ""
	}
	mapped := MapModel(raw)
	m["model"] = mapped
	out, err := json.Marshal(m)
	if err != nil {
		return body, mapped
	}
	return out, mapped
}

func copyStream(dst http.ResponseWriter, src io.Reader) {
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

func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + path
}
