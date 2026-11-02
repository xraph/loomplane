package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
)

// A Cloud Run canary runs the candidate as a named revision of the
// service, beside the baseline revision, and splits the service's traffic
// between the two by percent.

// CanaryPermissions are what only a canary release on Cloud Run needs: it
// reads revisions to check one is ready, and deletes a candidate it
// reverts. They aren't in Permissions, so a service account without them
// still deploys.
var CanaryPermissions = []string{
	"run.revisions.delete",
	"run.revisions.get",
}

// RunRevision is the part of a Cloud Run revision a canary reads: whether
// it's up, and what it runs, which Cloud Run copied from the template
// that made it.
type RunRevision struct {
	Name string `json:"name"`
	// DeleteTime is set once the revision is being deleted.
	DeleteTime     string         `json:"deleteTime,omitempty"`
	Reconciling    bool           `json:"reconciling,omitempty"`
	Conditions     []RunCondition `json:"conditions,omitempty"`
	ServiceAccount string         `json:"serviceAccount,omitempty"`
	Scaling        *RunScaling    `json:"scaling,omitempty"`
	Containers     []RunContainer `json:"containers,omitempty"`
	Volumes        []RunVolume    `json:"volumes,omitempty"`
	VPCAccess      *RunVPCAccess  `json:"vpcAccess,omitempty"`
}

// Runs reports whether the revision runs what t would make: one container
// with the same image, arguments and dependencies, the same plain
// variables, the same secrets at the same versions, the same ports, size
// and scaling, the same Cloud SQL instances, in-memory volumes and VPC
// egress, and the same service account. A sidecar, such as a Cloud SQL
// PSC transport's, makes two containers, which never match. A release on
// the same commit has the same image, so the image alone can't tell a
// revision runs it.
// Anything Cloud Run filled in that t leaves to its defaults reads as a
// difference, which is the safe way to be wrong.
func (r *RunRevision) Runs(t RunTemplate) bool {
	if r == nil || len(r.Containers) != 1 || len(t.Containers) != 1 {
		return false
	}
	if !defaultAccount(t.ServiceAccount, r.ServiceAccount) {
		return false
	}
	return reflect.DeepEqual(shapeOf(t.Containers[0], t.Scaling, t.Volumes, t.VPCAccess),
		shapeOf(r.Containers[0], r.Scaling, r.Volumes, r.VPCAccess))
}

// defaultAccount reports whether a revision's service account is the
// template's, where a template that names none runs as the project's
// default compute account.
func defaultAccount(want, got string) bool {
	if want != "" {
		return want == got
	}
	return got == "" || strings.HasSuffix(got, "-compute@developer.gserviceaccount.com")
}

// releaseShape is what a revision runs, in a form two of them compare by.
type releaseShape struct {
	Image     string
	Args      []string
	DependsOn []string
	Env       map[string]string
	Secrets   map[string]string // name: secret:version
	Ports     []int
	Limits    map[string]string
	Mounts    map[string]string
	Min, Max  int
	CloudSQL  map[string]string // volume: instances, sorted
	EmptyDir  map[string]string // volume: medium and size limit
	VPC       string
}

