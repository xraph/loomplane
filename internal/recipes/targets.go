package recipes

// LookupForTarget keeps platform-specific storage contracts in the reviewed recipe.
// Engines registered with Default resolve through it; the five built-in
// engines keep their original tables.
func LookupForTarget(kind, version, architecture, target string) (*Recipe, error) {
	if e, ok := Default().engine(kind); ok && !e.builtin {
		return Default().LookupEngine(kind, version, architecture, target)
	}
	return builtinLookup(kind, version, architecture, target)
}
