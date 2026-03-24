package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WalletConfig holds configuration for the Lightning wallet tool.
type WalletConfig struct {
	LNbitsURL    string `json:"lnbits_url"`
	AdminKey     string `json:"admin_key"`
	InvoiceKey   string `json:"invoice_key"`
	DailyLimit   int64  `json:"daily_limit_sats"`
	MonthlyLimit int64  `json:"monthly_limit_sats"`
}

// WalletTool implements a Lightning Network wallet via LNbits REST API.
type WalletTool struct {
	lnbitsURL    string
	adminKey     string
	invoiceKey   string
	dailyLimit   int64
	monthlyLimit int64
	client       *http.Client
	mu           sync.Mutex
	spendLog     *SpendLog
	workspace    string
}

// SpendLog tracks outgoing payments for limit enforcement.
type SpendLog struct {
	Entries []SpendEntry `json:"entries"`
}

// SpendEntry records a single outgoing payment.
type SpendEntry struct {
	Amount      int64  `json:"amount"`
	Memo        string `json:"memo"`
	Timestamp   string `json:"timestamp"`
	PaymentHash string `json:"payment_hash"`
}

// NewWalletTool creates a new WalletTool. Returns nil if LNbitsURL or InvoiceKey is empty.
func NewWalletTool(workspace string, cfg WalletConfig) *WalletTool {
	if cfg.LNbitsURL == "" || cfg.InvoiceKey == "" {
		return nil
	}

	dailyLimit := cfg.DailyLimit
	if dailyLimit <= 0 {
		dailyLimit = 10000
	}
	monthlyLimit := cfg.MonthlyLimit
	if monthlyLimit <= 0 {
		monthlyLimit = 100000
	}

	wt := &WalletTool{
		lnbitsURL:    strings.TrimRight(cfg.LNbitsURL, "/"),
		adminKey:     cfg.AdminKey,
		invoiceKey:   cfg.InvoiceKey,
		dailyLimit:   dailyLimit,
		monthlyLimit: monthlyLimit,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		spendLog:  &SpendLog{},
		workspace: workspace,
	}

	wt.loadSpendLog()
	return wt
}

func (t *WalletTool) Name() string { return "wallet" }

func (t *WalletTool) Description() string {
	return "Lightning Network wallet via LNbits. Actions: balance (check sats), send (pay bolt11 invoice), receive (create invoice), history (recent transactions), limits (daily/monthly spend vs limits)."
}

func (t *WalletTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"balance", "send", "receive", "history", "limits"},
				"description": "Action to perform",
			},
			"bolt11": map[string]interface{}{
				"type":        "string",
				"description": "BOLT11 invoice to pay (for send action)",
			},
			"amount": map[string]interface{}{
				"type":        "integer",
				"description": "Amount in sats (for receive action)",
			},
			"memo": map[string]interface{}{
				"type":        "string",
				"description": "Memo/description (for receive action)",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Number of transactions to return (for history action, default 10)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *WalletTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)

	switch action {
	case "balance":
		return t.getBalance(ctx)
	case "send":
		bolt11, _ := args["bolt11"].(string)
		return t.sendPayment(ctx, bolt11)
	case "receive":
		amount, _ := args["amount"].(float64)
		memo, _ := args["memo"].(string)
		return t.createInvoice(ctx, int64(amount), memo)
	case "history":
		limit := 10
		if l, ok := args["limit"].(float64); ok && l > 0 {
			limit = int(l)
		}
		return t.getHistory(ctx, limit)
	case "limits":
		return t.getLimits()
	default:
		return ErrorResult(fmt.Sprintf("unknown wallet action: %s", action))
	}
}

// getBalance checks the wallet balance via LNbits API.
func (t *WalletTool) getBalance(ctx context.Context) *ToolResult {
	req, err := http.NewRequestWithContext(ctx, "GET", t.lnbitsURL+"/api/v1/wallet", nil)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create request: %v", err))
	}
	req.Header.Set("X-Api-Key", t.invoiceKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return ErrorResult(fmt.Sprintf("request failed: %v", err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10000))
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read response: %v", err))
	}

	if resp.StatusCode != http.StatusOK {
		return ErrorResult(fmt.Sprintf("LNbits API error (status %d): %s", resp.StatusCode, string(body)))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return ErrorResult(fmt.Sprintf("failed to parse response: %v", err))
	}

	// LNbits returns balance in msats
	balanceMsats, _ := result["balance"].(float64)
	balanceSats := int64(balanceMsats) / 1000
	name, _ := result["name"].(string)

	return SilentResult(fmt.Sprintf("Wallet: %s\nBalance: %d sats", name, balanceSats))
}

