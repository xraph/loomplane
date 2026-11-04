package agentknowledge

// NewSizeMismatch is the error Migrate returns for a table of size table.
func NewSizeMismatch(table int, err error) error { return &SizeMismatchError{Table: table, err: err} }
