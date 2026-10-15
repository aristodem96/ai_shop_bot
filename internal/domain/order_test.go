package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizePhone(t *testing.T) {
	for in, want := range map[string]string{
		"+380 67 123 45 67": "+380671234567",
		"067-123-45-67":     "+380671234567",
		"380671234567":      "+380671234567",
		"(067) 123 45 67":   "+380671234567",
	} {
		got, ok := NormalizePhone(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "12345", "+48 123 456 789", "067 123 45 6x", "0671234567890"} {
		_, ok := NormalizePhone(in)
		assert.False(t, ok, in)
	}
}

func TestOrderTransitions(t *testing.T) {
	assert.True(t, OrderPaid.CanMoveTo(OrderConfirmed))
	assert.True(t, OrderConfirmed.CanMoveTo(OrderShipped))
	assert.True(t, OrderPaid.CanMoveTo(OrderCancelled))
	assert.False(t, OrderShipped.CanMoveTo(OrderCancelled), "a shipped order is not cancelled from the chat")
	assert.False(t, OrderAwaitingPayment.CanMoveTo(OrderShipped), "nothing ships unpaid")
	assert.False(t, OrderExpired.CanMoveTo(OrderPaid), "payment is not a manager's move")

	assert.True(t, OrderAwaitingPayment.HoldsStock())
	assert.False(t, OrderExpired.HoldsStock())
	assert.False(t, OrderNeedsRefund.HoldsStock())
}
