from __future__ import annotations

import json
import subprocess

import pytest

from tests.e2e.core.grpc_client import PUBLIC_API, GRPCClient
from tests.e2e.core.helpers import assert_grpc_rejected
from tests.e2e.core.runner import poll_until, run, run_unchecked


def test_enable_service_via_helm_upgrade(
    grpc: GRPCClient, namespace: str, osac_chart_path: str, helm_release_name: str
) -> None:
    chart_path = osac_chart_path
    release_name = helm_release_name

    with pytest.raises(subprocess.CalledProcessError) as exc_info:
        grpc.call(service=f"{PUBLIC_API}.BareMetalInstances/List")
    assert_grpc_rejected(exc_info, "Unavailable")

    run(
        "helm",
        "upgrade",
        release_name,
        chart_path,
        "-n",
        namespace,
        "--reuse-values",
        "--set",
        "services.bmaas.enabled=true",
        timeout=120,
    )

    _wait_for_rollout(namespace=namespace, deployment="fulfillment-service")
    _wait_for_rollout(namespace=namespace, deployment="osac-operator")

    poll_until(
        fn=lambda: grpc.call_unchecked(service=f"{PUBLIC_API}.BareMetalInstances/List"),
        until=lambda result: result[1] == 0,
        retries=30,
        delay=5,
        description="BareMetalInstances.List becomes available after upgrade",
        retry_on_error=True,
    )

    _verify_operator_controller_enabled(namespace=namespace)
    _verify_bmf_pods_running(namespace=namespace)

    for svc in (
        f"{PUBLIC_API}.Clusters/List",
        f"{PUBLIC_API}.ComputeInstances/List",
        f"{PUBLIC_API}.BareMetalInstances/List",
    ):
        output, rc = grpc.call_unchecked(service=svc)
        assert rc == 0, f"{svc} should succeed after enabling all services, got rc={rc}: {output}"

    response = grpc.call(service=f"{PUBLIC_API}.Capabilities/Get")
    enabled = response.get("enabledServices", response.get("enabled_services", []))
    for svc_name in ("caas", "vmaas", "bmaas", "maas"):
        assert svc_name in enabled, f"Capabilities should include {svc_name} after upgrade, got: {enabled}"


def _wait_for_rollout(*, namespace: str, deployment: str) -> None:
    run("kubectl", "rollout", "status", f"deployment/{deployment}", "-n", namespace, "--timeout=120s", timeout=150)


def _verify_operator_controller_enabled(*, namespace: str) -> None:
    pods_json = run(
        "kubectl",
        "--as",
        "system:admin",
        "get",
        "pods",
        "-n",
        namespace,
        "-l",
        "app.kubernetes.io/name=osac-operator",
        "-o",
        "json",
    )
    pods = json.loads(pods_json)
    assert pods.get("items"), "osac-operator pod(s) should exist after upgrade"
    for pod in pods["items"]:
        for container in pod.get("spec", {}).get("containers", []):
            if "manager" in container.get("name", ""):
                env_map = {e["name"]: e.get("value", "") for e in container.get("env", [])}
                assert env_map.get("OSAC_ENABLE_BAREMETAL_INSTANCE_CONTROLLER") == "true", (
                    f"Expected OSAC_ENABLE_BAREMETAL_INSTANCE_CONTROLLER=true after upgrade, got: {env_map}"
                )


def _verify_bmf_pods_running(*, namespace: str) -> None:
    poll_until(
        fn=lambda: run_unchecked(
            "kubectl",
            "--as",
            "system:admin",
            "get",
            "pods",
            "-n",
            namespace,
            "-l",
            "app.kubernetes.io/name=bare-metal-fulfillment-operator",
            "--no-headers",
        ),
        until=lambda result: result[1] == 0 and any(line.strip() for line in result[0].strip().splitlines()),
        retries=30,
        delay=5,
        description="BMF operator pods running after upgrade",
    )
