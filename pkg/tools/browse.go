package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const (
	browseMaxBodySize = 500 * 1024 // 500KB
	browseMaxText     = 5000
	browseUserAgent   = "Mozilla/5.0 (X11; Linux aarch64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// BrowseTool provides structured web browsing: fetch pages, extract links/forms, submit forms.
// Actions: fetch/extract_links/extract_forms/submit use HTTP (fast). navigate/click/fill/screenshot/wait/eval use headless Chrome (JS-capable).
type BrowseTool struct {
	client      *http.Client
	maxBodySize int
	userAgent   string
	browser     *cdpBrowser // lazy-initialized headless Chrome
}

// pageLink represents an extracted <a> element.
type pageLink struct {
	Text string
	Href string
}

// formField represents an <input>, <select>, or <textarea> inside a form.
type formField struct {
	Name     string
	Type     string // text, email, password, hidden, submit, select, textarea, etc.
	Value    string
	Required bool
}

// pageForm represents an extracted <form> element.
type pageForm struct {
	Action string
	Method string
	Fields []formField
}

// pageData holds the structured extraction of a page.
type pageData struct {
	Title    string
	FinalURL string
	Text     string
	Links    []pageLink
	Forms    []pageForm
}

func NewBrowseTool(workspace string) *BrowseTool {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:     jar,
		Timeout: 15 * time.Second,
	}
	return &BrowseTool{
		client:      client,
		maxBodySize: browseMaxBodySize,
		userAgent:   browseUserAgent,
		browser:     newCDPBrowser(workspace),
	}
}

// Close releases browser resources. Call on shutdown.
func (t *BrowseTool) Close() {
	if t.browser != nil {
		t.browser.Close()
	}
}

func (t *BrowseTool) Name() string        { return "browse" }
func (t *BrowseTool) Description() string {
	return "Browse web pages. HTTP actions (fast): fetch, extract_links, extract_forms, submit. " +
		"Browser actions (JS-capable): navigate, click, fill, screenshot, wait, eval."
}

func (t *BrowseTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Action: fetch/extract_links/extract_forms/submit (HTTP), navigate/click/fill/screenshot/wait/eval (browser+JS)",
				"enum":        []string{"fetch", "extract_links", "extract_forms", "submit", "navigate", "click", "fill", "screenshot", "wait", "eval"},
			},
			"url": map[string]interface{}{
				"type":        "string",
				"description": "URL to navigate to",
			},
			"method": map[string]interface{}{
				"type":        "string",
				"description": "HTTP method for submit (POST or GET). Default: POST",
			},
			"fields": map[string]interface{}{
				"type":        "object",
				"description": "Form fields as key-value pairs for submit action",
			},
			"selector": map[string]interface{}{
				"type":        "string",
				"description": "CSS selector for click/fill/wait actions",
			},
			"value": map[string]interface{}{
				"type":        "string",
				"description": "Text value for fill action",
			},
			"script": map[string]interface{}{
				"type":        "string",
				"description": "JavaScript code for eval action",
			},
			"timeout": map[string]interface{}{
				"type":        "number",
				"description": "Timeout in seconds for browser actions (default: 30)",
			},
		},
		"required": []string{"action", "url"},
	}
}

