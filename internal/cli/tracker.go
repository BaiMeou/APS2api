// Copyright (c) 2026 BaiMeow. All rights reserved.
package cli

import (
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gosuri/uilive"
)

type ReqState struct {
	ID         string
	Model      string
	State      string
	Color      string // ANSI 颜色代码
	WinnerNode string // 胜出的代理节点
	Detail     string
	StartTime  time.Time
}

const (
	boxWidth      = 86 // 面板总宽度（不含两端边框）
	boxInnerWidth = 84 // 面板内部可容纳的纯文本视觉列宽
	maxLogs       = 10 // 日志窗口固定显示的行数
)

var (
	//nolint:gochecknoglobals // Internal CLI state
	mu sync.Mutex
	//nolint:gochecknoglobals // Internal CLI state
	activeReqs = make(map[string]*ReqState)
	//nolint:gochecknoglobals // Internal CLI state
	enabled bool
	//nolint:gochecknoglobals // Internal CLI state
	osStdout = os.Stdout

	// 环形日志缓冲区，存放最近 maxLogs 条日志
	//nolint:gochecknoglobals // Internal CLI state
	logBuffer []string

	// uilive 托管的 live 写入器
	//nolint:gochecknoglobals // Internal CLI state
	liveWriter *uilive.Writer

	// 软件版本与平台常驻信息
	//nolint:gochecknoglobals // Internal CLI state
	appVersion = "dev"
	//nolint:gochecknoglobals // Internal CLI state
	buildInfo = "Build: unknown / unknown"
	//nolint:gochecknoglobals // Internal CLI state
	platformInfo = "Platform: unknown"

	//nolint:gochecknoglobals // Internal CLI state
	spinners = []rune(`⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`)
	//nolint:gochecknoglobals // Internal CLI state
	spinnerIdx int
)

// SetAppInfo 供 main.go 初始化时传入版本及编译属性
func SetAppInfo(ver, commit, bTime, goos, goarch string) {
	mu.Lock()
	defer mu.Unlock()
	appVersion = ver
	buildInfo = fmt.Sprintf("Build: %s / %s", commit, bTime)
	platformInfo = fmt.Sprintf("Platform: %s/%s", goos, goarch)

	// 存储格式化后的头部信息
	logBuffer = append(logBuffer, truncateAndPad(fmt.Sprintf("[vproxy] 启动成功: Version=%s, Commit=%s, Built=%s", ver, commit, bTime), boxInnerWidth))
	logBuffer = append(logBuffer, truncateAndPad(fmt.Sprintf("[vproxy] 运行平台: %s/%s", goos, goarch), boxInnerWidth))
}

// runeWidth 估算单个字符在终端中所占的视觉单元宽度（1 或 2）
func runeWidth(r rune) int {
	if r >= 0x1100 && ((r >= 0x1100 && r <= 0x115F) || // Hangul Jamo
		(r >= 0x2E80 && r <= 0xA4CF && r != 0x303F) || // CJK 汉字区
		(r >= 0xAC00 && r <= 0xD7A3) || // Hangul 拼音
		(r >= 0xF900 && r <= 0xFAFF) || // CJK 兼容
		(r >= 0xFE10 && r <= 0xFE19) || // 竖排
		(r >= 0xFE30 && r <= 0xFE6F) || // CJK 兼容表单
		(r >= 0xFF00 && r <= 0xFF60) || // 全角字符
		(r >= 0xFFE0 && r <= 0xFFE6) || // 全角符号
		(r >= 0x1F000 && r <= 0x1F9FF) || // 现代 Emoji
		(r >= 0x20000 && r <= 0x2FA1F) || // 扩展 CJK
		(r >= 0x2600 && r <= 0x27BF)) { // 经典杂项符号（如 ⚡, 💬）
		return 2
	}
	return 1
}

