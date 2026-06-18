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

// ParseURI 解析各种协议的节点链接
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
	b64Str := uri[8:]
	if idx := strings.Index(b64Str, "?"); idx != -1 {
		b64Str = b64Str[:idx]
	}
	if idx := strings.Index(b64Str, "#"); idx != -1 {
		b64Str = b64Str[:idx]
	}
	b, err := base64.StdEncoding.DecodeString(padB64(b64Str))
	if err != nil {
		return nil, err
	}
	var d map[string]any
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	portStr := fmt.Sprintf("%v", d["port"])
	port, _ := strconv.Atoi(portStr)
	
	// 初始化 VMess 出站基本参数
	out := map[string]any{
		"type":        "vmess",
		"server":      d["add"],
		"server_port": port,
		"uuid":        d["id"],
		"security":    "auto",
	}

	// 1. 映射 alterId (aid)
	if aidVal, ok := d["aid"]; ok {
		switch v := aidVal.(type) {
		case float64:
			out["alter_id"] = int(v)
		case int:
			out["alter_id"] = v
		case string:
			if n, err := strconv.Atoi(v); err == nil {
				out["alter_id"] = n
			}
		}
	}

	// 2. 补全 TLS 配置（极关键，修复免费-日本1等节点的 TLS 缺失）
	tlsStr, _ := d["tls"].(string)
	if strings.ToLower(tlsStr) == "tls" {
		host, _ := d["host"].(string)
		sni := host
		if sni == "" {
			sni, _ = d["add"].(string)
		}
		out["tls"] = map[string]any{
			"enabled":     true,
			"server_name": sni,
		}
	}

	// 3. 补全 V2Ray 传输层配置（WS / gRPC，修复 IEPL 等节点的 WS 缺失）
	netType, _ := d["net"].(string)
	netType = strings.ToLower(strings.TrimSpace(netType))
	if netType != "" && netType != "tcp" {
		path, _ := d["path"].(string)
		host, _ := d["host"].(string)
		
		transportCfg := map[string]any{
			"type": netType,
		}
		
		switch netType {
		case "ws":
			transportCfg["path"] = path
			if host != "" {
				transportCfg["headers"] = map[string]any{
					"Host": host,
				}
			}
		case "grpc":
			transportCfg["service_name"] = path // gRPC 模式下 path 常代表 serviceName
		}
		
		out["transport"] = transportCfg
	}

	return out, nil
}

func parseSS(uri string) (map[string]any, error) {
	body := uri[5:]
	if idx := strings.Index(body, "#"); idx != -1 {
		body = body[:idx]
	}
	if idx := strings.Index(body, "@"); idx != -1 {
		userInfo := body[:idx]
		hp := strings.Split(body[idx+1:], ":")
		if len(hp) < 2 {
			return nil, fmt.Errorf("ss parse failed: invalid host:port")
		}
		port, _ := strconv.Atoi(hp[1])

		var method, password string

		// 适配两种形式的 Shadowsocks Base64 用户信息表达
		if colonIdx := strings.Index(userInfo, ":"); colonIdx != -1 {
			// 形式 A: base64(method) : base64(password)
			mBytes, errM := base64.StdEncoding.DecodeString(padB64(userInfo[:colonIdx]))
			pBytes, errP := base64.StdEncoding.DecodeString(padB64(userInfo[colonIdx+1:]))
			if errM == nil && errP == nil {
				method = string(mBytes)
				password = string(pBytes)
			}
		}

		if method == "" || password == "" {
			// 形式 B: 传统的整个 method:password 一起进行 base64 编码
			b, err := base64.StdEncoding.DecodeString(padB64(userInfo))
			if err == nil {
				parts := strings.SplitN(string(b), ":", 2)
				if len(parts) == 2 {
					method = parts[0]
					password = parts[1]
				}
			}
		}

		if method == "" || password == "" {
			return nil, fmt.Errorf("ss parse failed: invalid userinfo (cannot decode method or password)")
		}

		return map[string]any{
			"type":        "shadowsocks",
			"server":      hp[0],
			"server_port": port,
			"method":      method,
			"password":    password,
		}, nil
	}
	return nil, fmt.Errorf("ss parse failed")
}