package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/security"
)

var _ DomainStore = (*PgStore)(nil)

func (s *PgStore) DomainProject(ctx context.Context, org, project string) error {
	var found string
	return storeError(s.db.QueryRow(ctx, `SELECT id FROM loomplane_projects WHERE org_id=$1 AND id=$2`, org, project).Scan(&found))
}

// CreateZone stores a new zone. One without a proof of its own is verified
// through the domain it sits under, so that domain must still be there.
// The organization lock orders this with RemoveZone: a domain removed after
// the caller looked fails the create, rather than leaving a zone nothing
// proves.
func (s *PgStore) CreateZone(ctx context.Context, zone *Zone) error {
	raw, err := json.Marshal(zone)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, zone.OrgID); checkErr != nil {
		return checkErr
	}
	if !zone.hasControlProof() && zone.VerifiedAt.IsZero() {
		ancestors, checkErr := zonesForShare(ctx, tx, zone.OrgID, ancestorNames(zone.Name))
		if checkErr != nil {
			return checkErr
		}
		if len(ancestors) == 0 {
			return fmt.Errorf("%w: the domain above %s was just removed, so %s wasn't added. Try again", ErrConflict, zone.Name, zone.Name)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_dns_zones(id,org_id,name,connection_id,revision,body) VALUES($1,$2,$3,$4,$5,$6)`, zone.ID, zone.OrgID, zone.Name, zone.ConnectionID, zone.Revision, raw); err != nil {
		return storeError(err)
	}
	return tx.Commit()
}

func (s *PgStore) Zone(ctx context.Context, org, zoneID string) (*Zone, error) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT body FROM loomplane_dns_zones WHERE org_id=$1 AND id=$2`, org, zoneID).Scan(&raw); err != nil {
		return nil, storeError(err)
	}
	var out Zone
	return &out, json.Unmarshal(raw, &out)
}

