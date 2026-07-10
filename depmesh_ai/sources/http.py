"""Tiny stdlib HTTP helper shared by all sources.

Distinguishes "the registry said 404" (authoritative: package absent)
from "the registry could not be reached" (network/policy failure) — the
vet engine treats these very differently.
"""
from __future__ import annotations

import json
import urllib.error
import urllib.request
from typing import Any, Optional

USER_AGENT = "depmesh-ai/0.1 (+https://github.com/jhberges/depmesh-ai)"
DEFAULT_TIMEOUT = 20.0


class SourceUnavailable(Exception):
    """The source could not be reached or answered abnormally."""


class NotFound(Exception):
    """The source authoritatively reported the resource as absent."""


def _open(url: str, timeout: float, accept: Optional[str]) -> bytes:
    headers = {"User-Agent": USER_AGENT}
    if accept:
        headers["Accept"] = accept
    request = urllib.request.Request(url, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.read()
    except urllib.error.HTTPError as err:
        if err.code == 404:
            raise NotFound(url) from err
        raise SourceUnavailable(f"{url} -> HTTP {err.code}") from err
    except (urllib.error.URLError, OSError, TimeoutError) as err:
        raise SourceUnavailable(f"{url} -> {err}") from err


def get_json(url: str, timeout: float = DEFAULT_TIMEOUT) -> Any:
    body = _open(url, timeout, accept="application/json")
    try:
        return json.loads(body)
    except json.JSONDecodeError as err:
        raise SourceUnavailable(f"{url} -> invalid JSON") from err


def get_text(url: str, timeout: float = DEFAULT_TIMEOUT) -> str:
    return _open(url, timeout, accept=None).decode("utf-8", errors="replace")
