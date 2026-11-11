package fleet

import (
	"errors"
	"strings"
)

var ErrAction = errors.New("invalid fleet action")

// ActionInput pins the instance version and carries exactly one typed payload.
type ActionInput struct {
	Kind               string        `json:"kind"`
	IdempotencyKey     string        `json:"idempotency_key"`
	ExpectedRevision   int64         `json:"expected_revision"`
	ExpectedGeneration int64         `json:"expected_generation"`
	Update             *UpdateInput  `json:"update,omitempty"`
	Scale              *ScaleInput   `json:"scale,omitempty"`
	Resize             *ResizeInput  `json:"resize,omitempty"`
	Suspend            *SuspendInput `json:"suspend,omitempty"`
	Resume             *ResumeInput  `json:"resume,omitempty"`
	Retire             *RetireInput  `json:"retire,omitempty"`
	Delete             *DeleteInput  `json:"delete,omitempty"`
	Restore            *RestoreInput `json:"restore,omitempty"`
	Move               *MoveInput    `json:"move,omitempty"`
}

// UpdateInput selects a release. Null maps retain existing values; empty maps
// reset them to the new template's defaults and required-field validation.
type UpdateInput struct {
	ReleaseID  string                    `json:"release_id"`
	Rollout    *RolloutMemberPin         `json:"rollout,omitempty"`
	Parameters map[string]ParameterValue `json:"parameters"`
	SecretRefs map[string]SecretRef      `json:"secret_refs"`
}

type ResizeInput struct {
	ProfileID string `json:"profile_id"`
}
type SuspendInput struct{}
type ResumeInput struct{}
type RetireInput struct {
	RetentionPolicyID string `json:"retention_policy_id"`
}
type DeleteInput struct {
	RetentionPolicyID string `json:"retention_policy_id"`
}
type RestoreInput struct {
	RecoveryPointID string `json:"recovery_point_id"`
	TargetStageID   string `json:"target_stage_id"`
	TargetProfileID string `json:"target_profile_id"`
}
type MoveInput struct {
	TargetCloudID string `json:"target_cloud_id"`
	TargetRegion  string `json:"target_region"`
	CutoverMode   string `json:"cutover_mode"`
}

func actionReference(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

// Validate checks shape only. Current policy, data and ownership are checked by
// the service and again at each authorized effect boundary.
func (in ActionInput) Validate() error {
	if !actionReference(in.IdempotencyKey) || in.ExpectedRevision < 1 || in.ExpectedGeneration < 1 {
		return ErrAction
	}
	count := 0
	for _, present := range []bool{in.Update != nil, in.Scale != nil, in.Resize != nil, in.Suspend != nil, in.Resume != nil, in.Retire != nil, in.Delete != nil, in.Restore != nil, in.Move != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return ErrAction
	}
	valid := false
	switch in.Kind {
	case "update":
		valid = in.Update != nil && actionReference(in.Update.ReleaseID)
	case "scale":
		if in.Scale == nil {
			return ErrAction
		}
		if err := in.Scale.Validate(); err != nil {
			return errors.Join(ErrAction, err)
		}
		valid = true
	case "resize":
		valid = in.Resize != nil && actionReference(in.Resize.ProfileID)
	case "suspend":
		valid = in.Suspend != nil
	case "resume":
		valid = in.Resume != nil
	case "retire":
		valid = in.Retire != nil && actionReference(in.Retire.RetentionPolicyID)
	case "delete":
		valid = in.Delete != nil && actionReference(in.Delete.RetentionPolicyID)
	case "restore":
		valid = in.Restore != nil && actionReference(in.Restore.RecoveryPointID) && actionReference(in.Restore.TargetStageID) && actionReference(in.Restore.TargetProfileID)
	case "move":
		valid = in.Move != nil && actionReference(in.Move.TargetCloudID) && actionReference(in.Move.TargetRegion) && in.Move.CutoverMode == "offline"
	}
	if !valid {
		return ErrAction
	}
	return nil
}
