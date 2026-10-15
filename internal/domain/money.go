package domain

import (
	"strconv"
	"strings"
)

// Money is an amount in kopiyky, the minor unit of the hryvnia.
//
// An integer rather than a float: 0.1 + 0.2 is not 0.3 in binary floating
// point, and a payment provider that receives 289899.99999 kopiyky rejects the
// invoice at best. Monobank takes minor units, so this value goes to it as is.
type Money int64

// UAH builds an amount from whole hryvnias, which is how prices are written.
func UAH(hryvnias int64) Money { return Money(hryvnias * 100) }

// String renders the amount the way Ukrainian shops print prices:
// "2 899 грн", or "2 899,50 грн" when there are kopiyky.
func (m Money) String() string {
	sign := ""
	if m < 0 {
		sign = "-"
		m = -m
	}
	whole, frac := int64(m)/100, int64(m)%100

	s := groupThousands(strconv.FormatInt(whole, 10))
	if frac != 0 {
		s += "," + twoDigits(frac)
	}
	return sign + s + " грн"
}

// groupThousands separates thousands with a narrow no-break space, so a price
// is never split across two lines in a chat bubble.
func groupThousands(digits string) string {
	if len(digits) <= 3 {
		return digits
	}
	var b strings.Builder
	head := len(digits) % 3
	if head > 0 {
		b.WriteString(digits[:head])
	}
	for i := head; i < len(digits); i += 3 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

func twoDigits(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}
