use super::rotation::Rotator;
use super::types::{CompletionRequest, LLMResponse, ProviderAdapter, ProviderError};

pub struct OpusConfig {
    pub api_keys: Vec<String>,
    pub base_url: String,
    pub endpoint: String,
    pub model: String,
    pub temperature: f64,
}

pub struct OpusAdapter {
    cfg: OpusConfig,
    rotator: Rotator,
}

impl OpusAdapter {
    pub fn new(cfg: OpusConfig) -> Self {
        Self {
            rotator: Rotator::new(cfg.api_keys.clone()),
            cfg,
        }
    }
}

impl ProviderAdapter for OpusAdapter {
    fn complete(&self, _request: CompletionRequest) -> Result<LLMResponse, ProviderError> {
        let _ = &self.rotator;
        Err(ProviderError::new("not_implemented", "opus adapter not implemented", false))
    }
}
