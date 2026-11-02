package workloadidentity

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	domain "github.com/xraph/loomplane/pkg/federation"
)

var ErrWorkloadUncertain = errors.New("workload registration creation outcome remains unresolved")

// WorkloadIntent comes from an authorized controller after live owner-chain and
// image verification. The guard must recheck that authority and live workload.
// This journal does not expose issuance through a management endpoint.
type WorkloadIntent struct {
	PodName         string                 `json:"pod_name,omitempty"`
	OperatingSystem string                 `json:"operating_system,omitempty"`
	Architecture    string                 `json:"architecture,omitempty"`
	ImageIndexes    [][]byte               `json:"image_indexes,omitempty"`
	BindingID       string                 `json:"binding_id"`
	Controller      kube.ControllerReceipt `json:"controller"`
	Workload        WorkloadBinding        `json:"workload"`
}
type WorkloadRecord struct {
	ID           string         `json:"id"`
	Intent       WorkloadIntent `json:"intent"`
	Phase        string         `json:"-"`
	Settled      bool           `json:"-"`
	ReleaseAfter time.Time      `json:"-"`
}
type WorkloadIssuer interface {
	RegisterWorkload(context.Context, WorkloadBinding) (*spiretypes.Entry, error)
}
type WorkloadRevoker interface {
	Revoke(context.Context, string) error
}
type WorkloadStore struct {
	db     *pgdriver.PgDB
	domain string
}

func NewWorkloadStore(db *pgdriver.PgDB, trustDomain string) *WorkloadStore {
	return &WorkloadStore{db: db, domain: trustDomain}
}

// Current ownership is checked in SQL so the same predicate drives issuance and
// cleanup eligibility. Receipt replacement or a changed binding invalidates it.
const workloadCurrentFrom = `loomplane_workload_registrations w
 JOIN loomplane_federation_destinations d ON d.org_id=w.org_id AND d.id=w.destination_id
 JOIN loomplane_bindings b ON b.org_id=w.org_id AND b.id=w.binding_id
 JOIN loomplane_workload_controllers c ON c.org_id=w.org_id AND c.connection_id=w.connection_id AND c.instance_id=w.instance_id AND c.runtime_ref=w.runtime_ref
 JOIN loomplane_cloud_connections k ON k.org_id=w.org_id AND k.id=w.connection_id`
const workloadCurrentWhere = `NOT d.quarantined AND d.project_id=w.project_id AND d.environment_id=w.environment_id
 AND d.connection_id=w.connection_id AND d.generation=w.destination_generation AND d.identity_revision=w.identity_revision
 AND b.project_id=w.project_id AND b.environment_id=w.environment_id AND b.subject='service' AND b.name=w.service_id
 AND b.provider='kubernetes' AND b.engine='ctrlplane' AND b.external_id=w.instance_id AND b.connection_id=w.connection_id
 AND b.revision=w.image AND b.plan_id=w.plan_id AND b.drift=''
 AND c.body=w.body->'intent'->'controller' AND k.status='ready' AND k.cloud='kubernetes'
 AND k.details->>'cluster_ca'=c.body->>'cluster_ca' AND k.details->>'namespace_uid'=c.body->>'namespace_uid' AND k.details->>'namespace'=c.body->>'namespace'`

