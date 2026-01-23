use std::collections::HashMap;
use std::sync::RwLock;

use serde_json::Value;

use super::types::{ToolEntry, ToolHandler, ToolResult, ToolSchema};

pub struct ToolRegistry {
    tools: RwLock<HashMap<String, ToolEntry>>,
}

impl ToolRegistry {
    pub fn new() -> Self {
        Self {
            tools: RwLock::new(HashMap::new()),
        }
    }

    pub fn register(&self, name: &str, handler: ToolHandler, mut schema: ToolSchema) -> Result<(), String> {
        if name.is_empty() {
            return Err("invalid tool".to_string());
        }
        if schema.name.is_empty() {
            schema.name = name.to_string();
        }
        if schema.name != name {
            return Err("schema name mismatch".to_string());
        }

        let mut map = self.tools.write().map_err(|_| "lock error".to_string())?;
        if map.contains_key(name) {
            return Err("tool already registered".to_string());
        }
        map.insert(
            name.to_string(),
            ToolEntry {
                name: name.to_string(),
                handler,
                schema,
            },
        );
        Ok(())
    }

    pub fn execute(&self, name: &str, args: Value) -> ToolResult {
        let map = match self.tools.read() {
            Ok(lock) => lock,
            Err(_) => {
                return ToolResult {
                    success: false,
                    output: None,
                    error: Some("lock error".to_string()),
                }
            }
        };

        let entry = match map.get(name) {
            Some(entry) => entry,
            None => {
                return ToolResult {
                    success: false,
                    output: None,
                    error: Some("tool not found".to_string()),
                }
            }
        };

        match (entry.handler)(args) {
            Ok(output) => ToolResult {
                success: true,
                output: Some(output),
                error: None,
            },
            Err(err) => ToolResult {
                success: false,
                output: None,
                error: Some(err),
            },
        }
    }

    pub fn get_schemas(&self) -> Vec<ToolSchema> {
        let map = match self.tools.read() {
            Ok(lock) => lock,
            Err(_) => return vec![],
        };
        map.values().map(|entry| entry.schema.clone()).collect()
    }

    pub fn has(&self, name: &str) -> bool {
        let map = match self.tools.read() {
            Ok(lock) => lock,
            Err(_) => return false,
        };
        map.contains_key(name)
    }

    pub fn count(&self) -> usize {
        let map = match self.tools.read() {
            Ok(lock) => lock,
            Err(_) => return 0,
        };
        map.len()
    }
}
