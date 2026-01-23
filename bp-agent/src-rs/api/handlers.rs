use std::sync::{Arc, Mutex};

use axum::extract::{Query, State};
use axum::Json;
use serde::{Deserialize, Serialize};
use serde_json::json;

use crate::agent::Agent;
use crate::result::AgentResult;

#[derive(Debug, Deserialize)]
pub struct ExecuteRequest {
    pub instruction: String,
    pub system_prompt: Option<String>,
    pub provider: Option<String>,
    pub model: Option<String>,
    pub temperature: Option<f64>,
    pub debug: Option<bool>,
}

#[derive(Debug, Serialize)]
pub struct ExecuteResponse {
    pub success: bool,
    pub output: String,
    pub task_id: Option<String>,
    pub trace: Option<serde_json::Value>,
    pub error: Option<String>,
}

#[derive(Debug, Deserialize, Default)]
pub struct TasksQuery {
    pub limit: Option<usize>,
}

pub async fn handle_health() -> Json<serde_json::Value> {
    Json(json!({"status": "ok", "version": "0.1.0"}))
}

pub async fn handle_tasks(
    State(agent): State<Arc<Mutex<Agent>>>,
    Query(query): Query<TasksQuery>,
) -> Json<serde_json::Value> {
    let limit = query.limit.unwrap_or(10);
    let agent = match agent.lock() {
        Ok(agent) => agent,
        Err(_) => {
            return Json(json!({"error": "agent lock error"}));
        }
    };

    if let Some(store) = &agent.tasks {
        let tasks = store.list(limit);
        Json(json!({"tasks": tasks}))
    } else {
        Json(json!({"error": "task store disabled"}))
    }
}

pub async fn handle_execute(
    State(agent): State<Arc<Mutex<Agent>>>,
    Json(req): Json<ExecuteRequest>,
) -> Json<ExecuteResponse> {
    if req.instruction.trim().is_empty() {
        return Json(ExecuteResponse {
            success: false,
            output: String::new(),
            task_id: None,
            trace: None,
            error: Some("instruction required".to_string()),
        });
    }

    let instruction = req.instruction.clone();
    let provider = req.provider.clone();
    let model = req.model.clone();
    let temperature = req.temperature;
    let system_prompt = req.system_prompt.clone();

    let result = tokio::task::spawn_blocking(move || {
        let mut agent = agent.lock().map_err(|_| "agent lock error".to_string())?;
        let original_config = agent.config.clone();
        let original_prompt = agent.system_prompt.clone();

        if let Some(provider) = provider {
            agent.config.provider = provider;
        }
        if let Some(model) = model {
            agent.config.model = model;
        }
        if let Some(temp) = temperature {
            agent.config.temperature = temp;
        }
        if let Some(prompt) = system_prompt {
            agent.system_prompt = prompt;
        }

        let result = agent.execute(&instruction);

        agent.config = original_config;
        agent.system_prompt = original_prompt;

        Ok(result)
    })
    .await;

    match result {
        Ok(Ok(result)) => Json(to_response(result)),
        Ok(Err(err)) => Json(ExecuteResponse {
            success: false,
            output: String::new(),
            task_id: None,
            trace: None,
            error: Some(err),
        }),
        Err(err) => Json(ExecuteResponse {
            success: false,
            output: String::new(),
            task_id: None,
            trace: None,
            error: Some(err.to_string()),
        }),
    }
}

fn to_response(result: AgentResult) -> ExecuteResponse {
    ExecuteResponse {
        success: result.success,
        output: result.output,
        task_id: result.task_id,
        trace: result.trace,
        error: None,
    }
}
