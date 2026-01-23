use std::io::{self, Write};

use crate::models::{CLIConfig, ChatMessage, ExecuteResponse, TaskInfo};

pub fn banner(cfg: &CLIConfig) {
    println!("Base Agent Debug CLI");
    println!("API: {}", cfg.base_url);
    println!(
        "Provider: {}  Model: {}  Temp: {:.2}",
        cfg.provider,
        cfg.model.clone().unwrap_or_default(),
        cfg.temperature
    );
    println!("Type /help for commands.");
}

pub fn prompt() {
    print!("> ");
    let _ = io::stdout().flush();
}

pub fn help() {
    println!("Commands:");
    println!("  /help                 Show commands");
    println!("  /exit | /quit          Exit");
    println!("  /system <prompt>       Set system prompt");
    println!("  /provider <name>       Set provider");
    println!("  /model <name>          Set model");
    println!("  /temp <float>          Set temperature");
    println!("  /debug [on|off]        Toggle debug output");
    println!("  /tasks [limit]         List tasks");
    println!("  /history               Show chat history");
    println!("  /reset                 Clear chat history");
    println!("  /config                Show current config");
    println!("  /base <url>            Update base URL");
    println!("  /token <token>         Update bearer token");
}

pub fn response(resp: &ExecuteResponse, debug: bool) {
    if let Some(err) = &resp.error {
        println!("error: {}", err);
        return;
    }
    println!("assistant> {}", resp.output);
    if debug {
        if let Some(trace) = &resp.trace {
            println!("trace: {}", trace);
        }
    }
}

pub fn tasks(tasks: &[TaskInfo]) {
    if tasks.is_empty() {
        println!("no tasks");
        return;
    }
    for task in tasks {
        println!("[{}] {} - {}", task.status, task.id, task.instruction);
    }
}

pub fn config(cfg: &CLIConfig) {
    println!("config:");
    println!("  base: {}", cfg.base_url);
    println!("  provider: {}", cfg.provider);
    println!("  model: {}", cfg.model.clone().unwrap_or_default());
    println!("  temp: {:.2}", cfg.temperature);
    println!("  debug: {}", cfg.debug);
    if let Some(system) = &cfg.system_prompt {
        println!("  system: {}", system);
    }
}

pub fn history(items: &[ChatMessage]) {
    if items.is_empty() {
        println!("no history");
        return;
    }
    for msg in items {
        println!("{}> {}", msg.role, msg.content);
    }
}

pub fn info(msg: &str) {
    println!("{}", msg);
}

pub fn error(msg: &str) {
    eprintln!("error: {}", msg);
}
