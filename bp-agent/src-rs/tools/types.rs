use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::sync::Arc;

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ToolSchema {
    pub name: String,
    pub description: String,
    pub parameters: Option<Value>,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ToolResult {
    pub success: bool,
    pub output: Option<Value>,
    pub error: Option<String>,
}

pub type ToolHandler = Arc<dyn Fn(Value) -> Result<Value, String> + Send + Sync>;

pub struct ToolEntry {
    pub name: String,
    pub handler: ToolHandler,
    pub schema: ToolSchema,
}
