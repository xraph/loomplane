package cloudinfra

import (
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

// ClusterCleanupTombstone pins release-scoped identities independently of a binding.
type ClusterCleanupTombstone struct {
	ProjectID          string              `json:"project_id"`
	EnvironmentID      string              `json:"environment_id"`
	Service            string              `json:"service"`
	InstanceID         string              `json:"instance_id"`
	ConnectionID       string              `json:"connection_id"`
	Ref                provider.ClusterRef `json:"ref"`
	ResourceVersions   map[string]string   `json:"resource_versions"`
	DeploymentName     string              `json:"deployment_name,omitempty"`
	OriginalGeneration map[string]int64    `json:"original_generation,omitempty"`

	SchemaVersion        int                 `json:"schema_version"`
	OrgID                string              `json:"org_id"`
	CloudID              string              `json:"cloud_id"`
	ResourceID           string              `json:"resource_id"`
	GrantID              string              `json:"grant_id"`
	ReleaseID            string              `json:"release_id"`
	Namespace            string              `json:"namespace"`
	NamespaceUID         string              `json:"namespace_uid"`
	ClusterCA            string              `json:"cluster_ca"`
	ObservedAt           time.Time           `json:"observed_at"`
	ObservationStartedAt time.Time           `json:"observation_started_at"`
	InventoryBarrier     int64               `json:"inventory_barrier"`
	Shared               bool                `json:"shared,omitempty"`
	Deployments          []string            `json:"deployment_uids,omitempty"`
	ReplicaSets          []string            `json:"replicaset_uids,omitempty"`
	Pods                 []string            `json:"pod_uids,omitempty"`
	HPAs                 []ClusterCleanupHPA `json:"hpas,omitempty"`
}

type ClusterCleanupHPA struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type cleanupResidualKey struct {
	Resource string
	UID      string
}

type cleanupInventoryMatch struct {
	complete  bool
	residuals map[cleanupResidualKey]bool
	// A shared UID is exempt only after the store verifies the observed newer receipt.
	surviving   []ClusterCleanupObject
	servingHPAs []ClusterCleanupHPAObservation
}

// Initial shared retirement depends on the current serving owner. A later
// read can prove the old release absent after that controller has also gone.
func (m cleanupInventoryMatch) needsServingProof(t *ClusterCleanupTombstone, initial bool) bool {
	return t.Shared && (initial || len(m.surviving) > 0 || len(m.servingHPAs) > 0)
}

type cleanupInventoryIndex struct {
	complete   bool
	namespaces map[string]bool
	byUID      map[string]ClusterCleanupObject
	byRelease  map[string][]ClusterCleanupObject
	byOwner    map[string][]ClusterCleanupObject
	hpas       map[string]map[string]ClusterCleanupHPAObservation
}

func indexCleanupInventory(in *ClusterCleanupInventory) cleanupInventoryIndex {
	var out cleanupInventoryIndex
	if in == nil || in.SchemaVersion != 1 {
		return out
	}
	out.complete = in.Complete
	out.namespaces = map[string]bool{}
	for _, uid := range in.NamespaceUIDs {
		if uid == "" || out.namespaces[uid] {
			out.complete = false
		}
		out.namespaces[uid] = true
	}
	out.byUID = make(map[string]ClusterCleanupObject, len(in.Objects))
	out.byOwner = map[string][]ClusterCleanupObject{}
	out.byRelease = map[string][]ClusterCleanupObject{}
	out.hpas = map[string]map[string]ClusterCleanupHPAObservation{}
	for _, obj := range in.Objects {
		if obj.UID == "" || !out.namespaces[obj.NamespaceUID] || out.byUID[obj.UID].UID != "" {
			out.complete = false
			continue
		}
		out.byUID[obj.UID] = obj
		if obj.ControllerUID != "" {
			out.byOwner[obj.ControllerUID] = append(out.byOwner[obj.ControllerUID], obj)
		}
		if obj.ReleaseID != "" {
			out.byRelease[obj.ReleaseID] = append(out.byRelease[obj.ReleaseID], obj)
		}
	}
	for _, h := range in.HPAs {
		if out.hpas[h.NamespaceUID] == nil {
			out.hpas[h.NamespaceUID] = map[string]ClusterCleanupHPAObservation{}
		}
		if _, duplicated := out.hpas[h.NamespaceUID][h.Name]; duplicated {
			h.Complete = false
		}
		out.hpas[h.NamespaceUID][h.Name] = h
	}
	return out
}

func (i cleanupInventoryIndex) match(t *ClusterCleanupTombstone, active []cleanupResidualKey) cleanupInventoryMatch {
	out := cleanupInventoryMatch{complete: i.complete && t.SchemaVersion == 1 && i.namespaces[t.NamespaceUID], residuals: map[cleanupResidualKey]bool{}}
	add := func(o ClusterCleanupObject, resource string) {
		if o.NamespaceUID == t.NamespaceUID && o.Resource == resource && o.UID != "" {
			out.residuals[cleanupResidualKey{o.Resource, o.UID}] = true
		}
	}
	known := func(uid, resource string) ClusterCleanupObject {
		o := i.byUID[uid]
		if o.UID != "" && (o.Resource != resource || o.NamespaceUID != t.NamespaceUID) {
			out.complete = false
			return ClusterCleanupObject{}
		}
		return o
	}
	for _, o := range i.byRelease[t.ReleaseID] {
		add(o, o.Resource)
	}
	for _, uid := range t.Pods {
		add(known(uid, "pods"), "pods")
	}
	for _, uid := range t.ReplicaSets {
		add(known(uid, "replicasets"), "replicasets")
		for _, pod := range i.byOwner[uid] {
			add(pod, "pods")
		}
	}
	for _, uid := range t.Deployments {
		d := known(uid, "deployments")
		if t.Shared {
			if d.Resource == "deployments" && d.NamespaceUID == t.NamespaceUID && d.ReleaseID != t.ReleaseID {
				if d.Deleting || d.ReleaseID == "" {
					out.complete = false
				}
				out.surviving = append(out.surviving, d)
			}
		} else {
			add(d, "deployments")
			for _, rs := range i.byOwner[uid] {
				add(rs, "replicasets")
			}
		}
	}
	// Once a late object was seen, removing its marker cannot prove it is gone.
	for _, old := range active {
		add(known(old.UID, old.Resource), old.Resource)
	}
	for _, old := range t.HPAs {
		h, ok := i.hpas[t.NamespaceUID][old.Name]
		if !ok || !h.Complete {
			out.complete = false
			continue
		}
		if h.UID == old.UID {
			if t.Shared {
				if h.Deleting || h.DeploymentName == "" {
					out.complete = false
				}
				out.servingHPAs = append(out.servingHPAs, h)
			} else {
				out.residuals[cleanupResidualKey{"horizontalpodautoscalers", h.UID}] = true
			}
		}
		for _, previous := range active {
			if previous.Resource == "horizontalpodautoscalers" && previous.UID == h.UID {
				out.residuals[previous] = true
			}
		}
	}
	return out
}