func (s *PgStore) Zones(ctx context.Context, org string) ([]*Zone, error) {
	rows, err := s.db.Query(ctx, `SELECT body FROM loomplane_dns_zones WHERE org_id=$1 ORDER BY name,id`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []*Zone{}
	for rows.Next() {
		var raw []byte
		var zone Zone
		if checkErr := rows.Scan(&raw); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(raw, &zone); checkErr != nil {
			return nil, checkErr
		}
		result = append(result, &zone)
	}
	return result, rows.Err()
}

// zonesForShare reads the organization's zones with the given names inside
// tx and locks them against change until it ends.
func zonesForShare(ctx context.Context, tx driver.Tx, org string, names []string) ([]*Zone, error) {
	out := []*Zone{}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT body FROM loomplane_dns_zones WHERE org_id=$1 AND name=ANY($2) FOR SHARE`, org, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var zone Zone
		if checkErr := rows.Scan(&raw); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(raw, &zone); checkErr != nil {
			return nil, checkErr
		}
		out = append(out, &zone)
	}
	return out, rows.Err()
}

// zoneBodyTime reads a time from a zone body, NULL unless it is RFC 3339,
// so one malformed row can't fail a query over every zone.
func zoneBodyTime(field string) string {
	value := "body->>'" + field + "'"
	return "(CASE WHEN " + value + ` ~ '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$' THEN (` + value + ")::timestamptz END)"
}

// ZoneProofsDue reads zones whose proof the background still checks,
// across organizations. It serves the background check only. Unset times are
// stored as Go's zero time, which sorts before any check. A row whose times
// don't parse is left out, and one that doesn't decode is skipped, rather
// than failing the pass.
func (s *PgStore) ZoneProofsDue(ctx context.Context, now, checkedBefore, recheckBefore time.Time, limit int) ([]*Zone, error) {
	zero := "'0001-01-01T00:00:00Z'::timestamptz"
	// zoneProofDue's rule: a managed zone's pending proof until it expires,
	// and any verified zone's own record daily, or every pass while it's
	// missing.
	rows, err := s.db.Query(ctx, `SELECT body FROM loomplane_dns_zones
 WHERE NOT (coalesce(body->>'proof_publish_attempted','false')='true' AND coalesce(`+zoneBodyTime("proof_publish_attempted_at")+`,`+zero+`) > $2)
 AND ((body->>'mode'='managed'
   AND `+zoneBodyTime("verified_at")+` <= `+zero+`
   AND `+zoneBodyTime("proof_expires_at")+` > $1
   AND `+zoneBodyTime("proof_checked_at")+` <= $2)
  OR (`+zoneBodyTime("verified_at")+` > `+zero+`
   AND body->>'proof_name' = '_loomplane.'||(body->>'name')
   AND coalesce(body->>'proof_value','') <> ''
   AND `+zoneBodyTime("proof_expires_at")+` > `+zero+`
   AND `+zoneBodyTime("proof_checked_at")+` <= CASE WHEN coalesce(`+zoneBodyTime("proof_missing_at")+`,`+zero+`) > `+zero+` THEN $2 ELSE $4 END))
 ORDER BY `+zoneBodyTime("proof_checked_at")+`, id LIMIT $3`, now, checkedBefore, limit, recheckBefore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []*Zone{}
	for rows.Next() {
		var raw []byte
		var zone Zone
		if checkErr := rows.Scan(&raw); checkErr != nil {
			return nil, checkErr
		}
		if json.Unmarshal(raw, &zone) != nil {
			continue
		}
		result = append(result, &zone)
	}
	return result, rows.Err()
}

func (s *PgStore) SaveZone(ctx context.Context, zone *Zone) error {
	prior := zone.Revision
	zone.Revision++
	raw, err := json.Marshal(zone)
	if err != nil {
		return err
	}
	err = s.db.QueryRow(ctx, `UPDATE loomplane_dns_zones SET body=$3,revision=$4 WHERE org_id=$1 AND id=$2 AND revision=$5 RETURNING id`, zone.OrgID, zone.ID, raw, zone.Revision, prior).Scan(new(string))
	if err != nil {
		zone.Revision = prior
		return ErrConflict
	}
	return nil
}

// Initial Create persists its revision and ServiceRefs. Updated instances need
// the ready runtime receipt and the latest exact controller deployment success.
const routeRuntimeJoin = ` LEFT JOIN LATERAL (SELECT d.id,d.release_id,d.state FROM cp_deployments d WHERE d.tenant_id=i.tenant_id AND d.instance_id=i.id ORDER BY d.created_at DESC,d.id DESC LIMIT 1) deployment ON true`
const routeRuntimeRevision = `CASE WHEN i.state='running' AND deployment.id IS NULL AND coalesce(i.labels->>'` + owned.LabelReceiptRelease + `','')='' THEN i.labels->>'` + owned.LabelRevision + `' WHEN i.state='running' AND deployment.state='succeeded' AND deployment.release_id=i.labels->>'` + owned.LabelReceiptRelease + `' AND deployment.release_id=i.labels->>'` + owned.LabelRevision + `' THEN deployment.release_id ELSE '' END`
const routedRuntimeContainer = `CASE WHEN deployment.id IS NULL THEN i.service_refs->>routed.name ELSE i.labels->>('` + owned.LabelReceiptServicePrefix + `'||routed.name) END`

// routableComponent matches a composed catalog component's process on a port
// its recipe marks routable. Planning lists only those in routable_ports, never
// a control port, and gives engine databases, the composed resource itself and
// its jobs none; the role and shape checks keep a stray list from widening it.
// A dependency-only kind's component is its own composed resource (its
// component config names itself), which is never routed. port is the query
// parameter holding the routed port.
func routableComponentOn(port string) string {
	return `b.subject='resource' AND b.config->'component' IS NOT NULL AND b.config->'composition' IS NULL AND b.config->'catalog' IS NULL
 AND b.config->'component'->>'resource' <> b.name
 AND b.config->'component'->'component'->>'role'='process' AND jsonb_typeof(b.config->'routable_ports')='array' AND b.config->'routable_ports' @> jsonb_build_array(` + port + `::int)`
}

var routableComponent = routableComponentOn("$6")

// A service binding routes a port on its main service. A catalog component
// that routableComponent accepts routes the port on the service that declares
// it, so a database or private admin port never takes a public route.
const routedService = ` JOIN LATERAL (SELECT svc->>'name' AS name FROM jsonb_array_elements(i.services) svc
 WHERE (b.subject='resource' OR svc->>'name'='main') AND EXISTS(SELECT 1 FROM jsonb_array_elements(svc->'ports') p WHERE (p->>'container')::int=$6)
 ORDER BY svc->>'name' LIMIT 1) routed ON true`

var routeBindingQuery = `SELECT b.id,b.config,s.id,t.id,c.id,c.generation,b.external_id,coalesce(c.details->>'docker_id',''),coalesce(` + routeRuntimeRevision + `,''),coalesce(` + routedRuntimeContainer + `,''),coalesce(nullif(c.details->>'advertise_host',''),c.details->>'host','')
 FROM loomplane_bindings b
 JOIN loomplane_environments e ON e.org_id=b.org_id AND e.project_id=b.project_id AND e.id=b.environment_id
 JOIN loomplane_cloud_targets t ON t.org_id=b.org_id AND t.cloud_id=$5 AND t.id=b.config->'cloud_placement'->>'target_id'
 JOIN loomplane_cloud_project_grants g ON g.org_id=b.org_id AND g.cloud_id=t.cloud_id AND g.project_id=b.project_id AND NOT g.revoked AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids))
 JOIN loomplane_cloud_servers s ON s.org_id=b.org_id AND s.cloud_id=t.cloud_id AND s.id=b.config->'cloud_placement'->>'server_id' AND s.status='ready'
 JOIN loomplane_cloud_connections c ON c.org_id=s.org_id AND c.id=s.runtime_connection_id AND c.id=b.connection_id AND c.status='ready'
 JOIN cp_instances i ON i.tenant_id=b.org_id AND i.id=b.external_id` + routeRuntimeJoin + routedService + `
 WHERE b.org_id=$1 AND b.project_id=$2 AND b.environment_id=$3 AND b.name=$4 AND b.provider='server'
 AND (b.subject='service' OR (` + routableComponent + `))
 ORDER BY b.subject='service' DESC`

func scanRouteBinding(row pgx.Row) (RouteBinding, error) {
	var out RouteBinding
	var config []byte
	err := row.Scan(&out.BindingID, &config, &out.ServerID, &out.TargetID, &out.ConnectionID, &out.ConnectionGeneration, &out.InstanceID, &out.RuntimeID, &out.ReleaseID, &out.ContainerID, &out.Address)
	if err != nil {
		return out, storeError(err)
	}
	out.ConfigDigest = digest(json.RawMessage(config))
	if out.RuntimeID == "" || out.InstanceID == "" || out.ReleaseID == "" || out.ContainerID == "" || !validRouteAddress(out.Address) {
		return out, fmt.Errorf("%w: no successful exact runtime receipt; redeploy this service successfully before configuring its route", ErrConflict)
	}
	return out, nil
}

// A Kubernetes binding routes to its connection's ingress. With a Cloud ID it
// needs a live grant covering its placement, a target or the Cloud's default,
// and that target, or one of the Cloud's for the default, on its connection.
// Without one it must have no placement at all: it runs on the environment's
// own connection, and the project's infrastructure permission authorizes it.
var kubernetesRouteBindingQuery = `SELECT b.id,c.id,c.generation,b.external_id,coalesce(b.config->'cloud_placement'->>'target_id',''),coalesce(c.details->>'namespace_uid',''),coalesce(c.details->>'namespace',''),coalesce(c.details->>'ingress_class',''),coalesce(c.details->>'ingress_address','')
 FROM loomplane_bindings b
 JOIN loomplane_environments e ON e.org_id=b.org_id AND e.project_id=b.project_id AND e.id=b.environment_id
 JOIN loomplane_cloud_connections c ON c.org_id=b.org_id AND c.id=b.connection_id AND c.cloud='kubernetes' AND c.status='ready'
 JOIN cp_instances i ON i.tenant_id=b.org_id AND i.id=b.external_id` + routedService + `
 WHERE b.org_id=$1 AND b.project_id=$2 AND b.environment_id=$3 AND b.name=$4 AND b.provider='kubernetes'
 AND (b.subject='service' OR (` + routableComponent + `))
 AND CASE WHEN $5='' THEN coalesce(b.config->'cloud_placement','null'::jsonb)='null'::jsonb
 ELSE b.config->'cloud_placement'->>'cloud_id'=$5 AND EXISTS(SELECT 1 FROM loomplane_cloud_project_grants g WHERE g.org_id=b.org_id AND g.cloud_id=$5 AND g.project_id=b.project_id AND NOT g.revoked
 AND CASE WHEN coalesce(b.config->'cloud_placement'->>'target_id','')='' THEN coalesce((b.config->'cloud_placement'->>'default')::boolean,false) AND cardinality(g.target_ids)=0
 AND EXISTS(SELECT 1 FROM loomplane_cloud_targets t WHERE t.org_id=b.org_id AND t.cloud_id=$5 AND t.connection_id=c.id)
 ELSE (cardinality(g.target_ids)=0 OR b.config->'cloud_placement'->>'target_id'=ANY(g.target_ids))
 AND EXISTS(SELECT 1 FROM loomplane_cloud_targets t WHERE t.org_id=b.org_id AND t.cloud_id=$5 AND t.id=b.config->'cloud_placement'->>'target_id' AND t.connection_id=c.id) END) END
 ORDER BY b.subject='service' DESC`

// scanKubernetesRouteBinding leaves the config digest, release and container
// empty: the Ingress reaches the Service by name, so a release keeps the binding.
func scanKubernetesRouteBinding(row pgx.Row) (RouteBinding, error) {
	out := RouteBinding{Backend: RouteBackendKubernetes}
	err := row.Scan(&out.BindingID, &out.ConnectionID, &out.ConnectionGeneration, &out.InstanceID, &out.TargetID, &out.RuntimeID, &out.Namespace, &out.IngressClass, &out.Address)
	if err != nil {
		return out, storeError(err)
	}
	instance, parseErr := id.Parse(out.InstanceID)
	if out.RuntimeID == "" || out.Namespace == "" || parseErr != nil {
		return out, fmt.Errorf("%w: the Kubernetes connection has no checked namespace or the service has no instance; check the connection and deploy the service first", ErrConflict)
	}
	out.ServiceName = kubernetes.WorkloadName(instance)
	if out.Address == "" {
		return out, fmt.Errorf("%w: set the Kubernetes connection's ingress address before routing to it", ErrInvalid)
	}
	return out, nil
}

// routeBindingBackend is the provider of the binding a route names. A service
// wins over a catalog component with the same name, as in the binding queries.
func (s *PgStore) routeBindingBackend(ctx context.Context, org, project string, in RouteInput) (string, error) {
	var backend string
	err := s.db.QueryRow(ctx, `SELECT provider FROM loomplane_bindings WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND name=$4 AND subject IN ('service','resource') ORDER BY subject='service' DESC LIMIT 1`, org, project, in.EnvironmentID, in.Service).Scan(&backend)
	return backend, storeError(err)
}

func (s *PgStore) ResolveRouteBinding(ctx context.Context, org, project string, in RouteInput) (RouteBinding, error) {
	backend, err := s.routeBindingBackend(ctx, org, project, in)
	if err != nil {
		return RouteBinding{}, err
	}
	if backend == RouteBackendKubernetes {
		return scanKubernetesRouteBinding(s.db.QueryRow(ctx, kubernetesRouteBindingQuery, org, project, in.EnvironmentID, in.Service, in.CloudID, in.Port))
	}
	if in.CloudID == "" {
		return RouteBinding{}, fmt.Errorf("%w: %s", ErrInvalid, noCloudPlacement)
	}
	return scanRouteBinding(s.db.QueryRow(ctx, routeBindingQuery, org, project, in.EnvironmentID, in.Service, in.CloudID, in.Port))
}

// A service and a catalog component may share a name. The service wins here
// and in routeBindingQuery.
func (s *PgStore) RouteBindingCloud(ctx context.Context, org, project, environment, service string) (string, error) {
	var cloud string
	err := s.db.QueryRow(ctx, `SELECT coalesce(config->'cloud_placement'->>'cloud_id','') FROM loomplane_bindings
 WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND name=$4 AND subject IN ('service','resource') ORDER BY subject='service' DESC LIMIT 1`, org, project, environment, service).Scan(&cloud)
	return cloud, storeError(err)
}

type routeCheckpoint struct {
	ExecutionPrincipal *security.Principal     `json:"execution_principal,omitempty"`
	Cancelled          *IngressTransition      `json:"cancelled,omitempty"`
	Source             *IngressTransition      `json:"source,omitempty"`
	Desired            *server.IngressConfig   `json:"desired,omitempty"`
	Previous           *server.IngressSnapshot `json:"previous,omitempty"`
	Applied            *server.IngressSnapshot `json:"applied,omitempty"`
}

func routeBytes(op *RouteOperation) (body, principal, checkpoint []byte, err error) {
	body, err = json.Marshal(op)
	if err == nil {
		principal, err = json.Marshal(op.Principal)
	}
	if err == nil {
		checkpoint, err = json.Marshal(routeCheckpoint{ExecutionPrincipal: op.ExecutionPrincipal, Cancelled: op.CancelledIngress, Source: op.SourceTransition, Desired: op.DesiredConfig, Previous: op.PreviousConfig, Applied: op.AppliedConfig})
	}
	return
}

func scanRouteOperation(row pgx.Row) (*RouteOperation, error) {
	var raw, principal, checkpoint []byte
	if err := row.Scan(&raw, &principal, &checkpoint); err != nil {
		return nil, storeError(err)
	}
	var op RouteOperation
	var cp routeCheckpoint
	if err := json.Unmarshal(raw, &op); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(principal, &op.Principal); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(checkpoint, &cp); err != nil {
		return nil, err
	}
	op.ExecutionPrincipal = cp.ExecutionPrincipal
	op.DesiredConfig, op.PreviousConfig, op.AppliedConfig, op.SourceTransition, op.CancelledIngress = cp.Desired, cp.Previous, cp.Applied, cp.Source, cp.Cancelled
	return &op, nil
}

func insertRouteOperation(ctx context.Context, tx driver.Tx, op *RouteOperation) error {
	if op.CreatedAt.IsZero() {
		op.CreatedAt = time.Now().UTC()
	}
	raw, principal, checkpoint, err := routeBytes(op)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_route_operations(id,org_id,project_id,route_id,server_id,cloud_id,status,revision,generation,body,principal,checkpoint,backend) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, op.ID, op.Route.OrgID, op.Route.ProjectID, op.Route.ID, op.Route.Binding.ServerID, op.Route.Input.CloudID, op.Status, op.Revision, op.Generation, raw, principal, checkpoint, op.Route.Binding.BackendName())
	return storeError(err)
}

func (s *PgStore) PreviewRoute(ctx context.Context, op *RouteOperation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := insertRouteOperation(ctx, tx, op); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}

func (s *PgStore) AcceptRoute(ctx context.Context, op *RouteOperation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	route := &op.Route
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, route.OrgID); checkErr != nil {
		return checkErr
	}
	if checkErr := guardEdgeRoute(ctx, tx, route, op.Kind); checkErr != nil {
		return checkErr
	}
	if checkErr := guardRouteAttachment(ctx, tx, route, op.Kind); checkErr != nil {
		return checkErr
	}
	if op.Kind == "apply" {
		args := []any{route.OrgID, route.ProjectID, route.Input.EnvironmentID, route.Input.Service, route.Input.CloudID, route.Input.Port}
		var current RouteBinding
		var lookupErr error
		// The organization lock already orders this with grant changes.
		if route.Binding.BackendName() == RouteBackendKubernetes {
			current, lookupErr = scanKubernetesRouteBinding(tx.QueryRow(ctx, kubernetesRouteBindingQuery+" FOR SHARE OF b,c,i", args...))
		} else {
			current, lookupErr = scanRouteBinding(tx.QueryRow(ctx, routeBindingQuery+" FOR SHARE OF b,g,t,s,c,i", args...))
		}
		if lookupErr != nil {
			return lookupErr
		}
		if !reflect.DeepEqual(current, route.Binding) {
			return ErrConflict
		}
		var rawZone []byte
		if checkErr := tx.QueryRow(ctx, `SELECT body FROM loomplane_dns_zones WHERE org_id=$1 AND id=$2 FOR SHARE`, route.OrgID, route.Input.ZoneID).Scan(&rawZone); checkErr != nil {
			return storeError(checkErr)
		}
		var zone Zone
		if checkErr := json.Unmarshal(rawZone, &zone); checkErr != nil {
			return checkErr
		}
		// A subdomain is verified through its domain, so its ancestors are
		// read and locked here too, after the zone itself. Removing zones
		// must delete them in that same order. None can lose its
		// verification under this route.
		ancestors, ancestorErr := zonesForShare(ctx, tx, route.OrgID, ancestorNames(zone.Name))
		if ancestorErr != nil {
			return ancestorErr
		}
		LinkZoneParents(append([]*Zone{&zone}, ancestors...))
		if !slices.Contains(zone.ProjectIDs, route.ProjectID) || !zone.verified() {
			return ErrNotFound
		}
		// Zones are added under the organization lock, so a subdomain that
		// holds this name can't appear before the route is stored.
		if names := deeperZoneNames(zone.Name, route.Input.Hostname); len(names) > 0 {
			var sub string
			if checkErr := tx.QueryRow(ctx, `SELECT coalesce((SELECT name FROM loomplane_dns_zones WHERE org_id=$1 AND name=ANY($2) ORDER BY length(name) DESC LIMIT 1),'')`, route.OrgID, names).Scan(&sub); checkErr != nil {
				return checkErr
			}
			if sub != "" {
				return errBelongsTo(route.Input.Hostname, sub)
			}
		}
		// The zone row is locked, so its credential can't detach under this route.
		if isWildcard(route.Input.Hostname) && !zone.Wildcards() {
			return ErrWildcardUnsupported
		}
		raw, encodeErr := json.Marshal(route)
		if encodeErr != nil {
			return encodeErr
		}
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_domain_routes(id,org_id,project_id,environment_id,cloud_id,server_id,connection_id,zone_id,hostname,status,revision,body,backend) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, route.ID, route.OrgID, route.ProjectID, route.Input.EnvironmentID, route.Input.CloudID, route.Binding.ServerID, route.Binding.ConnectionID, route.Input.ZoneID, route.Input.Hostname, route.Status, route.Revision, raw, route.Binding.BackendName())
		if err != nil {
			return storeError(err)
		}
		op.Revision++
		body, _, checkpoint, encodeErr := routeBytes(op)
		if encodeErr != nil {
			return encodeErr
		}
		if checkErr := tx.QueryRow(ctx, `UPDATE loomplane_route_operations SET status='pending',revision=$3,body=$4,checkpoint=$5 WHERE org_id=$1 AND id=$2 AND status='preview' AND revision=$6 RETURNING id`, route.OrgID, op.ID, op.Revision, body, checkpoint, op.Revision-1).Scan(new(string)); checkErr != nil {
			return storeError(checkErr)
		}
	} else {
		var current int64
		if checkErr := tx.QueryRow(ctx, `SELECT revision FROM loomplane_domain_routes WHERE org_id=$1 AND project_id=$2 AND id=$3 AND status<>'removed' FOR UPDATE`, route.OrgID, route.ProjectID, route.ID).Scan(&current); checkErr != nil {
			return storeError(checkErr)
		}
		if current != route.Revision {
			return ErrConflict
		}
		if op.Kind == "cutover" || op.Kind == "rollback" || op.Kind == "release" {
			var released bool
			if checkErr := tx.QueryRow(ctx, `SELECT released FROM loomplane_route_handoffs WHERE org_id=$1 AND id=$2 AND route_id=$3 FOR UPDATE`, route.OrgID, op.HandoffID, route.ID).Scan(&released); checkErr != nil {
				return storeError(checkErr)
			}
			if released {
				return ErrConflict
			}
		}
		if op.Kind == "remove" {
			var retained bool
			if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_route_handoffs WHERE org_id=$1 AND route_id=$2 AND NOT released)`, route.OrgID, route.ID).Scan(&retained); checkErr != nil {
				return checkErr
			}
			if retained {
				return ErrConflict
			}
		}
		if op.Kind == "remove" || op.Kind == "rollback" || op.Kind == "release" {
			active, activeErr := scanRouteOperation(tx.QueryRow(ctx, `SELECT body,principal,checkpoint FROM loomplane_route_operations WHERE org_id=$1 AND route_id=$2 AND status NOT IN ('preview','succeeded','cancelled') FOR UPDATE`, route.OrgID, route.ID))
			if activeErr != nil && !errors.Is(activeErr, ErrNotFound) {
				return activeErr
			}
			if active != nil {
				if active.Status == "running" || (active.DNSAttempted && active.Receipt == nil) {
					return ErrConflict
				}
				if op.Kind == "remove" {
					if active.Receipt != nil {
						op.Receipt = active.Receipt
					} else if active.PreviousReceipt != nil {
						op.Receipt = active.PreviousReceipt
					}
					op.CancelledIngress = active.CancelledIngress
					if active.DesiredConfig != nil {
						op.CancelledIngress = &IngressTransition{Desired: *active.DesiredConfig, Previous: active.PreviousConfig, Applied: active.AppliedConfig}
					}
				}
				active.Status = "cancelled"
				active.Revision++
				body, _, _, encodeErr := routeBytes(active)
				if encodeErr != nil {
					return encodeErr
				}
				if _, err = tx.Exec(ctx, `UPDATE loomplane_route_operations SET status='cancelled',revision=$3,body=$4 WHERE org_id=$1 AND id=$2`, route.OrgID, active.ID, active.Revision, body); err != nil {
					return err
				}
			}
		}
		if checkErr := insertRouteOperation(ctx, tx, op); checkErr != nil {
			return checkErr
		}
	}
	return tx.Commit()
}

