// Copyright (c) 2026 BaiMeow. All rights reserved.
package cli

import (
	"fmt"
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

var (
	//nolint:gochecknoglobals // Internal CLI state
	mu sync.Mutex
	//nolint:gochecknoglobals // Internal CLI state
	activeReqs = make(map[string]*ReqState)
	//nolint:gochecknoglobals // Internal CLI state
	enabled bool
	//nolint:gochecknoglobals // Internal CLI state
	osStdout = os.Stdout

	// uilive 托管的 live 写入器
	//nolint:gochecknoglobals // Internal CLI state
	liveWriter *uilive.Writer

	//nolint:gochecknoglobals // Internal CLI state
	spinners = []rune(`⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`)
	//nolint:gochecknoglobals // Internal CLI state
	spinnerIdx int
)

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

// InitTracker 初始化 uilive 状态面板
func InitTracker() {
	fileInfo, err := osStdout.Stat()
	if err == nil && (fileInfo.Mode()&os.ModeCharDevice) != 0 {
		enabled = true

		// 创建并启动 uilive 写入器
		liveWriter = uilive.New()
		liveWriter.Start()

		// 重定向标准 log 输出到 uilive.Bypass()，
		// 这样所有的 log.Printf 日志会自动显示在动态面板的上方，不会破坏面板结构
		log.SetOutput(liveWriter.Bypass())

		// 定时更新底部动态面板的动画帧与时间
		go func() {
			ticker := time.NewTicker(120 * time.Millisecond)
			for range ticker.C {
				mu.Lock()
				spinnerIdx = (spinnerIdx + 1) % len(spinners)
				if len(activeReqs) > 0 {
					drawTUI()
				} else {
					// 无活跃请求时，清空面板输出
					_, _ = fmt.Fprint(liveWriter, "")
					_ = liveWriter.Flush()
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
		Model:     "初始化中...",
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
	if len(activeReqs) == 0 {
		return
	}

	var reqs []*ReqState
	for _, r := range activeReqs {
		reqs = append(reqs, r)
	}
	sort.Slice(reqs, func(i, j int) bool {
		return reqs[i].StartTime.Before(reqs[j].StartTime)
	})

	var sb strings.Builder
	sb.WriteString("\n\033[36m╭── 🚀 请求追踪器 ─────────────────────────────────────────────────────────────────────╮\033[0m\n")
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

		// 精准对齐中英文和 Emoji
		idVal := truncateAndPad(id, 6)
		modelVal := truncateAndPad(r.Model, 18)
		stateVal := truncateAndPad(r.State, 12)
		detailVal := truncateAndPad(detailStr, 25)

		line := fmt.Sprintf("\033[36m│\033[0m \033[36m%c\033[0m %s \033[36m│\033[0m %s \033[36m│\033[0m %s%s\033[0m \033[36m│\033[0m %4.1fs \033[36m│\033[0m \033[90m%s\033[0m \033[36m│\033[0m\n",
			spinners[spinnerIdx], idVal, modelVal, r.Color, stateVal, elapsed, detailVal)

		sb.WriteString(line)
	}
	sb.WriteString("\033[36m╰──────────────────────────────────────────────────────────────────────────────────────╯\033[0m\n")

	// 直接写入 uilive.Writer 并 Flush
	_, _ = fmt.Fprint(liveWriter, sb.String())
	_ = liveWriter.Flush()
}
