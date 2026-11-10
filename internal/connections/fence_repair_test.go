package connections

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/security"
)

func TestFenceRepairNeedsCloudAccountManage(t *testing.T) {
	secrets := &inventorySecrets{}
	called := false
	service := &Service{
		store: cloudConnections{cloud: Server}, secrets: secrets,
		authz: inventoryAuthorization{denied: security.CloudAccountManage},
		FenceRepairer: func(context.Context, *Connection, []byte, string) (*server.FenceRepair, error) {
			called = true
			return &server.FenceRepair{}, nil
		},
	}
	p := security.Principal{OrgID: "org"}
	if _, err := service.ReviewFenceRepair(t.Context(), p, "connection"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("review: %v", err)
	}
	if _, err := service.RepairFence(t.Context(), p, "connection", FenceRepairInput{Review: strings.Repeat("a", 64)}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("repair: %v", err)
	}
	if secrets.reads != 0 || called {
		t.Fatalf("unauthorized repair reached the server: reads=%d called=%v", secrets.reads, called)
	}
}

func TestFenceRepairRefusesOtherClouds(t *testing.T) {
	secrets := &inventorySecrets{}
	service := &Service{store: cloudConnections{cloud: Kubernetes}, secrets: secrets, authz: inventoryAuthorization{}}
	_, err := service.ReviewFenceRepair(t.Context(), security.Principal{OrgID: "org"}, "connection")
	if !errors.Is(err, ErrInvalid) || secrets.reads != 0 {
		t.Fatalf("kubernetes connection: err=%v reads=%d", err, secrets.reads)
	}
}

// A review reaches the server with no token and changes nothing. A repair
// carries the reviewed token and is audited once it has cleared the mark.
func TestFenceRepairReviewsThenRepairs(t *testing.T) {
	token := strings.Repeat("a", 64)
	var reviews []string
	audit := &connAudit{}
	service := &Service{
		store: cloudConnections{cloud: Server}, secrets: &inventorySecrets{}, authz: inventoryAuthorization{}, audit: audit,
		FenceRepairer: func(_ context.Context, c *Connection, credential []byte, review string) (*server.FenceRepair, error) {
			if c.Cloud != Server || string(credential) != "private" {
				t.Fatalf("repairer got %s %q", c.Cloud, credential)
			}
			reviews = append(reviews, review)
			return &server.FenceRepair{Daemon: "daemon", Dirty: true, Review: token, Repaired: review != ""}, nil
		},
	}
	p := security.Principal{OrgID: "org", UserID: "user"}
	review, err := service.ReviewFenceRepair(t.Context(), p, "connection")
	if err != nil || review.Review != token || review.Repaired {
		t.Fatalf("review: %+v %v", review, err)
	}
	if len(audit.actions) != 0 {
		t.Fatalf("a review was audited: %v", audit.actions)
	}
	repaired, err := service.RepairFence(t.Context(), p, "connection", FenceRepairInput{Review: token})
	if err != nil || !repaired.Repaired {
		t.Fatalf("repair: %+v %v", repaired, err)
	}
	if !slices.Equal(reviews, []string{"", token}) {
		t.Fatalf("server saw reviews %q", reviews)
	}
	if !slices.Equal(audit.actions, []string{"cloud_connection.fence_repair"}) {
		t.Fatalf("audit: %v", audit.actions)
	}
}

func TestFenceRepairNeedsAReview(t *testing.T) {
	called := false
	service := &Service{
		store: cloudConnections{cloud: Server}, secrets: &inventorySecrets{}, authz: inventoryAuthorization{},
		FenceRepairer: func(context.Context, *Connection, []byte, string) (*server.FenceRepair, error) {
			called = true
			return &server.FenceRepair{}, nil
		},
	}
	_, err := service.RepairFence(t.Context(), security.Principal{OrgID: "org"}, "connection", FenceRepairInput{})
	if !errors.Is(err, ErrInvalid) || called {
		t.Fatalf("repair without a review: err=%v called=%v", err, called)
	}
}

// The helper's refusals say what to do, so they reach the caller. Anything
// else from the SSH session or node stream stays hidden, as other live reads do.
func TestFenceRepairErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		want   error
		reason bool
	}{
		{name: "refused", err: fmt.Errorf("%w: an exec is still running in a Loomplane container", server.ErrFenceRepairRefused), want: ErrFenceRepairRefused, reason: true},
		{name: "invalid", err: fmt.Errorf("%w: give the review digest a fence repair review returned", server.ErrInvalid), want: ErrInvalid, reason: true},
		{name: "transport", err: errors.New("ssh: secret detail"), want: ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &Service{
				store: cloudConnections{cloud: Server}, secrets: &inventorySecrets{}, authz: inventoryAuthorization{}, audit: &connAudit{},
				FenceRepairer: func(context.Context, *Connection, []byte, string) (*server.FenceRepair, error) {
					return nil, tc.err
				},
			}
			_, err := service.ReviewFenceRepair(t.Context(), security.Principal{OrgID: "org"}, "connection")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret detail") || tc.reason && !strings.Contains(err.Error(), strings.SplitN(tc.err.Error(), ": ", 2)[1]) {
				t.Fatalf("err %q", err)
			}
		})
	}
}