func (s *PgStore) RouteOperation(ctx context.Context, org, operationID string) (*RouteOperation, error) {
	return scanRouteOperation(s.db.QueryRow(ctx, `SELECT body,principal,checkpoint FROM loomplane_route_operations WHERE org_id=$1 AND id=$2`, org, operationID))
}

func (s *PgStore) SaveRouteOperation(ctx context.Context, op *RouteOperation) error {
	prior := op.Revision
	op.Revision++
	body, _, checkpoint, err := routeBytes(op)
	if err != nil {
		return err
	}
	err = s.db.QueryRow(ctx, `UPDATE loomplane_route_operations SET status=$3,revision=$4,generation=$5,body=$6,checkpoint=$7 WHERE org_id=$1 AND id=$2 AND revision=$8 AND status<>'preview' RETURNING id`, op.Route.OrgID, op.ID, op.Status, op.Revision, op.Generation, body, checkpoint, prior).Scan(new(string))
	if err != nil {
		op.Revision = prior
		return ErrConflict
	}
	return nil
}

func (s *PgStore) Route(ctx context.Context, org, project, routeID string) (*Route, error) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT body FROM loomplane_domain_routes WHERE org_id=$1 AND project_id=$2 AND id=$3`, org, project, routeID).Scan(&raw); err != nil {
		return nil, storeError(err)
	}
	var route Route
	return &route, json.Unmarshal(raw, &route)
}

func (s *PgStore) Routes(ctx context.Context, org, project string) ([]*Route, error) {
	rows, err := s.db.Query(ctx, `SELECT body FROM loomplane_domain_routes WHERE org_id=$1 AND project_id=$2 ORDER BY hostname,id`, org, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []*Route{}
	for rows.Next() {
		var raw []byte
		var route Route
		if checkErr := rows.Scan(&raw); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(raw, &route); checkErr != nil {
			return nil, checkErr
		}
		result = append(result, &route)
	}
	return result, rows.Err()
}

func (s *PgStore) PublishRoute(ctx context.Context, op *RouteOperation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	// A Kubernetes route has no server ingress config to fence on; the route
	// lock and the revisions below order its workers.
	if op.Route.Binding.BackendName() != RouteBackendKubernetes {
		var generation int64
		if checkErr := tx.QueryRow(ctx, `SELECT generation FROM loomplane_ingress_configs WHERE org_id=$1 AND server_id=$2 AND generation=$3 FOR UPDATE`, op.Route.OrgID, op.Route.Binding.ServerID, op.Generation).Scan(&generation); checkErr != nil {
			return ErrConflict
		}
	}
	op.Route.Receipt = op.Receipt
	op.Route.OperationID = op.ID
	prior := op.Route.Revision
	op.Route.Revision++
	raw, err := json.Marshal(op.Route)
	if err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `UPDATE loomplane_domain_routes SET status=$4,revision=$5,body=$6,cloud_id=$8,server_id=$9,connection_id=$10 WHERE org_id=$1 AND project_id=$2 AND id=$3 AND revision=$7 RETURNING id`, op.Route.OrgID, op.Route.ProjectID, op.Route.ID, op.Route.Status, op.Route.Revision, raw, prior, op.Route.Input.CloudID, op.Route.Binding.ServerID, op.Route.Binding.ConnectionID).Scan(new(string))
	if err != nil {
		op.Route.Revision = prior
		return ErrConflict
	}
	// Keep the public route and the worker's accepted revision in one commit.
	// A crash after publication must not strand the next retry on an old CAS.
	priorOperation := op.Revision
	op.Revision++
	body, _, checkpoint, err := routeBytes(op)
	if err != nil {
		return err
	}
	if checkErr := tx.QueryRow(ctx, `UPDATE loomplane_route_operations SET revision=$3,body=$4,checkpoint=$5 WHERE org_id=$1 AND id=$2 AND revision=$6 AND generation=$7 RETURNING id`, op.Route.OrgID, op.ID, op.Revision, body, checkpoint, priorOperation, op.Generation).Scan(new(string)); checkErr != nil {
		return ErrConflict
	}
	return tx.Commit()
}

func (s *PgStore) BeginIngress(ctx context.Context, op *RouteOperation) (*IngressState, error) {
	if op.Route.Binding.BackendName() == RouteBackendKubernetes {
		if err := s.SaveRouteOperation(ctx, op); err != nil {
			return nil, err
		}
		return &IngressState{}, nil
	}
	state, err := s.BeginServerIngress(ctx, op.Route.OrgID, op.Route.Binding.ServerID)
	if err != nil {
		return nil, err
	}
	op.Generation = state.Generation
	if checkErr := s.SaveRouteOperation(ctx, op); checkErr != nil {
		return nil, checkErr
	}
	return state, nil
}
func (s *PgStore) BeginServerIngress(ctx context.Context, org, serverID string) (*IngressState, error) {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_ingress_configs(org_id,server_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, org, serverID)
	if err != nil {
		return nil, err
	}
	var raw []byte
	var revision, generation int64
	err = s.db.QueryRow(ctx, `UPDATE loomplane_ingress_configs SET generation=generation+1 WHERE org_id=$1 AND server_id=$2 RETURNING body,revision,generation`, org, serverID).Scan(&raw, &revision, &generation)
	if err != nil {
		return nil, err
	}
	var state IngressState
	if checkErr := json.Unmarshal(raw, &state); checkErr != nil {
		return nil, checkErr
	}
	state.Revision, state.Generation = revision, generation
	return &state, nil
}
func (s *PgStore) SaveIngress(ctx context.Context, op *RouteOperation, state *IngressState) error {
	if state.Generation != op.Generation {
		return ErrConflict
	}
	return s.SaveServerIngress(ctx, op.Route.OrgID, op.Route.Binding.ServerID, state)
}
func (s *PgStore) SaveServerIngress(ctx context.Context, org, serverID string, state *IngressState) error {
	prior := state.Revision
	state.Revision++
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	err = s.db.QueryRow(ctx, `UPDATE loomplane_ingress_configs SET revision=$3,body=$4 WHERE org_id=$1 AND server_id=$2 AND revision=$5 AND generation=$6 RETURNING generation`, org, serverID, state.Revision, raw, prior, state.Generation).Scan(new(int64))
	if err != nil {
		state.Revision = prior
		return ErrConflict
	}
	return nil
}

// A retained source is an accepted former binding, checked against its exact
// controller instance and accepted runtime without treating it as current placement.
func (s *PgStore) ValidateRetainedRoute(ctx context.Context, r Route) error {
	return s.validateRetainedRoute(ctx, r, false)
}

// ValidateRouteCleanup checks runtime identity without restoring a Cloud grant.
// It grants no authority to publish or move the route.
func (s *PgStore) ValidateRouteCleanup(ctx context.Context, r Route) error {
	return s.validateRetainedRoute(ctx, r, true)
}

// A route accepted on a catalog component checks the container of the service
// that declares its port, as routeBindingQuery chose it. Anything else, including
// a binding that no longer exists, checks main as before. Cleanup skips the
// routableComponent test so a port that stopped being routable can still be removed.
const retainedRoutedService = ` LEFT JOIN loomplane_bindings b ON b.org_id=i.tenant_id AND b.project_id=$7 AND b.environment_id=$14 AND b.name=$15 AND b.id=$13 AND b.subject='resource' AND b.provider='server'
 CROSS JOIN LATERAL (SELECT CASE WHEN b.id IS NULL THEN 'main' ELSE (SELECT svc->>'name' FROM jsonb_array_elements(i.services) svc
 WHERE EXISTS(SELECT 1 FROM jsonb_array_elements(svc->'ports') p WHERE (p->>'container')::int=$16) ORDER BY svc->>'name' LIMIT 1) END AS name) routed`

func (s *PgStore) validateRetainedRoute(ctx context.Context, r Route, cleanup bool) error {
	var instance string
	return storeError(s.db.QueryRow(ctx, `SELECT i.id FROM cp_instances i`+routeRuntimeJoin+retainedRoutedService+`
 JOIN loomplane_cloud_servers s ON s.org_id=i.tenant_id AND s.id=$3 AND s.cloud_id=$4 AND s.runtime_connection_id=$5 AND s.status='ready'
 JOIN loomplane_cloud_connections c ON c.org_id=s.org_id AND c.id=s.runtime_connection_id AND ($11 OR c.generation=$6) AND c.status='ready'
 WHERE i.tenant_id=$1 AND i.id=$2 AND `+routeRuntimeRevision+`=$9 AND `+routedRuntimeContainer+`=$10
 AND (b.id IS NULL OR $11 OR (`+routableComponentOn("$16")+`))
 AND ($11 OR EXISTS(SELECT 1 FROM loomplane_cloud_project_grants g WHERE g.org_id=s.org_id AND g.cloud_id=s.cloud_id AND g.project_id=$7 AND NOT g.revoked AND (cardinality(g.target_ids)=0 OR $8=ANY(g.target_ids))))
 AND $12<>'' AND c.details->>'docker_id'=$12`, r.OrgID, r.Binding.InstanceID, r.Binding.ServerID, r.Input.CloudID, r.Binding.ConnectionID, r.Binding.ConnectionGeneration, r.ProjectID, r.Binding.TargetID, r.Binding.ReleaseID, r.Binding.ContainerID, cleanup, r.Binding.RuntimeID,
		r.Binding.BindingID, r.Input.EnvironmentID, r.Input.Service, r.Input.Port).Scan(&instance))
}

func (s *PgStore) PendingRoutes(ctx context.Context) ([]*RouteOperation, error) {
	rows, err := s.db.Query(ctx, `SELECT body,principal,checkpoint FROM loomplane_route_operations WHERE status IN ('pending','running','waiting') ORDER BY id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []*RouteOperation{}
	for rows.Next() {
		op, scanErr := scanRouteOperation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, op)
	}
	return result, rows.Err()
}

