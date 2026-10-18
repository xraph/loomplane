package workloadidentity

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	entryv1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/entry/v1"
	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"k8s.io/apimachinery/pkg/util/validation"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// Binding is a server-verified attestation decision. Management requests contain
// only a destination ID; the enrollment controller resolves these fields from
// checked destination and workload records before calling the issuer.
type Binding struct {
	EntryID        string
	Agent          domain.AgentIdentity
	Destination    domain.Destination
	ParentURI      string
	CheckedAt      time.Time
	Method         string
	Namespace      string
	NamespaceUID   string
	ServiceAccount string
	PodUID         string
	Container      string
	UnixUID        *uint32
	Executable     string
	SHA256         string
	TTL            time.Duration
}

type EntryClient interface {
	BatchCreateEntry(context.Context, *entryv1.BatchCreateEntryRequest, ...grpc.CallOption) (*entryv1.BatchCreateEntryResponse, error)
	BatchDeleteEntry(context.Context, *entryv1.BatchDeleteEntryRequest, ...grpc.CallOption) (*entryv1.BatchDeleteEntryResponse, error)
}

type SPIREIssuer struct {
	client EntryClient
	domain string
	now    func() time.Time
}

func NewSPIREIssuer(connection grpc.ClientConnInterface, trustDomain string) (*SPIREIssuer, error) {
	td, err := spiffeid.TrustDomainFromString(trustDomain)
	if err != nil || td.Name() != trustDomain || connection == nil {
		return nil, ErrIdentity
	}
	return &SPIREIssuer{client: entryv1.NewEntryClient(connection), domain: trustDomain, now: time.Now}, nil
}
func (i *SPIREIssuer) Register(ctx context.Context, binding Binding) (*spiretypes.Entry, error) {
	entry, err := i.entry(binding)
	if err != nil {
		return nil, err
	}
	response, err := i.client.BatchCreateEntry(ctx, &entryv1.BatchCreateEntryRequest{Entries: []*spiretypes.Entry{entry}})
	if err != nil {
		return nil, err
	}
	if response == nil || len(response.Results) != 1 || response.Results[0] == nil || response.Results[0].Status == nil {
		return nil, ErrIdentity
	}
	result := response.Results[0]
	if result.Status.Code != int32(codes.OK) && result.Status.Code != int32(codes.AlreadyExists) {
		return nil, ErrIdentity
	}
	if !sameRegistration(entry, result.Entry) {
		return nil, ErrIdentity
	}
	return result.Entry, nil
}
func (i *SPIREIssuer) Revoke(ctx context.Context, entryID string) error {
	if !validEntryID(entryID) {
		return ErrIdentity
	}
	response, err := i.client.BatchDeleteEntry(ctx, &entryv1.BatchDeleteEntryRequest{Ids: []string{entryID}})
	if err != nil {
		return err
	}
	if response == nil || len(response.Results) != 1 || response.Results[0] == nil || response.Results[0].Status == nil {
		return ErrIdentity
	}
	result := response.Results[0]
	if result.Id != entryID || (result.Status.Code != int32(codes.OK) && result.Status.Code != int32(codes.NotFound)) {
		return ErrIdentity
	}
	return nil
}
func validEntryID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}
func (i *SPIREIssuer) entry(b Binding) (*spiretypes.Entry, error) {
	now := i.now()
	if !validEntryID(b.EntryID) || b.CheckedAt.IsZero() || b.CheckedAt.Before(now.Add(-5*time.Minute)) || b.CheckedAt.After(now.Add(30*time.Second)) || b.Destination.Quarantined || b.Destination.Generation < 1 || b.Destination.Identity.Revision() == "" || b.Agent.Scope != b.Destination.Scope || b.Agent.DestinationID != b.Destination.ID {
		return nil, ErrIdentity
	}
	uri, err := AgentURI(i.domain, b.Agent)
	if err != nil || b.Agent.URI != uri {
		return nil, ErrIdentity
	}
	parent, err := spiffeid.FromString(b.ParentURI)
	if err != nil || parent.TrustDomain().Name() != i.domain || parent.String() == uri || parent.Path() == "/spire/server" {
		return nil, ErrIdentity
	}
	ttl := int64(b.TTL / time.Second)
	if ttl < 30 || ttl > 3600 || b.TTL%time.Second != 0 {
		return nil, ErrIdentity
	}
	selectors := []*spiretypes.Selector{}
	switch b.Method {
	case "kubernetes":
		if b.Destination.Identity.Provider != "kubernetes" || b.NamespaceUID == "" || b.NamespaceUID != b.Destination.Identity.NamespaceUID || len(validation.IsDNS1123Label(b.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(b.ServiceAccount)) != 0 || !identitySegment.MatchString(b.PodUID) || len(validation.IsDNS1123Label(b.Container)) != 0 {
			return nil, ErrIdentity
		}
		for _, value := range []string{"ns:" + b.Namespace, "sa:" + b.ServiceAccount, "pod-uid:" + b.PodUID, "container-name:" + b.Container} {
			selectors = append(selectors, &spiretypes.Selector{Type: "k8s", Value: value})
		}
	case "unix":
		digest, decodeErr := hex.DecodeString(b.SHA256)
		if decodeErr != nil || len(digest) != 32 || strings.ToLower(b.SHA256) != b.SHA256 || b.Destination.Identity.Provider != "server" || b.UnixUID == nil || !filepath.IsAbs(b.Executable) || filepath.Clean(b.Executable) != b.Executable {
			return nil, ErrIdentity
		}
		for _, value := range []string{"uid:" + strconv.FormatUint(uint64(*b.UnixUID), 10), "path:" + b.Executable, "sha256:" + b.SHA256} {
			selectors = append(selectors, &spiretypes.Selector{Type: "unix", Value: value})
		}
	default:
		return nil, ErrIdentity
	}
	id, err := spiffeid.FromString(uri)
	if err != nil {
		return nil, err
	}
	return &spiretypes.Entry{Id: b.EntryID, SpiffeId: &spiretypes.SPIFFEID{TrustDomain: i.domain, Path: id.Path()}, ParentId: &spiretypes.SPIFFEID{TrustDomain: i.domain, Path: parent.Path()}, Selectors: selectors, X509SvidTtl: int32(ttl), JwtSvidTtl: int32(ttl)}, nil
}
func sameRegistration(want, got *spiretypes.Entry) bool {
	if got == nil {
		return false
	}
	a, ok := proto.Clone(want).(*spiretypes.Entry)
	if !ok {
		return false
	}
	b, ok := proto.Clone(got).(*spiretypes.Entry)
	if !ok {
		return false
	}
	a.CreatedAt = 0
	b.CreatedAt = 0
	a.RevisionNumber = 0
	b.RevisionNumber = 0
	less := func(a, b *spiretypes.Selector) int {
		if a == nil || b == nil {
			return 0
		}
		return strings.Compare(a.Type+":"+a.Value, b.Type+":"+b.Value)
	}
	slices.SortFunc(a.Selectors, less)
	slices.SortFunc(b.Selectors, less)
	return proto.Equal(a, b)
}