// stringWidth 计算整条字符串在终端中的实际视觉占用宽度
func stringWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// truncateAndPad 精准控制字符串在终端中占用的视觉列数（超出截断，不足补齐）
func truncateAndPad(s string, maxCol int) string {
	w := stringWidth(s)
	if w <= maxCol {
		return s + strings.Repeat(" ", maxCol-w)
	}

	var sb strings.Builder
	curCol := 0
	for _, r := range s {
		rw := runeWidth(r)
		if curCol+rw > maxCol-2 {
			break
		}
		sb.WriteRune(r)
		curCol += rw
	}
	sb.WriteString("..")
	curCol += 2

	if curCol < maxCol {
		sb.WriteString(strings.Repeat(" ", maxCol-curCol))
	}
	return sb.String()
}

var additionalLogWriter io.Writer

// logInterceptor 拦截器：劫持标准 log 输出
type logInterceptor struct{}

func (logInterceptor) Write(p []byte) (int, error) {
	if additionalLogWriter != nil {
		_, _ = additionalLogWriter.Write(p)
	}
	if enabled {
		addLogLine(string(p))
	} else {
		// 非 TTY (Docker/Systemd) 自动回退，向标准错误直接输出，防止吞日志
		_, _ = os.Stderr.Write(p)
	}
	return len(p), nil
}

// addLogLine 将新日志 line 写入环形队列
func addLogLine(text string) {
	lines := strings.Split(text, "\n")
	mu.Lock()
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lineVal := truncateAndPad(line, boxInnerWidth)
		logBuffer = append(logBuffer, lineVal)
	}
	for len(logBuffer) > maxLogs {
		logBuffer = logBuffer[1:]
	}
	mu.Unlock()

	if enabled {
		mu.Lock()
		drawTUI()
		mu.Unlock()
	}
}

// InitTracker 初始化 uilive 状态面板与日志拦截器
func InitTracker(fileLogger io.Writer) {
	additionalLogWriter = fileLogger
	fileInfo, err := osStdout.Stat()
	if err == nil && (fileInfo.Mode()&os.ModeCharDevice) != 0 {
		enabled = true

		liveWriter = uilive.New()
		liveWriter.Start()

		log.SetOutput(logInterceptor{})

		go func() {
			ticker := time.NewTicker(120 * time.Millisecond)
			for range ticker.C {
				mu.Lock()
				spinnerIdx = (spinnerIdx + 1) % len(spinners)
				if len(activeReqs) > 0 {
					drawTUI()
				}
				mu.Unlock()
			}
		}()
	}
}

func StartReq(id string) {
	mu.Lock()
	defer mu.Unlock()
	activeReqs[id] = &ReqState{ //nolint:exhaustruct
		ID:        id,
		Model:     "连接中...",
		State:     "🔗 连接中",
		Color:     "\033[90m", // 灰色
		StartTime: time.Now(),
	}
	if enabled {
		drawTUI()
	}
}

func UpdateReqModel(id, model string) {
	mu.Lock()
	defer mu.Unlock()
	if req, ok := activeReqs[id]; ok {
		req.Model = model
	}
}

func UpdateReqState(id, state, color, detail string) {
	mu.Lock()
	defer mu.Unlock()
	if req, ok := activeReqs[id]; ok {
		req.State = state
		req.Color = color
		if detail != "" {
			req.Detail = detail
		}
	}
}

func UpdateReqWinner(id, nodeName string) {
	mu.Lock()
	defer mu.Unlock()
	if req, ok := activeReqs[id]; ok {
		req.WinnerNode = nodeName
	}
}

func FinishReq(id string) {
	mu.Lock()
	defer mu.Unlock()
	delete(activeReqs, id)
	if enabled {
		drawTUI()
	}
}

