// Package autodb is a client of the Auto-DB Pro parts catalogue API: makes,
// models and modifications (engines) of vehicles, the parts sections of a
// modification and the parts in each, and what is known of a part --
// specifications, photos, OEM numbers, analogues.
//
// The catalogue is read from the vehicle's side: "what fits this car" is
// getSectionParts, which works. The part's side, "which cars does this
// fit", is getVehicles, which returned nothing for every part tried; the
// client has no method for it until the provider fixes it.
//
// The API answers with a byte order mark (twice) before its JSON, gives
// some ids as strings and others as numbers, and lets a login in from one
// IP address only. The client deals with all three, so callers see plain Go
// values and a clear error for a refused address.
package autodb

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
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// Errors of the API.
var (
	// ErrIPNotAllowed means the account does not accept requests from this
	// machine's address; it is set in the Auto-DB Pro dashboard.
	ErrIPNotAllowed = errors.New("autodb: this IP address is not allowed for the account")
	// ErrUnauthorized means the login or password is wrong.
	ErrUnauthorized = errors.New("autodb: wrong login or password")
	// ErrUnavailable wraps failures worth retrying later: timeouts, 5xx.
	ErrUnavailable = errors.New("autodb: unavailable")
)

// Config configures a Client.
type Config struct {
	BaseURL  string // https://auto-db.pro/api/v1/
	User     string
	Password string
	// Timeout bounds one request. getSectionParts takes up to several
	// seconds, so the default is generous.
	Timeout time.Duration
	// RPS caps requests per second, all callers together; zero is no cap.
	// BackgroundRPS is the part of it calls marked Background may use.
	RPS           int
	BackgroundRPS int
}

// Client calls the Auto-DB Pro API.
type Client struct {
	base     string
	user     string
	password string
	http     *http.Client
	// all paces every call; background paces background calls on top
	// while buyers are asking.
	all        *rate.Limiter
	background *rate.Limiter
	// lastBuyer is when a call not marked Background last came, in Unix
	// nanoseconds.
	lastBuyer atomic.Int64
}

// buyerQuiet is how long after a buyer's call background work keeps to its
// share. Past it nobody is waiting, and the whole rate is the background's.
const buyerQuiet = 3 * time.Second

// New returns a Client.
func New(cfg Config) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	c := &Client{
		base: strings.TrimRight(cfg.BaseURL, "/") + "/", user: cfg.User, password: cfg.Password,
		http: &http.Client{Timeout: cfg.Timeout},
	}
	if cfg.RPS > 0 {
		c.all = rate.NewLimiter(rate.Limit(cfg.RPS), 1)
		bg := cfg.BackgroundRPS
		if bg <= 0 || bg > cfg.RPS {
			bg = cfg.RPS
		}
		c.background = rate.NewLimiter(rate.Limit(bg), 1)
	}
	return c
}

type backgroundKey struct{}

// Background marks calls made under ctx as background work -- a crawl, an
// enrichment pass -- which keeps to its smaller share of the rate while
// buyers are asking, and takes the whole of it when they are not.
func Background(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}

// wait paces a call: a background one while buyers are asking through its
// own limiter first, then every call through the common one.
func (c *Client) wait(ctx context.Context) error {
	if c.all == nil {
		return nil
	}
	if bg, _ := ctx.Value(backgroundKey{}).(bool); !bg {
		c.lastBuyer.Store(time.Now().UnixNano())
	} else if time.Since(time.Unix(0, c.lastBuyer.Load())) < buyerQuiet {
		if err := c.background.Wait(ctx); err != nil {
			return err
		}
	}
	return c.all.Wait(ctx)
}

// Group is a vehicle type. Passenger cars are the default.
type Group string

// Vehicle groups.
const (
	Passenger  Group = "passenger"
	Commercial Group = "commercial"
	Motorbike  Group = "motorbike"
)

// Make is a vehicle make.
type Make struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Model is a vehicle model of a make, a generation: "LACETTI (J200)".
type Model struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Years is the production span as given: "03.2003 - " or "03.2003 -
	// 03.2013".
	Years string `json:"constructioninterval"`
}

// Modification is a model with an engine, the level parts differ at.
type Modification struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Attrs are the specification pairs, "Power" -> ["80 kW", "109 PS"].
	Attrs map[string][]string `json:"-"`
}

// Section is a node of a modification's parts tree.
type Section struct {
	ID          int64  `json:"id"`
	Name        string `json:"description"`
	HasChildren bool   `json:"-"`
}

// Part is a part in a section: a brand's article.
type Part struct {
	SupplierID int64  `json:"supplierid"`
	Number     string `json:"part_number"`
	Brand      string `json:"supplier_name"`
	Name       string `json:"product_name"`
}

// Article is a search hit.
type Article struct {
	SupplierID int64  `json:"supplierId"`
	Number     string `json:"DataSupplierArticleNumber"`
	Name       string `json:"NormalizedDescription"`
	State      string `json:"ArticleStateDisplayValue"`
}

// Spec is one characteristic of a part.
type Spec struct {
	Name  string `json:"description"`
	Value string `json:"displayvalue"`
}