// RemoveZone deletes one zone at revision. A zone with another zone under
// it is kept: its subdomains go first. That holds for one added since the
// caller looked too, since the organization lock orders this with
// CreateZone.
func (s *PgStore) RemoveZone(ctx context.Context, org, zoneID string, revision int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, org); checkErr != nil {
		return checkErr
	}
	var name string
	var below bool
	if checkErr := tx.QueryRow(ctx, `SELECT z.name, EXISTS(SELECT 1 FROM loomplane_dns_zones s WHERE s.org_id=z.org_id AND right(s.name, length(z.name)+1)='.'||z.name)
 FROM loomplane_dns_zones z WHERE z.org_id=$1 AND z.id=$2 AND z.revision=$3`, org, zoneID, revision).Scan(&name, &below); checkErr != nil {
		return storeError(checkErr)
	}
	if below {
		return fmt.Errorf("%w: a subdomain was added under %s since you looked, so %s was kept", ErrConflict, name, name)
	}
	if checkErr := tx.QueryRow(ctx, `DELETE FROM loomplane_dns_zones WHERE org_id=$1 AND id=$2 AND revision=$3 RETURNING id`, org, zoneID, revision).Scan(new(string)); checkErr != nil {
		return storeError(checkErr)
	}
	return tx.Commit()
}