func shapeOf(c RunContainer, scaling *RunScaling, volumes []RunVolume, vpc *RunVPCAccess) releaseShape {
	s := releaseShape{
		Image: c.Image, Args: c.Args, DependsOn: c.DependsOn,
		Env: map[string]string{}, Secrets: map[string]string{}, Mounts: map[string]string{}, Limits: map[string]string{},
		CloudSQL: map[string]string{}, EmptyDir: map[string]string{},
	}
	for _, e := range c.Env {
		if e.ValueSource != nil {
			s.Secrets[e.Name] = RevisionName(e.ValueSource.SecretKeyRef.Secret) + ":" + e.ValueSource.SecretKeyRef.Version
		} else {
			s.Env[e.Name] = e.Value
		}
	}
	for _, p := range c.Ports {
		s.Ports = append(s.Ports, p.ContainerPort)
	}
	if c.Resources != nil {
		maps.Copy(s.Limits, c.Resources.Limits)
	}
	for _, m := range c.Mounts {
		s.Mounts[m.Name] = m.MountPath
	}
	if scaling != nil {
		s.Min, s.Max = scaling.Min, scaling.Max
	}
	for _, v := range volumes {
		if v.CloudSQLInstance != nil {
			s.CloudSQL[v.Name] = strings.Join(slices.Sorted(slices.Values(v.CloudSQLInstance.Instances)), ",")
		}
		if v.EmptyDir != nil {
			s.EmptyDir[v.Name] = v.EmptyDir.Medium + ":" + v.EmptyDir.SizeLimit
		}
	}
	if vpc != nil {
		data, _ := json.Marshal(vpc) //nolint:errcheck // a plain struct always encodes
		s.VPC = string(data)
	}
	return s
}

// Up reports whether the revision can serve: it's there, it isn't being
// deleted, and Cloud Run found it ready. Cloud Run scales a revision to
// nothing when it takes no requests, so having no instances doesn't make
// one down: readiness is what Cloud Run checked when it started it.
func (r *RunRevision) Up() bool {
	if r == nil || r.DeleteTime != "" {
		return false
	}
	for _, c := range r.Conditions {
		if c.Type == "Ready" {
			return c.State == "CONDITION_SUCCEEDED"
		}
	}
	return false
}

// Why says why a revision isn't up.
func (r *RunRevision) Why() string {
	switch {
	case r == nil:
		return "it's gone"
	case r.DeleteTime != "":
		return "it's being deleted"
	}
	for _, c := range r.Conditions {
		if c.Type == "Ready" && c.State != "CONDITION_SUCCEEDED" {
			if c.Message != "" {
				return c.Message
			}
			return "it isn't ready (" + c.State + ")"
		}
	}
	return "it isn't ready"
}

func (s *Session) revisionPath(region, service, revision string) string {
	return s.servicePath(region, service) + "/revisions/" + url.PathEscape(revision)
}

// GetRevision reads one of a service's revisions; a missing one is
// IsNotFound.
func (s *Session) GetRevision(ctx context.Context, region, service, revision string) (*RunRevision, error) {
	var rev RunRevision
	if err := s.do(ctx, http.MethodGet, s.c.url("run.googleapis.com", s.revisionPath(region, service, revision)), nil, &rev); err != nil {
		return nil, err
	}
	return &rev, nil
}

// DeleteRevision deletes a revision and waits until it's gone. One that's
// gone already is not an error. Cloud Run refuses to delete a revision
// that takes traffic.
func (s *Session) DeleteRevision(ctx context.Context, region, service, revision string) error {
	var op operation
	err := s.do(ctx, http.MethodDelete, s.c.url("run.googleapis.com", s.revisionPath(region, service, revision)), nil, &op)
	if IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.waitOperation(ctx, op, "Deleting revision "+revision)
}

// ReplaceService replaces what an existing service runs and how its
// traffic is split with svc, and waits until Cloud Run has rolled it out.
// It never creates a service.
func (s *Session) ReplaceService(ctx context.Context, region, name string, svc RunService) (*RunService, error) {
	if _, err := s.GetService(ctx, region, name); err != nil {
		if IsNotFound(err) {
			return nil, fmt.Errorf("cloud run service %s is gone: %w", name, err)
		}
		return nil, err
	}
	var op operation
	if err := s.do(ctx, http.MethodPatch, s.c.url("run.googleapis.com", s.servicePath(region, name)), svc, &op); err != nil {
		return nil, err
	}
	if err := s.waitOperation(ctx, op, "Rolling out the service's template"); err != nil {
		return nil, err
	}
	return s.GetService(ctx, region, name)
}

