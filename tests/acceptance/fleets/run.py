#!/usr/bin/env python3
"""Validate a bounded synthetic Fleet qualification run without provisioning."""

import argparse
import json
import os
import re
import sys
from pathlib import Path
from urllib.parse import urlsplit

REQUIRED_CAPABILITIES = {
    "namespace_isolation",
    "pod_groups",
    "lifecycle_jobs",
    "database_credentials",
    "bucket_credentials",
}
LIVE_CHECKS = (
    "two_instance_access_denial",
    "concurrent_create_and_idempotency",
    "process_crash_recovery",
    "hpa_growth_and_node_expansion",
    "safe_downscale",
    "budget_and_region_denial",
    "suspend_and_resume",
    "database_and_bucket_restore",
    "wave_pause",
    "incompatible_rollback",
    "webhook_loss_and_replay",
    "old_writer_fencing",
)


class InputError(ValueError):
    """A rejected qualification input, safe to summarize without its values."""


def fields(value, expected, name):
    if not isinstance(value, dict) or set(value) != set(expected):
        raise InputError(name + " has missing or unsupported fields")


def positive(value, name):
    if type(value) is not int or value <= 0:
        raise InputError(name + " must be a positive integer")


def identity(value, name):
    if not isinstance(value, str) or not re.fullmatch(
        r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}", value
    ):
        raise InputError(name + " must be a bounded opaque identifier")


def validate_config(config):
    fields(
        config,
        (
            "version",
            "run_id",
            "target",
            "budget",
            "bounds",
            "declared_capabilities",
            "cleanup",
        ),
        "configuration",
    )
    if type(config["version"]) is not int or config["version"] != 1:
        raise InputError("unsupported configuration version")
    identity(config["run_id"], "run_id")
    target = config["target"]
    fields(
        target,
        ("api_url", "org_id", "fleet_id", "stage_id", "cloud_id", "region", "runtime"),
        "target",
    )
    for key in ("org_id", "fleet_id", "stage_id", "cloud_id", "region"):
        identity(target[key], key)
    if target["runtime"] != "kubernetes":
        raise InputError("unsupported runtime: qualification requires Kubernetes")
    if not isinstance(target["api_url"], str):
        raise InputError("api_url must be a URL")
    url = urlsplit(target["api_url"])
    if (
        not url.hostname
        or url.username
        or url.password
        or url.query
        or url.fragment
        or url.path not in ("", "/")
        or url.scheme not in ("http", "https")
        or (
            url.scheme == "http"
            and url.hostname not in ("127.0.0.1", "::1", "localhost")
        )
    ):
        raise InputError(
            "api_url requires HTTPS or loopback HTTP and no credentials, query or path"
        )
    try:
        port = url.port
    except ValueError as error:
        raise InputError("api_url port is invalid") from error
    if port == 0:
        raise InputError("api_url port is invalid")
    fields(config["budget"], ("currency", "max_minor_units"), "budget")
    if not isinstance(config["budget"]["currency"], str) or not re.fullmatch(
        r"[A-Z]{3}", config["budget"]["currency"]
    ):
        raise InputError("budget currency must be a three-letter currency code")
    positive(config["budget"]["max_minor_units"], "budget max_minor_units")
    bounds = config["bounds"]
    fields(
        bounds,
        (
            "max_instances",
            "max_replicas_per_service",
            "max_cpu_millis",
            "max_memory_mib",
            "max_storage_gib",
            "max_buckets",
            "max_duration_seconds",
        ),
        "bounds",
    )
    for key, value in bounds.items():
        positive(value, key)
    capabilities = config["declared_capabilities"]
    if (
        not isinstance(capabilities, list)
        or not all(isinstance(v, str) for v in capabilities)
        or len(set(capabilities)) != len(capabilities)
        or set(capabilities) != REQUIRED_CAPABILITIES
    ):
        raise InputError("missing or unsupported declared capability")
    cleanup = config["cleanup"]
    fields(cleanup, ("policy", "resource_ids"), "cleanup")
    if cleanup["policy"] != "retain_data":
        raise InputError("cleanup must retain attached data")
    ids = cleanup["resource_ids"]
    if not isinstance(ids, list):
        raise InputError("cleanup resource_ids must be an explicit list")
    for value in ids:
        identity(value, "cleanup resource ID")
    if len(ids) != len(set(ids)):
        raise InputError("cleanup resource IDs must be unique")