func (t *BrowseTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)
	urlStr, _ := args["url"].(string)

	if action == "" {
		return ErrorResult("action is required (fetch, extract_links, extract_forms, submit)")
	}
	if urlStr == "" {
		return ErrorResult("url is required")
	}

	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return ErrorResult(fmt.Sprintf("invalid URL: %v", err))
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return ErrorResult("only http/https URLs are allowed")
	}
	if parsedURL.Host == "" {
		return ErrorResult("missing host in URL")
	}

	// Parse timeout for browser actions (default 30s)
	timeoutSec := 30
	if t, ok := args["timeout"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}
	selector, _ := args["selector"].(string)

	switch action {
	// HTTP actions (fast, no JS)
	case "fetch":
		return t.doFetch(ctx, urlStr)
	case "extract_links":
		return t.doExtractLinks(ctx, urlStr)
	case "extract_forms":
		return t.doExtractForms(ctx, urlStr)
	case "submit":
		return t.doSubmit(ctx, urlStr, args)

	// Browser actions (headless Chrome, JS-capable)
	case "navigate":
		return t.doNavigate(ctx, urlStr, timeoutSec)
	case "click":
		if selector == "" {
			return ErrorResult("selector is required for click action")
		}
		return t.doClick(ctx, urlStr, selector, timeoutSec)
	case "fill":
		if selector == "" {
			return ErrorResult("selector is required for fill action")
		}
		value, _ := args["value"].(string)
		return t.doFill(ctx, urlStr, selector, value, timeoutSec)
	case "screenshot":
		return t.doScreenshot(ctx, urlStr, timeoutSec)
	case "wait":
		if selector == "" {
			return ErrorResult("selector is required for wait action")
		}
		return t.doWait(ctx, urlStr, selector, timeoutSec)
	case "eval":
		script, _ := args["script"].(string)
		if script == "" {
			return ErrorResult("script is required for eval action")
		}
		return t.doEval(ctx, urlStr, script, timeoutSec)

	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

// doFetch GETs a page and returns structured summary.
func (t *BrowseTool) doFetch(ctx context.Context, urlStr string) *ToolResult {
	page, err := t.fetchAndParse(ctx, urlStr)
	if err != nil {
		return ErrorResult(fmt.Sprintf("fetch failed: %v", err))
	}

	text := page.Text
	if len(text) > browseMaxText {
		text = text[:browseMaxText] + "... (truncated)"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Title: %s\n", page.Title)
	fmt.Fprintf(&sb, "URL: %s\n", page.FinalURL)
	fmt.Fprintf(&sb, "Text:\n%s\n", text)
	fmt.Fprintf(&sb, "Links: %d found", len(page.Links))
	if len(page.Links) > 0 {
		sb.WriteString(" (use extract_links for full list)")
	}
	fmt.Fprintf(&sb, "\nForms: %d found", len(page.Forms))
	if len(page.Forms) > 0 {
		sb.WriteString(" (use extract_forms for details)")
	}

	return SilentResult(sb.String())
}

// doExtractLinks GETs a page and returns all links.
func (t *BrowseTool) doExtractLinks(ctx context.Context, urlStr string) *ToolResult {
	page, err := t.fetchAndParse(ctx, urlStr)
	if err != nil {
		return ErrorResult(fmt.Sprintf("fetch failed: %v", err))
	}

	if len(page.Links) == 0 {
		return SilentResult(fmt.Sprintf("No links found on %s", page.FinalURL))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Links on %s (%d total):\n", page.FinalURL, len(page.Links))
	for i, link := range page.Links {
		text := strings.TrimSpace(link.Text)
		if text == "" {
			text = "(no text)"
		}
		fmt.Fprintf(&sb, "%d. [%s](%s)\n", i+1, text, link.Href)
	}

	return SilentResult(sb.String())
}

// doExtractForms GETs a page and returns form details.
func (t *BrowseTool) doExtractForms(ctx context.Context, urlStr string) *ToolResult {
	page, err := t.fetchAndParse(ctx, urlStr)
	if err != nil {
		return ErrorResult(fmt.Sprintf("fetch failed: %v", err))
	}

	if len(page.Forms) == 0 {
		return SilentResult(fmt.Sprintf("No forms found on %s", page.FinalURL))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Forms on %s (%d total):\n\n", page.FinalURL, len(page.Forms))
	for i, form := range page.Forms {
		method := strings.ToUpper(form.Method)
		if method == "" {
			method = "GET"
		}
		fmt.Fprintf(&sb, "Form %d: %s %s\n", i+1, method, form.Action)
		if len(form.Fields) == 0 {
			sb.WriteString("  (no fields)\n")
		}
		for _, f := range form.Fields {
			req := ""
			if f.Required {
				req = ", required"
			}
			val := ""
			if f.Value != "" {
				val = fmt.Sprintf(", value: %q", f.Value)
			}
			name := f.Name
			if name == "" {
				name = "(unnamed)"
			}
			fmt.Fprintf(&sb, "  - %s (%s%s%s)\n", name, f.Type, req, val)
		}
		sb.WriteString("\n")
	}

	return SilentResult(sb.String())
}

// doSubmit submits a form (POST or GET) and returns the response page.
func (t *BrowseTool) doSubmit(ctx context.Context, urlStr string, args map[string]interface{}) *ToolResult {
	method := "POST"
	if m, ok := args["method"].(string); ok && m != "" {
		method = strings.ToUpper(m)
	}
	if method != "POST" && method != "GET" {
		return ErrorResult("method must be POST or GET")
	}

	fields, _ := args["fields"].(map[string]interface{})
	if fields == nil {
		return ErrorResult("fields is required for submit (key-value pairs)")
	}

	// Encode form values
	formData := url.Values{}
	for k, v := range fields {
		formData.Set(k, fmt.Sprintf("%v", v))
	}

	var req *http.Request
	var err error

	if method == "GET" {
		parsedURL, _ := url.Parse(urlStr)
		parsedURL.RawQuery = formData.Encode()
		req, err = http.NewRequestWithContext(ctx, "GET", parsedURL.String(), nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, "POST", urlStr, strings.NewReader(formData.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create request: %v", err))
	}

	req.Header.Set("User-Agent", t.userAgent)

	resp, err := t.client.Do(req)
	if err != nil {
		return ErrorResult(fmt.Sprintf("submit failed: %v", err))
	}
	defer resp.Body.Close()

	body, err := t.readBody(resp.Body)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read response: %v", err))
	}

	page := t.parseHTML(body, resp.Request.URL)
	page.FinalURL = resp.Request.URL.String()

	text := page.Text
	if len(text) > browseMaxText {
		text = text[:browseMaxText] + "... (truncated)"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Submit Result (HTTP %d)\n", resp.StatusCode)
	fmt.Fprintf(&sb, "Title: %s\n", page.Title)
	fmt.Fprintf(&sb, "URL: %s\n", page.FinalURL)
	fmt.Fprintf(&sb, "Text:\n%s\n", text)
	fmt.Fprintf(&sb, "Links: %d found\n", len(page.Links))
	fmt.Fprintf(&sb, "Forms: %d found\n", len(page.Forms))

	return SilentResult(sb.String())
}

// fetchAndParse performs a GET and parses the HTML response.
func (t *BrowseTool) fetchAndParse(ctx context.Context, urlStr string) (*pageData, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", t.userAgent)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}

	body, err := t.readBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	page := t.parseHTML(body, resp.Request.URL)
	page.FinalURL = resp.Request.URL.String()

	return page, nil
}

// readBody reads the response body up to maxBodySize.
func (t *BrowseTool) readBody(r io.Reader) (string, error) {
	limited := io.LimitReader(r, int64(t.maxBodySize))
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// parseHTML parses an HTML string and extracts structured data.
func (t *BrowseTool) parseHTML(body string, baseURL *url.URL) *pageData {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return &pageData{Text: body}
	}

	page := &pageData{}

	// Extract title
	page.Title = t.extractTitle(doc)

	// Extract text content
	var textBuf strings.Builder
	t.extractText(doc, &textBuf)
	page.Text = strings.TrimSpace(textBuf.String())

	// Extract links
	page.Links = t.extractLinks(doc, baseURL)

	// Extract forms
	page.Forms = t.extractForms(doc, baseURL)

	return page
}

// extractTitle finds the <title> content.
func (t *BrowseTool) extractTitle(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "title" {
		return t.innerText(n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if title := t.extractTitle(c); title != "" {
			return title
		}
	}
	return ""
}

// skipTags are elements whose text content should be ignored.
var skipTags = map[string]bool{
	"script":   true,
	"style":    true,
	"noscript": true,
	"svg":      true,
	"head":     true,
}

// blockTags get a newline before/after their content.
var blockTags = map[string]bool{
	"p": true, "div": true, "br": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "li": true,
	"tr": true, "blockquote": true, "pre": true, "section": true,
	"article": true, "header": true, "footer": true, "nav": true,
	"main": true, "aside": true, "dl": true, "dt": true, "dd": true,
}

// extractText walks the DOM and writes visible text to buf.
func (t *BrowseTool) extractText(n *html.Node, buf *strings.Builder) {
	if n.Type == html.ElementNode && skipTags[n.Data] {
		return
	}
	if n.Type == html.TextNode {
		text := strings.TrimSpace(n.Data)
		if text != "" {
			if buf.Len() > 0 {
				buf.WriteByte(' ')
			}
			buf.WriteString(text)
		}
		return
	}
	if n.Type == html.ElementNode && blockTags[n.Data] {
		buf.WriteByte('\n')
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		t.extractText(c, buf)
	}
	if n.Type == html.ElementNode && blockTags[n.Data] {
		buf.WriteByte('\n')
	}
}

// innerText returns the concatenated text content of a node.
func (t *BrowseTool) innerText(n *html.Node) string {
	var buf strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			buf.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(buf.String())
}

// extractLinks finds all <a href="..."> elements.
func (t *BrowseTool) extractLinks(n *html.Node, baseURL *url.URL) []pageLink {
	var links []pageLink
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			href := t.getAttr(n, "href")
			if href != "" {
				resolved := t.resolveURL(href, baseURL)
				text := t.innerText(n)
				links = append(links, pageLink{Text: text, Href: resolved})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return links
}

// extractForms finds all <form> elements with their fields.
func (t *BrowseTool) extractForms(n *html.Node, baseURL *url.URL) []pageForm {
	var forms []pageForm
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "form" {
			form := pageForm{
				Action: t.resolveURL(t.getAttr(n, "action"), baseURL),
				Method: strings.ToUpper(t.getAttr(n, "method")),
			}
			if form.Method == "" {
				form.Method = "GET"
			}
			form.Fields = t.extractFormFields(n)
			forms = append(forms, form)
			return // don't recurse into form children for more forms
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return forms
}

// extractFormFields finds input, select, and textarea elements inside a form node.
func (t *BrowseTool) extractFormFields(formNode *html.Node) []formField {
	var fields []formField
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "input":
				ftype := t.getAttr(n, "type")
				if ftype == "" {
					ftype = "text"
				}
				fields = append(fields, formField{
					Name:     t.getAttr(n, "name"),
					Type:     ftype,
					Value:    t.getAttr(n, "value"),
					Required: t.hasAttr(n, "required"),
				})
			case "select":
				fields = append(fields, formField{
					Name:     t.getAttr(n, "name"),
					Type:     "select",
					Value:    t.getSelectedValue(n),
					Required: t.hasAttr(n, "required"),
				})
			case "textarea":
				fields = append(fields, formField{
					Name:     t.getAttr(n, "name"),
					Type:     "textarea",
					Value:    t.innerText(n),
					Required: t.hasAttr(n, "required"),
				})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(formNode)
	return fields
}

// getSelectedValue returns the value of the first <option selected> or first <option>.
func (t *BrowseTool) getSelectedValue(selectNode *html.Node) string {
	var firstValue string
	var walk func(*html.Node) string
	walk = func(n *html.Node) string {
		if n.Type == html.ElementNode && n.Data == "option" {
			val := t.getAttr(n, "value")
			if val == "" {
				val = t.innerText(n)
			}
			if firstValue == "" {
				firstValue = val
			}
			if t.hasAttr(n, "selected") {
				return val
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if v := walk(c); v != "" {
				return v
			}
		}
		return ""
	}
	if v := walk(selectNode); v != "" {
		return v
	}
	return firstValue
}

// getAttr returns the value of a named attribute, or "".
func (t *BrowseTool) getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// hasAttr checks if a node has a named attribute (boolean attrs like "required").
func (t *BrowseTool) hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

// resolveURL resolves a possibly-relative href against the base URL.
func (t *BrowseTool) resolveURL(href string, baseURL *url.URL) string {
	if href == "" {
		if baseURL != nil {
			return baseURL.String()
		}
		return ""
	}
	parsed, err := url.Parse(href)
	if err != nil {
		return href
	}
	if baseURL != nil {
		return baseURL.ResolveReference(parsed).String()
	}
	return href
}
