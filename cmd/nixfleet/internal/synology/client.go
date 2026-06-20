package synology

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client talks to the Synology DSM Web API over HTTPS using a session id (sid).
// DSM presents a self-signed cert, so TLS verification is skipped (the host is
// reached over the trusted LAN, same as the synology-csi driver).
type Client struct {
	baseURL string
	user    string
	passwd  string
	sid     string
	http    *http.Client
}

// NewClient builds a client for the given DSM host. Password is supplied
// separately (sourced from 1Password/env by the caller) and never logged.
func NewClient(host string, port int, https bool, user, passwd string) *Client {
	scheme := "https"
	if !https {
		scheme = "http"
	}
	return &Client{
		baseURL: fmt.Sprintf("%s://%s:%d/webapi", scheme, host, port),
		user:    user,
		passwd:  passwd,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // DSM self-signed cert on trusted LAN
			},
		},
	}
}

// apiResponse is the standard DSM envelope.
type apiResponse struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code int `json:"code"`
	} `json:"error"`
}

// Login authenticates and stores the session id. Must be called before any API
// call. format=sid returns the sid in the body (no cookie juggling).
func (c *Client) Login(ctx context.Context) error {
	q := url.Values{
		"api":     {"SYNO.API.Auth"},
		"version": {"3"},
		"method":  {"login"},
		"account": {c.user},
		"passwd":  {c.passwd},
		"session": {"NixFleet"},
		"format":  {"sid"},
	}
	var out struct {
		SID string `json:"sid"`
	}
	if err := c.do(ctx, "auth.cgi", q, &out); err != nil {
		return fmt.Errorf("DSM login failed for %s: %w", c.user, err)
	}
	if out.SID == "" {
		return fmt.Errorf("DSM login returned no session id")
	}
	c.sid = out.SID
	return nil
}

// Logout invalidates the session. Safe to call with no session.
func (c *Client) Logout(ctx context.Context) {
	if c.sid == "" {
		return
	}
	q := url.Values{
		"api":     {"SYNO.API.Auth"},
		"version": {"3"},
		"method":  {"logout"},
		"session": {"NixFleet"},
	}
	_ = c.do(ctx, "auth.cgi", q, nil) // best-effort
	c.sid = ""
}

// get calls an entry.cgi API method (read or write) and decodes data into out.
func (c *Client) get(ctx context.Context, api, method string, version int, extra url.Values) (json.RawMessage, error) {
	q := url.Values{
		"api":     {api},
		"version": {fmt.Sprintf("%d", version)},
		"method":  {method},
	}
	if c.sid != "" {
		q.Set("_sid", c.sid)
	}
	for k, vs := range extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	var raw json.RawMessage
	if err := c.do(ctx, "entry.cgi", q, &rawCapture{&raw}); err != nil {
		return nil, err
	}
	return raw, nil
}

// rawCapture lets do() hand back the raw data payload.
type rawCapture struct{ into *json.RawMessage }

// do issues the request, checks the DSM envelope, and decodes data into out.
// out may be nil (ignore data), a *rawCapture (keep raw), or any json target.
func (c *Client) do(ctx context.Context, cgi string, q url.Values, out any) error {
	reqURL := c.baseURL + "/" + cgi + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s: %w", cgi, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var env apiResponse
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("decode %s response: %w", cgi, err)
	}
	if !env.Success {
		code := -1
		if env.Error != nil {
			code = env.Error.Code
		}
		return fmt.Errorf("DSM API %s error: code %d (%s)", cgi, code, dsmErrText(code))
	}
	switch t := out.(type) {
	case nil:
		return nil
	case *rawCapture:
		*t.into = env.Data
		return nil
	default:
		if len(env.Data) == 0 {
			return nil
		}
		return json.Unmarshal(env.Data, out)
	}
}

// unmarshalData decodes a raw DSM data payload, tolerating an empty payload.
func unmarshalData(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// dsmErrText maps common DSM auth/API error codes to a hint.
func dsmErrText(code int) string {
	switch code {
	case 400, 401:
		return "invalid credentials"
	case 403:
		return "2FA/OTP required — not supported by this backend"
	case 105:
		return "insufficient permission for this account"
	case 119:
		return "session expired"
	default:
		return "see DSM API error reference"
	}
}
