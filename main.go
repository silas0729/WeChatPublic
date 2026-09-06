package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	xhtml "golang.org/x/net/html"
)

//go:embed web/*
var webFiles embed.FS

const maxRequestBytes = 16 << 20

type renderRequest struct {
	Content  string `json:"content"`
	Theme    string `json:"theme"`
	Emphasis bool   `json:"emphasis"`
}

type renderResponse struct {
	HTML     string `json:"html"`
	Emphasis int    `json:"emphasis"`
}

type structureRequest struct {
	Content string `json:"content"`
	Output  string `json:"output"`
}

type structureResponse struct {
	Content    string `json:"content"`
	Paragraphs int    `json:"paragraphs"`
	Headings   int    `json:"headings"`
	Lists      int    `json:"lists"`
}

type structureStats struct {
	paragraphs int
	headings   int
	lists      int
}

var (
	chapterHeading = regexp.MustCompile(`^(?:第[一二三四五六七八九十百0-9]+[章节部分篇]|[一二三四五六七八九十百]+[、.．]|[0-9]+[、.．])\s*\S+`)
	bulletLine     = regexp.MustCompile(`^[•●▪◦·]\s*(.+)$`)
	orderedLine    = regexp.MustCompile(`^[0-9]+[.)、．]\s+\S+`)
	markdownList   = regexp.MustCompile(`^[-+*]\s+\S+`)
	dataStatement  = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?(?:%|％|万|亿|元|倍|年|个月|个|人|次|小时|分钟|天)`)
	insightPattern = regexp.MustCompile(`不是.+而是|真正.{0,10}是|本质|关键|核心|意味着|值得|必须|不要|别让|最重要|归根结底|换句话说|说到底|之所以|与其.+不如|只有.+才|越.+越`)
	turnPattern    = regexp.MustCompile(`但|却|然而|反而|其实|事实上|可惜|好在`)
)

type emphasisCandidate struct {
	node    *xhtml.Node
	segment int
	score   int
	level   string
}

type app struct {
	markdown goldmark.Markdown
	policy   *bluemonday.Policy
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "local address to listen on")
	flag.Parse()

	webRoot, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal(err)
	}

	a := newApp()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/render", a.renderMarkdown)
	mux.HandleFunc("POST /api/inline", a.renderRichText)
	mux.HandleFunc("POST /api/structure", a.structureText)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("/", http.FileServer(http.FS(webRoot)))

	server := &http.Server{
		Addr:              *addr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("WeChat Publisher is running at http://%s", *addr)
	log.Fatal(server.ListenAndServe())
}

func newApp() *app {
	policy := bluemonday.UGCPolicy()
	policy.AllowDataURIImages()
	policy.AllowAttrs("class").OnElements("code")
	policy.AllowAttrs("start").OnElements("ol")
	policy.AllowAttrs("colspan", "rowspan").OnElements("td", "th")

	return &app{
		markdown: goldmark.New(
			goldmark.WithExtensions(extension.GFM, extension.Footnote),
			goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		),
		policy: policy,
	}
}

func (a *app) renderMarkdown(w http.ResponseWriter, r *http.Request) {
	req, err := decodeRequest(w, r)
	if err != nil {
		writeError(w, err)
		return
	}

	var raw bytes.Buffer
	if err := a.markdown.Convert([]byte(req.Content), &raw); err != nil {
		writeError(w, fmt.Errorf("convert markdown: %w", err))
		return
	}

	html, emphasis, err := a.inlineWithEmphasis(string(a.policy.SanitizeBytes(raw.Bytes())), req.Theme, req.Emphasis)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, renderResponse{HTML: html, Emphasis: emphasis})
}

func (a *app) renderRichText(w http.ResponseWriter, r *http.Request) {
	req, err := decodeRequest(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	html, emphasis, err := a.inlineWithEmphasis(a.policy.Sanitize(req.Content), req.Theme, req.Emphasis)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, renderResponse{HTML: html, Emphasis: emphasis})
}

func (a *app) structureText(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[structureRequest](w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	markdown, stats := structurePlainText(req.Content)
	result := markdown
	if req.Output == "html" {
		var raw bytes.Buffer
		if err := a.markdown.Convert([]byte(markdown), &raw); err != nil {
			writeError(w, fmt.Errorf("structure text: %w", err))
			return
		}
		result = a.policy.Sanitize(raw.String())
	} else if req.Output != "markdown" {
		writeError(w, errors.New("output must be markdown or html"))
		return
	}
	writeJSON(w, http.StatusOK, structureResponse{
		Content:    result,
		Paragraphs: stats.paragraphs,
		Headings:   stats.headings,
		Lists:      stats.lists,
	})
}

func decodeRequest(w http.ResponseWriter, r *http.Request) (renderRequest, error) {
	return decodeJSON[renderRequest](w, r)
}

func decodeJSON[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req T
	if err := decoder.Decode(&req); err != nil {
		return req, fmt.Errorf("invalid request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return req, errors.New("invalid request: only one JSON value is allowed")
	}
	return req, nil
}

func structurePlainText(input string) (string, structureStats) {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	lines := strings.Split(strings.TrimSpace(input), "\n")
	out := make([]string, 0, len(lines)*2)
	stats := structureStats{}
	previousKind := ""
	inFence := false
	firstContent := true

	appendBlock := func(line, kind string) {
		grouped := kind == previousKind && (kind == "list" || kind == "quote" || kind == "table" || kind == "code")
		if len(out) > 0 && out[len(out)-1] != "" && !grouped {
			out = append(out, "")
		}
		out = append(out, line)
		previousKind = kind
	}

	for index, original := range lines {
		line := strings.TrimSpace(original)
		if line == "" {
			if len(out) > 0 && out[len(out)-1] != "" && !inFence {
				out = append(out, "")
			}
			previousKind = ""
			continue
		}

		if strings.HasPrefix(line, "```") {
			if !inFence {
				appendBlock(line, "code")
			} else {
				out = append(out, line)
			}
			inFence = !inFence
			previousKind = "code"
			firstContent = false
			continue
		}
		if inFence {
			out = append(out, original)
			previousKind = "code"
			continue
		}

		switch {
		case strings.HasPrefix(line, "#"):
			appendBlock(line, "heading")
			stats.headings++
		case chapterHeading.MatchString(line) && utf8.RuneCountInString(line) <= 48:
			appendBlock("## "+line, "heading")
			stats.headings++
		case firstContent && looksLikeTitle(line, nextLineIsBlank(lines, index)):
			appendBlock("# "+line, "heading")
			stats.headings++
		case bulletLine.MatchString(line):
			appendBlock(bulletLine.ReplaceAllString(line, "- $1"), "list")
			stats.lists++
		case markdownList.MatchString(line) || orderedLine.MatchString(line):
			appendBlock(line, "list")
			stats.lists++
		case strings.HasPrefix(line, ">"):
			appendBlock(line, "quote")
			stats.paragraphs++
		case strings.Contains(line, "|"):
			appendBlock(line, "table")
		default:
			appendBlock(line, "paragraph")
			stats.paragraphs++
		}
		firstContent = false
	}

	return strings.TrimSpace(strings.Join(out, "\n")), stats
}

