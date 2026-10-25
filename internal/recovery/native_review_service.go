package recovery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

type nativeReviewClaims struct {
	Version       int                 `json:"version"`
	RequestID     string              `json:"request_id"`
	OrgID         string              `json:"org_id"`
	UserID        string              `json:"user_id"`
	ProjectID     string              `json:"project_id"`
	EnvironmentID string              `json:"environment_id"`
	SourceID      string              `json:"source_id"`
	SourceDigest  string              `json:"source_digest"`
	Point         NativePoint         `json:"point"`
	Target        NativeRestoreTarget `json:"target"`
	TTLHours      int                 `json:"ttl_hours"`
	ValidUntil    time.Time           `json:"valid_until"`
}

type NativeCopyQueries interface {
	GetNativeRestoreByRequest(context.Context, string, string, string) (*NativeRestore, error)
	ListNativeCopies(context.Context, string, string, NativePageInput) (*NativeCopiesPage, error)
}

func (s *NativeService) ReviewCopy(ctx context.Context, p security.Principal, sourceID, pointID string, in NativeCopyReviewInput) (*NativeCopyReview, error) {
	if s.Admission == nil || len(s.ReviewKey) < sha256.Size || s.Store == nil || s.Backend == nil || s.Authz == nil || !requestKey.MatchString(in.RequestID) || in.TTLHours < 1 || in.TTLHours > 24 {
		return nil, ErrInvalid
	}
	source, err := s.authorizedSource(ctx, p, sourceID, security.RestoreManage)
	if err != nil {
		return nil, err
	}
	if checkErr := s.requireNative(ctx, p, source.ProjectID); checkErr != nil {
		return nil, checkErr
	}
	if source.Provider != "cloud-sql" && source.Provider != "rds" {
		return nil, ErrInvalid
	}
	target, err := s.Admission.ReviewNativeTarget(ctx, p.OrgID, source.ProjectID, in.CloudID, in.TargetID)
	if err != nil {
		return nil, err
	}
	point, err := s.CheckPointAt(ctx, p, sourceID, pointID, in.RestoreTime)
	if err != nil {
		return nil, err
	}
	capacity, err := nativeReviewCapacity(source.Binding, *point, target)
	if err != nil {
		return nil, err
	}
	digest, err := nativeSourceDigest(source.Binding)
	if err != nil {
		return nil, err
	}
	until := time.Now().UTC().Add(10 * time.Minute)
	claims := nativeReviewClaims{Version: 1, RequestID: in.RequestID, OrgID: p.OrgID, UserID: p.UserID, ProjectID: source.ProjectID, EnvironmentID: source.EnvironmentID, SourceID: source.ID, SourceDigest: digest, Point: *point, Target: target, TTLHours: in.TTLHours, ValidUntil: until}
	raw, err := json.Marshal(claims)
	if err != nil {
		return nil, err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	token := payload + "." + base64.RawURLEncoding.EncodeToString(s.signNativeReview(payload))
	return &NativeCopyReview{RestoreTime: point.RestoreTime, RequestID: in.RequestID, ReviewToken: token, SourceID: sourceID, PointID: pointID, Target: target, Capacity: capacity, TTLHours: in.TTLHours, ValidUntil: until, Notice: "Charges continue until deletion is confirmed. Cleanup can fail; check copy status. The copy must pass database verification before use."}, nil
}

func nativeReviewCapacity(source planning.Binding, point NativePoint, target NativeRestoreTarget) (NativeCopyCapacity, error) {
	cfg, err := NativeSQLCopyConfig(source, point, target)
	if err != nil || !nativeTargetPinned(target) {
		return NativeCopyCapacity{}, ErrInvalid
	}
	profile := cfg.Managed.Profile
	capacity := NativeCopyCapacity{ProfileID: profile.ID, ProfileRevision: profile.Revision, ComputeClass: profile.ComputeClass, StorageGB: profile.StorageGB, StorageType: profile.StorageType, Availability: profile.Availability}
	if point.Kind == "pitr" && source.Provider == "rds" {
		capacity.IOPS, capacity.ThroughputMBPS, capacity.MaxStorageGB = 3000, 125, profile.StorageGB
	}
	return capacity, nil
}

// NativeSQLCopyConfig is shared by admission and execution so their supported
// source profile, native backup and isolated destination cannot diverge.
func NativeSQLCopyConfig(source planning.Binding, point NativePoint, target NativeRestoreTarget) (*planning.ResourceConfig, error) {
	if source.Provider == "rds" {
		return nativeRDSCopyConfig(source, point, target)
	}
	if point.Kind == "pitr" {
		return nativeCloudSQLPITRCopyConfig(source, point, target)
	}
	if point.PITR != nil || point.RestoreTime != nil {
		return nil, ErrInvalid
	}
	var cfg planning.ResourceConfig
	profile, err := manageddb.Postgres("cloud-sql", "17")
	parts := strings.Split(point.ProviderID, "/")
	instance := strings.Split(source.ExternalID, ":")
	if err != nil || source.Subject != planning.ResourceSubject || source.Provider != "cloud-sql" || source.PhysicalID == "" || json.Unmarshal(source.Config, &cfg) != nil || cfg.Provider != source.Provider || cfg.Kind != "postgres" || cfg.Version != "17" || cfg.Region != source.Region || cfg.Managed == nil || cfg.Managed.Profile != *profile || cfg.Managed.ConnectionID != source.ConnectionID || cfg.Managed.AccountIdentity == "" || point.Engine != "postgres" || point.Version != "17" || !point.Encrypted || len(parts) != 4 || parts[0] != "projects" || parts[2] != "backups" || parts[1] == "" || parts[3] != point.Name || len(instance) != 3 || instance[0] != parts[1] || instance[1] != source.Region || instance[2] == "" || target.Provider != "cloud-sql" || target.NativeProject == "" || target.NativeProject == parts[1] || target.Region == "" {
		return nil, ErrInvalid
	}
	return &cfg, nil
}

func (s *NativeService) signNativeReview(payload string) []byte {
	mac := hmac.New(sha256.New, s.ReviewKey)
	_, _ = mac.Write([]byte(payload)) // hash.Hash.Write never returns an error.
	return mac.Sum(nil)
}

func (s *NativeService) readNativeReview(p security.Principal, token string) (*nativeReviewClaims, error) {
	if len(s.ReviewKey) < sha256.Size || len(token) > 32<<10 {
		return nil, ErrInvalid
	}
	payload, signature, ok := strings.Cut(token, ".")
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if !ok || err != nil || !hmac.Equal(sig, s.signNativeReview(payload)) {
		return nil, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, ErrInvalid
	}
	var claims nativeReviewClaims
	if json.Unmarshal(raw, &claims) != nil || claims.Version != 1 || !requestKey.MatchString(claims.RequestID) || claims.ProjectID == "" || claims.EnvironmentID == "" || claims.SourceID == "" || claims.Point.ID == "" || claims.Point.SourceID != claims.SourceID || claims.TTLHours < 1 || claims.TTLHours > 24 || claims.ValidUntil.IsZero() || !nativeTargetPinned(claims.Target) {
		return nil, ErrInvalid
	}
	if p.OrgID == "" || p.UserID == "" || claims.OrgID != p.OrgID || claims.UserID != p.UserID {
		return nil, security.ErrForbidden
	}
	return &claims, nil
}

func (s *NativeService) CreateCopy(ctx context.Context, p security.Principal, in NativeCopyCreateInput) (*NativeRestore, error) {
	queries, ok := s.Admission.(NativeCopyQueries)
	if !ok || s.Store == nil || s.EnqueueCopy == nil || !in.AcknowledgeCharges || !requestKey.MatchString(in.RequestID) {
		return nil, ErrInvalid
	}
	claims, err := s.readNativeReview(p, in.ReviewToken)
	if err != nil {
		return nil, err
	}
	if claims.RequestID != in.RequestID {
		return nil, ErrConflict
	}
	if checkErr := s.requireNative(ctx, p, claims.ProjectID); checkErr != nil {
		return nil, checkErr
	}
	existing, err := queries.GetNativeRestoreByRequest(ctx, p.OrgID, claims.ProjectID, in.RequestID)
	if err == nil {
		expected := &NativeRestore{OrgID: p.OrgID, ProjectID: claims.ProjectID, EnvironmentID: claims.EnvironmentID, SourceID: claims.SourceID, PointID: claims.Point.ID, Point: claims.Point, Target: claims.Target, RequestID: in.RequestID, CreatedBy: p.UserID, CreatedAt: existing.CreatedAt, ExpiresAt: existing.CreatedAt.Add(time.Duration(claims.TTLHours) * time.Hour)}
		if !sameNativeRestoreRequest(existing, expected) || checkReviewedNativeSource(existing, claims.SourceDigest) != nil {
			return nil, ErrConflict
		}
		return s.enqueueNativeCopy(ctx, existing)
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if !time.Now().Before(claims.ValidUntil) {
		return nil, ErrInvalid
	}
	if checkErr := s.Admission.CheckNativeTarget(ctx, &NativeRestore{OrgID: p.OrgID, ProjectID: claims.ProjectID, Target: claims.Target}); checkErr != nil {
		return nil, checkErr
	}
	source, err := s.authorizedSource(ctx, p, claims.SourceID, security.RestoreManage)
	if err != nil {
		return nil, err
	}
	if source.ProjectID != claims.ProjectID || source.EnvironmentID != claims.EnvironmentID {
		return nil, ErrConflict
	}
	point, err := s.CheckPointAt(ctx, p, claims.SourceID, claims.Point.ID, claims.Point.RestoreTime)
	if err != nil {
		return nil, err
	}
	if !sameNativePointSelection(*point, claims.Point) {
		return nil, ErrConflict
	}
	if _, err = nativeReviewCapacity(source.Binding, *point, claims.Target); err != nil {
		return nil, err
	}
	id, err := typeid.Generate("nativecopy")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	r, err := s.Admission.CreateReviewedNativeRestore(ctx, &NativeRestore{ID: id.String(), OrgID: p.OrgID, ProjectID: claims.ProjectID, EnvironmentID: claims.EnvironmentID, SourceID: claims.SourceID, PointID: claims.Point.ID, Point: claims.Point, Target: claims.Target, RequestID: in.RequestID, CreatedBy: p.UserID, CreatedAt: now, ExpiresAt: now.Add(time.Duration(claims.TTLHours) * time.Hour)}, claims.SourceDigest)
	if err != nil {
		return nil, err
	}
	if r.ID == id.String() && s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: "service.native_restore.request", Severity: "info", Principal: p, ProjectID: r.ProjectID, Attributes: map[string]any{"restore_id": r.ID, "source_id": r.SourceID, "point_id": r.PointID, "cloud_id": r.Target.CloudID, "target_id": r.Target.TargetID, "ttl_hours": claims.TTLHours}})
	}
	return s.enqueueNativeCopy(ctx, r)
}

func (s *NativeService) enqueueNativeCopy(ctx context.Context, r *NativeRestore) (*NativeRestore, error) {
	if r.Status == "queued" {
		return r, s.EnqueueCopy(ctx, r.OrgID, r.ID)
	}
	return r, nil
}

func (s *NativeService) GetCopy(ctx context.Context, p security.Principal, id string) (*NativeRestore, error) {
	if s.Copies == nil {
		return nil, ErrInvalid
	}
	r, err := s.Copies.GetNativeRestore(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if r == nil || r.ID != id || r.OrgID != p.OrgID {
		return nil, ErrNotFound
	}
	if err := s.requireNativePermission(ctx, p, r.ProjectID, security.BackupRead); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *NativeService) ListCopies(ctx context.Context, p security.Principal, project string, page NativePageInput) (*NativeCopiesPage, error) {
	if err := s.requireNativePermission(ctx, p, project, security.BackupRead); err != nil {
		return nil, err
	}
	queries, ok := s.Admission.(NativeCopyQueries)
	if !ok {
		return nil, ErrInvalid
	}
	if _, err := page.pageSize(); err != nil {
		return nil, err
	}
	return queries.ListNativeCopies(ctx, p.OrgID, project, page)
}
