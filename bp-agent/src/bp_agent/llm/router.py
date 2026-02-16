"""Provider router."""

from __future__ import annotations

from typing import Protocol

from .types import CompletionRequest, LLMResponse, StreamChunk, StreamIterator


class ProviderAdapter(Protocol):
    def complete(self, request: CompletionRequest) -> LLMResponse:
        ...


class LLMRouter:
    def __init__(self, default_provider: str = "gemini"):
        self.default_provider = default_provider
        self._providers: dict[str, ProviderAdapter] = {}

    def register_provider(self, name: str, adapter: ProviderAdapter):
        self._providers[name] = adapter

    def complete(self, request: CompletionRequest) -> LLMResponse:
        provider = request.provider or self.default_provider
        if provider not in self._providers:
            raise ValueError(f"Provider not registered: {provider}")
        return self._providers[provider].complete(request)

    def complete_stream(self, request: CompletionRequest) -> StreamIterator:
        provider = request.provider or self.default_provider
        if provider not in self._providers:
            raise ValueError(f"Provider not registered: {provider}")
        adapter = self._providers[provider]
        if hasattr(adapter, "complete_stream"):
            return adapter.complete_stream(request)
        # Fallback: call complete() and yield a single chunk
        response = adapter.complete(request)
        return self._fallback_stream(response)

    @staticmethod
    def _fallback_stream(response: LLMResponse) -> StreamIterator:
        yield StreamChunk(delta=response.content, finish_reason="stop")
