use serde_json::Value;

use crate::llm::{CompletionRequest, Message};
use crate::task::{TaskStatus, TaskStore};
use crate::tools::{ToolHandler, ToolRegistry, ToolSchema};

use crate::config::AgentConfig;
use crate::helpers::build_llm_router;
use crate::result::AgentResult;

pub struct Agent {
    pub name: String,
    pub config: AgentConfig,
    pub system_prompt: String,
    pub router: crate::llm::LLMRouter,
    pub tools: ToolRegistry,
    pub tasks: Option<TaskStore>,
}

impl Agent {
    pub fn new(name: &str, mut config: AgentConfig, system_prompt: &str) -> Self {
        let resolved_name = if name.is_empty() { "agent" } else { name };
        let prompt = if system_prompt.is_empty() {
            "You are a helpful assistant."
        } else {
            system_prompt
        };
        if config.provider.is_empty() {
            config.provider = AgentConfig::default().provider;
        }
        if config.model.is_empty() {
            config.model = AgentConfig::default().model;
        }
        if config.max_iterations == 0 {
            config.max_iterations = AgentConfig::default().max_iterations;
        }
        if config.temperature == 0.0 {
            config.temperature = AgentConfig::default().temperature;
        }
        let router = build_llm_router(&config).expect("failed to build LLM router");
        let tasks = if config.enable_task_store {
            Some(TaskStore::new(false, None))
        } else {
            None
        };
        Self {
            name: resolved_name.to_string(),
            config,
            system_prompt: prompt.to_string(),
            router,
            tools: ToolRegistry::new(),
            tasks,
        }
    }

    pub fn add_tool(&mut self, name: &str, handler: ToolHandler, schema: ToolSchema) -> Result<(), String> {
        self.tools.register(name, handler, schema)
    }

    pub fn execute(&self, instruction: &str) -> AgentResult {
        let mut task_id = None;
        if let Some(store) = &self.tasks {
            let task = store.create(instruction);
            task_id = Some(task.id.clone());
        }

        let mut messages = vec![
            Message {
                role: "system".to_string(),
                content: self.system_prompt.clone(),
            },
            Message {
                role: "user".to_string(),
                content: instruction.to_string(),
            },
        ];

        let tool_schemas = if self.tools.count() > 0 {
            Some(self.tools.get_schemas())
        } else {
            None
        };

        for _ in 0..self.config.max_iterations {
            let request = CompletionRequest {
                messages: messages.clone(),
                tools: tool_schemas.clone(),
                temperature: Some(self.config.temperature),
                model: Some(self.config.model.clone()),
                provider: Some(self.config.provider.clone()),
                metadata: None,
            };
            let response = match self.router.complete(request) {
                Ok(resp) => resp,
                Err(err) => {
                    if let (Some(store), Some(id)) = (&self.tasks, task_id.as_ref()) {
                        let _ = store.update(id, TaskStatus::Failed, None, Some(err.message));
                    }
                    return AgentResult {
                        success: false,
                        output: String::new(),
                        task_id,
                        trace: None,
                    };
                }
            };

            if response.tool_calls.is_empty() {
                if let (Some(store), Some(id)) = (&self.tasks, task_id.as_ref()) {
                    let _ = store.update(id, TaskStatus::Completed, Some(response.content.clone()), None);
                }
                return AgentResult {
                    success: true,
                    output: response.content,
                    task_id,
                    trace: None,
                };
            }

            messages.push(Message {
                role: "assistant".to_string(),
                content: response.content.clone(),
            });

            for call in response.tool_calls {
                let result = self.tools.execute(&call.name, call.args.clone());
                let content = if result.success {
                    format!("Tool {} result: {}", call.name, render_value(result.output))
                } else {
                    format!(
                        "Tool {} error: {}",
                        call.name,
                        result.error.unwrap_or_else(|| "unknown error".to_string())
                    )
                };
                messages.push(Message {
                    role: "user".to_string(),
                    content,
                });
            }
        }

        if let (Some(store), Some(id)) = (&self.tasks, task_id.as_ref()) {
            let _ = store.update(id, TaskStatus::Failed, None, Some("max iterations reached".to_string()));
        }

        AgentResult {
            success: false,
            output: String::new(),
            task_id,
            trace: None,
        }
    }
}

fn render_value(value: Option<Value>) -> String {
    match value {
        Some(val) => val.to_string(),
        None => "".to_string(),
    }
}
