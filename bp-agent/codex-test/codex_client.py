#!/usr/bin/env python3
"""
Codex CLI gibi davranan Python client.
~/.codex/auth.json'daki token'ları kullanır.

Kullanim:
    from codex_client import CodexClient

    client = CodexClient()
    response = client.chat("Merhaba!")

    # Reasoning seviyesi ile
    response = client.chat("Karmasik bir problem", reasoning_effort="xhigh")
"""

import json
import uuid
import requests
from pathlib import Path
from typing import Optional, Literal, Generator
from dataclasses import dataclass

# Reasoning effort seviyeleri
ReasoningEffort = Literal["none", "minimal", "low", "medium", "high", "xhigh"]

# Mevcut modeller
MODELS = {
    "gpt-5.2-codex": "Latest frontier agentic coding model",
    "gpt-5.1-codex-max": "Deep and fast reasoning",
    "gpt-5.1-codex-mini": "Cheaper, faster, but less capable",
    "gpt-5.2": "Latest frontier model",
    "gpt-5.1": "Broad world knowledge with strong reasoning",
}

DEFAULT_MODEL = "gpt-5.2-codex"
DEFAULT_REASONING = "medium"


class AccountManager:
    """
    Birden fazla Codex hesabını yönet.

    Kullanım:
        manager = AccountManager()
        manager.add_account("work", "/path/to/work/auth.json")
        manager.add_account("personal", "/path/to/personal/auth.json")

        # Hesap seç ve client al
        client = manager.get_client("work")
        response = client.chat("Merhaba")

        # Hesapları listele
        print(manager.list_accounts())

        # Round-robin ile sırayla kullan
        for account_name, client in manager.round_robin():
            response = client.chat("Test")
    """

    def __init__(self):
        self._accounts: dict[str, dict] = {}
        self._clients: dict[str, CodexClient] = {}
        self._round_robin_index = 0

    def add_account(
        self,
        name: str,
        auth_file: Optional[str] = None,
        auth_data: Optional[dict] = None
    ) -> "AccountManager":
        """Hesap ekle. Zincirleme kullanım için self döner."""
        if auth_file:
            with open(auth_file) as f:
                self._accounts[name] = {
                    "auth_data": json.load(f),
                    "auth_file": auth_file
                }
        elif auth_data:
            self._accounts[name] = {
                "auth_data": auth_data,
                "auth_file": None
            }
        else:
            raise ValueError("auth_file veya auth_data gerekli")
        return self

    def add_default_account(self, name: str = "default") -> "AccountManager":
        """~/.codex/auth.json'u ekle"""
        default_path = Path.home() / ".codex" / "auth.json"
        return self.add_account(name, auth_file=str(default_path))

    def list_accounts(self) -> list[str]:
        """Kayıtlı hesap isimlerini döner"""
        return list(self._accounts.keys())

    def get_client(
        self,
        name: str,
        model: str = DEFAULT_MODEL,
        reasoning_effort: ReasoningEffort = DEFAULT_REASONING
    ) -> "CodexClient":
        """Belirtilen hesap için client döner (cache'li)"""
        cache_key = f"{name}:{model}:{reasoning_effort}"

        if cache_key not in self._clients:
            if name not in self._accounts:
                raise ValueError(f"Hesap bulunamadı: {name}. Mevcut: {self.list_accounts()}")

            self._clients[cache_key] = CodexClient(
                model=model,
                reasoning_effort=reasoning_effort,
                auth_data=self._accounts[name]["auth_data"]
            )

        return self._clients[cache_key]

    def round_robin(
        self,
        model: str = DEFAULT_MODEL,
        reasoning_effort: ReasoningEffort = DEFAULT_REASONING
    ) -> Generator[tuple[str, "CodexClient"], None, None]:
        """Hesapları sırayla döner (round-robin)"""
        accounts = self.list_accounts()
        if not accounts:
            return

        while True:
            name = accounts[self._round_robin_index % len(accounts)]
            self._round_robin_index += 1
            yield name, self.get_client(name, model, reasoning_effort)

    def get_next(
        self,
        model: str = DEFAULT_MODEL,
        reasoning_effort: ReasoningEffort = DEFAULT_REASONING
    ) -> tuple[str, "CodexClient"]:
        """Sıradaki hesabı döner"""
        return next(self.round_robin(model, reasoning_effort))

    def get_account_info(self, name: str) -> dict:
        """Hesap bilgilerini döner"""
        if name not in self._accounts:
            raise ValueError(f"Hesap bulunamadı: {name}")

        account = self._accounts[name]
        tokens = account["auth_data"].get("tokens", {})
        return {
            "name": name,
            "auth_file": account["auth_file"],
            "account_id": tokens.get("account_id", "N/A")[:20] + "...",
            "has_access_token": bool(tokens.get("access_token")),
            "has_refresh_token": bool(tokens.get("refresh_token"))
        }


