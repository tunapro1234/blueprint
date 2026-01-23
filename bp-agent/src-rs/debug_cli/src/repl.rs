use std::io;

use crate::client::HTTPClient;
use crate::models::{ChatMessage, CLIConfig, ExecuteRequest};
use crate::render;

pub struct REPL {
    pub config: CLIConfig,
    pub client: HTTPClient,
    pub history: Vec<ChatMessage>,
}

impl REPL {
    pub fn new(config: CLIConfig, client: HTTPClient) -> Self {
        Self {
            config,
            client,
            history: Vec::new(),
        }
    }

    pub fn run(&mut self) {
        render::banner(&self.config);
        loop {
            render::prompt();
            let mut line = String::new();
            if io::stdin().read_line(&mut line).is_err() {
                break;
            }
            let line = line.trim().to_string();
            if line.is_empty() {
                continue;
            }
            if line.starts_with('/') {
                if self.handle_command(&line) {
                    break;
                }
                continue;
            }
            self.send(&line);
        }
    }

    fn handle_command(&mut self, line: &str) -> bool {
        let mut parts = line.splitn(2, ' ');
        let cmd = parts.next().unwrap_or("").trim_start_matches('/');
        let rest = parts.next().unwrap_or("").trim();
        match cmd {
            "exit" | "quit" => return true,
            "help" => render::help(),
            "system" => {
                if rest.is_empty() {
                    render::info(&format!("system prompt: {:?}", self.config.system_prompt));
                } else {
                    self.config.system_prompt = Some(rest.to_string());
                    render::info("system prompt updated");
                }
            }
            "provider" => {
                if rest.is_empty() {
                    render::info(&format!("provider: {}", self.config.provider));
                } else {
                    self.config.provider = rest.to_string();
                    render::info("provider updated");
                }
            }
            "model" => {
                if rest.is_empty() {
                    render::info(&format!("model: {:?}", self.config.model));
                } else {
                    self.config.model = Some(rest.to_string());
                    render::info("model updated");
                }
            }
            "temp" => {
                if rest.is_empty() {
                    render::info(&format!("temperature: {:.2}", self.config.temperature));
                } else if let Ok(val) = rest.parse::<f64>() {
                    self.config.temperature = val;
                    render::info("temperature updated");
                } else {
                    render::error("invalid temperature");
                }
            }
            "debug" => {
                if rest.is_empty() {
                    self.config.debug = !self.config.debug;
                    render::info(&format!("debug: {}", self.config.debug));
                } else if let Some(flag) = parse_on_off(rest) {
                    self.config.debug = flag;
                    render::info(&format!("debug: {}", self.config.debug));
                } else {
                    render::error("invalid debug flag");
                }
            }
            "tasks" => {
                let limit = rest.parse::<usize>().unwrap_or(10);
                self.list_tasks(limit);
            }
            "history" => render::history(&self.history),
            "reset" => {
                self.history.clear();
                render::info("history cleared");
            }
            "config" => render::config(&self.config),
            "base" => {
                if rest.is_empty() {
                    render::info(&format!("base: {}", self.config.base_url));
                } else {
                    self.config.base_url = rest.to_string();
                    self.client = HTTPClient::new(&self.config.base_url, self.config.token.clone());
                    render::info("base url updated");
                }
            }
            "token" => {
                if rest.is_empty() {
                    render::info("token updated");
                } else {
                    self.config.token = Some(rest.to_string());
                    self.client = HTTPClient::new(&self.config.base_url, self.config.token.clone());
                    render::info("token updated");
                }
            }
            _ => render::info("unknown command, type /help"),
        }
        false
    }

    fn send(&mut self, line: &str) {
        self.history.push(ChatMessage {
            role: "user".to_string(),
            content: line.to_string(),
        });

        let req = ExecuteRequest {
            instruction: line.to_string(),
            system_prompt: self.config.system_prompt.clone(),
            provider: Some(self.config.provider.clone()),
            model: self.config.model.clone(),
            temperature: Some(self.config.temperature),
            debug: self.config.debug,
        };

        match self.client.execute(req) {
            Ok(resp) => {
                if !resp.output.is_empty() {
                    self.history.push(ChatMessage {
                        role: "assistant".to_string(),
                        content: resp.output.clone(),
                    });
                }
                render::response(&resp, self.config.debug);
            }
            Err(err) => render::error(&err),
        }
    }

    fn list_tasks(&self, limit: usize) {
        match self.client.list_tasks(limit) {
            Ok(tasks) => render::tasks(&tasks),
            Err(err) => render::error(&err),
        }
    }
}

fn parse_on_off(value: &str) -> Option<bool> {
    match value.to_lowercase().as_str() {
        "on" | "true" | "1" | "yes" => Some(true),
        "off" | "false" | "0" | "no" => Some(false),
        _ => None,
    }
}
