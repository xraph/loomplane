package currentauthority

import (
	"context"
	"testing"

	"github.com/xraph/warden"
)

func TestFreshCacheAndOrdinaryInvalidation(t *testing.T) {
	req := &warden.CheckRequest{Subject: warden.Subject{Kind: warden.SubjectUser, ID: "u"}, Action: warden.Action{Name: "read"}, Resource: warden.Resource{Type: "project", ID: "p"}}
	result := &warden.CheckResult{Allowed: true}
	ordinary := warden.NewMemoryCache()
	c, a := routerAttempt(t, &recordingStore{})
	cache := NewFreshCache(c, ordinary)
	ctx := context.Background()
	cache.Set(ctx, "tenant", "/", req, result)
	if got, ok := cache.Get(ctx, "tenant", "/", req); !ok || !got.Allowed {
		t.Fatal("ordinary cache did not warm")
	}
	if _, ok := cache.Get(a.ctx, "tenant", "/", req); ok {
		t.Fatal("current read hit ordinary cache")
	}
	cache.Set(a.ctx, "tenant", "/", req, &warden.CheckResult{Allowed: false})
	if got, ok := cache.Get(ctx, "tenant", "/", req); !ok || !got.Allowed {
		t.Fatal("current write changed ordinary cache")
	}
	for _, invalidate := range []func(){
		func() { cache.InvalidateTenant(a.ctx, "tenant") },
		func() { cache.InvalidateSubject(a.ctx, "tenant", warden.SubjectUser, "u") },
		func() { cache.Clear(a.ctx) },
	} {
		cache.Set(ctx, "tenant", "/", req, result)
		invalidate()
		if _, ok := cache.Get(ctx, "tenant", "/", req); ok {
			t.Fatal("invalidation missed ordinary cache")
		}
	}
	cache.Set(a.ctx, "tenant", "/", req, result)
	if _, ok := cache.Get(ctx, "tenant", "/", req); ok {
		t.Fatal("current result populated ordinary cache")
	}
}

func TestInvalidScopedCacheCannotFallThrough(t *testing.T) {
	for _, mode := range []string{"unfenced", "foreign", "canceled", "expired", "profile"} {
		t.Run(mode, func(t *testing.T) {
			ordinary := warden.NewMemoryCache()
			c, a := routerAttempt(t, &recordingStore{})
			cache := NewFreshCache(c, ordinary)
			req := &warden.CheckRequest{Subject: warden.Subject{Kind: warden.SubjectUser, ID: "u"}}
			ordinary.Set(context.Background(), "t", "/", req, &warden.CheckResult{Allowed: true})
			switch mode {
			case "unfenced":
				a.fenced = false
			case "foreign":
				cache = NewFreshCache(&Coordinator{}, ordinary)
			case "canceled":
				a.cancel()
			case "expired":
				a.phase = sealed
			case "profile":
				c.profile = &profile{}
			}
			if _, ok := cache.Get(a.ctx, "t", "/", req); ok {
				t.Fatal("invalid scope read cache")
			}
			cache.Set(a.ctx, "t", "/", req, &warden.CheckResult{Allowed: false})
			got, ok := ordinary.Get(context.Background(), "t", "/", req)
			if !ok || !got.Allowed {
				t.Fatal("invalid scope wrote cache")
			}
			if mode != "expired" && a.poison == nil {
				t.Fatal("invalid scope did not poison")
			}
			if mode == "expired" && a.poison != nil {
				t.Fatal("post-seal call claimed revocation")
			}
		})
	}
}