def workload(config):
    """Describe fixture roles, not a Fleet release or an API request body."""
    org = config["target"]["org_id"]
    instances = []
    for name in ("instance-a", "instance-b"):
        instances.append(
            {
                "id": name,
                "org_id": org,
                "components": [
                    {
                        "role": "api",
                        "cpu_millis": 500,
                        "memory_mib": 128,
                        "replicas": 1,
                    },
                    {
                        "role": "sidecar",
                        "cpu_millis": 100,
                        "memory_mib": 32,
                        "replicas": 1,
                    },
                    {
                        "role": "worker",
                        "cpu_millis": 200,
                        "memory_mib": 64,
                        "replicas": 1,
                    },
                    {
                        "role": "bootstrap",
                        "cpu_millis": 100,
                        "memory_mib": 64,
                        "replicas": 1,
                    },
                ],
                "resources": [
                    {
                        "kind": "database",
                        "storage_gib": 1,
                        "ownership": "attached",
                        "adapter": "synthetic",
                    },
                    {"kind": "bucket", "ownership": "attached", "adapter": "synthetic"},
                ],
            }
        )
    return {
        "control": {"org_id": org, "cpu_millis": 200, "memory_mib": 64},
        "instances": instances,
        "load": {"max_jobs": 32, "hash_iterations_per_job": 1000},
    }


def plan(config, fixture=None):
    validate_config(config)
    fixture = workload(config) if fixture is None else fixture
    fields(fixture, ("control", "instances", "load"), "fixture")
    control = fixture["control"]
    fields(control, ("org_id", "cpu_millis", "memory_mib"), "control")
    if control["org_id"] != config["target"]["org_id"]:
        raise InputError("mixed organization control service")
    for key in ("cpu_millis", "memory_mib"):
        positive(control[key], key)
    demand = {
        "instances": 0,
        "cpu_millis": control["cpu_millis"],
        "memory_mib": control["memory_mib"],
        "storage_gib": 0,
        "buckets": 0,
    }
    instances = fixture["instances"]
    if not isinstance(instances, list) or len(instances) != 2:
        raise InputError("qualification requires exactly two independent instances")
    seen = set()
    for instance in instances:
        fields(instance, ("id", "org_id", "components", "resources"), "instance")
        identity(instance["id"], "instance ID")
        if instance["id"] in seen or instance["org_id"] != config["target"]["org_id"]:
            raise InputError("duplicate instance or mixed organization")
        seen.add(instance["id"])
        demand["instances"] += 1
        components = instance["components"]
        if not isinstance(components, list):
            raise InputError("components must be a list")
        roles = []
        for component in components:
            fields(
                component, ("role", "cpu_millis", "memory_mib", "replicas"), "component"
            )
            roles.append(component["role"])
            for key in ("cpu_millis", "memory_mib", "replicas"):
                positive(component[key], key)
            if component["replicas"] > config["bounds"]["max_replicas_per_service"]:
                raise InputError("replica bound exceeded")
            for key in ("cpu_millis", "memory_mib"):
                demand[key] += component[key] * component["replicas"]
        if sorted(roles) != ["api", "bootstrap", "sidecar", "worker"]:
            raise InputError(
                "fixture requires API, sidecar, worker and bootstrap roles"
            )
        resources = instance["resources"]
        if not isinstance(resources, list) or len(resources) != 2:
            raise InputError("fixture requires database and bucket adapters")
        kinds = []
        for resource in resources:
            if not isinstance(resource, dict):
                raise InputError("invalid resource")
            kind = resource.get("kind")
            kinds.append(kind)
            expected = (
                ("kind", "ownership", "adapter", "storage_gib")
                if kind == "database"
                else ("kind", "ownership", "adapter")
            )
            fields(resource, expected, "resource")
            if (
                resource["ownership"] != "attached"
                or resource["adapter"] != "synthetic"
            ):
                raise InputError(
                    "qualification fixture requires retained synthetic adapters"
                )
            if kind == "database":
                positive(resource["storage_gib"], "storage_gib")
                demand["storage_gib"] += resource["storage_gib"]
            elif kind == "bucket":
                demand["buckets"] += 1
            else:
                raise InputError("unsupported resource kind")
        if sorted(kinds) != ["bucket", "database"]:
            raise InputError(
                "fixture requires one database and one bucket per instance"
            )
    fields(fixture["load"], ("max_jobs", "hash_iterations_per_job"), "load")
    for key, limit in (("max_jobs", 32), ("hash_iterations_per_job", 1000)):
        positive(fixture["load"][key], key)
        if fixture["load"][key] > limit:
            raise InputError("synthetic load bound exceeded")
    for key, value in demand.items():
        if value > config["bounds"]["max_" + key]:
            raise InputError(key + " bound exceeded")
    return {
        "status": "dry_run_validated",
        "live_qualified": False,
        "run_id": config["run_id"],
        "target": config["target"],
        "budget": {
            **config["budget"],
            "estimated_cost": "unknown",
            "observed_cost": "unknown",
        },
        "bounds": config["bounds"],
        "demand": demand,
        "instances": instances,
        "capability_evidence": "operator_declared_only",
        "live_checks": [{"name": name, "status": "not_run"} for name in LIVE_CHECKS],
        "metadata_ladder": {
            "sizes": [10, 100, 1000],
            "status": "not_run",
            "reason": "requires_local_api_and_test_slot",
        },
    }