// drawTUI 构建并向 uilive.Writer 刷入面板内容
func drawTUI() {
	var sb strings.Builder
	bottomBorder := "╰" + strings.Repeat("─", boxWidth) + "╯\n"

	// 1. ─── 绘制常驻版权与安全防诈声明（已统一采用单线圆角风格，修复视觉错位） ───
	headerPrefix := "╭── 📢 Vertex AI Proxy "
	headerPrefixWidth := stringWidth(headerPrefix)
	headerDashes := boxWidth + 1 - headerPrefixWidth
	fmt.Fprintf(&sb, "\n\033[33m%s%s╮\033[0m\n", headerPrefix, strings.Repeat("─", headerDashes))

	line1 := fmt.Sprintf("Version: %s | %s", appVersion, platformInfo)
	sb.WriteString(fmt.Sprintf("\033[33m│\033[0m %s \033[33m│\033[0m\n", truncateAndPad(line1, boxInnerWidth)))

	line2 := buildInfo
	sb.WriteString(fmt.Sprintf("\033[33m│\033[0m %s \033[33m│\033[0m\n", truncateAndPad(line2, boxInnerWidth)))

	line3 := "⚠️  警告：本软件完全免费！如果你是付费购买的，你被骗了，请退款。"
	sb.WriteString(fmt.Sprintf("\033[33m│\033[0m \033[31m%s\033[0m \033[33m│\033[0m\n", truncateAndPad(line3, boxInnerWidth)))

	fmt.Fprintf(&sb, "\033[33m%s\033[0m", bottomBorder)

	// 2. ─── 绘制日志沙盒窗口 (Recent Logs Window) ───
	logPrefix := "╭── 📝 最近系统日志 "
	logPrefixWidth := stringWidth(logPrefix)
	logDashes := boxWidth + 1 - logPrefixWidth
	fmt.Fprintf(&sb, "\033[36m%s%s╮\033[0m\n", logPrefix, strings.Repeat("─", logDashes))

	for i := 0; i < maxLogs; i++ {
		var line string
		if i < len(logBuffer) {
			line = logBuffer[i]
		} else {
			line = strings.Repeat(" ", boxInnerWidth)
		}
		sb.WriteString(fmt.Sprintf("\033[36m│\033[0m %s \033[36m│\033[0m\n", line))
	}
	fmt.Fprintf(&sb, "\033[36m%s\033[0m", bottomBorder)

	// 3. ─── 绘制请求追踪面板 (Active Requests) ───
	if len(activeReqs) > 0 {
		var reqs []*ReqState
		for _, r := range activeReqs {
			reqs = append(reqs, r)
		}
		sort.Slice(reqs, func(i, j int) bool {
			return reqs[i].StartTime.Before(reqs[j].StartTime)
		})

		reqPrefix := "╭── 🚀 请求追踪器 "
		reqPrefixWidth := stringWidth(reqPrefix)
		reqDashes := boxWidth + 1 - reqPrefixWidth
		fmt.Fprintf(&sb, "\033[36m%s%s╮\033[0m\n", reqPrefix, strings.Repeat("─", reqDashes))
		sb.WriteString("\033[36m│\033[0m ID       \033[36m│\033[0m Model              \033[36m│\033[0m State        \033[36m│\033[0m Time  \033[36m│\033[0m Details                   \033[36m│\033[0m\n")
		sb.WriteString("\033[36m├──────────┼────────────────────┼──────────────┼───────┼───────────────────────────┤\033[0m\n")

		for _, r := range reqs {
			elapsed := time.Since(r.StartTime).Seconds()
			id := r.ID
			if len(id) > 6 {
				id = id[:6]
			}

			detailStr := r.Detail
			if r.WinnerNode != "" {
				detailStr = "🏆 " + r.WinnerNode
				if r.Detail != "" {
					detailStr += " | " + r.Detail
				}
			}

			idVal := truncateAndPad(id, 6)
			modelVal := truncateAndPad(r.Model, 18)
			stateVal := truncateAndPad(r.State, 12)
			detailVal := truncateAndPad(detailStr, 25)

			line := fmt.Sprintf("\033[36m│\033[0m \033[36m%c\033[0m %s \033[36m│\033[0m %s \033[36m│\033[0m %s%s\033[0m \033[36m│\033[0m %4.1fs \033[36m│\033[0m \033[90m%s\033[0m \033[36m│\033[0m\n",
				spinners[spinnerIdx], idVal, modelVal, r.Color, stateVal, elapsed, detailVal)

			sb.WriteString(line)
		}
		fmt.Fprintf(&sb, "\033[36m%s\033[0m", bottomBorder)
	}

	_, _ = fmt.Fprint(liveWriter, sb.String())
	_ = liveWriter.Flush()
}
