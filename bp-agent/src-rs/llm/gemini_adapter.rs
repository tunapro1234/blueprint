use reqwest::blocking::Client;
use serde_json::{json, Value};

use super::rotation::Rotator;
use super::types::{CompletionRequest, LLMResponse, Message, ProviderAdapter, ProviderError, ToolCall};
use crate::tools::ToolSchema;

const GEMINI_ALLOWED_MODELS: [&str; 2] = ["gemini-3-flash-preview", "gemini-3-pro-preview"];

pub struct GeminiConfig {
    pub api_keys: Vec<String>,
    pub base_url: String,
    pub model: String,
    pub temperature: f64,
}

pub struct GeminiAdapter {
    cfg: GeminiConfig,
    rotator: Rotator,
    client: Client,
}

impl GeminiAdapter {
    pub fn new(mut cfg: GeminiConfig) -> Self {
        if cfg.base_url.is_empty() {
            cfg.base_url = "https://generativelanguage.googleapis.com".to_string();
        }
        if cfg.model.is_empty() {
            cfg.model = "gemini-3-flash-preview".to_string();
        }
        if cfg.temperature == 0.0 {
            cfg.temperature = 0.3;
        }
        let client = Client::builder()
            .timeout(std::time::Duration::from_secs(60))
            .build()
            .expect("reqwest client");
        Self {
            rotator: Rotator::new(cfg.api_keys.clone()),
            cfg,
            client,
        }
    }
}

impl ProviderAdapter for GeminiAdapter {
    fn complete(&self, request: CompletionRequest) -> Result<LLMResponse, ProviderError> {
        let model = request
            .model
            .clone()
            .unwrap_or_else(|| self.cfg.model.clone());
        if !GEMINI_ALLOWED_MODELS.iter().any(|m| *m == model) {
            return Err(ProviderError::new(
                "invalid_model",
                &format!("model not allowed: {}", model),
                false,
            ));
        }
        let temperature = request.temperature.unwrap_or(self.cfg.temperature);
        let payload = build_payload(&request.messages, request.tools.as_ref(), temperature);

        let tries = self.cfg.api_keys.len();
        if tries == 0 {
            return Err(ProviderError::new("auth_error", "no Gemini API keys", false));
        }
        let mut last_err = None;
        for _ in 0..tries {
            let key = match self.rotator.next() {
                Some(key) => key,
                None => break,
            };
            match send_request(&self.client, &self.cfg.base_url, &model, &key, &payload) {
                Ok(resp) => return Ok(resp),
                Err(err) => last_err = Some(err),
            }
        }
        Err(last_err.unwrap_or_else(|| ProviderError::new("api_error", "request failed", true)))
    }
}

fn build_payload(messages: &[Message], tools: Option<&Vec<ToolSchema>>, temperature: f64) -> Value {
    let mut contents = Vec::new();
    let mut system_instruction = None;

    for msg in messages {
        if msg.role == "system" {
            system_instruction = Some(msg.content.clone());
            continue;
        }
        let role = if msg.role == "user" { "user" } else { "model" };
        contents.push(json!({
            "role": role,
            "parts": [{"text": msg.content}]
        }));
    }

    let mut payload = json!({
        "contents": contents,
        "generationConfig": {
            "temperature": temperature
        }
    });

    if let Some(system) = system_instruction {
        payload["systemInstruction"] = json!({
            "parts": [{"text": system}]
        });
    }

    if let Some(tools) = tools {
        let declarations: Vec<Value> = tools
            .iter()
            .map(|tool| {
                json!({
                    "name": tool.name,
                    "description": tool.description,
                    "parameters": tool.parameters.clone().unwrap_or(json!({})),
                })
            })
            .collect();
        payload["tools"] = json!([
            {
                "functionDeclarations": declarations
            }
        ]);
    }

    payload
}

fn send_request(
    client: &Client,
    base_url: &str,
    model: &str,
    api_key: &str,
    payload: &Value,
) -> Result<LLMResponse, ProviderError> {
    let endpoint = format!(
        "{}/v1beta/models/{}:generateContent",
        base_url.trim_end_matches('/'),
        model
    );
    let resp = client
        .post(endpoint)
        .header("Content-Type", "application/json")
        .header("x-goog-api-key", api_key)
        .json(payload)
        .send()
        .map_err(|err| ProviderError::new("network_error", &err.to_string(), true))?;

    let status = resp.status();
    let body = resp.text().unwrap_or_default();
    if status.is_client_error() || status.is_server_error() {
        let lowered = body.to_lowercase();
        if status.as_u16() == 401 || status.as_u16() == 403 {
            return Err(ProviderError::new("auth_error", &body, true));
        }
        if status.as_u16() == 429 || lowered.contains("quota") || lowered.contains("resource_exhausted") {
            return Err(ProviderError::new("rate_limit", &body, true));
        }
        if status.is_server_error() {
            return Err(ProviderError::new("server_error", &body, true));
        }
        return Err(ProviderError::new("api_error", &body, false));
    }

    let raw: Value = serde_json::from_str(&body)
        .map_err(|_| ProviderError::new("parse_error", "invalid json", false))?;
    let (content, tool_calls) = parse_response(&raw);
    Ok(LLMResponse {
        content,
        tool_calls,
        raw: Some(raw),
    })
}

fn parse_response(raw: &Value) -> (String, Vec<ToolCall>) {
    let mut text = String::new();
    let mut tool_calls = Vec::new();

    let candidates = raw.get("candidates").and_then(|v| v.as_array());
    let first = match candidates.and_then(|list| list.get(0)) {
        Some(value) => value,
        None => return (text, tool_calls),
    };
    let content = match first.get("content") {
        Some(value) => value,
        None => return (text, tool_calls),
    };
    let parts = match content.get("parts").and_then(|v| v.as_array()) {
        Some(parts) => parts,
        None => return (text, tool_calls),
    };

    for part in parts {
        if let Some(chunk) = part.get("text").and_then(|v| v.as_str()) {
            text.push_str(chunk);
        }
        if let Some(fc) = part.get("functionCall") {
            let name = fc.get("name").and_then(|v| v.as_str()).unwrap_or("");
            let args = fc.get("args").cloned().unwrap_or(json!({}));
            tool_calls.push(ToolCall {
                name: name.to_string(),
                args,
            });
        }
    }

    (text, tool_calls)
}
