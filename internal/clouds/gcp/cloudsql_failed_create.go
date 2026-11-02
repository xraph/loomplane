package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// SQLFailedCreateProof permits cleanup only. A terminal CREATE does not imply
// readiness, and absence does not grant ownership of a later reused name.
type SQLFailedCreateProof struct {
	SQLCreateProof
	Absent bool `json:"absent"`
}

func (p *SQLFailedCreateProof) Validate(project, region, instance, physical, intent, version string) error {
	if p == nil || p.Project != project || p.Region != region || p.Instance != instance || p.PhysicalID != physical || p.Intent != intent || p.DatabaseVersion != version || p.Caller == "" || p.RequestedAt.IsZero() || !validSQLPathID(p.Operation) || p.InsertedAt.IsZero() || p.StartedAt.IsZero() || p.EndedAt.IsZero() || p.InsertedAt.Before(p.RequestedAt) || p.StartedAt.Before(p.InsertedAt) || p.EndedAt.Before(p.StartedAt) {
		return errors.New("cloud sql failed create proof differs from its original request")
	}
	if p.Absent {
		if !p.CreatedAt.IsZero() {
			return errors.New("absent failed create has unexpected instance evidence")
		}
		return nil
	}
	return p.SQLCreateProof.Validate(project, region, instance, physical, intent, version)
}

// InspectSQLFailedCreate reads only a captured CREATE. A nil proof means the
// operation has not failed terminally; ordinary success handling remains separate.
func (s *Session) InspectSQLFailedCreate(ctx context.Context, request SQLCreateRequest, operation string, check func(context.Context) error) (*SQLFailedCreateProof, error) {
	if !validSQLPathID(operation) || !sqlNativeName(request.Instance) || request.Region == "" || request.Intent == "" || request.Caller == "" || request.RequestedAt.IsZero() || request.DatabaseVersion == "" || check == nil {
		return nil, errors.New("cloud sql failed create cleanup requires its captured operation and durable request")
	}
	if err := check(ctx); err != nil {
		return nil, err
	}
	var op sqlOperation
	if err := s.do(ctx, http.MethodGet, s.sqlPath("/operations/"+url.PathEscape(operation)), nil, &op); err != nil {
		return nil, err
	}
	if err := s.validateSQLOperation(op, operation, request.Instance, []string{"CREATE"}, true); err != nil {
		return nil, err
	}
	if op.Status != "DONE" || op.Error == nil {
		return nil, nil
	}
	if op.Warning != nil || op.User != request.Caller {
		return nil, errors.New("cloud sql failed create has incomplete or foreign operation evidence")
	}
	proof := &SQLFailedCreateProof{SQLCreateProof: SQLCreateProof{SQLCreateRequest: request, Project: s.key.ProjectID, Operation: operation, InsertedAt: op.InsertedAt, StartedAt: op.StartedAt, EndedAt: op.EndedAt}, Absent: true}
	if err := proof.Validate(s.key.ProjectID, request.Region, request.Instance, request.PhysicalID, request.Intent, request.DatabaseVersion); err != nil {
		return nil, err
	}
	if err := check(ctx); err != nil {
		return nil, err
	}
	in, err := s.GetSQLInstance(ctx, request.Instance)
	if IsNotFound(err) {
		return proof, check(ctx)
	}
	if err != nil {
		return nil, err
	}
	pin, err := in.Incarnation(s.key.ProjectID, request.Region, request.Instance)
	if err != nil {
		return nil, err
	}
	created, err := time.Parse(time.RFC3339Nano, in.CreateTime)
	if err != nil || (request.PhysicalID != "" && request.PhysicalID != pin) || in.Kind != "sql#instance" || in.DatabaseVersion != request.DatabaseVersion || in.Settings.UserLabels[SQLIntentLabel] != request.Intent || in.Settings.UserLabels["managed-by"] != "loomplane" {
		return nil, errors.New("cloud sql failed create instance differs from the original intent")
	}
	proof.PhysicalID, proof.CreatedAt, proof.Absent = pin, created, false
	if err := proof.Validate(s.key.ProjectID, request.Region, request.Instance, pin, request.Intent, request.DatabaseVersion); err != nil {
		return nil, err
	}
	return proof, check(ctx)
}
