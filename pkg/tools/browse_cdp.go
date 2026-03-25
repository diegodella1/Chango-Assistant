package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

// cdpBrowser manages a lazy-initialized headless Chrome instance.
type cdpBrowser struct {
	mu        sync.Mutex
	allocCtx  context.Context
	allocStop context.CancelFunc
	browserCtx context.Context
	browserStop context.CancelFunc
	idleTimer *time.Timer
	workspace string
}

const cdpIdleTimeout = 5 * time.Minute

func newCDPBrowser(workspace string) *cdpBrowser {
	return &cdpBrowser{workspace: workspace}
}

// ensureBrowser starts the headless browser if not already running.
func (b *cdpBrowser) ensureBrowser(ctx context.Context) (context.Context, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Reset idle timer
	if b.idleTimer != nil {
		b.idleTimer.Stop()
	}
	b.idleTimer = time.AfterFunc(cdpIdleTimeout, func() {
		b.Close()
	})

	if b.browserCtx != nil {
		// Check if still alive
		if b.browserCtx.Err() == nil {
			return b.browserCtx, nil
		}
		// Dead, clean up
		b.closeLocked()
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-translate", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux aarch64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"),
	)

	b.allocCtx, b.allocStop = chromedp.NewExecAllocator(context.Background(), opts...)
	b.browserCtx, b.browserStop = chromedp.NewContext(b.allocCtx)

	// Start browser with a simple navigation
	if err := chromedp.Run(b.browserCtx); err != nil {
		b.closeLocked()
		return nil, fmt.Errorf("failed to start browser: %w", err)
	}

	return b.browserCtx, nil
}

// newTab creates a new tab (context) from the browser.
func (b *cdpBrowser) newTab(ctx context.Context) (context.Context, context.CancelFunc, error) {
	browserCtx, err := b.ensureBrowser(ctx)
	if err != nil {
		return nil, nil, err
	}
	tabCtx, tabCancel := chromedp.NewContext(browserCtx)
	return tabCtx, tabCancel, nil
}

func (b *cdpBrowser) closeLocked() {
	if b.browserStop != nil {
		b.browserStop()
	}
	if b.allocStop != nil {
		b.allocStop()
	}
	b.browserCtx = nil
	b.browserStop = nil
	b.allocCtx = nil
	b.allocStop = nil
}

// Close shuts down the browser and frees resources.
func (b *cdpBrowser) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.idleTimer != nil {
		b.idleTimer.Stop()
		b.idleTimer = nil
	}
	b.closeLocked()
}

