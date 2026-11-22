package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
)

type DatabaseLayout struct {
	Nodes int      `json:"num_nodes"`
	Sizes []string `json:"sizes"`
}

type EngineCapacity struct {
	Regions []string         `json:"regions"`
	Layouts []DatabaseLayout `json:"layouts"`
}

type DatabaseCapacity struct {
	Postgres EngineCapacity `json:"pg"`
	MySQL    EngineCapacity `json:"mysql"`
	Redis    EngineCapacity `json:"valkey"`
}

func (c *Client) DatabaseCapacity(ctx context.Context, token string) (DatabaseCapacity, error) {
	var out struct {
		Options DatabaseCapacity `json:"options"`
	}
	err := c.do(ctx, token, http.MethodGet, "/v2/databases/options", nil, nil, &out)
	return out.Options, err
}

func (c *Client) ResizeDatabase(ctx context.Context, token, id string, spec DatabaseSpec) (*Database, error) {
	requested := false
	for {
		db, err := c.GetDatabase(ctx, token, id)
		if err != nil {
			return nil, err
		}
		if id == "" || db.ID != id || db.Engine != spec.Engine || db.Region != spec.Region {
			return nil, errors.New("DigitalOcean database identity or region changed")
		}
		if db.Status == "failed" || db.Status == "deleting" {
			return nil, errors.New("DigitalOcean database cannot be resized in its current state")
		}
		if db.Status == databaseOnline {
			if db.Size == "" || db.NumNodes < 1 {
				return nil, errors.New("DigitalOcean did not confirm the current database capacity")
			}
			if db.Size == spec.Size && db.NumNodes == spec.NumNodes {
				return db, nil
			}
			if !requested {
				if err := c.do(ctx, token, http.MethodPut, "/v2/databases/"+url.PathEscape(id)+"/resize", nil, map[string]any{"size": spec.Size, "num_nodes": spec.NumNodes}, nil); err != nil {
					return nil, err
				}
				requested = true
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.poll):
		}
	}
}