func (s *WorkloadStore) Reserve(ctx context.Context, in WorkloadIntent, guard func(context.Context) error) (record *WorkloadRecord, err error) {
	r, b := in.Controller, in.Workload.Binding
	if guard == nil || !domain.ValidIdentitySegment(in.BindingID) || !r.Valid() || b.EntryID != "" || b.Method != "kubernetes" || r.Scope != b.Destination.Scope || r.ConnectionID != b.Destination.ConnectionID || r.ClusterCA != b.Destination.Identity.Account || r.NamespaceUID != b.Destination.Identity.NamespaceUID || r.NamespaceUID != b.Destination.Identity.ResourceID || r.Namespace != b.Namespace || r.NamespaceUID != b.NamespaceUID || r.ServiceAccount != b.ServiceAccount || r.Container != b.Container || r.ServiceID != in.Workload.Workload.ServiceID {
		return nil, ErrIdentity
	}
	if selector, valid := domain.ResolveImageSelector(r.Image, in.Workload.ContainerImage, in.OperatingSystem, in.Architecture, in.ImageIndexes); !valid || selector != in.Workload.ContainerImage {
		return nil, ErrIdentity
	}
	in.Workload.Binding.EntryID = uuid.NewString()
	validator := &SPIREIssuer{domain: s.domain, now: time.Now}
	if _, err = validator.workloadEntry(in.Workload); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = enrollmentFinish(tx, err) }()
	var destination string
	if stepErr := tx.QueryRow(ctx, `SELECT id FROM loomplane_federation_destinations WHERE org_id=$1 AND id=$2 FOR UPDATE`, r.OrgID, b.Destination.ID).Scan(&destination); stepErr != nil {
		return nil, enrollmentError(stepErr)
	}
	var count int
	if stepErr := tx.QueryRow(ctx, `SELECT count(*) FROM loomplane_workload_registrations WHERE org_id=$1 AND destination_id=$2`, r.OrgID, destination).Scan(&count); stepErr != nil {
		return nil, stepErr
	}
	if count >= 128 {
		return nil, ErrIdentity
	}
	record = &WorkloadRecord{ID: uuid.NewString(), Intent: in, Phase: "pending", Settled: true}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_workload_registrations(id,org_id,project_id,environment_id,destination_id,destination_generation,identity_revision,connection_id,instance_id,runtime_ref,binding_id,service_id,plan_id,image,entry_id,phase,body,revoke_after) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,'pending',$16,$17)`, record.ID, r.OrgID, r.ProjectID, r.EnvironmentID, destination, b.Destination.Generation, b.Destination.Identity.Revision(), r.ConnectionID, r.InstanceID, r.RuntimeRef, in.BindingID, r.ServiceID, r.PlanID, r.Image, in.Workload.Binding.EntryID, raw, in.Workload.PruneAfter); err != nil {
		return nil, err
	}
	if stepErr := checkWorkloadCurrent(ctx, tx, r.OrgID, record.ID); stepErr != nil {
		return nil, stepErr
	}
	if stepErr := guard(ctx); stepErr != nil {
		return nil, stepErr
	}
	if stepErr := workloadEvent(ctx, tx, record, "reserved"); stepErr != nil {
		return nil, stepErr
	}
	return record, tx.Commit()
}
func checkWorkloadCurrent(ctx context.Context, tx driver.Tx, org, id string) error {
	var found string
	return enrollmentError(tx.QueryRow(ctx, `SELECT w.id FROM `+workloadCurrentFrom+` WHERE w.org_id=$1 AND w.id=$2 AND `+workloadCurrentWhere+` FOR SHARE OF d,b,c,k`, org, id).Scan(&found))
}
func (s *WorkloadStore) Get(ctx context.Context, org, id string) (*WorkloadRecord, error) {
	return scanWorkload(s.db.QueryRow(ctx, `SELECT body,phase,settled,release_after FROM loomplane_workload_registrations WHERE org_id=$1 AND id=$2`, org, id), org, id)
}
func loadWorkload(ctx context.Context, tx driver.Tx, org, id string) (*WorkloadRecord, error) {
	return scanWorkload(tx.QueryRow(ctx, `SELECT body,phase,settled,release_after FROM loomplane_workload_registrations WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id), org, id)
}
func scanWorkload(row interface{ Scan(...any) error }, org, id string) (*WorkloadRecord, error) {
	var raw []byte
	var phase string
	var settled bool
	var release *time.Time
	if err := row.Scan(&raw, &phase, &settled, &release); err != nil {
		return nil, enrollmentError(err)
	}
	var r WorkloadRecord
	if json.Unmarshal(raw, &r) != nil || r.ID != id || r.Intent.Controller.OrgID != org || r.Intent.Workload.Workload.OrgID != org || !validEntryID(r.Intent.Workload.Binding.EntryID) {
		return nil, ErrIdentity
	}
	r.Phase, r.Settled = phase, settled
	if release != nil {
		r.ReleaseAfter = *release
	}
	return &r, nil
}
func workloadEvent(ctx context.Context, tx driver.Tx, r *WorkloadRecord, phase string) error {
	raw, err := json.Marshal(map[string]string{"id": r.ID, "phase": phase})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT loomplane_append_event($1,$2,$3,$4,$5)`, r.Intent.Controller.OrgID, r.Intent.Controller.ProjectID, "workload.registration."+phase, r.ID, raw)
	return err
}

// Issue commits its one-time claim before contacting SPIRE. An interrupted or
// ambiguous call cannot be replayed. Its journal remains for repeated revocation
// and operator reconciliation; a successful delete alone cannot settle a create.
func (s *WorkloadStore) Issue(ctx context.Context, org, id string, issuer WorkloadIssuer, guard func(context.Context) error) error {
	if issuer == nil || guard == nil {
		return ErrIdentity
	}
	r, err := s.claimWorkload(ctx, org, id, guard)
	if err != nil || r == nil {
		return err
	}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	entry, err := issuer.RegisterWorkload(call, r.Intent.Workload)
	if err != nil {
		return errors.Join(ErrWorkloadUncertain, err)
	}
	if entry == nil || entry.Id != r.Intent.Workload.Binding.EntryID {
		return errors.Join(ErrWorkloadUncertain, ErrIdentity)
	}
	return s.settleWorkload(ctx, r, guard)
}
func (s *WorkloadStore) claimWorkload(ctx context.Context, org, id string, guard func(context.Context) error) (record *WorkloadRecord, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = enrollmentFinish(tx, err) }()
	r, err := loadWorkload(ctx, tx, org, id)
	if err != nil {
		return nil, err
	}
	if !r.Settled {
		return nil, ErrWorkloadUncertain
	}
	if r.Phase != "pending" && r.Phase != "ready" {
		return nil, ErrIdentity
	}
	validator := &SPIREIssuer{domain: s.domain, now: time.Now}
	if _, err = validator.workloadEntry(r.Intent.Workload); err != nil {
		return nil, err
	}
	if stepErr := checkWorkloadCurrent(ctx, tx, org, id); stepErr != nil {
		return nil, stepErr
	}
	if stepErr := guard(ctx); stepErr != nil {
		return nil, stepErr
	}
	if r.Phase == "ready" {
		return nil, tx.Commit()
	}
	var claimed string
	if stepErr := tx.QueryRow(ctx, `UPDATE loomplane_workload_registrations SET phase='issuing',settled=false,updated_at=clock_timestamp() WHERE org_id=$1 AND id=$2 AND revoke_after>clock_timestamp() RETURNING id`, org, id).Scan(&claimed); stepErr != nil {
		return nil, enrollmentError(stepErr)
	}
	if stepErr := workloadEvent(ctx, tx, r, "issuing"); stepErr != nil {
		return nil, stepErr
	}
	return r, tx.Commit()
}
func (s *WorkloadStore) settleWorkload(ctx context.Context, r *WorkloadRecord, guard func(context.Context) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = enrollmentFinish(tx, err) }()
	current, err := loadWorkload(ctx, tx, r.Intent.Controller.OrgID, r.ID)
	if err != nil {
		return err
	}
	if current.Settled || (current.Phase != "issuing" && current.Phase != "revoking") {
		return ErrIdentity
	}
	phase := "revoking"
	if current.Phase == "issuing" && current.Intent.Workload.PruneAfter.After(time.Now()) {
		if checkErr := checkWorkloadCurrent(ctx, tx, r.Intent.Controller.OrgID, r.ID); checkErr == nil {
			if guardErr := guard(ctx); guardErr == nil {
				phase = "ready"
			}
		} else if !errors.Is(checkErr, ErrIdentity) {
			return checkErr
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_workload_registrations SET phase=$3,settled=true,next_attempt=clock_timestamp(),updated_at=clock_timestamp() WHERE org_id=$1 AND id=$2`, r.Intent.Controller.OrgID, r.ID, phase); err != nil {
		return err
	}
	if stepErr := workloadEvent(ctx, tx, r, phase); stepErr != nil {
		return stepErr
	}
	if stepErr := tx.Commit(); stepErr != nil {
		return stepErr
	}
	if phase != "ready" {
		return ErrIdentity
	}
	return nil
}
