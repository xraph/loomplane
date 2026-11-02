package planning

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
)

// OwnsResources validates non-secret provider ownership evidence. An empty
// object represents a binding with no auxiliary resources to remove.
func (b *Binding) OwnsResources() (bool, error) {
	if len(b.OwnedResources) == 0 {
		return false, nil
	}
	if b.PhysicalID == "" {
		return false, errors.New("binding ownership requires the original physical resource identity")
	}
	return true, nil
}

// RetainsOwnership accepts additional resource kinds but keeps every saved
// identity unchanged. JSON formatting and object key order do not affect identity.
func (b *Binding) RetainsOwnership(original *Binding) bool {
	for key, previous := range original.OwnedResources {
		current, ok := b.OwnedResources[key]
		if !ok || !sameOwnershipValue(previous, current) {
			return false
		}
	}
	return true
}

func sameOwnershipValue(a, b json.RawMessage) bool {
	var left, right any
	x, y := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	x.UseNumber()
	y.UseNumber()
	if !json.Valid(a) || !json.Valid(b) || x.Decode(&left) != nil || y.Decode(&right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}
