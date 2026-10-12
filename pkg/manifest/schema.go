package manifest

import _ "embed"

// Schema is the JSON Schema for version 1, for editors: point a YAML
// language server at it and loomplane.yaml gets completion and checks as
// you type. Parse is still the authority; the schema can't know which
// providers or repository paths exist.
//
//go:embed schema.json
var Schema []byte

// SchemaID is the schema's $id.
const SchemaID = "https://loomplane.dev/schemas/loomplane.v1.json"
