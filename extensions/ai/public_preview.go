package ai

import (
	infraext "github.com/xraph/loomplane/extensions/cloudinfra"
	"github.com/xraph/loomplane/internal/models"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/pkg/provider"
)

type DeployPreview struct {
	Entry              models.EntrySummary  `json:"entry"`
	Quantization       string               `json:"quantization"`
	Kind               string               `json:"kind"`
	Version            string               `json:"version"`
	Target             models.TargetChoice  `json:"target"`
	TargetName         string               `json:"target_name"`
	GPU                *provider.GPURequest `json:"gpu,omitempty"`
	Fit                registry.Fit         `json:"fit"`
	MaxModelLen        int                  `json:"max_model_len"`
	Resource           string               `json:"resource"`
	AddsTo             string               `json:"adds_to,omitempty"`
	Cost               models.Cost          `json:"cost"`
	Environments       []models.Reach       `json:"environments"`
	Gated              bool                 `json:"gated"`
	TokenReady         bool                 `json:"token_ready"`
	Problems           []models.Problem     `json:"problems"`
	CanDeploy          bool                 `json:"can_deploy"`
	Server             *infraext.Operation  `json:"server,omitempty"`
	ServerReviewNeeded bool                 `json:"server_review_needed,omitempty"`
}

func publicDeployPreview(v *models.DeployPreview) *DeployPreview {
	if v == nil {
		return nil
	}
	return &DeployPreview{Entry: v.Entry, Quantization: v.Quantization, Kind: v.Kind, Version: v.Version, Target: v.Target, TargetName: v.TargetName, GPU: v.GPU, Fit: v.Fit, MaxModelLen: v.MaxModelLen, Resource: v.Resource, AddsTo: v.AddsTo, Cost: v.Cost, Environments: v.Environments, Gated: v.Gated, TokenReady: v.TokenReady, Problems: v.Problems, CanDeploy: v.CanDeploy, Server: infraext.PublicOperation(v.Server), ServerReviewNeeded: v.ServerReviewNeeded}
}
