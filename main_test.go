package main

import (
	"strings"
	"testing"
)

func TestMarkdownProducesInlineStyles(t *testing.T) {
	a := newApp()
	var raw strings.Builder
	if err := a.markdown.Convert([]byte("## 标题\n\n正文与 `代码`。"), &raw); err != nil {
		t.Fatal(err)
	}
	got, err := a.inline(a.policy.Sanitize(raw.String()), "default")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<h2", "border-left:5px solid #2563eb", "<p style=", "<code style="} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %s", want, got)
		}
	}
}

func TestUnsafeHTMLIsRemoved(t *testing.T) {
	a := newApp()
	input := `<p>safe</p><script>alert(1)</script><img src="javascript:alert(1)">`
	got, err := a.inline(a.policy.Sanitize(input), "default")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "script") || strings.Contains(got, "javascript:") {
		t.Fatalf("unsafe content survived sanitization: %s", got)
	}
}

func TestThemeAccent(t *testing.T) {
	a := newApp()
	got, err := a.inline("<h2>标题</h2>", "green")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "#047857") {
		t.Fatalf("green accent missing: %s", got)
	}
}

func TestCodeBlockDoesNotUseInlineCodeDecoration(t *testing.T) {
	a := newApp()
	var raw strings.Builder
	if err := a.markdown.Convert([]byte("```go\nfmt.Println(1)\n```"), &raw); err != nil {
		t.Fatal(err)
	}
	got, err := a.inline(a.policy.Sanitize(raw.String()), "default")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "background:transparent") || !strings.Contains(got, "white-space:pre") {
		t.Fatalf("code block style missing: %s", got)
	}
}

func TestDataURIImageIsAllowed(t *testing.T) {
	a := newApp()
	input := `<img alt="local" src="data:image/png;base64,iVBORw0KGgo=">`
	got, err := a.inline(a.policy.Sanitize(input), "default")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "data:image/png;base64") {
		t.Fatalf("data URI image was removed: %s", got)
	}
}

func TestStructurePlainTextRecognizesArticleBlocks(t *testing.T) {
	input := "一篇文章标题\n\n这是第一段。\n这是第二段。\n\n一、为什么要排版\n• 重点一\n• 重点二"
	got, stats := structurePlainText(input)
	for _, want := range []string{"# 一篇文章标题", "这是第一段。\n\n这是第二段。", "## 一、为什么要排版", "- 重点一\n- 重点二"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in:\n%s", want, got)
		}
	}
	if stats.headings != 2 || stats.paragraphs != 2 || stats.lists != 2 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestStructurePlainTextKeepsCodeFenceTogether(t *testing.T) {
	input := "说明\n\n```go\nfunc main() {}\n```\n\n结尾。"
	got, _ := structurePlainText(input)
	if !strings.Contains(got, "```go\nfunc main() {}\n```") {
		t.Fatalf("code fence was split: %s", got)
	}
}

func TestStructurePlainTextBuildsThreeHeadingLevels(t *testing.T) {
	input := "深度思考的价值，究竟是什么？\n\n这是一段导语。\n\n为什么深度思考越来越稀缺\n\n这是章节正文。\n\n（一）信息过载\n\n这是小节正文。"
	got, stats := structurePlainText(input)
	for _, want := range []string{"# 深度思考的价值，究竟是什么？", "## 为什么深度思考越来越稀缺", "### （一）信息过载"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected hierarchy %q in:\n%s", want, got)
		}
	}
	if stats.headings != 3 {
		t.Fatalf("expected 3 headings, got %+v", stats)
	}
}

func TestThemeHierarchyStylesAreDistinct(t *testing.T) {
	a := newApp()
	input := "<h1>主标题</h1><p>导语内容。</p><h2>章节标题</h2><h3>小节标题</h3>"
	warm, err := a.inline(input, "warm")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"text-align:center", "background:#c2410c", "border-bottom:1px solid #c2410c", "font-size:17px;line-height:1.9"} {
		if !strings.Contains(warm, want) {
			t.Fatalf("warm hierarchy style %q missing: %s", want, warm)
		}
	}
	ink, err := a.inline(input, "ink")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ink, "letter-spacing:0.16em") || !strings.Contains(ink, "border-bottom:2px solid #171717") {
		t.Fatalf("ink hierarchy styles missing: %s", ink)
	}
}

func TestSmartEmphasisHighlightsInsightWithoutOverdoingParagraph(t *testing.T) {
	a := newApp()
	input := "<p>这是一段普通的背景说明。真正重要的不是使用什么工具，而是能否持续创造价值。其他内容保持普通样式。</p>"
	got, count, err := a.inlineWithEmphasis(a.policy.Sanitize(input), "warm", true)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one emphasis, got %d: %s", count, got)
	}
	if !strings.Contains(got, "font-weight:700;color:#c2410c") || !strings.Contains(got, "真正重要的不是") {
		t.Fatalf("key sentence was not highlighted: %s", got)
	}
	if strings.Contains(got, "data-auto-emphasis") {
		t.Fatalf("internal marker leaked into output: %s", got)
	}
}

func TestSmartEmphasisHighlightsData(t *testing.T) {
	a := newApp()
	input := "<p>调研覆盖1200人，其中68%的人更关注阅读效率。</p>"
	got, count, err := a.inlineWithEmphasis(a.policy.Sanitize(input), "violet", true)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || !strings.Contains(got, "background:#faf5ff") {
		t.Fatalf("data sentence was not highlighted: %s", got)
	}
}

func TestSmartEmphasisCanBeDisabled(t *testing.T) {
	a := newApp()
	input := "<p>真正重要的不是形式，而是内容。</p>"
	got, count, err := a.inlineWithEmphasis(a.policy.Sanitize(input), "default", false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || strings.Contains(got, "font-weight:700;color:#2563eb") {
		t.Fatalf("disabled emphasis changed the paragraph: %s", got)
	}
}
