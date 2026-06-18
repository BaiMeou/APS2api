// Package recaptcha 实现 Google reCAPTCHA Enterprise 匿名 token 的现抓现用。
//
// 流程：anchor iframe GET 抠出 base token，再 reload POST
// 拿到最终 token（rresp）。token 用于 batchGraphql 的 recaptchaToken 字段。
package recaptcha

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"regexp"
	"strings"

	"github.com/bsfdsagfadg/vertex/internal/transport"
)

// recaptcha 相关硬编码常量（逐字节保持既定常量）。
const (
	recaptchaBase = "https://www.google.com"
	siteKey       = "6LdCjtspAAAAAMcV4TGdWLJqRTEk1TfpdLqEnKdj"
	recaptchaCo   = "aHR0cHM6Ly9jb25zb2xlLmNsb3VkLmdvb2dsZS5jb206NDQz"
	recaptchaHl   = "zh-CN"
	recaptchaV    = "jdMmXeCQEkPbnFDy9T04NbgJ"
	recaptchaVh   = "6581054572"
	randomCharset = "abcdefghijklmnopqrstuvwxyz0123456789"
)

var (
	// 从 anchor HTML 抠 base token。用正则而非 HTML 解析器（已实测可行、无需额外依赖）。
	tokenRe = regexp.MustCompile(`id="recaptcha-token"[^>]*value="([^"]+)"`)
	// 从 reload 响应抠最终 token。
	rrespRe = regexp.MustCompile(`rresp","(.*?)"`)
)

func randomString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = randomCharset[rand.Intn(len(randomCharset))]
	}
	return string(b)
}

// FetchRecaptchaToken 获取 Google reCAPTCHA token（隔离特征）。
//
// 最多 3 次重试，每次新建一个短超时 Session
// （即用即毁，FRESH_CONNECT 语义）。全部失败返回 ("", nil) —— 返回空值表示失败，
// 调用方按“空则换新/重试”处理。返回非空字符串即成功。
func FetchRecaptchaToken(net *transport.NetworkClient, proxyURI string) (string, error) {
	for retry := 0; retry < 3; retry++ {
		log.Printf("[Recaptcha] 开始获取 reCAPTCHA token (尝试 %d/3)", retry+1)
		if token, ok := fetchOnce(net, proxyURI); ok {
			log.Printf("[Recaptcha] 成功获取 reCAPTCHA token")
			return token, nil
		}
	}
	log.Printf("[Recaptcha] 3次尝试后获取 reCAPTCHA token 失败")
	return "", nil
}

func fetchOnce(net *transport.NetworkClient, proxyURI string) (string, bool) {
	sess, err := net.CreateSession(15, proxyURI)
	if err != nil {
		return "", false
	}
	defer sess.Close()

	cb := randomString(10)
	anchorURL := fmt.Sprintf(
		"%s/recaptcha/enterprise/anchor?ar=1&k=%s&co=%s&hl=%s&v=%s&size=invisible&anchor-ms=20000&execute-ms=15000&cb=%s",
		recaptchaBase, siteKey, recaptchaCo, recaptchaHl, recaptchaV, cb,
	)

	// token 预取与具体请求无关（后台细流），故用 context.Background()，不随某个请求取消。
	_, anchorBody, err := sess.DoAndRead(context.Background(), "GET", anchorURL, transport.AnchorHeaders(), nil)
	if err != nil {
		return "", false
	}
	m := tokenRe.FindSubmatch(anchorBody)
	if m == nil {
		return "", false
	}
	baseToken := string(m[1])

	form := url.Values{
		"v":      {recaptchaV},
		"reason": {"q"},
		"k":      {siteKey},
		"c":      {baseToken},
		"co":     {recaptchaCo},
		"hl":     {recaptchaHl},
		"size":   {"invisible"},
		"vh":     {recaptchaVh},
		"chr":    {""},
		"bg":     {""},
	}
	reloadURL := recaptchaBase + "/recaptcha/enterprise/reload?k=" + siteKey
	header := transport.XHRHeaders(
		"application/x-www-form-urlencoded;charset=UTF-8", "*/*",
		recaptchaBase, anchorURL, "same-origin",
	)

	_, reloadBody, err := sess.DoAndRead(context.Background(), "POST", reloadURL, header, strings.NewReader(form.Encode()))
	if err != nil {
		return "", false
	}
	rm := rrespRe.FindSubmatch(reloadBody)
	if rm == nil {
		return "", false
	}
	return string(rm[1]), true
}