// sendPayment pays a BOLT11 Lightning invoice after checking spend limits.
func (t *WalletTool) sendPayment(ctx context.Context, bolt11 string) *ToolResult {
	if bolt11 == "" {
		return ErrorResult("bolt11 invoice is required for send action")
	}
	if t.adminKey == "" {
		return ErrorResult("admin_key is required for sending payments")
	}

	// Decode the invoice first to check amount
	decodedAmount, err := t.decodeInvoiceAmount(ctx, bolt11)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to decode invoice: %v", err))
	}

	// Check spend limits
	t.mu.Lock()
	dailySpent := t.sumSpend(time.Now(), "daily")
	monthlySpent := t.sumSpend(time.Now(), "monthly")

	if dailySpent+decodedAmount > t.dailyLimit {
		t.mu.Unlock()
		return ErrorResult(fmt.Sprintf("daily limit exceeded: spent %d + %d = %d sats (limit: %d sats)",
			dailySpent, decodedAmount, dailySpent+decodedAmount, t.dailyLimit))
	}
	if monthlySpent+decodedAmount > t.monthlyLimit {
		t.mu.Unlock()
		return ErrorResult(fmt.Sprintf("monthly limit exceeded: spent %d + %d = %d sats (limit: %d sats)",
			monthlySpent, decodedAmount, monthlySpent+decodedAmount, t.monthlyLimit))
	}
	t.mu.Unlock()

	// Pay the invoice
	payload := fmt.Sprintf(`{"out": true, "bolt11": %q}`, bolt11)
	req, err := http.NewRequestWithContext(ctx, "POST", t.lnbitsURL+"/api/v1/payments", strings.NewReader(payload))
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create request: %v", err))
	}
	req.Header.Set("X-Api-Key", t.adminKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return ErrorResult(fmt.Sprintf("payment request failed: %v", err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10000))
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read response: %v", err))
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return ErrorResult(fmt.Sprintf("payment failed (status %d): %s", resp.StatusCode, string(body)))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return ErrorResult(fmt.Sprintf("failed to parse payment response: %v", err))
	}

	paymentHash, _ := result["payment_hash"].(string)

	// Log the spend
	t.mu.Lock()
	t.spendLog.Entries = append(t.spendLog.Entries, SpendEntry{
		Amount:      decodedAmount,
		Memo:        "outgoing payment",
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		PaymentHash: paymentHash,
	})
	t.saveSpendLog()
	t.mu.Unlock()

	return SilentResult(fmt.Sprintf("Payment sent: %d sats\nPayment hash: %s", decodedAmount, paymentHash))
}

// createInvoice creates a Lightning invoice for receiving payments.
func (t *WalletTool) createInvoice(ctx context.Context, amount int64, memo string) *ToolResult {
	if amount <= 0 {
		return ErrorResult("amount must be greater than 0")
	}
	if memo == "" {
		memo = "PicoClaw invoice"
	}

	payload := fmt.Sprintf(`{"out": false, "amount": %d, "memo": %q}`, amount, memo)
	req, err := http.NewRequestWithContext(ctx, "POST", t.lnbitsURL+"/api/v1/payments", strings.NewReader(payload))
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create request: %v", err))
	}
	req.Header.Set("X-Api-Key", t.invoiceKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return ErrorResult(fmt.Sprintf("request failed: %v", err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10000))
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read response: %v", err))
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return ErrorResult(fmt.Sprintf("invoice creation failed (status %d): %s", resp.StatusCode, string(body)))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return ErrorResult(fmt.Sprintf("failed to parse response: %v", err))
	}

	paymentHash, _ := result["payment_hash"].(string)
	paymentRequest, _ := result["payment_request"].(string)

	return SilentResult(fmt.Sprintf("Invoice created: %d sats\nMemo: %s\nPayment hash: %s\nBOLT11: %s",
		amount, memo, paymentHash, paymentRequest))
}

