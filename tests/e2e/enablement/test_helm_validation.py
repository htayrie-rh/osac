from __future__ import annotations

from typing import Any

from tests.e2e.enablement.conftest import HelmTemplate


def _find_deployment(manifests: list[dict[str, Any]], name_contains: str) -> dict[str, Any] | None:
    for doc in manifests:
        if doc is None:
            continue
        if doc.get("kind") != "Deployment":
            continue
        if name_contains in doc.get("metadata", {}).get("name", ""):
            return doc
    return None


def _get_container_args(deployment: dict[str, Any], container_name_contains: str) -> list[str]:
    for container in deployment.get("spec", {}).get("template", {}).get("spec", {}).get("containers", []):
        if container_name_contains in container.get("name", ""):
            return container.get("args", [])
    return []


def _get_container_env(deployment: dict[str, Any], container_name_contains: str) -> dict[str, str]:
    for container in deployment.get("spec", {}).get("template", {}).get("spec", {}).get("containers", []):
        if container_name_contains in container.get("name", ""):
            return {e["name"]: e.get("value", "") for e in container.get("env", [])}
    return {}


def test_selective_services_render_correct_args(helm_template: HelmTemplate) -> None:
    manifests = helm_template.render(
        set_values=["global.services.bmaas.enabled=false", "global.services.maas.enabled=false"]
    )

    fs_deploy = _find_deployment(manifests, "fulfillment-service")
    assert fs_deploy is not None, "fulfillment-service deployment not found in rendered manifests"

    args = _get_container_args(fs_deploy, "fulfillment-service")
    assert "--enable-caas" in args
    assert "--enable-vmaas" in args
    assert "--enable-bmaas" not in args
    assert "--enable-maas" not in args

    op_deploy = _find_deployment(manifests, "osac-operator")
    assert op_deploy is not None, "osac-operator deployment not found in rendered manifests"

    env_vars = _get_container_env(op_deploy, "manager")
    assert env_vars.get("OSAC_ENABLE_BAREMETAL_INSTANCE_CONTROLLER") == "false"

    bmf_deploy = _find_deployment(manifests, "bare-metal-fulfillment")
    assert bmf_deploy is None, "BMF operator deployment should be absent when BMaaS is disabled"


def test_default_values_enable_all_services(helm_template: HelmTemplate) -> None:
    manifests = helm_template.render()

    fs_deploy = _find_deployment(manifests, "fulfillment-service")
    assert fs_deploy is not None, "fulfillment-service deployment not found in rendered manifests"

    args = _get_container_args(fs_deploy, "fulfillment-service")
    for flag in ("--enable-caas", "--enable-vmaas", "--enable-bmaas", "--enable-maas"):
        assert flag in args, f"Expected {flag} in default render, got args: {args}"

    op_deploy = _find_deployment(manifests, "osac-operator")
    assert op_deploy is not None, "osac-operator deployment not found in rendered manifests"

    env_vars = _get_container_env(op_deploy, "manager")
    for var in (
        "OSAC_ENABLE_CLUSTER_CONTROLLER",
        "OSAC_ENABLE_COMPUTE_INSTANCE_CONTROLLER",
        "OSAC_ENABLE_BAREMETAL_INSTANCE_CONTROLLER",
    ):
        assert env_vars.get(var) == "true", f"Expected {var}=true, got {env_vars.get(var)}"

    bmf_deploy = _find_deployment(manifests, "bare-metal-fulfillment")
    assert bmf_deploy is not None, "BMF operator deployment should be present when all services enabled"


def test_caas_without_compute_rejected(helm_template: HelmTemplate) -> None:
    output, rc = helm_template.render_expect_failure(
        set_values=[
            "global.services.caas.enabled=true",
            "global.services.vmaas.enabled=false",
            "global.services.bmaas.enabled=false",
        ]
    )
    assert rc != 0, f"helm template should fail for CaaS without VMaaS/BMaaS, got rc=0: {output}"
    assert "caas" in output.lower() or "vmaas" in output.lower() or "bmaas" in output.lower(), (
        f"Error should reference CaaS/VMaaS/BMaaS dependency, got: {output}"
    )


def test_maas_without_caas_rejected(helm_template: HelmTemplate) -> None:
    output, rc = helm_template.render_expect_failure(
        set_values=["global.services.maas.enabled=true", "global.services.caas.enabled=false"]
    )
    assert rc != 0, f"helm template should fail for MaaS without CaaS, got rc=0: {output}"
    assert "maas" in output.lower() or "caas" in output.lower(), (
        f"Error should reference MaaS/CaaS dependency, got: {output}"
    )
