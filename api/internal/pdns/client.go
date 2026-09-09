// Package pdns is a minimal PowerDNS Authoritative HTTP API client covering
// what the management layer needs: zones, records via RRsets, and DNSSEC.
//
// Reference: https://doc.powerdns.com/authoritative/http-api/
package pdns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// ---- Wire types -----------------------------------------------------------

type Zone struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind,omitempty"` // Native | Master | Slave
	Serial      uint32   `json:"serial,omitempty"`
	Nameservers []string `json:"nameservers,omitempty"`
	Masters     []string `json:"masters,omitempty"`
	RRsets      []RRset  `json:"rrsets,omitempty"`
	DNSSEC      bool     `json:"dnssec,omitempty"`
}

type RRset struct {
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	TTL        int       `json:"ttl"`
	ChangeType string    `json:"changetype,omitempty"` // REPLACE | DELETE
	Records    []Record  `json:"records"`
	Comments   []Comment `json:"comments,omitempty"`
}

type Record struct {
	Content  string `json:"content"`
	Disabled bool   `json:"disabled"`
}

type Comment struct {
	Content    string `json:"content"`
	Account    string `json:"account,omitempty"`
	ModifiedAt int64  `json:"modified_at,omitempty"`
}

type errorResp struct {
	Error string `json:"error"`
}

// ---- HTTP plumbing --------------------------------------------------------

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		buf, _ := io.ReadAll(resp.Body)
		var er errorResp
		_ = json.Unmarshal(buf, &er)
		msg := er.Error
		if msg == "" {
			msg = strings.TrimSpace(string(buf))
		}
		return &APIError{Status: resp.StatusCode, Message: msg}
	}

	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("pdns: %d %s", e.Status, e.Message) }
func (e *APIError) NotFound() bool { return e.Status == http.StatusNotFound }

// ---- Zones ----------------------------------------------------------------

func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	var zs []Zone
	err := c.do(ctx, http.MethodGet, "/servers/localhost/zones", nil, &zs)
	return zs, err
}

func (c *Client) GetZone(ctx context.Context, name string) (*Zone, error) {
	var z Zone
	if err := c.do(ctx, http.MethodGet, "/servers/localhost/zones/"+canonical(name), nil, &z); err != nil {
		return nil, err
	}
	return &z, nil
}

func (c *Client) CreateZone(ctx context.Context, z Zone) (*Zone, error) {
	// Canonicalize the zone name.
	z.Name = canonical(z.Name)
	// canonicalize NS names too
	for i, ns := range z.Nameservers {
		z.Nameservers[i] = canonical(ns)
	}
	var out Zone
	err := c.do(ctx, http.MethodPost, "/servers/localhost/zones", z, &out)
	return &out, err
}

func (c *Client) DeleteZone(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/servers/localhost/zones/"+canonical(name), nil, nil)
}

// PatchRRsets sends a set of RRset changes atomically.
func (c *Client) PatchRRsets(ctx context.Context, zone string, rrsets []RRset) error {
	body := map[string]any{"rrsets": rrsets}
	for i := range rrsets {
		rrsets[i].Name = canonical(rrsets[i].Name)
	}
	return c.do(ctx, http.MethodPatch, "/servers/localhost/zones/"+canonical(zone), body, nil)
}

// ---- DNSSEC ---------------------------------------------------------------

type Cryptokey struct {
	ID        int    `json:"id,omitempty"`
	Type      string `json:"type,omitempty"` // Cryptokey
	KeyType   string `json:"keytype"`        // ksk | zsk | csk
	Active    bool   `json:"active"`
	Published bool   `json:"published"`
	DNSKey    string `json:"dnskey,omitempty"`
	DS        []string `json:"ds,omitempty"`
	Algorithm string `json:"algorithm,omitempty"`
	Bits      int    `json:"bits,omitempty"`
}

func (c *Client) ListKeys(ctx context.Context, zone string) ([]Cryptokey, error) {
	var ks []Cryptokey
	err := c.do(ctx, http.MethodGet, "/servers/localhost/zones/"+canonical(zone)+"/cryptokeys", nil, &ks)
	return ks, err
}

func (c *Client) CreateKey(ctx context.Context, zone string, k Cryptokey) (*Cryptokey, error) {
	var out Cryptokey
	err := c.do(ctx, http.MethodPost, "/servers/localhost/zones/"+canonical(zone)+"/cryptokeys", k, &out)
	return &out, err
}

// ---- Health ---------------------------------------------------------------

func (c *Client) Ping(ctx context.Context) error {
	// The servers/localhost endpoint returns useful metadata; use it as a ping.
	return c.do(ctx, http.MethodGet, "/servers/localhost", nil, nil)
}

// ---- Helpers --------------------------------------------------------------

// canonical ensures names end with a trailing dot, per RFC 1035.
func canonical(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}
	if !strings.HasSuffix(name, ".") {
		return name + "."
	}
	return name
}
