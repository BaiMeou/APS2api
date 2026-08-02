// Package recaptcha 实现 Google reCAPTCHA Enterprise 匿名 token 的现抓现用。
//
// 流程：anchor iframe GET 抠出 base token，再 reload POST
// 拿到最终 token（rresp）。token 用于 batchGraphql 的 recaptchaToken 字段。
package recaptcha

import (
	"context"
	"fmt"
	"log"
	"math/rand/v2"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bsfdsagfadg/vertex/internal/nodes"
	"github.com/bsfdsagfadg/vertex/internal/transport"
)

// recaptcha 相关硬编码常量（逐字节保持既定常量）。
const (
	recaptchaBase = "https://www.google.com"
	siteKey       = "6LdCjtspAAAAAMcV4TGdWLJqRTEk1TfpdLqEnKdj"
	recaptchaCo   = "aHR0cHM6Ly9jb25zb2xlLmNsb3VkLmdvb2dsZS5jb206NDQz"
	recaptchaHl   = "zh-CN"
	recaptchaVh   = "6581054572"
	randomCharset = "abcdefghijklmnopqrstuvwxyz0123456789"
)

var (
	// 从 anchor HTML 抠 base token。用正则而非 HTML 解析器（已实测可行、无需额外依赖）。
	tokenRe = regexp.MustCompile(`id="recaptcha-token"[^>]*value="([^"]+)"`)
	// 从 reload 响应抠最终 token。
	rrespRe = regexp.MustCompile(`rresp","(.*?)"`)
	// 从 enterprise.js 提取 reCAPTCHA release 版本号（Google 定期滚动，不能硬编码）。
	versionRe = regexp.MustCompile(`releases/([A-Za-z0-9_-]{20,})`)

	versionMu sync.Mutex //nolint:gochecknoglobals
	cachedVer string //nolint:gochecknoglobals
)

// versionUA 拉取 enterprise.js 时使用的浏览器 UA（与 transport 包保持一致）。
const versionUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"

// fetchVersionFromJS 从 enterprise.js 提取当前 reCAPTCHA release 版本号。
//
// 版本号 Google 会定期滚动：硬编码旧版本会让 reload 换发的 token 第一次被
// batchGraphql 评估时失败（"Failed to verify action"），同 token 重试一次才过。
// 动态拉取当前版本后首帧即可通过（实测）。
func fetchVersionFromJS(net *transport.NetworkClient, proxyURI string) (string, bool) {
	sess, err := net.CreateSession(15, proxyURI, "recaptcha-version")
	if err != nil {
		return "", false
	}
	defer sess.Close()

	h := transport.Header{
		"user-agent":      {versionUA},
		"accept":          {"*/*"},
		"accept-language": {"zh-CN,zh;q=0.9,en;q=0.8"},
	}
	_, body, err := sess.DoAndRead(context.Background(), "GET", recaptchaBase+"/recaptcha/enterprise.js", h, nil)
	if err != nil {
		return "", false
	}
	m := versionRe.FindSubmatch(body)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

// currentVersion 返回缓存的 reCAPTCHA 版本号，未缓存则现场拉取。
func currentVersion(net *transport.NetworkClient, proxyURI string) (string, bool) {
	versionMu.Lock()
	defer versionMu.Unlock()
	if cachedVer != "" {
		return cachedVer, true
	}
	v, ok := fetchVersionFromJS(net, proxyURI)
	if ok {
		cachedVer = v
	}
	return v, ok
}

// invalidateVersion 清除版本缓存：token 获取失败时调用，强制下一次重新拉取版本
// （旧版本号过期是 token 失败的首要原因）。
func invalidateVersion() {
	versionMu.Lock()
	cachedVer = ""
	versionMu.Unlock()
}

func randomString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = randomCharset[rand.IntN(len(randomCharset))]
	}
	return string(b)
}

// FetchRecaptchaToken 获取 Google reCAPTCHA token（隔离特征）。
//
// 最多 3 次重试，每次新建一个 short Timeout Session
// （即用即毁，FRESH_CONNECT 语义）。全部失败返回 ("", nil) —— 返回空值表示失败，
// 调用方按“空则换新/重试”处理。返回非空字符串即成功。
func FetchRecaptchaToken(net *transport.NetworkClient, proxyURI string, debugMode bool) (string, error) {
	// 【核心修改：解析并缓存节点友好名称】
	nodeName := nodes.GetNodeName(proxyURI)
	if proxyURI == "" {
		nodeName = "直连 (Direct)"
	}

	start := time.Now()
	for retry := 0; retry < 3; retry++ {
		// 【核心修改：将具体的节点名称明确输出在日志归属中】
		if debugMode {
			log.Printf("[Recaptcha] [节点: %s] 开始获取 reCAPTCHA token (尝试 %d/3)", nodeName, retry+1)
		}
		version, ok := currentVersion(net, proxyURI)
		if !ok {
			invalidateVersion()
			if debugMode {
				log.Printf("[Recaptcha] [节点: %s] 拉取 reCAPTCHA 版本号失败 (尝试 %d/3)", nodeName, retry+1)
			}
			continue
		}
		if token, ok := fetchOnce(net, proxyURI, version); ok {
			elapsed := time.Since(start)
			if debugMode {
				log.Printf("[Recaptcha] [节点: %s] 成功获取 reCAPTCHA token, 耗时: %d ms", nodeName, elapsed.Milliseconds())
			}
			return token, nil
		}
		// token 获取失败：大概率版本号已过期，清缓存强制重新拉取。
		invalidateVersion()
	}
	elapsed := time.Since(start)
	if debugMode {
		log.Printf("[Recaptcha] [节点: %s] 3次尝试后获取 reCAPTCHA token 失败, 耗时: %d ms", nodeName, elapsed.Milliseconds())
	}
	return "", nil
}

func fetchOnce(net *transport.NetworkClient, proxyURI string, version string) (string, bool) {
	sess, err := net.CreateSession(15, proxyURI, "recaptcha")
	if err != nil {
		return "", false
	}
	defer sess.Close()

	cb := randomString(10)
	anchorURL := fmt.Sprintf(
		"%s/recaptcha/enterprise/anchor?ar=1&k=%s&co=%s&hl=%s&v=%s&size=invisible&anchor-ms=20000&execute-ms=15000&cb=%s",
		recaptchaBase, siteKey, recaptchaCo, recaptchaHl, version, cb,
	)

	// token 预取与具体请求无关（后台细流），故用 context.Background()，不随某个请求取消。
	_, anchorBody, err := sess.DoAndRead(context.Background(), "GET", anchorURL, transport.AnchorHeaders(), nil)
	if err != nil {
		return "", false
	}
	m := tokenRe.FindSubmatch(anchorBody)
	if m == nil {
		bodyStr := string(anchorBody)
		if len(bodyStr) > 500 {
			bodyStr = bodyStr[:500] + "..."
		}
		log.Printf("[Recaptcha] anchor token正则匹配失败, body前缀: %s", bodyStr)
		return "", false
	}
	baseToken := string(m[1])

	form := url.Values{
		"v":      {version},
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

	status, reloadBody, err := sess.DoAndRead(context.Background(), "POST", reloadURL, header, strings.NewReader(form.Encode()))
	if err != nil {
		return "", false
	}
	if status != 200 {
		log.Printf("[Recaptcha] Reload 失败, HTTP 状态码: %d, 返回内容: %s", status, string(reloadBody))
	}
	rm := rrespRe.FindSubmatch(reloadBody)
	if rm == nil {
		return "", false
	}
	return string(rm[1]), true
}
