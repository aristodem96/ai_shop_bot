package monobank

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

// fakeMonobank is the acquiring API in miniature, with a real ECDSA key.
type fakeMonobank struct {
	key        *ecdsa.PrivateKey
	created    map[string]any
	keyFetches atomic.Int32
	token      string
}

func newFakeMonobank(t *testing.T) (*fakeMonobank, *Client) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	f := &fakeMonobank{key: key}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.token = r.Header.Get("X-Token")
		switch r.URL.Path {
		case "/api/merchant/invoice/create":
			_ = json.NewDecoder(r.Body).Decode(&f.created)
			_, _ = io.WriteString(w, `{"invoiceId":"p2_9ZgpZVsl3","pageUrl":"https://pay.mbnk.biz/p2_9ZgpZVsl3"}`)
		case "/api/merchant/invoice/status":
			if r.URL.Query().Get("invoiceId") != "p2_9ZgpZVsl3" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"errCode":"NOT_FOUND","errText":"invoice not found"}`)
				return
			}
			_, _ = io.WriteString(w, `{"invoiceId":"p2_9ZgpZVsl3","status":"success","amount":145000,"ccy":980,
				"finalAmount":145000,"reference":"17","modifiedDate":"2026-10-15T10:00:05Z"}`)
		case "/api/merchant/pubkey":
			f.keyFetches.Add(1)
			_, _ = io.WriteString(w, `{"key":"`+f.publicKey(t)+`"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	return f, New(Config{BaseURL: srv.URL, Token: "test-token",
		WebhookURL: "https://shop.example/webhooks/monobank", RedirectURL: "https://t.me/shopexampleaibot"})
}

func (f *fakeMonobank) publicKey(t *testing.T) string {
	der, err := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func (f *fakeMonobank) sign(t *testing.T, body []byte) string {
	digest := sha256.Sum256(body)
	sig, err := ecdsa.SignASN1(rand.Reader, f.key, digest[:])
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(sig)
}

func TestCreateInvoice(t *testing.T) {
	f, c := newFakeMonobank(t)

	inv, err := c.CreateInvoice(context.Background(), service.InvoiceRequest{
		OrderID: 17, Amount: domain.UAH(1450), Destination: "Замовлення №17", Validity: 15 * time.Minute,
	})
	require.NoError(t, err)
	assert.Equal(t, "p2_9ZgpZVsl3", inv.ID)
	assert.Equal(t, "https://pay.mbnk.biz/p2_9ZgpZVsl3", inv.URL)

	assert.Equal(t, "test-token", f.token, "the token goes in X-Token")
	assert.EqualValues(t, 145000, f.created["amount"], "amount in kopiyky")
	assert.EqualValues(t, 980, f.created["ccy"])
	assert.EqualValues(t, 900, f.created["validity"], "the page lives as long as the reservation")
	assert.Equal(t, "https://shop.example/webhooks/monobank", f.created["webHookUrl"])
	assert.Equal(t, map[string]any{"reference": "17", "destination": "Замовлення №17"}, f.created["merchantPaymInfo"])
}

func TestInvoiceStatus(t *testing.T) {
	_, c := newFakeMonobank(t)

	ev, err := c.InvoiceStatus(context.Background(), "p2_9ZgpZVsl3")
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentSuccess, ev.Status)
	assert.Equal(t, domain.UAH(1450), ev.Amount)
	assert.Equal(t, "17", ev.Reference)
	assert.Equal(t, time.Date(2026, 10, 15, 10, 0, 5, 0, time.UTC), ev.ModifiedAt.UTC())

	_, err = c.InvoiceStatus(context.Background(), "nope")
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Status)
	assert.Equal(t, "NOT_FOUND", apiErr.Code)
}

const webhookBody = `{"invoiceId":"p2_9ZgpZVsl3","status":"success","amount":145000,"ccy":980,` +
	`"reference":"17","modifiedDate":"2026-10-15T10:00:05Z"}`

func post(h http.Handler, body, sign string) int {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/monobank", bytes.NewBufferString(body))
	req.Header.Set("X-Sign", sign)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestWebhookVerifiesAndDispatches(t *testing.T) {
	f, c := newFakeMonobank(t)
	var got []domain.PaymentEvent
	h := c.WebhookHandler(func(_ context.Context, ev domain.PaymentEvent) error {
		got = append(got, ev)
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.Equal(t, http.StatusOK, post(h, webhookBody, f.sign(t, []byte(webhookBody))))
	require.Len(t, got, 1)
	assert.Equal(t, domain.PaymentSuccess, got[0].Status)
	assert.Equal(t, int32(1), f.keyFetches.Load())

	assert.Equal(t, http.StatusOK, post(h, webhookBody, f.sign(t, []byte(webhookBody))))
	assert.Equal(t, int32(1), f.keyFetches.Load(), "the key is cached")
}

func TestWebhookRejectsForgeries(t *testing.T) {
	f, c := newFakeMonobank(t)
	called := false
	h := c.WebhookHandler(func(context.Context, domain.PaymentEvent) error {
		called = true
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	sig := f.sign(t, []byte(webhookBody))
	tampered := bytes.Replace([]byte(webhookBody), []byte("145000"), []byte("100"), 1)

	assert.Equal(t, http.StatusBadRequest, post(h, string(tampered), sig), "a changed amount breaks the signature")
	assert.Equal(t, http.StatusBadRequest, post(h, webhookBody, ""), "no signature")
	assert.Equal(t, http.StatusBadRequest, post(h, webhookBody, "not base64!"), "garbage signature")

	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(webhookBody))
	forged, err := ecdsa.SignASN1(rand.Reader, other, digest[:])
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, post(h, webhookBody, base64.StdEncoding.EncodeToString(forged)),
		"signed with someone else's key")

	assert.False(t, called, "nothing unverified reaches the orders")
}

func TestWebhookRefetchesARotatedKey(t *testing.T) {
	f, c := newFakeMonobank(t)
	h := c.WebhookHandler(func(context.Context, domain.PaymentEvent) error { return nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.Equal(t, http.StatusOK, post(h, webhookBody, f.sign(t, []byte(webhookBody))))

	rotated, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	f.key = rotated

	assert.Equal(t, http.StatusOK, post(h, webhookBody, f.sign(t, []byte(webhookBody))))
	assert.Equal(t, int32(2), f.keyFetches.Load(), "a failed check fetches the key again")
}

func TestWebhookHandlerFailureAsksForRetry(t *testing.T) {
	f, c := newFakeMonobank(t)
	h := c.WebhookHandler(func(context.Context, domain.PaymentEvent) error {
		return errors.New("postgres: connection refused")
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.Equal(t, http.StatusInternalServerError, post(h, webhookBody, f.sign(t, []byte(webhookBody))),
		"Monobank retries on 5xx")
}
