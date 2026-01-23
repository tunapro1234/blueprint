use super::rotation::Rotator;
use super::types::{CompletionRequest, LLMResponse, ProviderAdapter, ProviderError};

pub struct CodexConfig {
    pub api_keys: Vec<String>,
    pub auth_files: Vec<String>,
    pub model: String,
    pub reasoning_effort: Option<String>,
}

pub struct CodexAuth {
    pub auth_file: String,
}

pub struct CodexAdapter {
    cfg: CodexConfig,
    rotator: Rotator,
}

impl CodexAdapter {
    pub fn new(cfg: CodexConfig) -> Self {
        Self {
            rotator: Rotator::new(cfg.api_keys.clone()),
            cfg,
        }
    }
}

impl ProviderAdapter for CodexAdapter {
    fn complete(&self, _request: CompletionRequest) -> Result<LLMResponse, ProviderError> {
        let _ = &self.rotator;
        Err(ProviderError::new("not_implemented", "codex adapter not implemented", false))
    }
}
