package manifest

// CatalogOnly identifies configurations a repository-free resource plan can
// execute. Unknown configurations keep normal source-pinning requirements.
func (m *Manifest) CatalogOnly() bool {
	return m != nil && m.Version == Version && len(m.Services) == 0 && len(m.Gateways) == 0 && len(m.Links) == 0
}
