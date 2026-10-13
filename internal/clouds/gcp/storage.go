package gcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
)

// --- Cloud Storage buckets an application keeps its files in ---

// EnsurePrivateBucket creates a bucket in location with uniform access and
// public access prevention enforced, if there isn't one by that name.
func (s *Session) EnsurePrivateBucket(ctx context.Context, name, location string) error {
	u := s.c.url("storage.googleapis.com", "/storage/v1/b?project="+url.QueryEscape(s.key.ProjectID))
	err := s.do(ctx, http.MethodPost, u, map[string]any{
		"name": name, "location": location,
		"iamConfiguration": map[string]any{
			"uniformBucketLevelAccess": map[string]bool{"enabled": true},
			"publicAccessPrevention":   "enforced",
		},
	}, nil)
	if IsConflict(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create bucket %s: %w", name, err)
	}
	return nil
}

type bucketPolicy struct {
	Version  int `json:"version,omitempty"`
	Bindings []struct {
		Role    string   `json:"role"`
		Members []string `json:"members"`
	} `json:"bindings"`
	Etag string `json:"etag,omitempty"`
}

// GrantBucket gives member role on a bucket, keeping whoever else has a
// role there.
func (s *Session) GrantBucket(ctx context.Context, bucket, member, role string) error {
	u := s.c.url("storage.googleapis.com", "/storage/v1/b/"+url.PathEscape(bucket)+"/iam")
	var p bucketPolicy
	if err := s.do(ctx, http.MethodGet, u, nil, &p); err != nil {
		return fmt.Errorf("read bucket %s's policy: %w", bucket, err)
	}
	for i := range p.Bindings {
		if p.Bindings[i].Role == role {
			if slices.Contains(p.Bindings[i].Members, member) {
				return nil
			}
			p.Bindings[i].Members = append(p.Bindings[i].Members, member)
			return s.do(ctx, http.MethodPut, u, p, nil)
		}
	}
	p.Bindings = append(p.Bindings, struct {
		Role    string   `json:"role"`
		Members []string `json:"members"`
	}{Role: role, Members: []string{member}})
	if err := s.do(ctx, http.MethodPut, u, p, nil); err != nil {
		return fmt.Errorf("set bucket %s's policy: %w", bucket, err)
	}
	return nil
}

// BucketExists reports whether the bucket is there.
func (s *Session) BucketExists(ctx context.Context, name string) (bool, error) {
	err := s.do(ctx, http.MethodGet, s.c.url("storage.googleapis.com", "/storage/v1/b/"+url.PathEscape(name)), nil, nil)
	if IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// DeleteBucket deletes a bucket and everything in it. One that's already
// gone is fine.
func (s *Session) DeleteBucket(ctx context.Context, name string) error {
	list := s.c.url("storage.googleapis.com", "/storage/v1/b/"+url.PathEscape(name)+"/o")
	for {
		var page struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
		}
		err := s.do(ctx, http.MethodGet, list, nil, &page)
		if IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("list bucket %s: %w", name, err)
		}
		if len(page.Items) == 0 {
			break
		}
		for _, o := range page.Items {
			obj := s.c.url("storage.googleapis.com", "/storage/v1/b/"+url.PathEscape(name)+"/o/"+url.PathEscape(o.Name))
			if derr := s.do(ctx, http.MethodDelete, obj, nil, nil); derr != nil && !IsNotFound(derr) {
				return fmt.Errorf("empty bucket %s: %w", name, derr)
			}
		}
	}
	err := s.do(ctx, http.MethodDelete, s.c.url("storage.googleapis.com", "/storage/v1/b/"+url.PathEscape(name)), nil, nil)
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete bucket %s: %w", name, err)
	}
	return nil
}

// --- Memorystore for Redis ---

// RedisInstance is a Memorystore instance as it is.
type RedisInstance struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Host  string `json:"host"`
	Port  int    `json:"port"`
}

func (s *Session) redisPath(region, id string) string {
	return "/v1/projects/" + s.key.ProjectID + "/locations/" + region + "/instances/" + id
}

// GetRedis reads an instance. It returns an error IsNotFound recognises
// when there isn't one.
func (s *Session) GetRedis(ctx context.Context, region, id string) (*RedisInstance, error) {
	var in RedisInstance
	if err := s.do(ctx, http.MethodGet, s.c.url("redis.googleapis.com", s.redisPath(region, id)), nil, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// EnsureRedis creates a Basic tier instance on the project's default
// network with AUTH on, if there isn't one, and waits until it's ready.
func (s *Session) EnsureRedis(ctx context.Context, region, id string) (*RedisInstance, error) {
	if _, err := s.GetRedis(ctx, region, id); IsNotFound(err) {
		u := s.c.url("redis.googleapis.com", "/v1/projects/"+s.key.ProjectID+"/locations/"+region+"/instances?instanceId="+url.QueryEscape(id))
		err = s.do(ctx, http.MethodPost, u, map[string]any{
			"tier": "BASIC", "memorySizeGb": 1, "redisVersion": "REDIS_7_2",
			"authorizedNetwork": "projects/" + s.key.ProjectID + "/global/networks/default",
			"connectMode":       "DIRECT_PEERING", "authEnabled": true,
			"labels": map[string]string{"managed-by": "loomplane"},
		}, nil)
		if err != nil && !IsConflict(err) {
			return nil, fmt.Errorf("create redis %s: %w", id, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("read redis %s: %w", id, err)
	}
	for {
		in, err := s.GetRedis(ctx, region, id)
		if err != nil {
			return nil, fmt.Errorf("read redis %s: %w", id, err)
		}
		switch in.State {
		case "READY":
			return in, nil
		case "DELETING", "FAILING_OVER":
			return nil, fmt.Errorf("redis %s is %s", id, in.State)
		}
		progress(ctx, "The cache is "+in.State)
		if werr := s.wait(ctx); werr != nil {
			return nil, werr
		}
	}
}

// RedisAuth reads an instance's AUTH string.
func (s *Session) RedisAuth(ctx context.Context, region, id string) (string, error) {
	var out struct {
		AuthString string `json:"authString"`
	}
	if err := s.do(ctx, http.MethodGet, s.c.url("redis.googleapis.com", s.redisPath(region, id)+"/authString"), nil, &out); err != nil {
		return "", fmt.Errorf("read redis %s's auth string: %w", id, err)
	}
	return out.AuthString, nil
}

// DeleteRedis deletes an instance and its data. One that's already gone
// is fine.
func (s *Session) DeleteRedis(ctx context.Context, region, id string) error {
	err := s.do(ctx, http.MethodDelete, s.c.url("redis.googleapis.com", s.redisPath(region, id)), nil, nil)
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete redis %s: %w", id, err)
	}
	return nil
}
