use std::net::SocketAddr;
use std::sync::{Arc, Mutex};

use axum::routing::{get, post};
use axum::Router;

use crate::agent::Agent;
use crate::config::AgentConfig;
use crate::api::handlers::{handle_execute, handle_health, handle_tasks};

pub struct AgentServer {
    pub port: u16,
    pub agent: Arc<Mutex<Agent>>,
}

impl AgentServer {
    pub fn new(port: u16, agent: Option<Arc<Mutex<Agent>>>) -> Self {
        let agent = agent.unwrap_or_else(|| {
            Arc::new(Mutex::new(Agent::new(
                "api-agent",
                AgentConfig::default(),
                "",
            )))
        });
        Self { port, agent }
    }

    pub async fn start(&self) -> Result<(), String> {
        let app = Router::new()
            .route("/health", get(handle_health))
            .route("/tasks", get(handle_tasks))
            .route("/execute", post(handle_execute))
            .with_state(self.agent.clone());

        let addr = SocketAddr::from(([0, 0, 0, 0], self.port));
        axum::Server::bind(&addr)
            .serve(app.into_make_service())
            .await
            .map_err(|err| err.to_string())
    }
}