func looksLikeTitle(line string, followedByBlank bool) bool {
	length := utf8.RuneCountInString(line)
	if length == 0 || length > 36 || (length > 24 && !followedByBlank) {
		return false
	}
	return !strings.ContainsAny(line, "。！？!?；;，,")
}

func nextLineIsBlank(lines []string, index int) bool {
	return index+1 >= len(lines) || strings.TrimSpace(lines[index+1]) == ""
}

func (a *app) inline(fragment, themeName string) (string, error) {
	result, _, err := a.inlineWithEmphasis(fragment, themeName, false)
	return result, err
}

func (a *app) inlineWithEmphasis(fragment, themeName string, emphasize bool) (string, int, error) {
	styles := theme(themeName)
	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<main>" + fragment + "</main>"))
	if err != nil {
		return "", 0, fmt.Errorf("parse html: %w", err)
	}

	root := doc.Find("main").First()
	emphasisCount := 0
	if emphasize {
		emphasisCount = applySmartEmphasis(root)
	}
	root.Find("*").Each(func(_ int, s *goquery.Selection) {
		tag := goquery.NodeName(s)
		css, ok := styles[tag]
		if level, exists := s.Attr("data-auto-emphasis"); exists {
			css, ok = styles["emphasis "+level]
			s.RemoveAttr("data-auto-emphasis")
		}
		if tag == "code" && s.Parent().Is("pre") {
			css, ok = styles["pre code"]
		}
		if !ok {
			css = styles["*"]
		}
		if css != "" {
			previous, _ := s.Attr("style")
			s.SetAttr("style", mergeStyle(previous, css))
		}
		if tag == "a" {
			s.SetAttr("target", "_blank")
			s.SetAttr("rel", "noopener noreferrer")
		}
		if tag == "img" {
			s.SetAttr("data-ratio", "auto")
		}
	})

	result, err := root.Html()
	if err != nil {
		return "", 0, fmt.Errorf("serialize html: %w", err)
	}
	return result, emphasisCount, nil
}

