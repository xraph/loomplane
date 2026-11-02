package cloudinfra

import (
	"encoding/json"
	"errors"
)

// Aggregate versions describe mutable persistence bodies. Approved reviews and
// workload inputs retain their original JSON and economic fingerprints.
func (e *ClusterCapacityEnvelope) UnmarshalJSON(raw []byte) error {
	type body ClusterCapacityEnvelope
	var value body
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value.SchemaVersion != 1 {
		return errors.Join(ErrConflict, ErrClusterAggregateUnsupported)
	}
	*e = ClusterCapacityEnvelope(value)
	return nil
}

func (h *ClusterWorkloadHold) UnmarshalJSON(raw []byte) error {
	type body ClusterWorkloadHold
	var value body
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value.SchemaVersion != 1 || value.Revision < 1 || value.UpdatedAt.IsZero() {
		return errors.Join(ErrConflict, ErrClusterAggregateUnsupported)
	}
	*h = ClusterWorkloadHold(value)
	return nil
}