@dataclass
class CodexResponse:
    """Codex API yanıt objesi"""
    content: str
    model: str
    reasoning_effort: str
    response_id: str      # Yanıt ID'si (resp_...)
    conversation_id: str  # Konuşma ID'si (UUID) - codex resume için
    raw: dict


class CodexClient:
    """
    Codex API client - ChatGPT hesabı ile çalışır.

    Multi-account desteği:
        # Varsayılan (~/.codex/auth.json)
        client = CodexClient()

        # Farklı auth dosyası
        client = CodexClient(auth_file="/path/to/auth.json")

        # Dict olarak auth bilgisi
        client = CodexClient(auth_data={"tokens": {...}})
    """

    def __init__(
        self,
        model: str = DEFAULT_MODEL,
        reasoning_effort: ReasoningEffort = DEFAULT_REASONING,
        auth_file: Optional[str] = None,
        auth_data: Optional[dict] = None
    ):
        self.base_url = "https://chatgpt.com/backend-api/codex"
        self.default_model = model
        self.default_reasoning = reasoning_effort

        self._auth_data = None
        self._auth_file_path = None

        # Auth yükleme önceliği: auth_data > auth_file > default
        if auth_data:
            self._auth_data = auth_data
        else:
            self._auth_file_path = Path(auth_file) if auth_file else Path.home() / ".codex" / "auth.json"
            self._load_auth()

    def _load_auth(self):
        """Auth dosyasını yükle"""
        if not self._auth_file_path.exists():
            raise FileNotFoundError(
                f"Auth dosyası bulunamadı: {self._auth_file_path}\n"
                "Önce 'codex --login' çalıştırın veya doğru dosya yolunu verin."
            )

        with open(self._auth_file_path) as f:
            self._auth_data = json.load(f)

        if not self._auth_data.get("tokens"):
            raise ValueError(f"Token bilgisi bulunamadı: {self._auth_file_path}")

    @property
    def access_token(self) -> str:
        return self._auth_data["tokens"]["access_token"]

    @property
    def account_id(self) -> str:
        return self._auth_data["tokens"]["account_id"]

    def _get_headers(self) -> dict:
        """API için gerekli header'ları oluştur"""
        return {
            "Authorization": f"Bearer {self.access_token}",
            "Content-Type": "application/json",
            "ChatGPT-Account-Id": self.account_id,
            "User-Agent": "codex-cli"
        }

    def chat(
        self,
        message: str,
        model: Optional[str] = None,
        reasoning_effort: Optional[ReasoningEffort] = None,
        system_prompt: Optional[str] = None,
        conversation_id: Optional[str] = None
    ) -> CodexResponse:
        """
        Codex API'ye mesaj gönder.

        Args:
            message: Kullanıcı mesajı
            model: Model adı (default: gpt-5.2-codex)
            reasoning_effort: Düşünme seviyesi (none/minimal/low/medium/high/xhigh)
            system_prompt: Sistem prompt'u (opsiyonel)
            conversation_id: Konuşma ID'si (opsiyonel, verilmezse yeni oluşturulur)

        Returns:
            CodexResponse objesi
        """
        model = model or self.default_model
        effort = reasoning_effort or self.default_reasoning

        # Conversation ID yoksa yeni oluştur (UUID v4)
        conv_id = conversation_id or str(uuid.uuid4())

        url = f"{self.base_url}/responses"

        # Input mesajlarını oluştur (system prompt instructions'a gider)
        input_messages = [
            {"role": "user", "content": message}
        ]

        # System instructions (None = default prompt, "" = saf/raw mode)
        if system_prompt is None:
            instructions = "You are a helpful coding assistant."
        else:
            instructions = system_prompt  # Boş string dahil her şeyi kabul et

        payload = {
            "model": model,
            "instructions": instructions,
            "input": input_messages,
            "stream": True,  # API stream=true zorunlu kılıyor
            "store": False,
            "reasoning": {
                "effort": effort
            }
        }

        # Header'lara session_id ekle
        headers = self._get_headers()
        headers["session_id"] = conv_id

        # Streaming response'u topla
        full_content = []
        raw_events = []
        response_id = ""

        with requests.post(
            url,
            headers=headers,
            json=payload,
            stream=True,
            timeout=120
        ) as response:
            if response.status_code != 200:
                raise Exception(f"API Error {response.status_code}: {response.text}")

            for line in response.iter_lines():
                if line:
                    decoded = line.decode('utf-8')
                    if decoded.startswith('data: '):
                        data_str = decoded[6:]
                        if data_str and data_str != '[DONE]':
                            try:
                                event = json.loads(data_str)
                                raw_events.append(event)

                                # Response ID'yi çıkar
                                if not response_id:
                                    response_id = self._extract_response_id(event)

                                text = self._extract_stream_content(event)
                                if text:
                                    full_content.append(text)
                            except json.JSONDecodeError:
                                pass

        return CodexResponse(
            content="".join(full_content),
            model=model,
            reasoning_effort=effort,
            response_id=response_id,
            conversation_id=conv_id,
            raw={"events": raw_events}
        )

    def chat_stream(
        self,
        message: str,
        model: Optional[str] = None,
        reasoning_effort: Optional[ReasoningEffort] = None,
        system_prompt: Optional[str] = None
    ) -> Generator[str, None, None]:
        """
        Streaming response al.

        Yields:
            Her chunk için text parçası
        """
        model = model or self.default_model
        effort = reasoning_effort or self.default_reasoning

        url = f"{self.base_url}/responses"

        input_messages = [
            {"role": "user", "content": message}
        ]

        # System instructions (None = default prompt, "" = saf/raw mode)
        if system_prompt is None:
            instructions = "You are a helpful coding assistant."
        else:
            instructions = system_prompt

        payload = {
            "model": model,
            "instructions": instructions,
            "input": input_messages,
            "stream": True,
            "store": False,
            "reasoning": {
                "effort": effort
            }
        }

        with requests.post(
            url,
            headers=self._get_headers(),
            json=payload,
            stream=True,
            timeout=120
        ) as response:
            if response.status_code != 200:
                raise Exception(f"API Error {response.status_code}: {response.text}")

            for line in response.iter_lines():
                if line:
                    decoded = line.decode('utf-8')
                    if decoded.startswith('data: '):
                        data = decoded[6:]
                        if data != '[DONE]':
                            try:
                                chunk = json.loads(data)
                                text = self._extract_stream_content(chunk)
                                if text:
                                    yield text
                            except json.JSONDecodeError:
                                pass

    def _extract_content(self, data: dict) -> str:
        """API yanıtından content'i çıkar"""
        # Responses API formatı
        if "output" in data:
            for item in data.get("output", []):
                if item.get("type") == "message":
                    for content in item.get("content", []):
                        if content.get("type") == "output_text":
                            return content.get("text", "")

        # Alternatif formatlar
        if "choices" in data:
            return data["choices"][0].get("message", {}).get("content", "")

        return str(data)

    def _extract_response_id(self, event: dict) -> str:
        """Event'ten response ID'yi çıkar"""
        # response.created event'inde response objesi var
        if event.get("type") == "response.created":
            return event.get("response", {}).get("id", "")

        # Diğer eventlerde de response objesi olabilir
        if "response" in event:
            return event["response"].get("id", "")

        return ""

    def _extract_stream_content(self, event: dict) -> Optional[str]:
        """Stream event'inden content çıkar"""
        event_type = event.get("type", "")

        # response.output_text.delta - streaming text
        if event_type == "response.output_text.delta":
            return event.get("delta", "")

        # response.output_text.done - final text (alternatif)
        if event_type == "response.output_text.done":
            # Delta'lardan zaten aldık, bu duplicate olur
            return None

        return None


