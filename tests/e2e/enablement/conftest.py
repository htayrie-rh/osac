from __future__ import annotations

from pathlib import Path
from typing import Any, ClassVar

import pytest
import yaml

from tests.e2e.core.runner import env, run, run_unchecked


@pytest.fixture(scope="session")
def osac_chart_path() -> str:
    return env("OSAC_CHART_PATH", "osac-installer/charts/osac")


@pytest.fixture(scope="session")
def helm_release_name() -> str:
    return env("OSAC_HELM_RELEASE", "osac")


@pytest.fixture(scope="session")
def helm_template(osac_chart_path: str) -> HelmTemplate:
    chart = Path(osac_chart_path)
    if not chart.exists():
        pytest.skip(f"Helm chart not found at {osac_chart_path}")
    run("helm", "dependency", "build", str(chart))
    return HelmTemplate(chart_path=str(chart))


class HelmTemplate:
    _REQUIRED_OVERRIDES: ClassVar[list[str]] = [
        "service.externalHostname=fulfillment-api.example.com",
        "service.internalHostname=fulfillment-internal-api.example.com",
    ]

    def __init__(self, *, chart_path: str) -> None:
        self.chart_path = chart_path

    def render(self, *, set_values: list[str] | None = None) -> list[dict[str, Any]]:
        args = ["helm", "template", "test-release", self.chart_path]
        for override in self._REQUIRED_OVERRIDES:
            args.extend(["--set", override])
        for val in set_values or []:
            args.extend(["--set", val])
        output = run(*args, timeout=60)
        return list(yaml.safe_load_all(output))

    def render_expect_failure(self, *, set_values: list[str]) -> tuple[str, int]:
        args = ["helm", "template", "test-release", self.chart_path]
        for override in self._REQUIRED_OVERRIDES:
            args.extend(["--set", override])
        for val in set_values:
            args.extend(["--set", val])
        return run_unchecked(*args, timeout=60)
