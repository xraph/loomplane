package telemetry

// UsageRead records one read of a cloud's billing or monitoring: kind is
// spend or traffic, source the cloud or provider, outcome reporting,
// unavailable or failed.
func (r *Recorder) UsageRead(kind, source, outcome string) {
	r.count("loomplane_usage_reads_total", map[string]string{"kind": kind, "source": source, "outcome": outcome})
}
