use std::env;

use crate::llm::{CodexAdapter, CodexConfig, GeminiAdapter, GeminiConfig, LLMRouter, OpusAdapter, OpusConfig};

use crate::config::AgentConfig;

fn load_keys_from_env(primary: &str, prefix: &str) -> Vec<String> {
    let mut keys = Vec::new();
    if let Ok(raw) = env::var(primary) {
        for item in raw.split(',') {
            let trimmed = item.trim();
            if !trimmed.is_empty() {
                keys.push(trimmed.to_string());
            }
        }
    }
    for idx in 2..=10 {
        let key = format!("{}_{}", prefix, idx);
        if let Ok(value) = env::var(&key) {
            let trimmed = value.trim();
            if !trimmed.is_empty() {
                keys.push(trimmed.to_string());
            }
        }
    }
    keys
}

pub fn load_gemini_keys() -> Vec<String> {
    load_keys_from_env("GEMINI_API_KEY", "GEMINI_API_KEY")
}

pub fn load_codex_keys() -> Vec<String> {
    load_keys_from_env("CODEX_API_KEY", "CODEX_API_KEY")
}

pub fn load_opus_keys() -> Vec<String> {
    load_keys_from_env("OPUS_API_KEY", "OPUS_API_KEY")
}

pub fn build_llm_router(cfg: &AgentConfig) -> Result<LLMRouter, String> {
    let mut router = LLMRouter::new(&cfg.provider);

    let gemini_keys = load_gemini_keys();
    if !gemini_keys.is_empty() {
        let model = if cfg.provider == "gemini" {
            cfg.model.clone()
        } else {
            "gemini-3-flash-preview".to_string()
        };
        let adapter = GeminiAdapter::new(GeminiConfig {
            api_keys: gemini_keys,
            base_url: "https://generativelanguage.googleapis.com".to_string(),
            model,
            temperature: cfg.temperature,
        });
        router.register_provider("gemini", std::sync::Arc::new(adapter));
    } else if cfg.provider == "gemini" {
        return Err("gemini provider selected but no GEMINI_API_KEY found".to_string());
    }

    let codex_keys = load_codex_keys();
    if !codex_keys.is_empty() {
        let adapter = CodexAdapter::new(CodexConfig {
            api_keys: codex_keys,
            auth_files: Vec::new(),
            model: cfg.model.clone(),
            reasoning_effort: cfg.reasoning_effort.clone(),
        });
        router.register_provider("codex", std::sync::Arc::new(adapter));
    } else if cfg.provider == "codex" {
        return Err("codex provider selected but no CODEX_API_KEY found".to_string());
    }

    let opus_keys = load_opus_keys();
    if !opus_keys.is_empty() {
        let adapter = OpusAdapter::new(OpusConfig {
            api_keys: opus_keys,
            base_url: String::new(),
            endpoint: String::new(),
            model: cfg.model.clone(),
            temperature: cfg.temperature,
        });
        router.register_provider("opus", std::sync::Arc::new(adapter));
    } else if cfg.provider == "opus" {
        return Err("opus provider selected but no OPUS_API_KEY found".to_string());
    }

    Ok(router)
}
