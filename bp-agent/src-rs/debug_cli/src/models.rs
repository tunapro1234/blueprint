use serde::{Deserialize, Serialize};
use serde_json::Value;

#[derive(Clone, Debug)]
pub struct CLIConfig {
    pub base_url: String,
    pub provider: String,
    pub model: Option<String>,
    pub system_prompt: Option<String>,
    pub temperature: f64,
    pub debug: bool,
    pub token: Option<String>,
}

#[derive(Clone, Debug)]
pub struct ChatMessage {
    pub role: String,
    pub content: String,
}

#[derive(Debug, Serialize)]
pub struct ExecuteRequest {
    pub instruction: String,
    pub system_prompt: Option<String>,
    pub provider: Option<String>,
    pub model: Option<String>,
    pub temperature: Option<f64>,
    pub debug: bool,
}

#[derive(Debug, Deserialize)]
pub struct ExecuteResponse {
    pub success: bool,
    pub output: String,
    pub task_id: Option<String>,
    pub trace: Option<Value>,
    pub error: Option<String>,
}

#[derive(Debug, Deserialize)]
pub struct TaskInfo {
    pub id: String,
    pub status: String,
    pub instruction: String,
    pub output: Option<String>,
    pub created_at: String,
}