def cleanup_plan(config, receipt):
    """Inspect recorded ownership. This function never deletes resources."""
    validate_config(config)
    fields(receipt, ("version", "run_id", "target", "resources"), "receipt")
    if (
        type(receipt["version"]) is not int
        or receipt["version"] != 1
        or receipt["run_id"] != config["run_id"]
        or receipt["target"] != config["target"]
    ):
        raise InputError("cleanup receipt scope changed")
    if not isinstance(receipt["resources"], list):
        raise InputError("cleanup resources must be a list")
    resources = {}
    retained = []
    for resource in receipt["resources"]:
        fields(
            resource,
            ("id", "kind", "org_id", "run_id", "created_by_run"),
            "receipt resource",
        )
        identity(resource["id"], "resource ID")
        if (
            resource["id"] in resources
            or resource["org_id"] != config["target"]["org_id"]
            or resource["run_id"] != config["run_id"]
            or resource["created_by_run"] is not True
        ):
            raise InputError("cleanup resource ownership mismatch")
        if resource["kind"] not in (
            "database",
            "bucket",
            "volume",
            "workload",
            "namespace",
        ):
            raise InputError("unsupported cleanup resource kind")
        resources[resource["id"]] = resource
        if resource["kind"] in ("database", "bucket", "volume"):
            retained.append(resource["id"])
    if not set(config["cleanup"]["resource_ids"]).issubset(resources):
        raise InputError("cleanup requested an unrecorded resource")
    return {
        "status": "cleanup_dry_run",
        "deleted_resource_ids": [],
        "retained_data_ids": retained,
        "residual_resource_ids": list(resources),
        "blocker": "live_cleanup_adapter_unavailable",
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--config",
        type=Path,
        default=os.environ.get("LOOMPLANE_FLEET_ACCEPTANCE_CONFIG"),
    )
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--dry-run", action="store_true")
    mode.add_argument("--live", action="store_true")
    parser.add_argument("--cleanup-receipt", type=Path)
    args = parser.parse_args(argv)
    if args.config is None:
        parser.error("--config or LOOMPLANE_FLEET_ACCEPTANCE_CONFIG is required")
    try:
        config = json.loads(Path(args.config).read_text())
        report = plan(config)
        if args.cleanup_receipt:
            report["cleanup"] = cleanup_plan(
                config, json.loads(args.cleanup_receipt.read_text())
            )
        if args.live:
            report["status"] = "blocked"
            report["blocker"] = "live_adapter_unavailable"
            print(json.dumps(report, indent=2))
            return 2
        print(json.dumps(report, indent=2))
        return 0
    except (OSError, ValueError, TypeError) as error:
        # Parse failures may contain caller-controlled values. Do not echo them.
        message = (
            str(error)
            if isinstance(error, InputError)
            else "configuration or receipt could not be read"
        )
        print(json.dumps({"status": "rejected", "reason": message}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
