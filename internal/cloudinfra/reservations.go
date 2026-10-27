package cloudinfra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

// DrainReview fences the inventory the manager reviewed before disabling admission.
type DrainReview struct {
	ServerID       string       `json:"server_id"`
	ServerRevision int64        `json:"server_revision"`
	Review         string       `json:"review"`
	Reservations   int          `json:"reservations"`
	Usage          *ServerUsage `json:"usage"`
}
type MoveInput struct {
	ProjectID           string `json:"project_id"`
	EnvironmentID       string `json:"environment_id"`
	Service             string `json:"service"`
	PoolID              string `json:"pool_id"`
	DestinationServerID string `json:"destination_server_id"`
	Review              string `json:"review,omitempty"`
}
type MoveReview struct {
	SourceConfig   json.RawMessage `json:"-"`
	Input          MoveInput       `json:"input"`
	SourceServerID string          `json:"source_server_id"`
	Placement      Placement       `json:"placement"`
	Review         string          `json:"review"`
}

func digest(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (s *Service) PreviewDrain(ctx context.Context, p security.Principal, cloud, id string) (*DrainReview, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	st, ok := s.Store.(*PgStore)
	if !ok {
		return nil, ErrInvalid
	}
	return st.previewDrain(ctx, p.OrgID, cloud, id)
}
func (s *PgStore) previewDrain(ctx context.Context, org, cloud, id string) (*DrainReview, error) {
	v, err := s.GetServer(ctx, org, cloud, id)
	if err != nil {
		return nil, err
	}
	var refs []byte
	var count int
	err = s.db.QueryRow(ctx, `SELECT count(*),coalesce(jsonb_agg(body ORDER BY plan_id,service),'[]') FROM loomplane_capacity_reservations WHERE org_id=$1 AND server_id=$2`, org, id).Scan(&count, &refs)
	if err != nil {
		return nil, err
	}
	review := &DrainReview{ServerID: id, ServerRevision: v.Revision, Reservations: count}
	review.Usage, err = s.serverUsage(ctx, org, cloud, id)
	if err != nil {
		return nil, err
	}
	review.Review = digest([]any{org, cloud, id, v.Revision, json.RawMessage(refs), review.Usage})
	return review, nil
}
func (s *Service) ApplyDrain(ctx context.Context, p security.Principal, cloud, id string, in ApplyInput) (*Server, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	st, ok := s.Store.(*PgStore)
	if !ok {
		return nil, ErrInvalid
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT body FROM loomplane_cloud_servers WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR UPDATE`, p.OrgID, cloud, id).Scan(&raw); err != nil {
		return nil, storeError(err)
	}
	review, err := st.previewDrain(ctx, p.OrgID, cloud, id)
	if err != nil {
		return nil, err
	}
	if in.Review == "" || review.Review != in.Review {
		return nil, ErrConflict
	}
	var v Server
	if checkErr := json.Unmarshal(raw, &v); checkErr != nil {
		return nil, checkErr
	}
	v.Draining = true
	v.Revision++
	v.StateRevision++
	v.UpdatedAt = s.now().UTC()
	raw, err = json.Marshal(&v)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_cloud_servers SET body=$4,revision=$5 WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, p.OrgID, cloud, id, raw, v.Revision); err != nil {
		return nil, err
	}
	if checkErr := tx.Commit(); checkErr != nil {
		return nil, checkErr
	}
	return &v, nil
}

// PreviewMove selects an explicit stateless destination. Accepting it prepares
// the next deployment plan; the project still reviews that plan before effects.
func (s *Service) PreviewMove(ctx context.Context, p security.Principal, cloud string, in MoveInput) (*MoveReview, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	st, ok := s.Store.(*PgStore)
	if !ok {
		return nil, ErrInvalid
	}
	return st.previewMove(ctx, p.OrgID, cloud, in)
}
func (s *PgStore) previewMove(ctx context.Context, org, cloud string, in MoveInput) (*MoveReview, error) {
	var cfg []byte
	var bindingID, revision string
	err := s.db.QueryRow(ctx, `SELECT id,revision,config FROM loomplane_bindings WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND subject='service' AND name=$4`, org, in.ProjectID, in.EnvironmentID, in.Service).Scan(&bindingID, &revision, &cfg)
	if err != nil {
		return nil, storeError(err)
	}
	var old struct {
		CloudPlacement struct {
			Placement
			CloudID, TargetID string
			TargetRevision    int64
		}
		Catalog        json.RawMessage
		Scale          *struct{ Min, Max int }
		TargetSettings map[string]string
	}
	// Use explicit JSON tags for persisted service configuration.
	var doc map[string]json.RawMessage
	if checkErr := json.Unmarshal(cfg, &doc); checkErr != nil {
		return nil, checkErr
	}
	var pin struct {
		Placement
		CloudID        string `json:"cloud_id"`
		TargetID       string `json:"target_id"`
		TargetRevision int64  `json:"target_revision"`
	}
	if err = json.Unmarshal(doc["cloud_placement"], &pin); err != nil {
		return nil, ErrInvalid
	}
	if len(doc["catalog"]) > 0 && string(doc["catalog"]) != "null" {
		return nil, fmt.Errorf("%w: local data needs explicit migration", ErrInvalid)
	}
	if raw := doc["scale"]; len(raw) > 0 {
		if checkErr := json.Unmarshal(raw, &old.Scale); checkErr != nil {
			return nil, checkErr
		}
		if old.Scale != nil && (old.Scale.Min != 1 || old.Scale.Max != 1) {
			return nil, ErrInvalid
		}
	}
	if raw := doc["target_settings"]; len(raw) > 0 {
		if checkErr := json.Unmarshal(raw, &old.TargetSettings); checkErr != nil {
			return nil, checkErr
		}
	}
	if pin.CloudID != cloud || pin.PoolID != in.PoolID || in.DestinationServerID == "" || pin.ServerID == in.DestinationServerID {
		return nil, ErrInvalid
	}
	placement, err := s.PreviewPlacement(ctx, PlacementRequest{OrgID: org, ProjectID: in.ProjectID, EnvironmentID: in.EnvironmentID, CloudID: cloud, TargetID: pin.TargetID, TargetRevision: pin.TargetRevision, PoolID: in.PoolID, Service: in.Service, CPUMillis: pin.CPUMillis, MemoryBytes: pin.MemoryBytes, GPUKind: pin.GPUKind, GPUs: pin.GPUs, Replicas: 1, Architecture: old.TargetSettings["architecture"], StickyServerID: pin.ServerID, DestinationServerID: in.DestinationServerID})
	if err != nil {
		return nil, err
	}
	in.Review = ""
	out := &MoveReview{Input: in, SourceServerID: pin.ServerID, Placement: *placement}
	out.Review = digest([]any{org, cloud, bindingID, revision, json.RawMessage(cfg), out})
	out.SourceConfig = cfg
	return out, nil
}
func (s *Service) AcceptMove(ctx context.Context, p security.Principal, cloud string, in MoveInput) (*MoveReview, error) {
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	out, err := s.PreviewMove(ctx, p, cloud, in)
	if err != nil {
		return nil, err
	}
	if in.Review == "" || in.Review != out.Review {
		return nil, ErrConflict
	}
	st, ok := s.Store.(*PgStore)
	if !ok {
		return nil, ErrInvalid
	}
	_, err = st.db.Exec(ctx, `INSERT INTO loomplane_placement_moves(org_id,project_id,environment_id,service,cloud_id,pool_id,source_server_id,destination_server_id,review,expires_at,source_config) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(org_id,environment_id,service) DO UPDATE SET source_server_id=excluded.source_server_id,destination_server_id=excluded.destination_server_id,review=excluded.review,expires_at=excluded.expires_at,source_config=excluded.source_config WHERE loomplane_placement_moves.project_id=excluded.project_id AND loomplane_placement_moves.cloud_id=excluded.cloud_id AND loomplane_placement_moves.pool_id=excluded.pool_id`, p.OrgID, in.ProjectID, in.EnvironmentID, in.Service, cloud, in.PoolID, out.SourceServerID, in.DestinationServerID, out.Review, s.now().Add(15*time.Minute), out.SourceConfig)
	return out, err
}
