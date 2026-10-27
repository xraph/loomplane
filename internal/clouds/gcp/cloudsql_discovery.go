package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// SQLCreateRequest is the durable request scope available before a CREATE.
// Caller records the original service account, including after key rotation.
type SQLCreateRequest struct {
	Instance        string    `json:"instance"`
	Region          string    `json:"region"`
	PhysicalID      string    `json:"physical_id"`
	Intent          string    `json:"intent"`
	DatabaseVersion string    `json:"database_version"`
	RequestedAt     time.Time `json:"requested_at"`
	Caller          string    `json:"caller"`
}

// SQLCreateProof retains the native evidence used to recover a lost response.
// It contains no credentials and never proves a database/user mutation payload.
type SQLCreateProof struct {
	SQLCreateRequest
	Project    string    `json:"project"`
	Operation  string    `json:"operation"`
	CreatedAt  time.Time `json:"created_at"`
	InsertedAt time.Time `json:"inserted_at"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
}

func (s *Session) createSource(ctx context.Context, request SQLCreateRequest) (*SQLInstance, string, error) {
	in, err := s.GetSQLInstance(ctx, request.Instance)
	if err != nil {
		return nil, "", err
	}
	pin, err := in.Incarnation(s.key.ProjectID, request.Region, request.Instance)
	if err != nil {
		return nil, "", err
	}
	if request.PhysicalID != "" && request.PhysicalID != pin || in.Settings.UserLabels[SQLIntentLabel] != request.Intent || in.DatabaseVersion != request.DatabaseVersion || in.State != "RUNNABLE" {
		return nil, "", errors.New("cloud sql create discovery does not match the original instance incarnation and intent")
	}
	return in, pin, nil
}

func (s *Session) matchesCreate(op sqlOperation, request SQLCreateRequest, created time.Time) bool {
	return op.Kind == "sql#operation" && op.TargetProject == s.key.ProjectID && op.TargetID == request.Instance && op.Type == "CREATE" && op.Status == "DONE" && op.Error == nil && op.Warning == nil && op.User == request.Caller &&
		!op.InsertedAt.IsZero() && !op.StartedAt.IsZero() && !op.EndedAt.IsZero() &&
		!op.InsertedAt.Before(request.RequestedAt) && !op.StartedAt.Before(op.InsertedAt) && !created.Before(op.StartedAt) && !op.EndedAt.Before(created)
}

// FindSQLCreate requires a complete operations scan with one possible original
// CREATE. Its native time interval must then contain the instance's creation.
// It only reads Google; uncertain writes are never replayed here.
func (s *Session) FindSQLCreate(ctx context.Context, request SQLCreateRequest) (*SQLCreateProof, error) {
	if !sqlNativeName(request.Instance) || request.Region == "" || request.Intent == "" || request.Caller == "" || request.RequestedAt.IsZero() || request.DatabaseVersion == "" {
		return nil, errors.New("cloud sql create discovery requires the original durable request context")
	}
	source, pin, err := s.createSource(ctx, request)
	if err != nil {
		return nil, err
	}
	created, err := time.Parse(time.RFC3339Nano, source.CreateTime)
	if err != nil {
		return nil, err
	}
	request.PhysicalID = pin
	if created.Before(request.RequestedAt) {
		return nil, errors.New("cloud sql source creation precedes the durable request")
	}
	token := ""
	tokens, seen := map[string]bool{}, map[string]bool{}
	var candidate *sqlOperation
	complete := false
	for range 10 {
		tokens[token] = true
		query := url.Values{"instance": {request.Instance}, "location": {request.Region}, "maxResults": {"100"}}
		if token != "" {
			query.Set("pageToken", token)
		}
		var page struct {
			Kind     string         `json:"kind"`
			Items    []sqlOperation `json:"items"`
			Next     string         `json:"nextPageToken"`
			Warnings []struct{}     `json:"warnings"`
		}
		if err := s.do(ctx, http.MethodGet, s.sqlPath("/operations")+"?"+query.Encode(), nil, &page); err != nil {
			return nil, fmt.Errorf("read cloud sql create discovery: %w", err)
		}
		if page.Kind != "sql#operationsList" || len(page.Items) > 100 || len(page.Warnings) > 0 {
			return nil, errors.New("cloud sql create discovery has incomplete native operation metadata")
		}
		for _, op := range page.Items {
			if !validSQLPathID(op.Name) || seen[op.Name] {
				return nil, errors.New("cloud sql create discovery has an invalid or repeated operation identity")
			}
			seen[op.Name] = true
			if op.TargetProject != s.key.ProjectID || op.TargetID != request.Instance {
				return nil, errors.New("cloud sql create discovery returned a foreign operation target")
			}
			if op.Kind != "sql#operation" || op.Type == "" || op.Type == "CREATE" && (op.User == "" || op.InsertedAt.IsZero()) {
				return nil, errors.New("cloud sql create discovery has incomplete native create metadata")
			}
			// Count possible attempts before checking status or the current source.
			// Otherwise a replacement can hide the original successful CREATE.
			if op.Type != "CREATE" || op.User != request.Caller || op.InsertedAt.Before(request.RequestedAt) {
				continue
			}
			if candidate != nil {
				return nil, errors.New("cloud sql create discovery is ambiguous")
			}
			candidate = &op
		}
		token = page.Next
		if token == "" {
			complete = true
			break
		}
		if len(token) > 4096 || tokens[token] {
			return nil, errors.New("cloud sql create discovery has invalid or repeated pagination")
		}
	}
	if !complete || candidate == nil || !s.matchesCreate(*candidate, request, created) {
		return nil, errors.New("cloud sql create discovery cannot establish one original successful operation from a complete scan")
	}
	var exact sqlOperation
	if err := s.do(ctx, http.MethodGet, s.sqlPath("/operations/"+url.PathEscape(candidate.Name)), nil, &exact); err != nil {
		return nil, err
	}
	if exact.Name != candidate.Name || !s.matchesCreate(exact, request, created) || !exact.InsertedAt.Equal(candidate.InsertedAt) || !exact.StartedAt.Equal(candidate.StartedAt) || !exact.EndedAt.Equal(candidate.EndedAt) {
		return nil, errors.New("cloud sql create discovery operation evidence changed")
	}
	if _, _, err := s.createSource(ctx, request); err != nil {
		return nil, err
	}
	return &SQLCreateProof{SQLCreateRequest: request, Project: s.key.ProjectID, Operation: exact.Name, CreatedAt: created.UTC(), InsertedAt: exact.InsertedAt.UTC(), StartedAt: exact.StartedAt.UTC(), EndedAt: exact.EndedAt.UTC()}, nil
}

// Validate checks retained evidence against its original checkpoint scope.
func (p *SQLCreateProof) Validate(project, region, instance, physical, intent, version string) error {
	if p == nil || p.Project != project || p.Region != region || p.Instance != instance || p.PhysicalID == "" || p.PhysicalID != physical || p.Intent != intent || p.DatabaseVersion != version || p.Caller == "" || p.RequestedAt.IsZero() || !validSQLPathID(p.Operation) || p.CreatedAt.IsZero() || p.InsertedAt.IsZero() || p.StartedAt.IsZero() || p.EndedAt.IsZero() || p.InsertedAt.Before(p.RequestedAt) || p.StartedAt.Before(p.InsertedAt) || p.CreatedAt.Before(p.StartedAt) || p.EndedAt.Before(p.CreatedAt) {
		return errors.New("cloud sql create recovery proof does not match its original scope and time interval")
	}
	source := &SQLInstance{Name: instance, Project: project, Region: region, ConnectionName: project + ":" + region + ":" + instance, CreateTime: p.CreatedAt.UTC().Format(time.RFC3339Nano)}
	pin, err := source.Incarnation(project, region, instance)
	if err != nil || pin != physical {
		return errors.New("cloud sql create recovery proof has a different source creation pin")
	}
	return nil
}
