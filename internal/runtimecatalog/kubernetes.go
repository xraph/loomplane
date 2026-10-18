package runtimecatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/watch"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// EndpointSliceClient is satisfied by a namespace-scoped discovery/v1 client.
// The observer also verifies the namespace and Service UID on every object.
type EndpointSliceClient interface {
	List(context.Context, metav1.ListOptions) (*discoveryv1.EndpointSliceList, error)
	Watch(context.Context, metav1.ListOptions) (watch.Interface, error)
}

type KubernetesTarget struct {
	ExportID    string
	ServiceName string
	ServiceUID  types.UID
	PortName    string
}

// KubernetesObserver publishes complete registered-export sets. NextSequence
// must durably reserve a number before Publish; a restarted observer cannot
// reuse its previous sequence. Enrollment owns all targets and bounds.
type KubernetesObserver struct {
	Client          EndpointSliceClient
	Namespace       string
	Registration    Registration
	Targets         []KubernetesTarget
	NextSequence    func(context.Context) (uint64, error)
	Publish         func(context.Context, domain.ObservationBatch) error
	RefreshInterval time.Duration
}

func (o *KubernetesObserver) validate() error {
	if o.Client == nil || o.Publish == nil || o.NextSequence == nil || len(validation.IsDNS1123Label(o.Namespace)) != 0 || len(o.Targets) == 0 || len(o.Targets) > MaxBatch {
		return ErrInvalid
	}
	if err := ValidateRegistration(o.Registration); err != nil {
		return err
	}
	if len(o.Targets) != len(o.Registration.Exports) {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, target := range o.Targets {
		if _, ok := o.Registration.Exports[target.ExportID]; !ok || seen[target.ExportID] || len(validation.IsDNS1035Label(target.ServiceName)) != 0 || target.ServiceUID == "" {
			return ErrInvalid
		}
		seen[target.ExportID] = true
	}
	if o.RefreshInterval < 0 || o.RefreshInterval >= EndpointLease/2 {
		return ErrInvalid
	}
	return nil
}

// Run relists on watch close, expiry, errors and bounded renewal intervals.
// Failed API reads never refresh a cached endpoint lease. Cancellation stops the
// watch and retry timer; transient errors back off without spawning workers.
func (o *KubernetesObserver) Run(ctx context.Context) error {
	if err := o.validate(); err != nil {
		return err
	}
	delay := time.Second
	for ctx.Err() == nil {
		err := o.cycle(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrStale) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrLimit) {
			return err
		}
		if err == nil {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 5*time.Second)
	}
	return ctx.Err()
}

func (o *KubernetesObserver) selector() string {
	names := []string{}
	for _, target := range o.Targets {
		if !slices.Contains(names, target.ServiceName) {
			names = append(names, target.ServiceName)
		}
	}
	slices.Sort(names)
	return discoveryv1.LabelServiceName + " in (" + strings.Join(names, ",") + ")"
}

func (o *KubernetesObserver) cycle(ctx context.Context) error {
	if err := o.validate(); err != nil {
		return err
	}
	interval := o.RefreshInterval
	if interval == 0 {
		interval = 10 * time.Second
	}
	// A fresh list is required before every renewal interval, even when no watch
	// events arrive. Bound list + watch together so a hung API cannot renew data.
	ctx, cancel := context.WithTimeout(ctx, interval)
	defer cancel()
	objects := map[types.UID]*discoveryv1.EndpointSlice{}
	options := metav1.ListOptions{LabelSelector: o.selector(), Limit: 100}
	resourceVersion := ""
	for {
		page, err := o.Client.List(ctx, options)
		if err != nil {
			return err
		}
		if page.ResourceVersion == "" || (resourceVersion != "" && resourceVersion != page.ResourceVersion) {
			return ErrInvalid
		}
		resourceVersion = page.ResourceVersion
		for i := range page.Items {
			s := page.Items[i].DeepCopy()
			if s.UID == "" {
				return ErrInvalid
			}
			objects[s.UID] = s
		}
		if len(objects) > MaxBatch {
			return ErrLimit
		}
		if page.Continue == "" {
			break
		}
		if page.Continue == options.Continue {
			return ErrInvalid
		}
		options.Continue = page.Continue
	}
	if err := o.publish(ctx, objects); err != nil {
		return err
	}
	timeout := int64(max(1, interval/time.Second))
	stream, err := o.Client.Watch(ctx, metav1.ListOptions{LabelSelector: options.LabelSelector, ResourceVersion: resourceVersion, AllowWatchBookmarks: true, TimeoutSeconds: &timeout})
	if err != nil {
		return err
	}
	defer stream.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-stream.ResultChan():
			if !ok {
				return nil
			}
			if event.Type == watch.Error {
				return nil
			} // Includes HTTP 410: discard cache and relist.
			if event.Type == watch.Bookmark {
				continue
			}
			s, ok := event.Object.(*discoveryv1.EndpointSlice)
			if !ok || s.UID == "" || s.ResourceVersion == "" {
				return ErrInvalid
			}
			switch event.Type {
			case watch.Added, watch.Modified:
				objects[s.UID] = s.DeepCopy()
			case watch.Deleted:
				delete(objects, s.UID)
			default:
				return ErrInvalid
			}
			if len(objects) > MaxBatch {
				return ErrLimit
			}
			if err := o.publish(ctx, objects); err != nil {
				return err
			}
		}
	}
}

