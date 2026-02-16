#!/usr/bin/env python3
"""
Codex CLI gibi davranarak API'ye istek gönderen script.
~/.codex/auth.json'daki token'ları kullanır.
"""

import json
import requests
from pathlib import Path

def load_auth():
    """Codex auth dosyasını oku"""
    auth_file = Path.home() / ".codex" / "auth.json"
    with open(auth_file) as f:
        return json.load(f)

def send_message(message: str):
    """Codex API'sine mesaj gönder"""
    auth = load_auth()
    tokens = auth["tokens"]

    access_token = tokens["access_token"]
    account_id = tokens["account_id"]

    # Codex API endpoint
    url = "https://chatgpt.com/backend-api/codex/responses"

    headers = {
        "Authorization": f"Bearer {access_token}",
        "Content-Type": "application/json",
        "ChatGPT-Account-Id": account_id,
        "User-Agent": "codex-cli"
    }

    payload = {
        "model": "o3-mini",
        "input": [
            {"role": "user", "content": message}
        ],
        "stream": False
    }

    print(f"İstek gönderiliyor: {url}")
    print(f"Mesaj: {message}")
    print("-" * 50)

    resp = requests.post(url, headers=headers, json=payload)

    print(f"Status: {resp.status_code}")
    print(f"Response Headers: {dict(resp.headers)}")
    print("-" * 50)

    if resp.status_code == 200:
        return resp.json()
    else:
        print(f"Hata: {resp.text}")
        return None

if __name__ == "__main__":
    result = send_message("Merhaba! Nasılsın?")

    if result:
        print("\n=== YANIT ===")
        print(json.dumps(result, indent=2, ensure_ascii=False))
