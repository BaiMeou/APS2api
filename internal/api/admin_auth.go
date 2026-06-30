// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package api

import (
	cryptorand "crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bsfdsagfadg/vertex/internal/config"
)

const (
	adminCookieName = "admin_token"
	adminSessionTTL = 24 * time.Hour
)

var (
	adminSessionsMu sync.Mutex
	adminSessions   = map[string]time.Time{}
)

func issueAdminToken() string {
	b := make([]byte, 32)
	_, _ = cryptorand.Read(b)
	tok := hex.EncodeToString(b)
	adminSessionsMu.Lock()
	adminSessions[tok] = time.Now().Add(adminSessionTTL)
	adminSessionsMu.Unlock()
	return tok
}

func checkAdminToken(tok string) bool {
	if tok == "" {
		return false
	}
	adminSessionsMu.Lock()
	defer adminSessionsMu.Unlock()
	exp, ok := adminSessions[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(adminSessions, tok)
		return false
	}
	return true
}

func dropAdminToken(tok string) {
	if tok == "" {
		return
	}
	adminSessionsMu.Lock()
	delete(adminSessions, tok)
	adminSessionsMu.Unlock()
}

func cleanupAdminSessions() int {
	now := time.Now()
	adminSessionsMu.Lock()
	defer adminSessionsMu.Unlock()
	n := 0
	for tok, exp := range adminSessions {
		if now.After(exp) {
			delete(adminSessions, tok)
			n++
		}
	}
	return n
}

func adminTokenFromRequest(r *http.Request) string {
	if c, err := r.Cookie(adminCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

func requireAdmin(r *http.Request) bool {
	return checkAdminToken(adminTokenFromRequest(r))
}

func StartAdminSessionCleanup(interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			if n := cleanupAdminSessions(); n > 0 {
				log.Printf("[Admin] 已清理 %d 个过期会话 token", n)
			}
		}
	}()
}

func EnsureAdminPassword() {
	if strings.TrimSpace(config.Load().AdminPassword) != "" {
		return
	}
	b := make([]byte, 9)
	if _, err := cryptorand.Read(b); err != nil {
		log.Printf("[Admin] 生成管理员密码失败：%v", err)
		return
	}
	pw := base64.RawURLEncoding.EncodeToString(b)
	if err := config.WriteSettings(map[string]any{"admin_password": pw}); err != nil {
		log.Printf("[Admin] 写入管理员密码到 config.json 失败：%v", err)
		return
	}
	bar := strings.Repeat("=", 60)
	log.Printf("%s", bar)
	log.Printf("[Admin] 首次启动，已自动生成管理员密码：")
	log.Printf("[Admin]     密码: %s", pw)
	log.Printf("[Admin]     访问: http://<host>:<port>/admin")
	log.Printf("[Admin]     密码已写入 config/config.json，登录后可在面板修改")
	log.Printf("%s", bar)
}

func (adm *AdminHandler) adminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		adm.adminMethodNotAllowed(w)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !adm.decodeAdminBody(w, r, &body) {
		return
	}
	expected := strings.TrimSpace(config.Load().AdminPassword)
	if expected == "" {
		writeJSON(w, http.StatusInternalServerError, adminErr("管理员密码未初始化 (admin password not set)"))
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(body.Password)), []byte(expected)) != 1 {
		log.Printf("[Security] 警告：后台登录失败，密码错误。来源 IP: %s", r.RemoteAddr)
		writeJSON(w, http.StatusUnauthorized, adminErr("密码错误 (invalid password)"))
		return
	}
	log.Printf("[Admin] 管理后台登录成功。来源 IP: %s", r.RemoteAddr)
	cleanupAdminSessions()
	tok := issueAdminToken()
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		MaxAge:   int(adminSessionTTL / time.Second),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (adm *AdminHandler) adminLogout(w http.ResponseWriter, r *http.Request) {
	dropAdminToken(adminTokenFromRequest(r))
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (adm *AdminHandler) adminCheckAuth(w http.ResponseWriter, r *http.Request) {
	authenticated := requireAdmin(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated":     authenticated,
		"background_image":  adm.cfg.GetBackgroundImage(),
		"font_size":         adm.cfg.GetFontSize(),
		"font_color_type":   adm.cfg.GetFontColorType(),
		"font_color":        adm.cfg.GetFontColor(),
		"custom_bg_presets": adm.cfg.GetCustomBgPresets(),
	})
}