func (o *KubernetesObserver) publish(ctx context.Context, objects map[types.UID]*discoveryv1.EndpointSlice) error {
	sequence, err := o.NextSequence(ctx)
	if err != nil {
		return err
	}
	if sequence == 0 || sequence > math.MaxInt64 {
		return ErrStale
	}
	endpoints, err := o.endpoints(objects, int64(sequence))
	if err != nil {
		return err
	}
	return o.Publish(ctx, domain.ObservationBatch{Agent: o.Registration.Agent, DestinationGeneration: o.Registration.DestinationGeneration, Sequence: sequence, Full: true, Endpoints: endpoints})
}

func (o *KubernetesObserver) endpoints(objects map[types.UID]*discoveryv1.EndpointSlice, generation int64) ([]domain.Endpoint, error) {
	out := map[string]domain.Endpoint{}
	for _, s := range objects {
		if s.Namespace != o.Namespace || s.UID == "" || (s.AddressType != discoveryv1.AddressTypeIPv4 && s.AddressType != discoveryv1.AddressTypeIPv6) {
			return nil, ErrUnauthorized
		}
		matched := false
		for _, target := range o.Targets {
			if s.Labels[discoveryv1.LabelServiceName] != target.ServiceName {
				continue
			}
			matched = true
			owner := metav1.GetControllerOf(s)
			if owner == nil || owner.UID != target.ServiceUID || owner.Kind != "Service" || owner.APIVersion != "v1" || owner.Name != target.ServiceName {
				return nil, ErrUnauthorized
			}
			bounds, ok := o.Registration.Exports[target.ExportID]
			if !ok {
				return nil, ErrUnauthorized
			}
			portMatches := false
			for _, p := range s.Ports {
				name := ""
				if p.Name != nil {
					name = *p.Name
				}
				if name != target.PortName {
					continue
				}
				if p.Port == nil || *p.Port != int32(bounds.Port) || (p.Protocol != nil && *p.Protocol != corev1.ProtocolTCP) {
					return nil, ErrUnauthorized
				}
				portMatches = true
			}
			if !portMatches {
				return nil, ErrUnauthorized
			}
			for _, e := range s.Endpoints {
				readiness := domain.Ready
				if (e.Conditions.Ready != nil && !*e.Conditions.Ready) || (e.Conditions.Serving != nil && !*e.Conditions.Serving) {
					readiness = domain.Unready
				}
				if e.Conditions.Terminating != nil && *e.Conditions.Terminating {
					readiness = domain.Draining
				}
				zone := ""
				if e.Zone != nil {
					zone = *e.Zone
				}
				workloadUID := ""
				if e.TargetRef != nil {
					workloadUID = string(e.TargetRef.UID)
				}
				for _, address := range e.Addresses {
					addr, err := netip.ParseAddr(address)
					if err != nil || (s.AddressType == discoveryv1.AddressTypeIPv4) != addr.Is4() {
						return nil, ErrInvalid
					}
					key := fmt.Sprintf("%s/%s/%s/%d", target.ExportID, workloadUID, addr.String(), bounds.Port)
					hash := sha256.Sum256([]byte(key))
					id := hex.EncodeToString(hash[:])
					ep := domain.Endpoint{Metadata: domain.Metadata{Scope: bounds.Scope, ID: id, Generation: generation}, DestinationID: bounds.DestinationID, ExportID: target.ExportID, PlacementID: bounds.PlacementID, BindingID: bounds.BindingID, ObserverID: o.Registration.Agent.AgentID, Address: addr.String(), Port: bounds.Port, Readiness: readiness, Zone: zone}
					if err := validateEndpoint(o.Registration, ep); err != nil {
						return nil, err
					}
					// During slice redistribution Kubernetes may duplicate endpoints. Conflicting
					// copies close routing until the API converges instead of picking map order.
					if previous, ok := out[id]; ok && previous != ep {
						return nil, ErrInvalid
					}
					out[id] = ep
					if len(out) > MaxBatch {
						return nil, ErrLimit
					}
				}
			}
		}
		if !matched {
			return nil, ErrUnauthorized
		}
	}
	endpoints := make([]domain.Endpoint, 0, len(out))
	for _, ep := range out {
		endpoints = append(endpoints, ep)
	}
	slices.SortFunc(endpoints, func(a, b domain.Endpoint) int { return strings.Compare(a.ID, b.ID) })
	return endpoints, nil
}
