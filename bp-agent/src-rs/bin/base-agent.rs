use std::env;

use base_agent_rs::api::server::AgentServer;

#[tokio::main]
async fn main() {
    let port = env::var("PORT")
        .ok()
        .and_then(|raw| raw.parse::<u16>().ok())
        .unwrap_or(8080);

    let server = AgentServer::new(port, None);
    println!("base-agent listening on :{}", port);
    if let Err(err) = server.start().await {
        eprintln!("server error: {}", err);
    }
}