// getHistory returns recent transactions from LNbits.
func (t *WalletTool) getHistory(ctx context.Context, limit int) *ToolResult {
	url := fmt.Sprintf("%s/api/v1/payments?limit=%d", t.lnbitsURL, limit)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create request: %v", err))
	}
	req.Header.Set("X-Api-Key", t.invoiceKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return ErrorResult(fmt.Sprintf("request failed: %v", err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50000))
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read response: %v", err))
	}

	if resp.StatusCode != http.StatusOK {
		return ErrorResult(fmt.Sprintf("LNbits API error (status %d): %s", resp.StatusCode, string(body)))
	}

	var payments []map[string]interface{}
	if err := json.Unmarshal(body, &payments); err != nil {
		return ErrorResult(fmt.Sprintf("failed to parse response: %v", err))
	}

	if len(payments) == 0 {
		return SilentResult("No transactions found.")
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Last %d transactions:\n\n", len(payments)))
	for i, p := range payments {
		amountMsats, _ := p["amount"].(float64)
		amountSats := int64(amountMsats) / 1000
		memo, _ := p["memo"].(string)
		pending, _ := p["pending"].(bool)
		createdAt, _ := p["created_at"].(string)

		direction := "IN"
		if amountSats < 0 {
			direction = "OUT"
			amountSats = -amountSats
		}

		status := "completed"
		if pending {
			status = "pending"
		}

		sb.WriteString(fmt.Sprintf("%d. [%s] %d sats — %s (%s) %s\n",
			i+1, direction, amountSats, memo, status, createdAt))
	}

	return SilentResult(sb.String())
}

// getLimits shows current daily/monthly spend vs limits.
func (t *WalletTool) getLimits() *ToolResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	dailySpent := t.sumSpend(now, "daily")
	monthlySpent := t.sumSpend(now, "monthly")

	return SilentResult(fmt.Sprintf(
		"Spend limits:\n"+
			"  Daily:   %d / %d sats (remaining: %d)\n"+
			"  Monthly: %d / %d sats (remaining: %d)",
		dailySpent, t.dailyLimit, t.dailyLimit-dailySpent,
		monthlySpent, t.monthlyLimit, t.monthlyLimit-monthlySpent,
	))
}

// decodeInvoiceAmount fetches the invoice amount by calling LNbits decode endpoint,
// or falls back to paying and checking. Returns amount in sats.
func (t *WalletTool) decodeInvoiceAmount(ctx context.Context, bolt11 string) (int64, error) {
	payload := fmt.Sprintf(`{"data": %q}`, bolt11)
	req, err := http.NewRequestWithContext(ctx, "POST", t.lnbitsURL+"/api/v1/payments/decode", strings.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Api-Key", t.invoiceKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("decode request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10000))
	if err != nil {
		return 0, fmt.Errorf("failed to read decode response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("decode failed (status %d): %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("failed to parse decode response: %w", err)
	}

	// LNbits decode returns amount in msats
	if amountMsats, ok := result["amount_msat"].(float64); ok {
		return int64(amountMsats) / 1000, nil
	}
	// Fallback: some versions return amount in sats directly
	if amountSats, ok := result["amount"].(float64); ok {
		return int64(amountSats), nil
	}

	return 0, fmt.Errorf("could not determine invoice amount from decode response")
}

// sumSpend calculates total spend for a given period ("daily" or "monthly").
// Must be called with t.mu held.
func (t *WalletTool) sumSpend(now time.Time, period string) int64 {
	var total int64
	for _, e := range t.spendLog.Entries {
		ts, err := time.Parse(time.RFC3339, e.Timestamp)
		if err != nil {
			continue
		}
		switch period {
		case "daily":
			if ts.Year() == now.Year() && ts.YearDay() == now.YearDay() {
				total += e.Amount
			}
		case "monthly":
			if ts.Year() == now.Year() && ts.Month() == now.Month() {
				total += e.Amount
			}
		}
	}
	return total
}

// spendLogPath returns the file path for the spend log.
func (t *WalletTool) spendLogPath() string {
	return filepath.Join(t.workspace, "state", "wallet_spend.json")
}

// loadSpendLog reads the spend log from disk.
func (t *WalletTool) loadSpendLog() {
	data, err := os.ReadFile(t.spendLogPath())
	if err != nil {
		return
	}
	var log SpendLog
	if err := json.Unmarshal(data, &log); err != nil {
		return
	}
	t.spendLog = &log
}

// saveSpendLog writes the spend log to disk atomically.
// Must be called with t.mu held.
func (t *WalletTool) saveSpendLog() {
	dir := filepath.Dir(t.spendLogPath())
	os.MkdirAll(dir, 0755)

	data, err := json.MarshalIndent(t.spendLog, "", "  ")
	if err != nil {
		return
	}

	tmp := t.spendLogPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	if err := os.Rename(tmp, t.spendLogPath()); err != nil {
		os.Remove(tmp)
	}
}
