use std::env;

use crate::models::CLIConfig;

const DEFAULT_URL: &str = "http://localhost:8080";
const DEFAULT_PROVIDER: &str = "gemini";
const DEFAULT_MODEL: &str = "gemini-3-pro-preview";

pub fn parse_config() -> CLIConfig {
    let mut cfg = CLIConfig {
        base_url: env_or("BASE_AGENT_URL", DEFAULT_URL.to_string()),
        provider: env_or("BASE_AGENT_PROVIDER", DEFAULT_PROVIDER.to_string()),
        model: env_opt("BASE_AGENT_MODEL"),
        system_prompt: env_opt("BASE_AGENT_SYSTEM_PROMPT"),
        temperature: env_float("BASE_AGENT_TEMPERATURE", 0.3),
        debug: env_bool("BASE_AGENT_DEBUG", false),
        token: env_opt("BASE_AGENT_TOKEN"),
    };

    let args: Vec<String> = env::args().collect();
    let mut idx = 1;
    while idx < args.len() {
        match args[idx].as_str() {
            "--base" => {
                if let Some(value) = args.get(idx + 1) {
                    cfg.base_url = value.clone();
                    idx += 1;
                }
            }
            "--provider" => {
                if let Some(value) = args.get(idx + 1) {
                    cfg.provider = value.clone();
                    idx += 1;
                }
            }
            "--model" => {
                if let Some(value) = args.get(idx + 1) {
                    cfg.model = Some(value.clone());
                    idx += 1;
                }
            }
            "--system" => {
                if let Some(value) = args.get(idx + 1) {
                    cfg.system_prompt = Some(value.clone());
                    idx += 1;
                }
            }
            "--temp" => {
                if let Some(value) = args.get(idx + 1) {
                    if let Ok(parsed) = value.parse::<f64>() {
                        cfg.temperature = parsed;
                    }
                    idx += 1;
                }
            }
            "--debug" => {
                if let Some(value) = args.get(idx + 1) {
                    if value.starts_with("-") {
                        cfg.debug = true;
                    } else if let Ok(parsed) = value.parse::<bool>() {
                        cfg.debug = parsed;
                        idx += 1;
                    } else {
                        cfg.debug = true;
                    }
                } else {
                    cfg.debug = true;
                }
            }
            "--token" => {
                if let Some(value) = args.get(idx + 1) {
                    cfg.token = Some(value.clone());
                    idx += 1;
                }
            }
            _ => {}
        }
        idx += 1;
    }

    if cfg.model.is_none() {
        cfg.model = Some(DEFAULT_MODEL.to_string());
    }

    cfg
}

fn env_or(key: &str, fallback: String) -> String {
    env::var(key).unwrap_or(fallback)
}

fn env_opt(key: &str) -> Option<String> {
    match env::var(key) {
        Ok(value) if !value.trim().is_empty() => Some(value),
        _ => None,
    }
}

fn env_bool(key: &str, fallback: bool) -> bool {
    match env::var(key) {
        Ok(value) => value.parse::<bool>().unwrap_or(fallback),
        Err(_) => fallback,
    }
}

fn env_float(key: &str, fallback: f64) -> f64 {
    match env::var(key) {
        Ok(value) => value.parse::<f64>().unwrap_or(fallback),
        Err(_) => fallback,
    }
}
