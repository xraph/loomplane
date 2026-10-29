#!/usr/bin/env python3
"""Exercise a reviewed, bounded disposable cluster qualification run."""

import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import re
import subprocess
import time
import urllib.error
import urllib.request

STEPS = (
    "restart_worker", "deploy_worker", "observe_ready", "revoke_runtime",
    "observe_denied", "restore_runtime", "observe_restored", "cleanup_inventory",
)


def validate(config):
    required = ("api_url", "cloud_id", "account_id", "project_id", "region", "input", "commands")
    if any(not config.get(key) for key in required):
        raise ValueError("Supply explicit disposable Cloud, account, project, region, input and commands")
    if config.get("disposable") is not True or config.get("qualification_only") is not True:
        raise ValueError("The run must be disposable and qualification-only")
    if not 1 <= config.get("max_nodes", 0) <= 4 or not 1 <= config.get("timeout_seconds", 0) <= 3600:
        raise ValueError("Bound the run to 1-4 nodes and at most one hour")
    if not 1 <= config.get("max_node_hourly_micro_usd", 0) <= 10_000_000:
        raise ValueError("Supply a positive node-hour ceiling of at most $10")
    source = config["input"]
    if source.get("kind") != "create" or source.get("provider") != "digitalocean":
        raise ValueError("This qualification runner supports new DOKS worker clusters")
    if source.get("account_id") != config["account_id"] or source.get("spec", {}).get("region") != config["region"]:
        raise ValueError("Input does not match the explicitly bounded account and region")
    attachment = source.get("attachment", {})
    if attachment.get("project_id") != config["project_id"]:
        raise ValueError("Runtime attachment must match the disposable project")
    if any(attachment.get("prerequisites", {}).values()):
        raise ValueError("Qualification uses private workers without selected addons")
    pools = source.get("spec", {}).get("node_pools", [])
    if not pools or any(p.get("autoscale") or p.get("min") or p.get("max") or p.get("count", 0) < 1 for p in pools):
        raise ValueError("Only fixed positive node pools are supported")
    if sum(p["count"] for p in pools) > config["max_nodes"]:
        raise ValueError("Node count exceeds the disposable bound")
    if not re.fullmatch(r".+@sha256:[a-f0-9]{64}", source.get("worker_image", "")):
        raise ValueError("Pin the worker image by digest")
    for step in STEPS:
        command = config["commands"].get(step)
        if not isinstance(command, list) or not command or not all(isinstance(v, str) and v for v in command):
            raise ValueError("Supply an argv command for " + step)
    return config


def fingerprint(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False).encode()).hexdigest()

def scope_fingerprint(config):
    return fingerprint({key: config[key] for key in ("api_url", "cloud_id", "account_id", "project_id",
                                                    "region", "input", "max_nodes", "max_node_hourly_micro_usd")})

def matches_input(expected, actual):
    if isinstance(expected, dict):
        return isinstance(actual, dict) and all(key in actual and matches_input(value, actual[key]) for key, value in expected.items())
    if isinstance(expected, list):
        return isinstance(actual, list) and len(expected) == len(actual) and all(matches_input(left, right) for left, right in zip(expected, actual))
    return expected == actual


