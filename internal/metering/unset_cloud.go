//go:build cloud

package metering

// RefuseUnset is true where the edition bills: a service that spends the
// platform's money holds its calls until the gate has been chosen, so work
// queued before Start can't slip past it.
const RefuseUnset = true
