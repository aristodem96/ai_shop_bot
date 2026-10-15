package monobank

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// maxWebhookBody bounds what the handler reads. A status body is well under
// a kilobyte; anything near this is not from Monobank.
const maxWebhookBody = 64 << 10

// WebhookHandler verifies and dispatches Monobank's status webhooks.
//
// The signature is checked over the raw body before anything is parsed: the
// endpoint is public, and a forged "success" would otherwise mark an order
// paid. A body that fails verification gets 400; one whose handling fails
// gets 500, which makes Monobank retry it; anything else gets 200, including
// duplicates, since handling is idempotent.
func (c *Client) WebhookHandler(handle func(context.Context, domain.PaymentEvent) error, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		if err := c.keys.verify(r.Context(), body, r.Header.Get("X-Sign")); err != nil {
			log.WarnContext(r.Context(), "rejected monobank webhook", slog.Any("error", err))
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var s statusBody
		if err := json.Unmarshal(body, &s); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ev, err := s.event()
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := handle(r.Context(), *ev); err != nil {
			log.ErrorContext(r.Context(), "could not apply monobank webhook",
				slog.String("invoice_id", ev.InvoiceID), slog.Any("error", err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// ErrBadSignature means a webhook's X-Sign does not match its body.
var ErrBadSignature = errors.New("monobank: bad webhook signature")

// keyCache holds Monobank's signing key. It is fetched on first use and again
// when a signature fails, as Monobank's docs advise: the key may rotate.
type keyCache struct {
	mu    sync.Mutex
	key   *ecdsa.PublicKey
	fetch func(context.Context) (string, error)
}

func (k *keyCache) verify(ctx context.Context, body []byte, signature string) error {
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil || len(sig) == 0 {
		return fmt.Errorf("%w: X-Sign is not base64", ErrBadSignature)
	}
	digest := sha256.Sum256(body)

	key, err := k.current(ctx, false)
	if err != nil {
		return err
	}
	if ecdsa.VerifyASN1(key, digest[:], sig) {
		return nil
	}
	// Maybe the key rotated: fetch it again, once.
	key, err = k.current(ctx, true)
	if err != nil {
		return err
	}
	if ecdsa.VerifyASN1(key, digest[:], sig) {
		return nil
	}
	return ErrBadSignature
}

func (k *keyCache) current(ctx context.Context, refresh bool) (*ecdsa.PublicKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.key != nil && !refresh {
		return k.key, nil
	}
	encoded, err := k.fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("monobank: fetch public key: %w", err)
	}
	key, err := parsePublicKey(encoded)
	if err != nil {
		return nil, err
	}
	k.key = key
	return key, nil
}

// parsePublicKey reads the key as /api/merchant/pubkey returns it: base64 of
// a PEM block holding a PKIX ECDSA public key.
func parsePublicKey(encoded string) (*ecdsa.PublicKey, error) {
	pemBytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("monobank: public key is not base64: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("monobank: public key is not PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("monobank: parse public key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("monobank: public key is not ECDSA")
	}
	return key, nil
}
