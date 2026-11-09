//go:build !cloud

package metering

// RefuseUnset is false where nothing bills: until a gate is chosen,
// everything runs.
const RefuseUnset = false
