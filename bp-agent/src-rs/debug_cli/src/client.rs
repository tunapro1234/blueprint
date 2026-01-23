use reqwest::blocking::Client;
use reqwest::header::{HeaderMap, HeaderValue, AUTHORIZATION, CONTENT_TYPE};

use crate::models::{ExecuteRequest, ExecuteResponse, TaskInfo};

pub struct HTTPClient {
    pub base_url: String,
    pub token: Option<String>,
    client: Client,
}

impl HTTPClient {
    pub fn new(base_url: &str, token: Option<String>) -> Self {
        Self {
            base_url: base_url.to_string(),
            token,
            client: Client::builder()
                .timeout(std::time::Duration::from_secs(30))
                .build()
                .expect("reqwest client"),
        }
    }

    pub fn execute(&self, req: ExecuteRequest) -> Result<ExecuteResponse, String> {
        let url = format!("{}/execute", self.base_url.trim_end_matches('/'));
        let mut headers = HeaderMap::new();
        headers.insert(CONTENT_TYPE, HeaderValue::from_static("application/json"));
        if let Some(token) = &self.token {
            let value = format!("Bearer {}", token);
            if let Ok(header) = HeaderValue::from_str(&value) {
                headers.insert(AUTHORIZATION, header);
            }
        }

        let resp = self
            .client
            .post(url)
            .headers(headers)
            .json(&req)
            .send()
            .map_err(|err| err.to_string())?;

        if resp.status().is_success() {
            resp.json::<ExecuteResponse>().map_err(|err| err.to_string())
        } else {
            let status = resp.status();
            let body = resp.text().unwrap_or_default();
            Err(format!("http {}: {}", status.as_u16(), body))
        }
    }

    pub fn list_tasks(&self, limit: usize) -> Result<Vec<TaskInfo>, String> {
        let url = format!("{}/tasks?limit={}", self.base_url.trim_end_matches('/'), limit);
        let resp = self.client.get(url).send().map_err(|err| err.to_string())?;
        if resp.status().is_success() {
            let value = resp
                .json::<serde_json::Value>()
                .map_err(|err| err.to_string())?;
            let tasks = value
                .get("tasks")
                .and_then(|v| v.as_array())
                .cloned()
                .unwrap_or_default();
            let mut out = Vec::new();
            for item in tasks {
                if let Ok(task) = serde_json::from_value::<TaskInfo>(item) {
                    out.push(task);
                }
            }
            Ok(out)
        } else {
            let status = resp.status();
            let body = resp.text().unwrap_or_default();
            Err(format!("http {}: {}", status.as_u16(), body))
        }
    }
}