func (s *PgStore) CompleteHandoff(ctx context.Context, op *RouteOperation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var generation int64
	if checkErr := tx.QueryRow(ctx, `SELECT o.generation FROM loomplane_route_operations o JOIN loomplane_ingress_configs i ON i.org_id=o.org_id AND i.server_id=$5 WHERE o.org_id=$1 AND o.id=$2 AND o.revision=$3 AND o.generation=$4 AND i.generation=$4 AND o.status<>'cancelled' AND o.body->>'handoff_id'=$6 AND o.route_id=$7 AND o.body->>'kind'='release' AND o.body->'route'->'binding'->>'server_id'=$5 FOR UPDATE OF o,i`, op.Route.OrgID, op.ID, op.Revision, op.Generation, op.Route.Binding.ServerID, op.HandoffID, op.Route.ID).Scan(&generation); checkErr != nil {
		return ErrConflict
	}
	if checkErr := tx.QueryRow(ctx, `UPDATE loomplane_route_handoffs SET released=true WHERE org_id=$1 AND id=$2 AND route_id=$3 RETURNING id`, op.Route.OrgID, op.HandoffID, op.Route.ID).Scan(new(string)); checkErr != nil {
		return storeError(checkErr)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM loomplane_connection_uses WHERE org_id=$1 AND kind='route_handoff' AND owner_id=$2`, op.Route.OrgID, op.HandoffID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PgStore) RouteOperations(ctx context.Context, org, project, route string) ([]*RouteOperation, error) {
	rows, err := s.db.Query(ctx, `SELECT body,principal,checkpoint FROM loomplane_route_operations WHERE org_id=$1 AND project_id=$2 AND route_id=$3 ORDER BY body->>'created_at' DESC,id DESC LIMIT 100`, org, project, route)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*RouteOperation{}
	for rows.Next() {
		op, scanErr := scanRouteOperation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

func (s *PgStore) scanRoutes(ctx context.Context, query string, args ...any) ([]*Route, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Route{}
	for rows.Next() {
		var raw []byte
		var route Route
		if checkErr := rows.Scan(&raw); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(raw, &route); checkErr != nil {
			return nil, checkErr
		}
		out = append(out, &route)
	}
	return out, rows.Err()
}

func (s *PgStore) OrgRoutes(ctx context.Context, org string) ([]*Route, error) {
	return s.scanRoutes(ctx, `SELECT body FROM loomplane_domain_routes WHERE org_id=$1 AND status<>'removed' ORDER BY hostname,id`, org)
}

func (s *PgStore) ServiceRoutes(ctx context.Context, org, project, environment, service string) ([]*Route, error) {
	return s.scanRoutes(ctx, `SELECT body FROM loomplane_domain_routes WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND body->'input'->>'service'=$4 AND status<>'removed' ORDER BY hostname,id`, org, project, environment, service)
}

func (s *PgStore) LatestRouteOperations(ctx context.Context, org string, routeIDs []string) (map[string]*RouteOperation, error) {
	out := map[string]*RouteOperation{}
	if len(routeIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT DISTINCT ON (route_id) body,principal,checkpoint FROM loomplane_route_operations WHERE org_id=$1 AND route_id=ANY($2) ORDER BY route_id,(body->>'created_at')::timestamptz DESC,id DESC`, org, routeIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		op, scanErr := scanRouteOperation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out[op.Route.ID] = op
	}
	return out, rows.Err()
}