func applySmartEmphasis(root *goquery.Selection) int {
	total := 0
	root.Find("p, li").Each(func(_ int, block *goquery.Selection) {
		if block.Find("img, pre, code").Length() > 0 {
			return
		}

		textNodes := make([]*xhtml.Node, 0, 4)
		for _, node := range block.Nodes {
			collectTextNodes(node, false, &textNodes)
		}
		segments := make(map[*xhtml.Node][]string, len(textNodes))
		candidates := make([]emphasisCandidate, 0, len(textNodes))
		sentenceCount := 0
		for _, node := range textNodes {
			parts := splitSentences(node.Data)
			segments[node] = parts
			sentenceCount += len(parts)
			for index, part := range parts {
				score, level := scoreSentence(part)
				if score >= 2 {
					candidates = append(candidates, emphasisCandidate{node: node, segment: index, score: score, level: level})
				}
			}
		}
		if len(candidates) == 0 {
			return
		}
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
		limit := 1
		if sentenceCount >= 5 {
			limit = 2
		}
		if limit > len(candidates) {
			limit = len(candidates)
		}
		chosen := make(map[*xhtml.Node]map[int]string)
		for _, candidate := range candidates[:limit] {
			if chosen[candidate.node] == nil {
				chosen[candidate.node] = make(map[int]string)
			}
			chosen[candidate.node][candidate.segment] = candidate.level
		}

		for node, selections := range chosen {
			parent := node.Parent
			if parent == nil {
				continue
			}
			for index, part := range segments[node] {
				if part == "" {
					continue
				}
				if level, ok := selections[index]; ok {
					span := &xhtml.Node{Type: xhtml.ElementNode, Data: "span", Attr: []xhtml.Attribute{{Key: "data-auto-emphasis", Val: level}}}
					span.AppendChild(&xhtml.Node{Type: xhtml.TextNode, Data: part})
					parent.InsertBefore(span, node)
					total++
				} else {
					parent.InsertBefore(&xhtml.Node{Type: xhtml.TextNode, Data: part}, node)
				}
			}
			parent.RemoveChild(node)
		}
	})
	return total
}

func collectTextNodes(node *xhtml.Node, blocked bool, result *[]*xhtml.Node) {
	if node.Type == xhtml.ElementNode {
		switch node.Data {
		case "a", "strong", "b", "em", "i", "code", "mark":
			blocked = true
		}
	}
	if node.Type == xhtml.TextNode && !blocked && strings.TrimSpace(node.Data) != "" {
		*result = append(*result, node)
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectTextNodes(child, blocked, result)
	}
}

func splitSentences(text string) []string {
	parts := make([]string, 0, 3)
	start := 0
	for index, r := range text {
		if strings.ContainsRune("。！？!?；;\n", r) {
			end := index + utf8.RuneLen(r)
			parts = append(parts, text[start:end])
			start = end
		}
	}
	if start < len(text) {
		parts = append(parts, text[start:])
	}
	if len(parts) == 0 {
		return []string{text}
	}
	return parts
}

func scoreSentence(sentence string) (int, string) {
	text := strings.TrimSpace(sentence)
	length := utf8.RuneCountInString(text)
	if length < 8 || length > 100 || strings.Contains(text, "://") {
		return 0, ""
	}

	score := 0
	level := "insight"
	if dataStatement.MatchString(text) {
		score += 3
		level = "data"
	}
	if insightPattern.MatchString(text) {
		score += 3
		level = "key"
	}
	if turnPattern.MatchString(text) {
		score++
	}
	if strings.ContainsAny(text, "“”「」") {
		score++
	}
	if length >= 10 && length <= 32 && !strings.ContainsAny(text, "，,、") {
		score++
	}
	return score, level
}