def main():
    """Test scripti"""
    print("=" * 60)
    print("CODEX CLIENT TEST")
    print("=" * 60)

    # Client oluştur - default: gpt-5.2-codex, xhigh reasoning
    client = CodexClient(
        model="gpt-5.2-codex",
        reasoning_effort="xhigh"
    )

    print(f"\nModel: {client.default_model}")
    print(f"Reasoning: {client.default_reasoning}")
    print(f"Account ID: {client.account_id[:20]}...")
    print("-" * 60)

    # Test mesajı gönder
    print("\nMesaj gönderiliyor: 'Merhaba! Nasılsın?'")
    print("-" * 60)

    try:
        response = client.chat("Merhaba! Nasılsın?")

        print(f"\n=== YANIT ===")
        print(f"Conversation ID: {response.conversation_id}")
        print(f"Response ID: {response.response_id}")
        print(f"Model: {response.model}")
        print(f"Reasoning: {response.reasoning_effort}")
        print(f"\nContent:\n{response.content}")
        print(f"\n>>> Bu ID ile konusmaya devam edebilirsin:")
        print(f"    response = client.chat('Yeni mesaj', conversation_id='{response.conversation_id}')")

        print(f"\n=== RAW RESPONSE ===")
        print(json.dumps(response.raw, indent=2, ensure_ascii=False)[:2000])

    except Exception as e:
        print(f"HATA: {e}")


if __name__ == "__main__":
    main()