func (s *PgStore) names(ctx context.Context, query, org string) (map[string]string, error) {
	rows, err := s.db.Query(ctx, query, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, name string
		if checkErr := rows.Scan(&key, &name); checkErr != nil {
			return nil, checkErr
		}
		out[key] = name
	}
	return out, rows.Err()
}

func (s *PgStore) ProjectNames(ctx context.Context, org string) (map[string]string, error) {
	return s.names(ctx, `SELECT id,name FROM loomplane_projects WHERE org_id=$1`, org)
}

func (s *PgStore) RuntimeNames(ctx context.Context, org string) (map[string]string, error) {
	return s.names(ctx, `SELECT id,coalesce(body->>'name','') FROM loomplane_cloud_servers WHERE org_id=$1
 UNION ALL SELECT id,name FROM loomplane_cloud_connections WHERE org_id=$1`, org)
}

// RouteHandoffReceipts reads accepted receipt owners without the public history
// cap. Release safety retains the maximum across every exact handoff attempt.
func (s *PgStore) RouteHandoffReceipts(ctx context.Context, org, project, route, handoff string) ([]*RouteOperation, error) {
	rows, err := s.db.Query(ctx, `SELECT body,principal,checkpoint FROM loomplane_route_operations WHERE org_id=$1 AND project_id=$2 AND route_id=$3 AND body->>'handoff_id'=$4 AND body->>'kind' IN ('cutover','rollback') AND status<>'preview'`, org, project, route, handoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RouteOperation
	for rows.Next() {
		op, scanErr := scanRouteOperation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, op)
	}
	return out, rows.Err()
}
