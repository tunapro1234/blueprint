mod cli;
mod client;
mod models;
mod repl;
mod render;

use client::HTTPClient;
use repl::REPL;

fn main() {
    let config = cli::parse_config();
    let client = HTTPClient::new(&config.base_url, config.token.clone());
    let mut repl = REPL::new(config, client);
    repl.run();
}
