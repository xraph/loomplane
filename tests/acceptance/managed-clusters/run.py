#!/usr/bin/env python3
"""Exercise a reviewed, bounded disposable cluster qualification run."""

import argparse
import json
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
            "general_purchase_rollout": False,
        }
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

    def run(self):
        if self.state["completed"] == list(STEPS):
            return self.state
        if "operation_id" not in self.state:
            review = self.request("POST", "/clusters/preview", self.config["input"])
            if review["estimated_node_hourly_micro_usd"] > self.config["max_node_hourly_micro_usd"]:
                raise ValueError("Reviewed node cost exceeds the disposable bound")
            self.state.update(operation_id=review["id"], digest=review["digest"])
            self.save()
        operation = "/cluster-operations/" + self.state["operation_id"]
        self.request("POST", operation + "/apply", {"review": self.state["digest"]})
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
