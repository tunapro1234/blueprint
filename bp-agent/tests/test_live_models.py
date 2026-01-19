import json
import os
from urllib import request, error

import pytest

from llm.gemini_adapter import GEMINI_ALLOWED_MODELS


def _fetch_models(api_key: str) -> list[str]:
    url = f"https://generativelanguage.googleapis.com/v1beta/models?key={api_key}"
    req = request.Request(url, method="GET")

    with request.urlopen(req, timeout=15) as resp:
        payload = resp.read().decode("utf-8")
        data = json.loads(payload)

    names = []
    for item in data.get("models", []):
        name = item.get("name", "")
        if name.startswith("models/"):
            name = name.split("/", 1)[1]
        if name:
            names.append(name)
    return names


def test_allowed_models_exist_if_online():
    api_key = os.getenv("GEMINI_API_KEY")
    if not api_key:
        pytest.skip("GEMINI_API_KEY not set")

    try:
        available = _fetch_models(api_key)
    except error.HTTPError as exc:
        if exc.code in {401, 403, 429}:
            pytest.skip(f"Gemini API not available (HTTP {exc.code})")
        raise
    except error.URLError:
        pytest.skip("Network unavailable")

    missing = [m for m in GEMINI_ALLOWED_MODELS if m not in available]
    assert not missing, f"Allowed models not found in models.list: {missing}"
