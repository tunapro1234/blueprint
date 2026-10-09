//! Tiny HTTP/1.1 test server (Go `httptest.NewServer` stand-in): one thread per
//! connection, `Connection: close` framing so handlers can stream and stall.

#![allow(dead_code)]

use std::io::{BufRead, BufReader, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::Arc;

pub struct Server {
    pub url: String,
}

/// Writes a response head; without `content_length` the body ends at close.
pub fn head(stream: &mut TcpStream, status: &str, content_length: Option<usize>, extra: &str) {
    let mut head = format!("HTTP/1.1 {status}\r\nConnection: close\r\n{extra}");
    if let Some(n) = content_length {
        head.push_str(&format!("Content-Length: {n}\r\n"));
    }
    head.push_str("\r\n");
    let _ = stream.write_all(head.as_bytes());
}

/// 200 with a complete body.
pub fn ok(stream: &mut TcpStream, body: &[u8]) {
    head(stream, "200 OK", Some(body.len()), "");
    let _ = stream.write_all(body);
}

pub fn serve<F>(handler: F) -> Server
where
    F: Fn(&str, &mut TcpStream) + Send + Sync + 'static,
{
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let url = format!("http://{}", listener.local_addr().unwrap());
    let handler = Arc::new(handler);
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(mut stream) = stream else { continue };
            let handler = handler.clone();
            std::thread::spawn(move || {
                let _ = stream.set_nodelay(true);
                let mut reader = BufReader::new(stream.try_clone().unwrap());
                let mut line = String::new();
                if reader.read_line(&mut line).is_err() {
                    return;
                }
                let path = line.split_whitespace().nth(1).unwrap_or("/").to_string();
                loop {
                    let mut header = String::new();
                    match reader.read_line(&mut header) {
                        Ok(0) | Err(_) => return,
                        Ok(_) if header == "\r\n" || header == "\n" => break,
                        Ok(_) => {}
                    }
                }
                handler(&path, &mut stream);
                let _ = stream.flush();
            });
        }
    });
    Server { url }
}
