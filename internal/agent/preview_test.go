package agent

import (
	"strings"
	"testing"

	"wtable/internal/model"
)

func TestPermissionPreviewUsesCachedToolCall(t *testing.T) {
	c := &Client{}
	c.rememberToolCall("tc-1", toolCallSummary{
		Title:       "Run shell",
		Kind:        "execute",
		Locations:   []string{"/repo/a.go:12", "/repo"},
		RawInput:    `{"command":"git push origin main"}`,
		ContentText: "about to push",
	})
	perm := model.Permission{Title: "Run shell"}
	preview := permissionPreview(perm, c.cachedToolCall("tc-1"))
	for _, want := range []string{"Run shell", "execute", "/repo/a.go:12", "git push origin main", "about to push"} {
		if !strings.Contains(preview, want) {
			t.Fatalf("preview missing %q: %q", want, preview)
		}
	}
	if strings.ContainsAny(preview, "\x1b\r\x00") {
		t.Fatalf("preview contains control characters: %q", preview)
	}
	if !strings.Contains(preview, "\n") {
		t.Fatalf("preview lost its line structure: %q", preview)
	}
}

func TestToolCallCacheIsBounded(t *testing.T) {
	c := &Client{}
	for i := 0; i < 80; i++ {
		c.rememberToolCall(string(rune('a'+i%26))+strings.Repeat("x", i%7)+string(rune('0'+i/26)), toolCallSummary{Title: "t"})
	}
	c.toolMu.Lock()
	size := len(c.toolCalls)
	order := len(c.toolOrder)
	c.toolMu.Unlock()
	if size > 64 || order > 64 {
		t.Fatalf("tool cache grew to %d/%d", size, order)
	}
}

func TestPreviewTextNeutralisesControlCharacters(t *testing.T) {
	got := previewText("git\x1b[31m push\r\nnow\x07", 100)
	if strings.ContainsAny(got, "\x1b\r\x07") {
		t.Fatalf("control characters survived: %q", got)
	}
	if got != "git [31m push\nnow" {
		t.Fatalf("unexpected sanitised preview: %q", got)
	}
}
