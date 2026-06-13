package transport

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func padB64(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "-", "+"), "_", "/")
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	return s
}

func ParseURI(uri string) (map[string]any, error) {
	if strings.HasPrefix(uri, "vless://") {
		return parseSimple(uri, "vless")
	}
	if strings.HasPrefix(uri, "trojan://") {
		return parseSimple(uri, "trojan")
	}
	if strings.HasPrefix(uri, "vmess://") {
		return parseVmess(uri)
	}
	if strings.HasPrefix(uri, "ss://") {
		return parseSS(uri)
	}
	if strings.HasPrefix(uri, "hysteria2://") || strings.HasPrefix(uri, "hy2://") {
		return parseSimple(uri, "hysteria2")
	}
	if strings.HasPrefix(uri, "tuic://") {
		return parseSimple(uri, "tuic")
	}
	if strings.HasPrefix(uri, "clash://") {
		b, _ := base64.StdEncoding.DecodeString(padB64(uri[8:]))
		var d map[string]any
		json.Unmarshal(b, &d)
		return d, nil
	}
	safeURI := uri
	if len(safeURI) > 10 {
		safeURI = safeURI[:10]
	}
	return nil, fmt.Errorf("unsupported or complex protocol: %s", safeURI)
}

func parseSimple(uri, typ string) (map[string]any, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	port, _ := strconv.Atoi(u.Port())
	if port == 0 {
		port = 443
	}
	q := u.Query()
	out := map[string]any{"type": typ, "server": u.Hostname(), "server_port": port}
	if typ == "trojan" || typ == "hysteria2" {
		out["password"] = u.User.Username()
	} else {
		out["uuid"] = u.User.Username()
	}

	sec := strings.ToLower(q.Get("security"))
	if sec == "tls" || sec == "reality" || typ == "trojan" || typ == "hysteria2" || typ == "tuic" {
		tls := map[string]any{"enabled": true, "server_name": u.Hostname()}
		if sni := q.Get("sni"); sni != "" {
			tls["server_name"] = sni
		}
		if q.Get("allowInsecure") == "1" {
			tls["insecure"] = true
		}
		out["tls"] = tls
	}
	return out, nil
}

func parseVmess(uri string) (map[string]any, error) {
	b, _ := base64.StdEncoding.DecodeString(padB64(uri[8:]))
	var d map[string]any
	json.Unmarshal(b, &d)
	portStr := fmt.Sprintf("%v", d["port"])
	port, _ := strconv.Atoi(portStr)
	out := map[string]any{"type": "vmess", "server": d["add"], "server_port": port, "uuid": d["id"]}
	return out, nil
}

func parseSS(uri string) (map[string]any, error) {
	body := uri[5:]
	if idx := strings.Index(body, "#"); idx != -1 {
		body = body[:idx]
	}
	if idx := strings.Index(body, "@"); idx != -1 {
		b, _ := base64.StdEncoding.DecodeString(padB64(body[:idx]))
		parts := strings.SplitN(string(b), ":", 2)
		hp := strings.Split(body[idx+1:], ":")
		port, _ := strconv.Atoi(hp[1])
		return map[string]any{"type": "shadowsocks", "server": hp[0], "server_port": port, "method": parts[0], "password": parts[1]}, nil
	}
	return nil, fmt.Errorf("ss parse failed")
}
