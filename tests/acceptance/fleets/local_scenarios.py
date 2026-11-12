#!/usr/bin/env python3
"""Exercise local fixture processes. Run with Python; no cloud target is used."""

import json
import multiprocessing
import secrets
import sqlite3
import sys
import tempfile
from contextlib import contextmanager
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import ProxyHandler, Request, build_opener

from fixture import Store, server
from run import LIVE_CHECKS


@contextmanager
def state_directory(parent=None):
    """Remove only the temporary directory created by this invocation."""
    with tempfile.TemporaryDirectory(prefix="fleet-fixture-", dir=parent) as name:
        yield Path(name)


def require(condition):
    if not condition:
        raise RuntimeError("fixture observation did not match")


def serve(directory, instance, role, token, channel):
    with server(Store(directory, instance), role, token, port=0) as http:
        channel.send(http.server_port)
        channel.close()
        http.serve_forever()


def stop(process):
    if process.is_alive():
        process.terminate()
    process.join(timeout=3)
    if process.is_alive():
        process.kill()
        process.join(timeout=3)
    require(not process.is_alive())
    process.close()


@contextmanager
def child(target, *args):
    context = multiprocessing.get_context("spawn")
    parent, remote = context.Pipe()
    process = context.Process(target=target, args=(*args, remote))
    try:
        process.start()
        remote.close()
        yield process, parent
    finally:
        parent.close()
        remote.close()
        if process.pid is not None:
            stop(process)


def receive(channel):
    require(channel.poll(10))
    return channel.recv()


@contextmanager
def api(directory, instance, token):
    with child(serve, directory, instance, "api", token) as (_, channel):
        yield receive(channel)


def request(port, token, path, value=None):
    body = None if value is None else json.dumps({"value": value}).encode()
    req = Request(
        "http://127.0.0.1:" + str(port) + path,
        data=body,
        headers={"Authorization": "Bearer " + token},
        method="GET" if body is None else "PUT",
    )
    # Local fixture requests must not follow a workstation proxy configuration.
    opener = build_opener(ProxyHandler({}))
    try:
        with opener.open(req, timeout=3) as response:
            return response.status, json.load(response)
    except HTTPError as error:
        with error:
            return error.code, json.load(error)


def check_isolation():
    with state_directory() as directory:
        stores = [Store(directory, name) for name in ("instance-a", "instance-b")]
        tokens = [secrets.token_urlsafe(32), secrets.token_urlsafe(32)]
        with api(directory, "instance-a", tokens[0]) as a, api(
            directory, "instance-b", tokens[1]
        ) as b:
            ports = [a, b]
            for index, store in enumerate(stores):
                require(request(ports[index], tokens[index], "/health")[0] == 503)
                store.bootstrap()
                require(request(ports[index], tokens[index], "/health")[0] == 200)
                for kind in ("data", "bucket"):
                    path = "/" + kind + "/same-key"
                    require(
                        request(ports[index], tokens[index], path, store.instance)[0]
                        == 200
                    )
            for index, store in enumerate(stores):
                for kind in ("data", "bucket"):
                    path = "/" + kind + "/same-key"
                    for value in (None, "neighbor-write"):
                        require(
                            request(ports[index], tokens[1 - index], path, value)[0]
                            == 403
                        )
                    require(
                        request(ports[index], tokens[index], path)
                        == (200, {"value": store.instance})
                    )


def interrupted_worker(directory, channel):
    original_connection = Store.connection

    @contextmanager
    def observed_connection(store):
        with original_connection(store) as connection:
            writes = 0

            def checkpoint(statement):
                nonlocal writes
                if statement.startswith("UPDATE jobs SET result="):
                    writes += 1
                    if writes == 2:
                        # The first update has executed. Observe it on this
                        # connection before allowing the second update to run.
                        count = connection.execute(
                            "SELECT COUNT(*) FROM jobs WHERE result IS NOT NULL"
                        ).fetchone()[0]
                        channel.send(
                            {
                                "uncommitted_results": count,
                                "transaction_open": connection.in_transaction,
                            }
                        )
                        channel.recv()

            connection.set_trace_callback(checkpoint)
            yield connection

    Store.connection = observed_connection
    Store(directory, "instance-a").work()


def complete_worker(directory, channel):
    store = Store(directory, "instance-a")
    channel.send({"processed": store.work(), **store.metrics()})


def check_crash_recovery():
    with state_directory() as directory:
        store = Store(directory, "instance-a")
        store.bootstrap()
        for name in ("job-a", "job-b"):
            store.enqueue(name, "synthetic")
        with child(interrupted_worker, directory) as (process, channel):
            require(
                receive(channel)
                == {"uncommitted_results": 1, "transaction_open": True}
            )
            process.kill()
            process.join(timeout=3)
            require(process.exitcode is not None and process.exitcode != 0)
        require(store.metrics() == {"queue_depth": 2, "completed_jobs": 0})
        with child(complete_worker, directory) as (process, channel):
            require(
                receive(channel)
                == {"processed": 2, "queue_depth": 0, "completed_jobs": 2}
            )
            process.join(timeout=3)
            require(process.exitcode == 0)
        for name in ("job-a", "job-b"):
            store.enqueue(name, "synthetic")
        require(store.work() == 0)
        require(store.metrics() == {"queue_depth": 0, "completed_jobs": 2})


def check_snapshot_restore():
    with state_directory() as directory:
        source = Store(directory / "source", "instance-a")
        restored = Store(directory / "restore", "instance-b")
        source.bootstrap()
        for kind in ("data", "bucket"):
            source.put(kind, "sample", kind + "-snapshot")
        # Both adapters are SQLite tables in this fixture. This is not a managed
        # database backup or a remote bucket restore.
        with source.connection() as database:
            snapshot = sqlite3.connect(directory / "snapshot.sqlite3")
            try:
                database.backup(snapshot)
                with restored.connection() as destination:
                    snapshot.backup(destination)
            finally:
                snapshot.close()
        require(restored.ready())
        for kind in ("data", "bucket"):
            source.put(kind, "sample", kind + "-after-snapshot")
        token = secrets.token_urlsafe(32)
        with api(directory / "restore", "instance-b", token) as port:
            require(request(port, token, "/health")[0] == 200)
            for kind in ("data", "bucket"):
                path = "/" + kind + "/sample"
                require(
                    request(port, token, path)
                    == (200, {"value": kind + "-snapshot"})
                )
                require(request(port, token, path, "restored-write")[0] == 200)
                require(source.get(kind, "sample") == kind + "-after-snapshot")


def run_checks(checks=None):
    if checks is None:
        checks = {
            "fixture_two_process_isolation": check_isolation,
            "fixture_worker_transaction_crash": check_crash_recovery,
            "fixture_sqlite_snapshot_restore": check_snapshot_restore,
        }
    results = []
    for name, check in checks.items():
        try:
            check()
            status = "passed"
        except Exception:
            # Do not copy exceptions or HTTP payloads into qualification reports.
            status = "failed"
        results.append({"name": name, "status": status})
    passed = results and all(row["status"] == "passed" for row in results)
    return {
        "status": "passed" if passed else "failed",
        "evidence_kind": "synthetic_fixture",
        "live_qualified": False,
        "checks": results,
        "live_checks": [{"name": name, "status": "not_run"} for name in LIVE_CHECKS],
    }


def main():
    report = run_checks()
    print(json.dumps(report, indent=2))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
