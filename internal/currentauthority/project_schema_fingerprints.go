package currentauthority

// These exact prosrc hashes come from the final migration-owned definitions.
// Source-contract tests detect changed or additional migration references; the
// full migration fixture separately verifies the installed catalog. Changes,
// including whitespace, require explicit review and profile requalification.
var projectFunctionBodies = map[string]string{
	"loomplane_append_event":         "a2806614d7cb7c179957f7c263a68c1fe08bbf7b3ccbc573128b574148f2d534",
	"loomplane_domain_delete_guard":  "67f79a1622d98d496da2ed5c298f11b0b4f06ca03676f5504544ca30d5dc9bbe",
	"loomplane_fleet_project_mode":   "28c0fc5429d82323d4aaa318052c1987068fdd5e8e7ec5272cb7898512834b70",
	"loomplane_state_event_trigger":  "8ee0b0fd5457e1e7168cd12b4f9d7a17154a6b5274ce640bdb633e95952573fb",
	"loomplane_variable_source_gate": "c38efb5f6b507c818404cc09058f9c9f0d7fb5e083601b78674df619a67ca0e5",
}
