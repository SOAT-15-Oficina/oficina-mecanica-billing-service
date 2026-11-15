package mercadopago

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

const (
	DefaultStoreExternalID = "OFICINAMATRIZ"
	DefaultPOSExternalID   = "OFICINAMATRIZPOS01"
)

type User struct {
	ID     json.Number `json:"id"`
	SiteID string      `json:"site_id,omitempty"`
}

type Location struct {
	StreetNumber string  `json:"street_number"`
	StreetName   string  `json:"street_name"`
	CityName     string  `json:"city_name"`
	StateName    string  `json:"state_name"`
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	Reference    string  `json:"reference,omitempty"`
}

type Store struct {
	ID         json.Number `json:"id,omitempty"`
	Name       string      `json:"name"`
	ExternalID string      `json:"external_id"`
	Location   *Location   `json:"location,omitempty"`
}

type POSConfig struct {
	QR struct {
		OperatingMode string `json:"operating_mode"`
	} `json:"qr"`
}

type POS struct {
	ID         json.Number `json:"id,omitempty"`
	Name       string      `json:"name"`
	StoreID    string      `json:"store_id"`
	ExternalID string      `json:"external_id"`
	Status     string      `json:"status,omitempty"`
	Config     *POSConfig  `json:"config,omitempty"`
}

type searchResult[T any] struct {
	Results []T `json:"results"`
}

type SetupResult struct {
	UserID       string
	Store        Store
	StoreCreated bool
	POS          POS
	POSCreated   bool
}

func (c *Client) Me(ctx context.Context) (User, error) {
	var out User
	err := c.do(ctx, http.MethodGet, "/users/me", nil, nil, &out)
	return out, err
}

func (c *Client) FindStore(ctx context.Context, userID, externalID string) (*Store, error) {
	var out searchResult[Store]
	path := "/users/" + escape(userID) + "/stores/search?external_id=" + url.QueryEscape(externalID)
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	for i := range out.Results {
		if out.Results[i].ExternalID == externalID {
			return &out.Results[i], nil
		}
	}
	return nil, nil
}

func (c *Client) CreateStore(ctx context.Context, userID string, store Store) (Store, error) {
	var out Store
	err := c.do(ctx, http.MethodPost, "/users/"+escape(userID)+"/stores", store, nil, &out)
	return out, err
}

func (c *Client) FindPOS(ctx context.Context, externalID string) (*POS, error) {
	var out searchResult[POS]
	if err := c.do(ctx, http.MethodGet, "/pos?external_id="+url.QueryEscape(externalID), nil, nil, &out); err != nil {
		return nil, err
	}
	for i := range out.Results {
		if out.Results[i].ExternalID == externalID {
			return &out.Results[i], nil
		}
	}
	return nil, nil
}

func (c *Client) CreatePOS(ctx context.Context, pos POS, idempotencyKey string) (POS, error) {
	var out POS
	err := c.do(ctx, http.MethodPost, "/v2/pos", pos, idempotency(idempotencyKey), &out)
	return out, err
}

func NewStore(externalID string) Store {
	return Store{
		Name:       "Oficina Matriz",
		ExternalID: externalID,
		Location: &Location{
			StreetNumber: "1000",
			StreetName:   "Avenida Paulista",
			CityName:     "São Paulo",
			StateName:    "São Paulo",
			Latitude:     -23.5649,
			Longitude:    -46.6519,
			Reference:    "Oficina mecanica",
		},
	}
}

func NewPOS(storeID, externalID string) POS {
	cfg := &POSConfig{}
	cfg.QR.OperatingMode = "pdv"
	return POS{Name: "Caixa Oficina Matriz", StoreID: storeID, ExternalID: externalID, Config: cfg}
}

func Setup(ctx context.Context, c *Client, storeExternalID, posExternalID string) (SetupResult, error) {
	var res SetupResult
	me, err := c.Me(ctx)
	if err != nil {
		return res, fmt.Errorf("mercadopago: users/me: %w", err)
	}
	res.UserID = me.ID.String()

	store, err := c.FindStore(ctx, res.UserID, storeExternalID)
	if err != nil {
		return res, fmt.Errorf("mercadopago: search store: %w", err)
	}
	if store == nil {
		created, err := c.CreateStore(ctx, res.UserID, NewStore(storeExternalID))
		if err != nil {
			return res, fmt.Errorf("mercadopago: create store: %w", err)
		}
		store, res.StoreCreated = &created, true
	}
	res.Store = *store

	pos, err := c.FindPOS(ctx, posExternalID)
	if err != nil {
		return res, fmt.Errorf("mercadopago: search pos: %w", err)
	}
	if pos == nil {
		created, err := c.CreatePOS(ctx, NewPOS(store.ID.String(), posExternalID), "setup-"+res.UserID+"-"+posExternalID)
		if err != nil {
			return res, fmt.Errorf("mercadopago: create pos: %w", err)
		}
		pos, res.POSCreated = &created, true
	}
	res.POS = *pos
	return res, nil
}
