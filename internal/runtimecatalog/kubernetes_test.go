package runtimecatalog

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	domain "github.com/xraph/loomplane/pkg/federation"
)

type sliceClient struct {
	lists        []*discoveryv1.EndpointSliceList
	options      []metav1.ListOptions
	watches      []*watch.RaceFreeFakeWatcher
	watchOptions []metav1.ListOptions
}

func (s *sliceClient) List(_ context.Context, opts metav1.ListOptions) (*discoveryv1.EndpointSliceList, error) {
	s.options = append(s.options, opts)
	if len(s.lists) == 0 {
		return nil, errors.New("API unavailable")
	}
	out := s.lists[0]
	s.lists = s.lists[1:]
	return out, nil
}
func (s *sliceClient) Watch(_ context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	s.watchOptions = append(s.watchOptions, opts)
	if len(s.watches) == 0 {
		return nil, errors.New("watch unavailable")
	}
	out := s.watches[0]
	s.watches = s.watches[1:]
	return out, nil
}
func ptr[T any](v T) *T { return &v }
func sliceFixture() discoveryv1.EndpointSlice {
	return discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "slice", UID: "slice-uid", ResourceVersion: "10", Labels: map[string]string{discoveryv1.LabelServiceName: "api"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: "api", UID: "service-uid", Controller: ptr(true)}}}, AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: ptr("http"), Protocol: ptr(corev1.ProtocolTCP), Port: ptr(int32(8080))}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.20.0.2"}, TargetRef: &corev1.ObjectReference{UID: "pod-uid"}}}}
}
func TestKubernetesEndpointConditionsAndOwnership(t *testing.T) {
	c, _, agent, _ := catalogFixture(t)
	reg := c.store.(*MemoryStore).states[agent.AgentID].Registration
	target := KubernetesTarget{ExportID: "export", ServiceName: "api", ServiceUID: types.UID("service-uid"), PortName: "http"}
	for _, tc := range []struct {
		name   string
		mutate func(*discoveryv1.EndpointSlice)
		want   domain.Readiness
		denied bool
	}{
		{name: "nil conditions are ready", want: domain.Ready},
		{name: "not ready", mutate: func(s *discoveryv1.EndpointSlice) { s.Endpoints[0].Conditions.Ready = ptr(false) }, want: domain.Unready},
		{name: "not serving", mutate: func(s *discoveryv1.EndpointSlice) { s.Endpoints[0].Conditions.Serving = ptr(false) }, want: domain.Unready},
		{name: "terminating even when ready", mutate: func(s *discoveryv1.EndpointSlice) { s.Endpoints[0].Conditions.Terminating = ptr(true) }, want: domain.Draining},
		{name: "replaced service", mutate: func(s *discoveryv1.EndpointSlice) { s.OwnerReferences[0].UID = "replacement" }, denied: true},
		{name: "foreign namespace", mutate: func(s *discoveryv1.EndpointSlice) { s.Namespace = "foreign" }, denied: true},
		{name: "metadata", mutate: func(s *discoveryv1.EndpointSlice) { s.Endpoints[0].Addresses[0] = "169.254.169.254" }, denied: true},
		{name: "wrong port", mutate: func(s *discoveryv1.EndpointSlice) { s.Ports[0].Port = ptr(int32(5432)) }, denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := sliceFixture()
			if tc.mutate != nil {
				tc.mutate(&s)
			}
			obs := KubernetesObserver{Namespace: "ns", Registration: reg, Targets: []KubernetesTarget{target}}
			endpoints, err := obs.endpoints(map[types.UID]*discoveryv1.EndpointSlice{s.UID: &s}, 1)
			if tc.denied {
				if err == nil {
					t.Fatal("untrusted slice accepted")
				}
				return
			}
			if err != nil || len(endpoints) != 1 || endpoints[0].Readiness != tc.want {
				t.Fatalf("endpoints=%+v err=%v", endpoints, err)
			}
		})
	}
}
func TestKubernetesWatchRelistAndFailure(t *testing.T) {
	c, _, agent, _ := catalogFixture(t)
	reg := c.store.(*MemoryStore).states[agent.AgentID].Registration
	s := sliceFixture()
	w := watch.NewRaceFreeFake()
	w.Error(&metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonExpired, Code: 410})
	client := &sliceClient{lists: []*discoveryv1.EndpointSliceList{{ListMeta: metav1.ListMeta{ResourceVersion: "10"}, Items: []discoveryv1.EndpointSlice{s}}, {ListMeta: metav1.ListMeta{ResourceVersion: "20"}}}, watches: []*watch.RaceFreeFakeWatcher{w}}
	var sequence uint64
	var batches []domain.ObservationBatch
	obs := KubernetesObserver{Client: client, Namespace: "ns", Registration: reg, Targets: []KubernetesTarget{{ExportID: "export", ServiceName: "api", ServiceUID: "service-uid", PortName: "http"}}, NextSequence: func(context.Context) (uint64, error) { sequence++; return sequence, nil }, Publish: func(ctx context.Context, b domain.ObservationBatch) error {
		batches = append(batches, b)
		return c.Observe(ctx, agent, b)
	}, RefreshInterval: time.Second}
	if err := obs.cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := obs.cycle(t.Context()); err == nil {
		t.Fatal("missing watch failure")
	}
	if len(batches) != 2 || len(batches[1].Endpoints) != 0 || !batches[1].Full {
		t.Fatalf("relist retained old address: %+v", batches)
	}
	if client.watchOptions[0].ResourceVersion != "10" || client.options[0].LabelSelector == "" {
		t.Fatal("watch did not use scoped list boundary")
	}
	if err := obs.cycle(t.Context()); err == nil {
		t.Fatal("API outage hidden")
	}
	if len(batches) != 2 {
		t.Fatal("API outage renewed stale lease")
	}
	page := snapshot(t, c, agent, domain.SnapshotRequest{})
	if len(page.Endpoints) != 0 || len(page.Tombstones) != 1 {
		t.Fatalf("resync failed to tombstone missing endpoint: %+v", page)
	}
}

func TestKubernetesClosedWatchDoesNotSpin(t *testing.T) {
	c, _, agent, _ := catalogFixture(t)
	w := watch.NewRaceFreeFake()
	w.Stop()
	client := &sliceClient{lists: []*discoveryv1.EndpointSliceList{{ListMeta: metav1.ListMeta{ResourceVersion: "10"}}}, watches: []*watch.RaceFreeFakeWatcher{w}}
	obs := KubernetesObserver{Client: client, Namespace: "ns", Registration: c.store.(*MemoryStore).states[agent.AgentID].Registration, Targets: []KubernetesTarget{{ExportID: "export", ServiceName: "api", ServiceUID: "service-uid", PortName: "http"}}, NextSequence: func(context.Context) (uint64, error) { return 1, nil }, Publish: func(context.Context, domain.ObservationBatch) error { return nil }}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	if err := obs.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if len(client.options) != 1 {
		t.Fatalf("closed watch retried without backoff: %d", len(client.options))
	}
}
