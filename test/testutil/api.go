package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	Realm            = "wallet"
	Audience         = "wallet-api"
	InternalClient   = "internal-service"
	ProviderAClient  = "provider-a"
	ProviderBClient  = "provider-b"
	NoRoleClient     = "no-role"
	ShortLivedClient = "provider-short-lived"
)

func Secret(clientID string) string {
	return clientID + "-secret"
}

func Issuer(keycloakURL string) string {
	return strings.TrimRight(keycloakURL, "/") + "/realms/" + Realm
}

func Token(ctx context.Context, keycloakURL, clientID string) (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {Secret(clientID)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Issuer(keycloakURL)+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token for %s: %s: %s", clientID, res.Status, body)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("token for %s: unreadable response %s", clientID, body)
	}
	return out.AccessToken, nil
}

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

type Response struct {
	Status int
	Header http.Header
	Body   map[string]any
	Raw    []byte
}

func (r Response) String(key string) string {
	v, _ := r.Body[key].(string)
	return v
}

func (r Response) Bool(key string) bool {
	v, _ := r.Body[key].(bool)
	return v
}

func (r Response) Number(key string) float64 {
	v, _ := r.Body[key].(float64)
	return v
}

func (r Response) Amount(key string) string {
	m, _ := r.Body[key].(map[string]any)
	v, _ := m["amount"].(string)
	return v
}

func (r Response) List(key string) []any {
	v, _ := r.Body[key].([]any)
	return v
}

func (c *Client) Do(ctx context.Context, method, path string, body any, headers map[string]string) (Response, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return Response{}, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return Response{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return Response{}, err
	}
	out := Response{Status: res.StatusCode, Header: res.Header, Raw: raw}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.Body)
	}
	return out, nil
}

func (c *Client) Get(ctx context.Context, path string) (Response, error) {
	return c.Do(ctx, http.MethodGet, path, nil, nil)
}

func (c *Client) OpenWallet(ctx context.Context, playerID, amount string) (Response, error) {
	return c.Do(ctx, http.MethodPost, "/wallets", map[string]any{
		"playerId":       playerID,
		"initialBalance": map[string]string{"amount": amount, "currency": "BRL"},
	}, nil)
}

func (c *Client) Submit(ctx context.Context, w Wager, key string) (Response, error) {
	return c.Do(ctx, http.MethodPost, "/wagering/transactions", w.Body(), map[string]string{"Idempotency-Key": key})
}

type Wager struct {
	ProviderID            string
	ExternalTransactionID string
	PlayerID              string
	WalletID              string
	RoundID               string
	GameID                string
	Kind                  string
	Amount                string
	Currency              string
	Reference             string
}

func NewWager(walletID, playerID, externalID, kind, amount string) Wager {
	return Wager{
		ProviderID:            ProviderAClient,
		ExternalTransactionID: externalID,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  kind,
		Amount:                amount,
		Currency:              "BRL",
	}
}

func (w Wager) Key() string {
	return w.ProviderID + ":" + w.ExternalTransactionID
}

func (w Wager) Body() map[string]any {
	body := map[string]any{
		"providerId":            w.ProviderID,
		"externalTransactionId": w.ExternalTransactionID,
		"playerId":              w.PlayerID,
		"walletId":              w.WalletID,
		"roundId":               w.RoundID,
		"gameId":                w.GameID,
		"kind":                  w.Kind,
		"money":                 map[string]string{"amount": w.Amount, "currency": w.Currency},
	}
	if w.Reference != "" {
		body["referenceExternalTransactionId"] = w.Reference
	}
	return body
}

func (w Wager) Envelope(messageID string) map[string]any {
	data := w.Body()
	data["idempotencyKey"] = w.Key()
	return map[string]any{
		"messageId":  messageID,
		"type":       "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
		"data":       data,
	}
}

func Poll(ctx context.Context, timeout time.Duration, condition func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := condition()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("condition not met within " + timeout.String())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
