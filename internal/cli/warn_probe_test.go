package cli

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestWarnLineWidths(t *testing.T) {
	warn := "⚠️  本软件完全免费！付费即被骗，请退款。"
	t.Logf("warn full: ansi=%d", ansi.StringWidth(warn))
	t.Logf("'⚠️' (with FE0F): ansi=%d", ansi.StringWidth("⚠️"))
	t.Logf("'⚠' (no FE0F): ansi=%d", ansi.StringWidth("⚠"))

	m := TuiModel{width: 80, height: 24, activeReqs: map[string]*ReqState{}}
	content := m.buildContent(80)
	for i, ln := range splitKeep(content) {
		if containsWarn(ln) {
			t.Logf("warn line %d: ansi width=%d | %q", i, ansi.StringWidth(ln), ln)
		}
	}
}

func splitKeep(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func containsWarn(s string) bool {
	return ansi.StringWidth(s) > 0 && len(s) > 0 && (len(s) < 200)
}
