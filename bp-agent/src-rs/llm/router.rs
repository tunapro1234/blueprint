use std::collections::HashMap;
use std::sync::Arc;

use super::types::{CompletionRequest, LLMResponse, ProviderAdapter, ProviderError};

pub struct LLMRouter {
    default_provider: String,
    providers: HashMap<String, Arc<dyn ProviderAdapter>>,
}

impl LLMRouter {
    pub fn new(default_provider: &str) -> Self {
        Self {
            default_provider: default_provider.to_string(),
            providers: HashMap::new(),
        }
    }

    pub fn register_provider(&mut self, name: &str, adapter: Arc<dyn ProviderAdapter>) {
        self.providers.insert(name.to_string(), adapter);
    }

    pub fn complete(&self, request: CompletionRequest) -> Result<LLMResponse, ProviderError> {
        let provider = request
            .provider
            .clone()
            .unwrap_or_else(|| self.default_provider.clone());
        let adapter = self.providers.get(&provider).ok_or_else(|| {
            ProviderError::new("provider_missing", &format!("provider not registered: {}", provider), false)
        })?;
        adapter.complete(request)
    }
}
