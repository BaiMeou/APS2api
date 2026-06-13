package api

import (
	"encoding/base64"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bsfdsagfadg/vertex/internal/config"
	"github.com/bsfdsagfadg/vertex/internal/nodes"
	"github.com/bsfdsagfadg/vertex/internal/transport"
)

func (s *Server) adminGetNodes(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes.LoadNodes(), "health": nodes.LoadHealth()})
}

func (s *Server) adminFetchSub(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if !s.decodeAdminBody(w, r, &body) {
		return
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	req, err := http.NewRequestWithContext(r.Context(), "GET", body.URL, nil)
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, adminErr("创建请求失败: "+err.Error()))
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, adminErr("拉取失败: "+err.Error()))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.writeJSON(w, http.StatusBadRequest, adminErr("拉取失败: 服务器返回状态码 "+strconv.Itoa(resp.StatusCode)))
		return
	}
	data, _ := io.ReadAll(resp.Body)
	text := strings.TrimSpace(string(data))
	if b, err := decodeSubBase64(text); err == nil {
		text = string(b)
	}

	var newNodes []nodes.Node
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if out, err := transport.ParseURI(line); err == nil {
			t, _ := out["type"].(string)
			newNodes = append(newNodes, nodes.Node{Type: t, Name: line[:min(len(line), 40)], RawURI: line})
		}
	}
	nodes.MergeNodes(newNodes)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(newNodes)})
}

func (s *Server) adminTestAll(w http.ResponseWriter, r *http.Request) {
	go func() {
		list := nodes.LoadNodes()
		for _, n := range list {
			if n.Disabled {
				continue
			}
			start := time.Now()
			sess, err := s.vc.Net().CreateSession(10, n.RawURI)
			if err == nil {
				_, _, err = sess.DoAndRead(r.Context(), "GET", "https://www.google.com/generate_204", nil, nil)
				sess.Close()
			}
			nodes.RecordTest(n.RawURI, err == nil, float64(time.Since(start).Milliseconds()), errToStr(err))
		}
	}()
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminDedupNodes(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed_count": nodes.DedupNodes()})
}

func (s *Server) adminDeleteDisabledNodes(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted_count": nodes.DeleteDisabled()})
}

func (s *Server) adminUseNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RawURI string `json:"raw_uri"`
	}
	if !s.decodeAdminBody(w, r, &body) {
		return
	}
	config.WriteSettings(map[string]any{"active_node_uri": body.RawURI, "parallel_pool_enabled": false})
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminDeleteNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RawURI string `json:"raw_uri"`
	}
	if !s.decodeAdminBody(w, r, &body) {
		return
	}
	nodes.DeleteNode(body.RawURI)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func errToStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}


// decodeSubBase64 宽容解码订阅的 Base64 文本，兼容各种换行、空格及 URL 安全格式
func decodeSubBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, " ", "")
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	t := strings.ReplaceAll(strings.ReplaceAll(s, "-", "+"), "_", "/")
	if pad := len(t) % 4; pad != 0 {
		t += strings.Repeat("=", 4-pad)
	}
	return base64.StdEncoding.DecodeString(t)
}
