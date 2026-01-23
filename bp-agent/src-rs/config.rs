#[derive(Clone, Debug)]
pub struct AgentConfig {
    pub provider: String,
    pub model: String,
    pub reasoning_effort: Option<String>,
    pub max_iterations: usize,
    pub temperature: f64,
    pub enable_task_store: bool,
    pub codex_auth_file: Option<String>,
}

impl Default for AgentConfig {
    fn default() -> Self {
        Self {
            provider: "gemini".to_string(),
            model: "gemini-3-flash-preview".to_string(),
            reasoning_effort: None,
            max_iterations: 10,
            temperature: 0.3,
            enable_task_store: true,
            codex_auth_file: None,
        }
    }
}
