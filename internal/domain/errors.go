// Package domain holds the shop's entities and rules. It depends on nothing
// else in the module: storage, Telegram and (later) the LLM agent all depend on
// it, never the other way round.
package domain

import "errors"

// Sentinel errors of the domain. Transport layers map them onto what the user
// sees (see internal/bot), which keeps the domain free of any wording.
var (
	ErrNotFound = errors.New("not found")
	// ErrOutOfStock means the requested quantity exceeds what is in stock.
	ErrOutOfStock = errors.New("out of stock")
	// ErrQtyLimit means a cart line is already at MaxQtyPerLine.
	ErrQtyLimit = errors.New("quantity limit reached")
	// ErrCartFull means the cart already has MaxCartLines distinct lines.
	ErrCartFull = errors.New("cart is full")
	// ErrNoSuchSize means the product does not come in the requested size.
	ErrNoSuchSize = errors.New("no such size")
)
