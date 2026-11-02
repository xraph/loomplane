package kubernetes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

// farpPrefix starts every annotation key a service sends Octopus, and
// farpMaxValue bounds each value.
const (
	farpPrefix   = "farp."
	farpMaxValue = 1024
)

// farpService is a service's opt-in to FARP: the Octopus resource it feeds
// and the metadata Octopus reads from the Service's annotations. A nil
// farpService is a service that did not opt in.
type farpService struct {
	Gateway  string            `json:"gateway"`
	Metadata map[string]string `json:"metadata"`
}

// farpFromLabels decodes and checks LabelFarp before anything is written.
// The value is JSON, so it cannot be an object label; only the Service
// gets the label and annotations it turns into.
func farpFromLabels(labels map[string]string) (*farpService, error) {
	raw, ok := labels[owned.LabelFarp]
	if !ok {
		return nil, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	f := &farpService{}
	if err := decoder.Decode(f); err != nil {
		return nil, fmt.Errorf("invalid FARP label: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid FARP label: unexpected data after the value")
	}
	if f.Gateway == "" || len(validation.IsValidLabelValue(f.Gateway)) > 0 {
		return nil, errors.New("invalid FARP label: the gateway is not a valid Kubernetes label value")
	}
	for key, value := range f.Metadata {
		if !strings.HasPrefix(key, farpPrefix) || len(validation.IsQualifiedName(key)) > 0 {
			return nil, fmt.Errorf("invalid FARP label: metadata key %q must be an annotation key starting with %q", key, farpPrefix)
		}
		if len(value) > farpMaxValue {
			return nil, fmt.Errorf("invalid FARP label: metadata value for %q is over %d bytes", key, farpMaxValue)
		}
	}
	return f, nil
}

// decorate adds the labels Octopus selects a Service by and the metadata
// annotations it reads. The workload's own labels do not change.
func (f *farpService) decorate(meta *metav1.ObjectMeta) {
	if f == nil {
		return
	}
	meta.Labels[owned.FarpEnabledLabel] = "true"
	meta.Labels[owned.FarpGatewayLabel] = f.Gateway
	if len(f.Metadata) > 0 {
		meta.Annotations = maps.Clone(f.Metadata)
	}
}
