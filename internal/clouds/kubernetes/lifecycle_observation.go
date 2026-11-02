package kubernetes

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
)

// lifecycleSpecMatches allows API defaults and controller identity labels only.
// A matching name or annotation cannot stand in for the reviewed executable.
func lifecycleSpecMatches(actual, wanted *batchv1.Job) bool {
	copyJob := actual.DeepCopy()
	spec := &copyJob.Spec
	if spec.Selector != nil {
		if len(spec.Selector.MatchExpressions) != 0 {
			return false
		}
		for key, value := range spec.Selector.MatchLabels {
			if (key != "controller-uid" && key != "batch.kubernetes.io/controller-uid") || value != string(actual.UID) {
				return false
			}
		}
		spec.Selector = nil
	}
	for key, value := range spec.Template.Labels {
		switch key {
		case "controller-uid", "batch.kubernetes.io/controller-uid":
			if value != string(actual.UID) {
				return false
			}
			delete(spec.Template.Labels, key)
		case "job-name", "batch.kubernetes.io/job-name":
			if value != actual.Name {
				return false
			}
			delete(spec.Template.Labels, key)
		}
	}
	if spec.ManualSelector != nil && !*spec.ManualSelector {
		spec.ManualSelector = nil
	}
	if spec.Parallelism != nil && *spec.Parallelism == 1 {
		spec.Parallelism = nil
	}
	if spec.Completions != nil && *spec.Completions == 1 {
		spec.Completions = nil
	}
	if spec.Suspend != nil && !*spec.Suspend {
		spec.Suspend = nil
	}
	if spec.CompletionMode != nil && *spec.CompletionMode == batchv1.NonIndexedCompletion {
		spec.CompletionMode = nil
	}
	if spec.PodReplacementPolicy != nil && string(*spec.PodReplacementPolicy) == "TerminatingOrFailed" {
		spec.PodReplacementPolicy = nil
	}
	pod := &spec.Template.Spec
	if pod.ServiceAccountName == "default" {
		pod.ServiceAccountName = ""
	}
	if pod.DeprecatedServiceAccount == "default" {
		pod.DeprecatedServiceAccount = ""
	}
	if pod.DNSPolicy == corev1.DNSClusterFirst {
		pod.DNSPolicy = ""
	}
	if pod.SchedulerName == corev1.DefaultSchedulerName {
		pod.SchedulerName = ""
	}
	if pod.TerminationGracePeriodSeconds != nil && *pod.TerminationGracePeriodSeconds == 30 {
		pod.TerminationGracePeriodSeconds = nil
	}
	if pod.EnableServiceLinks != nil && *pod.EnableServiceLinks {
		pod.EnableServiceLinks = nil
	}
	if pod.SecurityContext != nil && apiequality.Semantic.DeepEqual(pod.SecurityContext, &corev1.PodSecurityContext{}) {
		pod.SecurityContext = nil
	}
	for i := range pod.Containers {
		container := &pod.Containers[i]
		if container.TerminationMessagePath == corev1.TerminationMessagePathDefault {
			container.TerminationMessagePath = ""
		}
		if container.TerminationMessagePolicy == corev1.TerminationMessageReadFile {
			container.TerminationMessagePolicy = ""
		}
	}
	return apiequality.Semantic.DeepEqual(spec, &wanted.Spec)
}