// OEM is an original equipment number of a part.
type OEM struct {
	Make   string `json:"description"`
	Number string `json:"OENbr"`
}

// Cross is an analogue: the same part from another brand.
type Cross struct {
	Brand  string `json:"description"`
	Number string `json:"PartsDataSupplierArticleNumber"`
}

// ArticleVehicle is a vehicle an article fits. ModID is the modification id
// getModifications and getSectionParts use.
type ArticleVehicle struct {
	ModID  int64
	Make   string
	Model  string
	Years  string // "03.2005 - " or "01.2005 - 12.2006"
	Engine string // "1.6", "2.0 D"
}

// Image is a photo of a part.
type Image struct {
	URL string `json:"url"`
}

// Makes lists the makes of a vehicle group.
func (c *Client) Makes(ctx context.Context, g Group) ([]Make, error) {
	var raw []struct {
		ID   flexInt `json:"id"`
		Name string  `json:"name"`
	}
	if err := c.get(ctx, url.Values{"action": {"getMakes"}, "group": {string(g)}}, &raw); err != nil {
		return nil, err
	}
	out := make([]Make, len(raw))
	for i, r := range raw {
		out[i] = Make{ID: int64(r.ID), Name: r.Name}
	}
	return out, nil
}

// Models lists the models of a make.
func (c *Client) Models(ctx context.Context, g Group, makeID int64) ([]Model, error) {
	var raw []struct {
		ID    flexInt `json:"id"`
		Name  string  `json:"name"`
		Years string  `json:"constructioninterval"`
	}
	q := url.Values{"action": {"getModels"}, "group": {string(g)}, "make": {strconv.FormatInt(makeID, 10)}}
	if err := c.get(ctx, q, &raw); err != nil {
		return nil, err
	}
	out := make([]Model, len(raw))
	for i, r := range raw {
		out[i] = Model{ID: int64(r.ID), Name: r.Name, Years: strings.TrimSpace(r.Years)}
	}
	return out, nil
}

// Modifications lists the engines of a model.
func (c *Client) Modifications(ctx context.Context, g Group, modelID int64) ([]Modification, error) {
	var raw []struct {
		ID    flexInt `json:"id"`
		Name  string  `json:"name"`
		Attrs string  `json:"attrs"`
	}
	q := url.Values{"action": {"getModifications"}, "group": {string(g)}, "model": {strconv.FormatInt(modelID, 10)}}
	if err := c.get(ctx, q, &raw); err != nil {
		return nil, err
	}
	out := make([]Modification, len(raw))
	for i, r := range raw {
		out[i] = Modification{ID: int64(r.ID), Name: r.Name, Attrs: parseAttrs(r.Attrs)}
	}
	return out, nil
}

// Sections lists the parts sections of a modification under parent, the
// top level for zero.
func (c *Client) Sections(ctx context.Context, g Group, modID, parent int64) ([]Section, error) {
	var raw []struct {
		ID       flexInt `json:"id"`
		Name     string  `json:"description"`
		HasChild string  `json:"havechild"`
	}
	q := url.Values{"action": {"getSections"}, "group": {string(g)}, "mod_id": {strconv.FormatInt(modID, 10)}}
	if parent != 0 {
		q.Set("parent", strconv.FormatInt(parent, 10))
	}
	if err := c.get(ctx, q, &raw); err != nil {
		return nil, err
	}
	out := make([]Section, len(raw))
	for i, r := range raw {
		out[i] = Section{ID: int64(r.ID), Name: r.Name, HasChildren: r.HasChild == "1"}
	}
	return out, nil
}

// SectionParts lists the parts of a section that fit a modification.
func (c *Client) SectionParts(ctx context.Context, g Group, modID, sectionID int64) ([]Part, error) {
	var raw []struct {
		SupplierID flexInt `json:"supplierid"`
		Number     string  `json:"part_number"`
		Brand      string  `json:"supplier_name"`
		Name       string  `json:"product_name"`
	}
	q := url.Values{"action": {"getSectionParts"}, "group": {string(g)},
		"mod_id": {strconv.FormatInt(modID, 10)}, "sec_id": {strconv.FormatInt(sectionID, 10)}}
	if err := c.get(ctx, q, &raw); err != nil {
		return nil, err
	}
	out := make([]Part, len(raw))
	for i, r := range raw {
		out[i] = Part{SupplierID: int64(r.SupplierID), Number: r.Number, Brand: r.Brand, Name: r.Name}
	}
	return out, nil
}

// Search finds articles by part number, spaces and dashes aside.
func (c *Client) Search(ctx context.Context, number string) ([]Article, error) {
	var raw []struct {
		SupplierID flexInt `json:"supplierId"`
		Number     string  `json:"DataSupplierArticleNumber"`
		Name       string  `json:"NormalizedDescription"`
		State      string  `json:"ArticleStateDisplayValue"`
	}
	if err := c.get(ctx, url.Values{"action": {"search"}, "q": {number}}, &raw); err != nil {
		return nil, err
	}
	out := make([]Article, len(raw))
	for i, r := range raw {
		out[i] = Article{SupplierID: int64(r.SupplierID), Number: r.Number, Name: r.Name, State: r.State}
	}
	return out, nil
}