// ApplyCandidateRevision starts a canary's candidate: svc's template, as
// the revision it names, with svc's traffic, which has to give the
// candidate none. It waits until the candidate is ready.
func (s *Session) ApplyCandidateRevision(ctx context.Context, region, name string, svc RunService) (*RunService, error) {
	candidate := svc.Template.Revision
	if candidate == "" {
		return nil, errors.New("a candidate revision needs a name")
	}
	if err := checkTraffic(svc.Traffic); err != nil {
		return nil, err
	}
	for _, t := range svc.Traffic {
		if t.Type == TrafficLatest || t.Revision == candidate {
			return nil, fmt.Errorf("candidate revision %s would take traffic as it starts", candidate)
		}
	}
	out, err := s.ReplaceService(ctx, region, name, svc)
	if err != nil {
		return nil, err
	}
	rev, err := s.GetRevision(ctx, region, name, candidate)
	if err != nil && !IsNotFound(err) {
		return nil, err
	}
	if !rev.Up() {
		return nil, fmt.Errorf("candidate revision %s didn't start: %s", candidate, rev.Why())
	}
	return out, nil
}

// SetTraffic splits a service's traffic as traffic says, and keeps
// everything else about the service as Cloud Run has it, the template
// above all, so no new revision is made. It waits until the split is in
// place.
func (s *Session) SetTraffic(ctx context.Context, region, name string, traffic []RunTraffic) error {
	if err := checkTraffic(traffic); err != nil {
		return err
	}
	u := s.c.url("run.googleapis.com", s.servicePath(region, name))
	// Read and sent back whole, so fields Loomplane doesn't model, and the
	// etag that guards against a change in between, go back as they came.
	var svc map[string]json.RawMessage
	if err := s.do(ctx, http.MethodGet, u, nil, &svc); err != nil {
		return err
	}
	data, err := json.Marshal(traffic)
	if err != nil {
		return err
	}
	svc["traffic"] = data
	var op operation
	if patchErr := s.do(ctx, http.MethodPatch, u, svc, &op); patchErr != nil {
		return patchErr
	}
	opErr := s.waitOperation(ctx, op, "Moving traffic")
	if opErr == nil {
		return nil
	}
	// The operation also reports a latest revision that failed to start,
	// such as a candidate being cleaned up, though the split it was asked
	// for is in place. What counts is the split.
	now, err := s.GetService(ctx, region, name)
	if err == nil && trafficIs(now, traffic) {
		return nil
	}
	return opErr
}

// trafficIs reports whether a service's traffic is split as traffic says.
func trafficIs(svc *RunService, traffic []RunTraffic) bool {
	want, got := map[string]int{}, map[string]int{}
	for _, t := range traffic {
		want[t.Type+"/"+t.Revision] += t.Percent
	}
	for _, t := range svc.TrafficStatuses {
		key := t.Type + "/"
		if t.Type == TrafficRevision {
			key += RevisionName(t.Revision)
		}
		got[key] += t.Percent
	}
	for k, v := range got {
		if v == 0 {
			delete(got, k)
		}
	}
	for k, v := range want {
		if v == 0 {
			delete(want, k)
		}
	}
	return maps.Equal(want, got)
}

// checkTraffic refuses a split that isn't all of the traffic, once each.
func checkTraffic(traffic []RunTraffic) error {
	if len(traffic) == 0 {
		return errors.New("a traffic split needs at least one target")
	}
	total, seen := 0, map[string]bool{}
	for _, t := range traffic {
		switch {
		case t.Type != TrafficLatest && t.Type != TrafficRevision:
			return fmt.Errorf("traffic target type %q isn't one Loomplane sets", t.Type)
		case t.Type == TrafficRevision && t.Revision == "":
			return errors.New("a traffic target by revision names no revision")
		case t.Percent < 0 || t.Percent > 100:
			return fmt.Errorf("a traffic target can't take %d%%", t.Percent)
		}
		key := t.Type + "/" + t.Revision
		if seen[key] {
			return fmt.Errorf("traffic names %s twice", t.Revision)
		}
		seen[key] = true
		total += t.Percent
	}
	if total != 100 {
		return fmt.Errorf("traffic adds up to %d%%, not 100%%", total)
	}
	return nil
}