// doNavigate opens a URL, waits for the page to load (including JS), and returns text content.
func (t *BrowseTool) doNavigate(ctx context.Context, urlStr string, timeoutSec int) *ToolResult {
	tabCtx, tabCancel, err := t.browser.newTab(ctx)
	if err != nil {
		return ErrorResult(fmt.Sprintf("browser error: %v", err))
	}
	defer tabCancel()

	timeout := time.Duration(timeoutSec) * time.Second
	tabCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()

	var title, text, html string
	err = chromedp.Run(tabCtx,
		chromedp.Navigate(urlStr),
		chromedp.WaitReady("body"),
		chromedp.Title(&title),
		chromedp.InnerHTML("body", &html, chromedp.ByQuery),
		chromedp.Text("body", &text, chromedp.ByQuery),
	)
	if err != nil {
		return ErrorResult(fmt.Sprintf("navigate failed: %v", err))
	}

	if len(text) > browseMaxText {
		text = text[:browseMaxText] + "... (truncated)"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Title: %s\n", title)
	fmt.Fprintf(&sb, "URL: %s\n", urlStr)
	fmt.Fprintf(&sb, "Text:\n%s\n", text)
	fmt.Fprintf(&sb, "(rendered with JavaScript)")

	return SilentResult(sb.String())
}

// doClick clicks an element by CSS selector.
func (t *BrowseTool) doClick(ctx context.Context, urlStr, selector string, timeoutSec int) *ToolResult {
	tabCtx, tabCancel, err := t.browser.newTab(ctx)
	if err != nil {
		return ErrorResult(fmt.Sprintf("browser error: %v", err))
	}
	defer tabCancel()

	timeout := time.Duration(timeoutSec) * time.Second
	tabCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()

	var text string
	err = chromedp.Run(tabCtx,
		chromedp.Navigate(urlStr),
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.Click(selector, chromedp.ByQuery),
		chromedp.Sleep(1*time.Second),
		chromedp.Text("body", &text, chromedp.ByQuery),
	)
	if err != nil {
		return ErrorResult(fmt.Sprintf("click failed: %v", err))
	}

	if len(text) > browseMaxText {
		text = text[:browseMaxText] + "... (truncated)"
	}

	return SilentResult(fmt.Sprintf("Clicked %q\n\nPage text:\n%s", selector, text))
}

// doFill fills a form field by CSS selector.
func (t *BrowseTool) doFill(ctx context.Context, urlStr, selector, value string, timeoutSec int) *ToolResult {
	tabCtx, tabCancel, err := t.browser.newTab(ctx)
	if err != nil {
		return ErrorResult(fmt.Sprintf("browser error: %v", err))
	}
	defer tabCancel()

	timeout := time.Duration(timeoutSec) * time.Second
	tabCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()

	err = chromedp.Run(tabCtx,
		chromedp.Navigate(urlStr),
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.Clear(selector, chromedp.ByQuery),
		chromedp.SendKeys(selector, value, chromedp.ByQuery),
	)
	if err != nil {
		return ErrorResult(fmt.Sprintf("fill failed: %v", err))
	}

	return SilentResult(fmt.Sprintf("Filled %q with value (length: %d chars)", selector, len(value)))
}

// doScreenshot captures a screenshot and returns it as media.
func (t *BrowseTool) doScreenshot(ctx context.Context, urlStr string, timeoutSec int) *ToolResult {
	tabCtx, tabCancel, err := t.browser.newTab(ctx)
	if err != nil {
		return ErrorResult(fmt.Sprintf("browser error: %v", err))
	}
	defer tabCancel()

	timeout := time.Duration(timeoutSec) * time.Second
	tabCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()

	var buf []byte
	err = chromedp.Run(tabCtx,
		chromedp.Navigate(urlStr),
		chromedp.WaitReady("body"),
		chromedp.Sleep(2*time.Second),
		chromedp.FullScreenshot(&buf, 80),
	)
	if err != nil {
		return ErrorResult(fmt.Sprintf("screenshot failed: %v", err))
	}

	// Save to workspace temp dir
	tmpDir := filepath.Join(t.browser.workspace, "tmp")
	os.MkdirAll(tmpDir, 0755)
	fname := fmt.Sprintf("screenshot_%d.png", time.Now().UnixMilli())
	fpath := filepath.Join(tmpDir, fname)
	if err := os.WriteFile(fpath, buf, 0644); err != nil {
		// Fallback: return as base64 data URI
		b64 := base64.StdEncoding.EncodeToString(buf)
		return &ToolResult{
			ForLLM: fmt.Sprintf("Screenshot captured (%d bytes)", len(buf)),
			Media:  []string{"data:image/png;base64," + b64},
		}
	}

	return &ToolResult{
		ForLLM: fmt.Sprintf("Screenshot saved: %s (%d bytes)", fpath, len(buf)),
		Media:  []string{fpath},
	}
}

// doWait waits for a CSS selector to appear in the DOM.
func (t *BrowseTool) doWait(ctx context.Context, urlStr, selector string, timeoutSec int) *ToolResult {
	tabCtx, tabCancel, err := t.browser.newTab(ctx)
	if err != nil {
		return ErrorResult(fmt.Sprintf("browser error: %v", err))
	}
	defer tabCancel()

	timeout := time.Duration(timeoutSec) * time.Second
	tabCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()

	var text string
	err = chromedp.Run(tabCtx,
		chromedp.Navigate(urlStr),
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.Text(selector, &text, chromedp.ByQuery),
	)
	if err != nil {
		return ErrorResult(fmt.Sprintf("wait for %q failed: %v", selector, err))
	}

	return SilentResult(fmt.Sprintf("Element %q found. Content: %s", selector, text))
}

// doEval evaluates JavaScript in the page and returns the result.
func (t *BrowseTool) doEval(ctx context.Context, urlStr, script string, timeoutSec int) *ToolResult {
	tabCtx, tabCancel, err := t.browser.newTab(ctx)
	if err != nil {
		return ErrorResult(fmt.Sprintf("browser error: %v", err))
	}
	defer tabCancel()

	timeout := time.Duration(timeoutSec) * time.Second
	tabCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()

	var result interface{}
	err = chromedp.Run(tabCtx,
		chromedp.Navigate(urlStr),
		chromedp.WaitReady("body"),
		chromedp.Evaluate(script, &result),
	)
	if err != nil {
		return ErrorResult(fmt.Sprintf("eval failed: %v", err))
	}

	return SilentResult(fmt.Sprintf("Result: %v", result))
}
