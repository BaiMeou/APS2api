// Copyright (c) 2026 BaiMeow. All rights reserved.
// Use of this source code is governed by the PolyForm Noncommercial License 1.0.0
// that can be found in the LICENSE file.

package api

import (
	"net/http"
	"strings"

	"github.com/bsfdsagfadg/vertex/internal/config"
)

var adminAllowedSettings = map[string]bool{
	"max_retries": true, "token_pool_size": true, "max_spill_mb": true,
	"max_request_mb": true, "max_n": true, "anti429_enabled": true,
	"anti429_target": true, "force_no_stream": true, "anti_tracking": true,
	"drop_max_tokens": true, "proxy_url": true, "admin_password": true,
	"parallel_pool_enabled": true, "parallel_pool_size": true,
	"telemetry_enabled":           true,
	"parallel_pool_delay_dynamic": true,
	"parallel_pool_delay_ms":      true,
	"recaptcha_expire_seconds":    true,
	"active_node_uri":             true,
	"sticky_pool_enabled":         true,
	"background_image":            true,
	"font_size":                   true,
	"font_color_type":             true,
	"font_color":                  true,
	"custom_bg_presets":           true,
}

func (adm *AdminHandler) adminGetSettings(w http.ResponseWriter, _ *http.Request) {
	telEnabled := true
	if adm.cfg.GetTelemetryEnabled() != nil {
		telEnabled = *adm.cfg.GetTelemetryEnabled()
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": map[string]any{
		"max_retries":       adm.cfg.GetMaxRetries(),
		"token_pool_size":   adm.cfg.GetTokenPoolSize(),
		"max_spill_mb":      adm.cfg.GetMaxSpillMB(),
		"max_request_mb":    adm.cfg.GetMaxRequestMB(),
		"max_n":             adm.cfg.GetMaxN(),
		"anti429_enabled":   adm.cfg.GetAnti429Enabled(),
		"anti429_target":    adm.cfg.GetAnti429Target(),
		"force_no_stream":   adm.cfg.GetForceNoStream(),
		"anti_tracking":     adm.cfg.GetAntiTracking(),
		"drop_max_tokens":   adm.cfg.GetDropMaxTokens(),
		"telemetry_enabled": telEnabled,
		"proxy_url":         adm.cfg.GetProxyURL(), "parallel_pool_enabled": adm.cfg.GetParallelPoolEnabled(), "parallel_pool_size": adm.cfg.GetParallelPoolSize(), "active_node_uri": adm.cfg.GetActiveNodeURI(),
		"parallel_pool_delay_dynamic": adm.cfg.GetParallelPoolDelayDynamic(),
		"parallel_pool_delay_ms":      adm.cfg.GetParallelPoolDelayMs(),
		"recaptcha_expire_seconds":    adm.cfg.GetRecaptchaExpireSeconds(),
		"sticky_pool_enabled":         adm.cfg.GetStickyPoolEnabled(),
		"background_image":            adm.cfg.GetBackgroundImage(),
		"font_size":                   adm.cfg.GetFontSize(),
		"font_color_type":             adm.cfg.GetFontColorType(),
		"font_color":                  adm.cfg.GetFontColor(),
		"custom_bg_presets":           adm.cfg.GetCustomBgPresets(),
	}})
}

func (adm *AdminHandler) adminPutSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Settings map[string]any `json:"settings"`
	}
	if !adm.decodeAdminBody(w, r, &body) {
		return
	}
	updates := map[string]any{}

	// 面板依赖校验：禁用并发池时强制禁用粘性池
	if ppEnabled, ok := body.Settings["parallel_pool_enabled"].(bool); ok && !ppEnabled {
		body.Settings["sticky_pool_enabled"] = false
	}

	for k, v := range body.Settings {
		if !adminAllowedSettings[k] {
			continue
		}
		switch k {
		case "max_retries", "token_pool_size", "max_spill_mb", "max_request_mb", "max_n", "parallel_pool_size", "parallel_pool_delay_ms", "recaptcha_expire_seconds":
			if f, ok := v.(float64); ok {
				updates[k] = int(f)
				continue
			}
		case "admin_password":
			if pw, ok := v.(string); !ok || strings.TrimSpace(pw) == "" {
				continue
			} else {
				updates[k] = strings.TrimSpace(pw)
				continue
			}
		}
		updates[k] = v
	}
	if err := config.WriteSettings(updates); err != nil {
		writeJSON(w, http.StatusInternalServerError, adminErr("写入配置失败 (failed to write config)"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (adm *AdminHandler) adminGetStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, metricsBody())
}

func (adm *AdminHandler) adminResetStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
