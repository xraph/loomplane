package graph

// SourceRef says where one part of a composed graph was read from.
type SourceRef struct {
	Alias      string `json:"alias"`
	Repository string `json:"repository"`
	Root       string `json:"root,omitempty"`
	CommitSHA  string `json:"commit_sha"`
	AnalysisID string `json:"analysis_id"`
}
