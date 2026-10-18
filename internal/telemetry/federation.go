package telemetry

// CatalogOperation records bounded operation/outcome labels. Endpoint addresses,
// identities and scope IDs never become metric labels.
func (r *Recorder) CatalogOperation(operation, outcome string) {
	r.count("loomplane_federation_catalog_operations_total", map[string]string{"operation": operation, "outcome": outcome})
}
