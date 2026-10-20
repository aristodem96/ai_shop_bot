// Package monobank is a client of the Monobank acquiring API: invoices,
// their status, and the signed webhooks reporting on them.
//
// Hand-written, like the Telegram client: three endpoints and a signature
// check do not justify a dependency, and the whole exchange is easy to
// exercise against httptest.
package monobank

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

// DefaultBaseURL is the production API. A test token issued at
// api.monobank.ua works against it too and moves no real money.
const DefaultBaseURL = "https://api.monobank.ua"

// uah is the ISO 4217 code of the hryvnia.
const uah = 980

// Config configures the client.
type Config struct {
	BaseURL string
	Token   string
	// WebhookURL is where Monobank posts status changes. Empty means no
	// webhook: payments are then picked up by the status poll alone.
	WebhookURL string
	// RedirectURL is where the payment page sends the buyer afterwards --
	// the bot's chat.
	RedirectURL string
}

// Client talks to the acquiring API.
type Client struct {
	cfg  Config
	http *http.Client
	keys *keyCache
}

var _ service.PaymentProvider = (*Client)(nil)

// New returns a client.
func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	c := &Client{cfg: cfg, http: &http.Client{Timeout: 15 * time.Second}}
	c.keys = &keyCache{fetch: c.fetchPublicKey}
	return c
}

// CreateInvoice creates a payment page for an order.
func (c *Client) CreateInvoice(ctx context.Context, req service.InvoiceRequest) (*service.Invoice, error) {
	body := map[string]any{
		"amount": int64(req.Amount),
		"ccy":    uah,
		// The order id travels as the reference and comes back in every
		// webhook, which is how an order is found even if the invoice id
		// never got saved.
		"merchantPaymInfo": map[string]any{
			"reference":   strconv.FormatInt(req.OrderID, 10),
			"destination": req.Destination,
		},
		"validity": int64(req.Validity.Seconds()),
	}
	if c.cfg.WebhookURL != "" {
		body["webHookUrl"] = c.cfg.WebhookURL
	}
	if c.cfg.RedirectURL != "" {
		body["redirectUrl"] = c.cfg.RedirectURL
	}

	var out struct {
		InvoiceID string `json:"invoiceId"`
		PageURL   string `json:"pageUrl"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/merchant/invoice/create", body, &out); err != nil {
		return nil, err
	}
	if out.InvoiceID == "" || out.PageURL == "" {
		return nil, errors.New("monobank: invoice created without an id or a page")
	}
	return &service.Invoice{ID: out.InvoiceID, URL: out.PageURL}, nil
}

// statusBody is an invoice's status: the reply of the status endpoint and,
// field for field, the body of a webhook.
type statusBody struct {
	InvoiceID     string    `json:"invoiceId"`
	Status        string    `json:"status"`
	Amount        int64     `json:"amount"`
	Ccy           int       `json:"ccy"`
	FinalAmount   int64     `json:"finalAmount"`
	Reference     string    `json:"reference"`
	ModifiedDate  time.Time `json:"modifiedDate"`
	FailureReason string    `json:"failureReason"`
}

func (s statusBody) event() (*domain.PaymentEvent, error) {
	if s.InvoiceID == "" || s.Status == "" || s.ModifiedDate.IsZero() {
		return nil, errors.New("monobank: status without invoice id, status or date")
	}
	amount := s.Amount
	if s.FinalAmount > 0 {
		amount = s.FinalAmount
	}
	if s.Ccy != 0 && s.Ccy != uah {
		// The shop invoices in hryvnias only; anything else cannot match
		// an order total, and is left for the amount check to flag.
		amount = -1
	}
	return &domain.PaymentEvent{
		InvoiceID:  s.InvoiceID,
		Reference:  s.Reference,
		Status:     domain.PaymentStatus(s.Status),
		Amount:     domain.Money(amount),
		ModifiedAt: s.ModifiedDate,
	}, nil
}

// InvoiceStatus asks for an invoice's current status.
func (c *Client) InvoiceStatus(ctx context.Context, invoiceID string) (*domain.PaymentEvent, error) {
	var s statusBody
	if err := c.do(ctx, http.MethodGet, "/api/merchant/invoice/status?invoiceId="+url.QueryEscape(invoiceID), nil, &s); err != nil {
		return nil, err
	}
	return s.event()
}

// RemoveInvoice invalidates an unpaid invoice: its payment page stops
// taking payments.
func (c *Client) RemoveInvoice(ctx context.Context, invoiceID string) error {
	var out struct{}
	return c.do(ctx, http.MethodPost, "/api/merchant/invoice/remove", map[string]string{"invoiceId": invoiceID}, &out)
}

func (c *Client) fetchPublicKey(ctx context.Context) (string, error) {
	var out struct {
		Key string `json:"key"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/merchant/pubkey", nil, &out); err != nil {
		return "", err
	}
	return out.Key, nil
}

// APIError is an error reply from Monobank.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("monobank: HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("monobank: encode request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("monobank: build request: %w", err)
	}
	// The token goes in a header, never in the URL, so transport errors --
	// which quote the URL -- cannot leak it into the logs.
	req.Header.Set("X-Token", c.cfg.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("monobank: %s %s: %w", method, strings.SplitN(path, "?", 2)[0], err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("monobank: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		apiErr := &APIError{Status: resp.StatusCode}
		var e struct {
			ErrCode string `json:"errCode"`
			ErrText string `json:"errText"`
		}
		if json.Unmarshal(raw, &e) == nil {
			apiErr.Code, apiErr.Message = e.ErrCode, e.ErrText
		}
		return apiErr
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("monobank: decode response: %w", err)
	}
	return nil
}