class Runner:
    def __init__(self, config, state_path, token, request=None, command=None):
        self.config = validate(config)
        self.path = Path(state_path)
        self.token = token
        self.request_impl = request
        self.command_impl = command
        self.state = json.loads(self.path.read_text()) if self.path.exists() else {
            "cloud_id": config["cloud_id"], "account_id": config["account_id"],
            "project_id": config["project_id"], "completed": [], "live_qualified": False,
            "general_purchase_rollout": False, "scope_fingerprint": scope_fingerprint(config),
        }
        if self.state.get("scope_fingerprint") != scope_fingerprint(config):
            raise ValueError("Saved qualification configuration differs or lacks a scope fingerprint; do not accept this review")
        for key in ("cloud_id", "account_id", "project_id"):
            if self.state[key] != config[key]:
                raise ValueError("Saved qualification scope does not match")

    def save(self):
        temporary = self.path.with_suffix(".tmp")
        temporary.write_text(json.dumps(self.state, indent=2) + "\n")
        temporary.chmod(0o600)
        temporary.replace(self.path)

    def request(self, method, suffix, body=None):
        if self.request_impl:
            return self.request_impl(method, suffix, body)
        request = urllib.request.Request(
            self.config["api_url"].rstrip("/") + "/api/v1/clouds/" + self.config["cloud_id"] + suffix,
            data=json.dumps(body).encode() if body is not None else None,
            headers={"Authorization": "Bearer " + self.token, "Content-Type": "application/json"},
            method=method,
        )
        # Disable redirects so the bearer credential stays on the configured origin.
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *_args, **_kwargs):
                return None
        with urllib.request.build_opener(NoRedirect).open(request, timeout=60) as response:
            return json.load(response)

    def run_step(self, step):
        if step in self.state["completed"]:
            return
        if self.command_impl:
            evidence = self.command_impl(step, self.state)
        else:
            env = {**os.environ, "LOOMPLANE_CLUSTER_QUALIFICATION": json.dumps({
                **{k: self.state[k] for k in ("cloud_id", "account_id", "project_id", "operation_id", "resource_id")},
                "worker_image": self.config["input"]["worker_image"],
            })}
            result = subprocess.run(self.config["commands"][step], env=env, capture_output=True,
                                    text=True, timeout=self.config["timeout_seconds"], check=False)
            if result.returncode:
                raise RuntimeError(step + " failed; inspect the disposable environment")
            evidence = json.loads(result.stdout)
        for key in ("cloud_id", "account_id", "project_id", "operation_id", "resource_id"):
            if evidence.get(key) != self.state[key]:
                raise ValueError(step + " evidence has the wrong scope")
        if evidence.get("passed") is not True:
            raise ValueError(step + " did not establish the required outcome")
        if step == "cleanup_inventory" and evidence.get("remaining_resources") != []:
            raise ValueError("Disposable resources remain in the cleanup inventory")
        self.state["completed"].append(step)
        self.save()

    def validate_review(self, review):
        if review.get("id") != self.state["operation_id"] or review.get("digest") != self.state["digest"]:
            raise ValueError("Saved review identity changed")
        if review.get("cloud_id") != self.config["cloud_id"] or not matches_input(self.config["input"], review.get("input")):
            raise ValueError("Saved review input differs from the configured scope")
        source = review["input"]
        validate({**self.config, "input": source})
        estimate = review.get("estimated_node_hourly_micro_usd")
        unknown = review.get("unknown_costs")
        if (isinstance(estimate, bool) or not isinstance(estimate, (int, float)) or
                not math.isfinite(estimate) or estimate <= 0 or
                not isinstance(unknown, list) or
                any(not isinstance(cost, str) or cost.startswith("nodes:") for cost in unknown)):
            raise ValueError("Every selected node pool requires a known positive price before paid qualification")
        if estimate > self.config["max_node_hourly_micro_usd"]:
            raise ValueError("Reviewed node cost exceeds the disposable bound")
        reviewed = fingerprint({"input": source, "digest": review["digest"], "cost": estimate, "unknown_costs": unknown})
        if self.state.get("review_fingerprint", reviewed) != reviewed:
            raise ValueError("Authoritative saved review changed")
        self.state["review_fingerprint"] = reviewed
        self.save()

    def run(self):
        if self.state["completed"] == list(STEPS):
            return self.state
        if "operation_id" not in self.state:
            review = self.request("POST", "/clusters/preview", self.config["input"])
            self.state.update(operation_id=review["id"], digest=review["digest"])
            self.save()
        operation = "/cluster-operations/" + self.state["operation_id"]
        current = self.request("GET", operation)
        if current["status"] == "preview":
            self.validate_review(current)
            self.request("POST", operation + "/apply", {"review": self.state["digest"]})
        elif current["status"] not in ("accepted", "running", "waiting_provider", "succeeded"):
            raise RuntimeError("Saved operation requires recovery: " + current["status"])
        # Restart after acceptance, before observing the durable runtime outcome.
        self.state.setdefault("resource_id", "")
        self.run_step("restart_worker")
        deadline = time.monotonic() + self.config["timeout_seconds"]
        while True:
            current = self.request("GET", operation)
            if current["status"] == "succeeded":
                if not current.get("enrollment", {}).get("runtime_verified"):
                    raise ValueError("Provider readiness did not establish runtime enrollment")
                self.state["resource_id"] = current["resource_id"]
                self.save()
                break
            if current["status"] not in ("accepted", "running", "waiting_provider"):
                raise RuntimeError("Saved operation requires recovery: " + current["status"])
            if time.monotonic() >= deadline:
                raise TimeoutError("Resume the saved operation after checking provider progress")
            time.sleep(2)
        for step in STEPS[1:]:
            self.run_step(step)
        # Manual cleanup evidence does not replace Plan 02 lifecycle qualification.
        self.state["live_qualified"] = True
        self.save()
        return self.state


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True)
    parser.add_argument("--state", required=True)
    parser.add_argument("--execute-paid-disposable-qualification", action="store_true")
    args = parser.parse_args()
    config = validate(json.loads(Path(args.config).read_text()))
    if not args.execute_paid_disposable_qualification:
        parser.error("Execution requires --execute-paid-disposable-qualification after reviewing bounded inputs")
    token = os.environ.get("LOOMPLANE_QUALIFICATION_TOKEN", "")
    if not token:
        parser.error("Set LOOMPLANE_QUALIFICATION_TOKEN for the isolated qualification operator")
    Runner(config, args.state, token).run()
    print("Disposable qualification checks passed. General purchase rollout remains disabled.")


if __name__ == "__main__":
    main()