// ArticleInfo is what is known of one article.
type ArticleInfo struct {
	Name   string
	Specs  []Spec
	Images []Image
	OEM    []OEM
	Cross  []Cross
	// Vehicles are the cars it fits, the first MaxVehicles of them: the API
	// cuts the list there and offers no next page.
	Vehicles []ArticleVehicle
}

// MaxVehicles is where the API cuts a part's list of vehicles. A list this
// long is not all of them.
const MaxVehicles = 100

// Info returns an article's card. A block the API failed to fetch comes
// back empty, as the API reports it as null.
func (c *Client) Info(ctx context.Context, supplierID int64, number string) (*ArticleInfo, error) {
	var raw struct {
		Status *struct {
			Name string `json:"NormalizedDescription"`
		} `json:"status"`
		Specs    []Spec  `json:"specs"`
		Images   []Image `json:"images"`
		OEM      []OEM   `json:"oem"`
		Cross    []Cross `json:"cross"`
		Vehicles []struct {
			ModID  flexInt `json:"car_id"`
			Make   string  `json:"make"`
			Model  string  `json:"model"`
			Years  string  `json:"constructioninterval"`
			Engine string  `json:"description"`
		} `json:"vehicles"`
	}
	q := url.Values{"action": {"getArtInfo"}, "number": {number}, "brand": {strconv.FormatInt(supplierID, 10)}}
	if err := c.get(ctx, q, &raw); err != nil {
		return nil, err
	}
	info := &ArticleInfo{Specs: specs(raw.Specs), OEM: raw.OEM, Cross: raw.Cross}
	for _, im := range raw.Images {
		info.Images = append(info.Images, Image{URL: imageURL(im.URL)})
	}
	for _, v := range raw.Vehicles {
		info.Vehicles = append(info.Vehicles, ArticleVehicle{ModID: int64(v.ModID), Make: v.Make, Model: v.Model,
			Years: v.Years, Engine: v.Engine})
	}
	if raw.Status != nil {
		info.Name = raw.Status.Name
	}
	return info, nil
}

// imageHost serves the photos. The url the API builds lacks the /images/
// directory the files are in, and answers 404 (October 2026); the client
// adds it, and leaves a url that has it alone.
const imageHost = "https://image.auto-db.pro/"

func imageURL(u string) string {
	if name, ok := strings.CutPrefix(u, imageHost); ok && !strings.HasPrefix(name, "images/") {
		return imageHost + "images/" + name
	}
	return u
}

// specs drops what is no characteristic: the API lists every pair twice
// and starts with the article's own number ("для артикула №").
func specs(in []Spec) []Spec {
	out := make([]Spec, 0, len(in))
	seen := make(map[Spec]bool, len(in))
	for _, s := range in {
		if seen[s] || strings.HasPrefix(s.Name, "для артикула") || s.Value == "" {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// bom is the UTF-8 byte order mark the API puts before its JSON.
var bom = []byte{0xEF, 0xBB, 0xBF}

// get calls one action and decodes its JSON into out.
func (c *Client) get(ctx context.Context, q url.Values, out any) error {
	if err := c.wait(ctx); err != nil {
		return fmt.Errorf("autodb: %s: %w", q.Get("action"), err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("autodb: build %s request: %w", q.Get("action"), err)
	}
	req.SetBasicAuth(c.user, c.password)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnavailable, q.Get("action"), err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("%w: read %s: %w", ErrUnavailable, q.Get("action"), err)
	}
	for bytes.HasPrefix(body, bom) {
		body = body[len(bom):]
	}

	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		switch {
		case strings.HasPrefix(e.Error, "Access denied from IP"):
			return fmt.Errorf("%w (%s)", ErrIPNotAllowed, strings.TrimPrefix(e.Error, "Access denied from "))
		case resp.StatusCode == http.StatusUnauthorized:
			return ErrUnauthorized
		case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
			return fmt.Errorf("%w: %s: HTTP %d", ErrUnavailable, q.Get("action"), resp.StatusCode)
		}
		return fmt.Errorf("autodb: %s: HTTP %d: %s", q.Get("action"), resp.StatusCode, e.Error)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("autodb: decode %s: %w", q.Get("action"), err)
	}
	return nil
}

// flexInt reads an id the API gives as a number in one method and a string
// in another.
type flexInt int64

// UnmarshalJSON reads "138" and 138 alike.
func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("autodb: id %s: %w", b, err)
	}
	*f = flexInt(n)
	return nil
}

// parseAttrs reads "ConstructionInterval:03.2005 - |Power:80 kW|Power:109
// PS" into a map; a name may repeat.
func parseAttrs(s string) map[string][]string {
	out := make(map[string][]string)
	for _, pair := range strings.Split(s, "|") {
		k, v, ok := strings.Cut(pair, ":")
		if k = strings.TrimSpace(k); ok && k != "" {
			out[k] = append(out[k], strings.TrimSpace(v))
		}
	}
	return out
}
