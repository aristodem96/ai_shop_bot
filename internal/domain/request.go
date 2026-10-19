package domain

import "time"

// RequestStatus is where a part request is.
type RequestStatus string

const (
	// RequestNew waits for a manager to look for the part.
	RequestNew RequestStatus = "new"
	// RequestQuoted has a price the buyer can pay until the quote expires.
	RequestQuoted RequestStatus = "quoted"
	// RequestNotFound means no supplier has the part.
	RequestNotFound RequestStatus = "not_found"
	// RequestDeclined means the buyer turned the price down.
	RequestDeclined RequestStatus = "declined"
	// RequestExpired means the quote ran out unpaid.
	RequestExpired RequestStatus = "expired"
)

// QuoteTTL is how long a quoted price holds. A supplier's price and stock
// do not hold for long.
const QuoteTTL = 24 * time.Hour

// PartRequest is a buyer asking the shop to order a catalogue part.
type PartRequest struct {
	ID     int64
	UserID int64
	// Vehicle is the buyer's car as it was when asked, for the manager.
	Vehicle  string
	Brand    string
	Number   string
	PartKey  string
	Name     string
	Category string
	Qty      int
	Status   RequestStatus
	// The fields below are set by a quote.
	ProductID int64
	Price     Money // per unit
	LeadDays  int
	ExpiresAt time.Time
	// AdminMessageID is the managers' message with the request's buttons.
	AdminMessageID int64
}

// Total is what the quoted quantity costs.
func (r *PartRequest) Total() Money { return r.Price * Money(r.Qty) }

// Label names the part: "Гальмівні колодки BREMBO P 85 075".
func (r *PartRequest) Label() string { return r.Name + " " + r.Brand + " " + r.Number }