func mergeStyle(existing, generated string) string {
	existing = strings.TrimSpace(existing)
	generated = strings.TrimSpace(generated)
	if existing == "" {
		return generated
	}
	if !strings.HasSuffix(existing, ";") {
		existing += ";"
	}
	return existing + generated
}

func theme(name string) map[string]string {
	accent := "#2563eb"
	codeBackground := "#f6f8fa"
	quoteBackground := "#f8fafc"
	if name == "warm" {
		accent = "#c2410c"
		codeBackground = "#fff7ed"
		quoteBackground = "#fffbeb"
	} else if name == "green" {
		accent = "#047857"
		codeBackground = "#f0fdf4"
		quoteBackground = "#ecfdf5"
	} else if name == "violet" {
		accent = "#7c3aed"
		codeBackground = "#f5f3ff"
		quoteBackground = "#faf5ff"
	} else if name == "ink" {
		accent = "#171717"
		codeBackground = "#f5f5f5"
		quoteBackground = "#fafafa"
	}

	return map[string]string{
		"*":                "max-width:100%;box-sizing:border-box;",
		"h1":               "margin:1.4em 0 0.8em;font-size:26px;line-height:1.4;font-weight:700;color:#111827;text-align:left;",
		"h2":               "margin:1.35em 0 0.7em;padding-left:10px;border-left:4px solid " + accent + ";font-size:22px;line-height:1.5;font-weight:700;color:#111827;",
		"h3":               "margin:1.25em 0 0.65em;font-size:19px;line-height:1.55;font-weight:700;color:" + accent + ";",
		"h4":               "margin:1.2em 0 0.6em;font-size:17px;line-height:1.6;font-weight:700;color:#1f2937;",
		"p":                "margin:0 0 1em;font-size:16px;line-height:1.85;letter-spacing:0.03em;color:#374151;text-align:justify;word-break:break-word;",
		"div":              "margin:0 0 1em;font-size:16px;line-height:1.85;letter-spacing:0.03em;color:#374151;word-break:break-word;",
		"strong":           "font-weight:700;color:#111827;",
		"b":                "font-weight:700;color:#111827;",
		"em":               "font-style:italic;color:#4b5563;",
		"i":                "font-style:italic;color:#4b5563;",
		"a":                "color:" + accent + ";text-decoration:none;border-bottom:1px solid " + accent + ";",
		"blockquote":       "margin:1em 0;padding:12px 16px;border-left:4px solid " + accent + ";background:" + quoteBackground + ";color:#4b5563;",
		"ul":               "margin:0 0 1em;padding-left:1.5em;color:#374151;",
		"ol":               "margin:0 0 1em;padding-left:1.5em;color:#374151;",
		"li":               "margin:0.35em 0;font-size:16px;line-height:1.75;color:#374151;",
		"pre":              "margin:1em 0;padding:14px 16px;overflow-x:auto;border-radius:6px;background:" + codeBackground + ";font-size:14px;line-height:1.65;white-space:pre;word-break:normal;",
		"code":             "padding:2px 5px;border-radius:4px;background:" + codeBackground + ";font-family:SFMono-Regular,Consolas,Liberation Mono,Menlo,monospace;font-size:0.9em;color:#be123c;",
		"pre code":         "padding:0;border-radius:0;background:transparent;font-family:SFMono-Regular,Consolas,Liberation Mono,Menlo,monospace;font-size:14px;line-height:1.65;color:#1f2937;white-space:pre;",
		"img":              "display:block;margin:1.2em auto;height:auto;max-width:100%;border:0;",
		"hr":               "margin:1.8em 0;border:0;border-top:1px solid #e5e7eb;",
		"table":            "width:100%;margin:1em 0;border-collapse:collapse;font-size:14px;color:#374151;",
		"thead":            "background:#f3f4f6;",
		"th":               "padding:9px 10px;border:1px solid #d1d5db;font-weight:700;text-align:left;",
		"td":               "padding:9px 10px;border:1px solid #d1d5db;text-align:left;",
		"del":              "text-decoration:line-through;color:#6b7280;",
		"s":                "text-decoration:line-through;color:#6b7280;",
		"emphasis key":     "font-weight:700;color:" + accent + ";",
		"emphasis data":    "padding:1px 3px;background:" + quoteBackground + ";font-weight:700;color:" + accent + ";",
		"emphasis insight": "font-weight:700;color:#111827;border-bottom:2px solid " + accent + ";",
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https: http:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
