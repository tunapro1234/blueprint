//! Port of internal/codexrpc/client_test.go.

use std::collections::HashSet;
use std::io::{BufRead, BufReader, Read, Write};
use std::os::unix::net::{UnixListener, UnixStream};
use std::sync::mpsc;
use std::thread;
use std::time::{Duration, Instant};

use base64::Engine as _;
use base64::engine::general_purpose::STANDARD;
use serde_json::{Value, json};
use sha1::{Digest, Sha1};

use super::websocket::{self, OP_CLOSE, OP_PING, OP_PONG, OP_TEXT};
use super::*;

#[derive(Default)]
struct TestRequest {
    id: u64,
    method: String,
    params: RawJson,
}
go_struct!(TestRequest {
    id: "id",
    method: "method",
    params: "params"
});

fn parse_request(line: &[u8]) -> TestRequest {
    let mut request = TestRequest::default();
    unmarshal(line, &mut request).expect("request JSON");
    request
}

fn params_value(request: &TestRequest) -> Value {
    serde_json::from_slice(request.params.as_bytes()).unwrap_or(Value::Null)
}

fn deadline(secs: u64) -> Option<Instant> {
    Some(Instant::now() + Duration::from_secs(secs))
}

fn write_line(out: &mut impl Write, value: &Value) -> std::io::Result<()> {
    out.write_all(format!("{value}\n").as_bytes())
}

/// A pipe pair for the fake server: (client stdout reader, server writer),
/// (server reader, client stdin writer).
fn pipes() -> (
    (std::io::PipeReader, std::io::PipeWriter),
    (std::io::PipeReader, std::io::PipeWriter),
) {
    (std::io::pipe().unwrap(), std::io::pipe().unwrap())
}

#[test]
fn test_stdio_matches_responses_by_id_and_forwards_notifications() {
    let ((client_out, mut server_out), (server_in, client_in)) = pipes();
    let (done_tx, done_rx) = mpsc::channel::<Result<(), String>>();
    thread::spawn(move || {
        let result = (|| -> Result<(), String> {
            let mut lines = BufReader::new(server_in).split(b'\n');
            let mut read = || -> Result<TestRequest, String> {
                let line = lines.next().ok_or("EOF")?.map_err(|e| e.to_string())?;
                Ok(parse_request(&line))
            };
            let initialize = read()?;
            if initialize.method != "initialize" {
                return Err(format!("initialize request: {}", initialize.method));
            }
            // The real app-server omits the jsonrpc field, so the fake does too.
            write_line(&mut server_out, &json!({"id": initialize.id, "result": {}}))
                .map_err(|e| e.to_string())?;
            let initialized = read()?;
            if initialized.method != "initialized" {
                return Err("missing initialized".into());
            }
            let first = read()?;
            let second = read()?;
            // A server-to-client request with a string id must be ignored, not fatal.
            write_line(
                &mut server_out,
                &json!({"id": "srv-1", "method": "item/reviewApproval", "params": {}}),
            )
            .map_err(|e| e.to_string())?;
            write_line(
                &mut server_out,
                &json!({"jsonrpc": "2.0", "method": "thread/status/changed", "params": {"threadId": "thread-1"}}),
            )
            .map_err(|e| e.to_string())?;
            for request in [second, first] {
                let tag = params_value(&request)["tag"]
                    .as_str()
                    .unwrap_or_default()
                    .to_owned();
                write_line(
                    &mut server_out,
                    &json!({"jsonrpc": "2.0", "id": request.id, "result": {"tag": tag}}),
                )
                .map_err(|e| e.to_string())?;
            }
            Ok(())
        })();
        let _ = done_tx.send(result);
    });

    let client = Arc::new(connect_stdio(client_out, client_in, deadline(3)).unwrap());
    #[derive(Default)]
    struct Tagged {
        tag: String,
    }
    go_struct!(Tagged { tag: "tag" });
    let (results_tx, results_rx) = mpsc::channel();
    for tag in ["alpha", "beta"] {
        let client = Arc::clone(&client);
        let results_tx = results_tx.clone();
        thread::spawn(move || {
            let mut got = Tagged::default();
            let err = client.call(
                deadline(3),
                "thread/list",
                json!({"tag": tag}),
                Some(&mut got),
            );
            let _ = results_tx.send((got.tag, err));
        });
    }
    let mut got = HashSet::new();
    for _ in 0..2 {
        let (tag, err) = results_rx.recv_timeout(Duration::from_secs(3)).unwrap();
        err.unwrap();
        got.insert(tag);
    }
    assert_eq!(got, HashSet::from(["alpha".to_owned(), "beta".to_owned()]));
    let notification = client
        .notifications()
        .recv_timeout(Duration::from_secs(3))
        .expect("notification forwarded");
    assert_eq!(notification.method, "thread/status/changed");
    assert!(String::from_utf8_lossy(notification.params.as_bytes()).contains("thread-1"));
    done_rx
        .recv_timeout(Duration::from_secs(3))
        .unwrap()
        .unwrap();
}

#[test]
fn test_thread_list_follows_pagination() {
    let ((client_out, mut server_out), (server_in, client_in)) = pipes();
    let (done_tx, done_rx) = mpsc::channel::<Result<(), String>>();
    thread::spawn(move || {
        let result = (|| -> Result<(), String> {
            let mut lines = BufReader::new(server_in).split(b'\n');
            let mut page = 0;
            while page < 3 {
                let line = lines.next().ok_or("EOF")?.map_err(|e| e.to_string())?;
                let request = parse_request(&line);
                if request.method == "initialized" {
                    continue;
                }
                let mut result = json!({});
                if page == 1 {
                    result = json!({"data": [{"id": "one", "name": "First", "cwd": "/one", "status": {"type": "idle"}}], "nextCursor": "next"});
                }
                if page == 2 {
                    let params = params_value(&request);
                    if params["cursor"] != "next" {
                        return Err(format!("cursor={}", params["cursor"]));
                    }
                    let update = json!({
                        "jsonrpc": "2.0",
                        "method": "thread/tokenUsage/updated",
                        "params": {
                            "threadId": "one",
                            "tokenUsage": {
                                "last": {"totalTokens": 12_000},
                                "total": {"totalTokens": 30_000},
                                "modelContextWindow": 200_000,
                            },
                        },
                    });
                    write_line(&mut server_out, &update).map_err(|e| e.to_string())?;
                    result = json!({"data": [{"id": "two", "name": "Second", "cwd": "/two", "status": {"type": "active"}}], "nextCursor": null});
                }
                write_line(
                    &mut server_out,
                    &json!({"jsonrpc": "2.0", "id": request.id, "result": result}),
                )
                .map_err(|e| e.to_string())?;
                page += 1;
            }
            Ok(())
        })();
        let _ = done_tx.send(result);
    });
    let client = connect_stdio(client_out, client_in, deadline(3)).unwrap();
    let threads = client.thread_list(deadline(3)).unwrap();
    assert_eq!(
        threads.iter().map(|t| t.id.as_str()).collect::<Vec<_>>(),
        ["one", "two"]
    );
    let usage = threads[0]
        .token_usage
        .as_ref()
        .expect("notification token usage");
    assert_eq!(usage.last.total_tokens, 12_000);
    assert_eq!(usage.model_context_window, Some(200_000));
    done_rx
        .recv_timeout(Duration::from_secs(3))
        .unwrap()
        .unwrap();
}

/// Reads one client frame: it must be final and masked.
fn read_client_frame(reader: &mut impl Read) -> Result<(Vec<u8>, u8), String> {
    let mut header = [0u8; 2];
    reader.read_exact(&mut header).map_err(|e| e.to_string())?;
    if header[0] & 0x80 == 0 || header[1] & 0x80 == 0 {
        return Err("client frame is not final and masked".into());
    }
    let mut length = u64::from(header[1] & 0x7f);
    if length == 126 {
        let mut encoded = [0u8; 2];
        reader.read_exact(&mut encoded).map_err(|e| e.to_string())?;
        length = u64::from(u16::from_be_bytes(encoded));
    } else if length == 127 {
        let mut encoded = [0u8; 8];
        reader.read_exact(&mut encoded).map_err(|e| e.to_string())?;
        length = u64::from_be_bytes(encoded);
    }
    let mut mask = [0u8; 4];
    reader.read_exact(&mut mask).map_err(|e| e.to_string())?;
    let mut payload = vec![0u8; length as usize];
    reader.read_exact(&mut payload).map_err(|e| e.to_string())?;
    for (i, b) in payload.iter_mut().enumerate() {
        *b ^= mask[i % 4];
    }
    Ok((payload, header[0] & 0xf))
}

fn write_server_frame(writer: &mut impl Write, opcode: u8, payload: &[u8]) -> std::io::Result<()> {
    let mut header = vec![0x80 | opcode];
    if payload.len() < 126 {
        header.push(payload.len() as u8);
    } else {
        header.extend_from_slice(&[126, (payload.len() >> 8) as u8, payload.len() as u8]);
    }
    writer.write_all(&header)?;
    writer.write_all(payload)
}

fn write_server_json(writer: &mut impl Write, value: &Value) -> std::io::Result<()> {
    write_server_frame(writer, OP_TEXT, value.to_string().as_bytes())
}

#[test]
fn test_dial_unix_websocket_handshake_masking_and_ping() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("app-server.sock");
    let listener = UnixListener::bind(&path).unwrap();
    let (done_tx, done_rx) = mpsc::channel::<Result<(), String>>();
    thread::spawn(move || {
        let result = (|| -> Result<(), String> {
            let (mut conn, _) = listener.accept().map_err(|e| e.to_string())?;
            let mut reader = BufReader::new(conn.try_clone().map_err(|e| e.to_string())?);
            let mut request_line = String::new();
            reader
                .read_line(&mut request_line)
                .map_err(|e| e.to_string())?;
            let mut headers = Vec::new();
            loop {
                let mut line = String::new();
                reader.read_line(&mut line).map_err(|e| e.to_string())?;
                let line = line.trim_end_matches(['\r', '\n']).to_owned();
                if line.is_empty() {
                    break;
                }
                let (k, v) = line.split_once(':').ok_or("bad header")?;
                headers.push((k.to_owned(), v.trim().to_owned()));
            }
            let get = |name: &str| {
                headers
                    .iter()
                    .find(|(k, _)| k.eq_ignore_ascii_case(name))
                    .map(|(_, v)| v.clone())
                    .unwrap_or_default()
            };
            if request_line != "GET / HTTP/1.1\r\n"
                || !websocket::header_has_token(&headers, "Connection", "upgrade")
                || get("Sec-WebSocket-Version") != "13"
            {
                return Err(format!("bad upgrade request: {request_line:?} {headers:?}"));
            }
            let accept = Sha1::digest(format!(
                "{}258EAFA5-E914-47DA-95CA-C5AB0DC85B11",
                get("Sec-WebSocket-Key")
            ));
            let response = format!(
                "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: {}\r\n\r\n",
                STANDARD.encode(accept)
            );
            conn.write_all(response.as_bytes())
                .map_err(|e| e.to_string())?;

            let (initialize, opcode) = read_client_frame(&mut reader)?;
            if opcode != OP_TEXT
                || !String::from_utf8_lossy(&initialize).contains(r#""method":"initialize""#)
            {
                return Err(format!(
                    "initialize frame: opcode={opcode} data={initialize:?}"
                ));
            }
            write_server_frame(&mut conn, OP_PING, b"hello").map_err(|e| e.to_string())?;
            let (pong, opcode) = read_client_frame(&mut reader)?;
            if opcode != OP_PONG || pong != b"hello" {
                return Err(format!("pong frame: opcode={opcode} data={pong:?}"));
            }
            let init_request = parse_request(&initialize);
            write_server_json(
                &mut conn,
                &json!({"jsonrpc": "2.0", "id": init_request.id, "result": {}}),
            )
            .map_err(|e| e.to_string())?;
            let (initialized, _) = read_client_frame(&mut reader)?;
            if !String::from_utf8_lossy(&initialized).contains(r#""method":"initialized""#) {
                return Err("missing initialized".into());
            }
            let (list, opcode) = read_client_frame(&mut reader)?;
            if opcode != OP_TEXT {
                return Err(format!("list frame: opcode={opcode}"));
            }
            let list_request = parse_request(&list);
            let result = json!({"data": [{"id": "ws-thread", "name": "Socket agent", "cwd": "/srv/socket", "status": {"type": "active"}}], "nextCursor": null});
            write_server_json(
                &mut conn,
                &json!({"jsonrpc": "2.0", "id": list_request.id, "result": result}),
            )
            .map_err(|e| e.to_string())?;
            Ok(())
        })();
        let _ = done_tx.send(result);
    });
    let client = dial_unix(&path.to_string_lossy(), deadline(3)).unwrap();
    let threads = client.thread_list(deadline(3)).unwrap();
    assert_eq!(threads.len(), 1, "{threads:?}");
    assert_eq!(threads[0].id, "ws-thread");
    assert_eq!(threads[0].status.kind, "active");
    done_rx
        .recv_timeout(Duration::from_secs(3))
        .unwrap()
        .unwrap();
    client.close();
}

fn ws_pair() -> (websocket::WsReader, websocket::WsWriter, UnixStream) {
    let (client, server) = UnixStream::pair().unwrap();
    let reader = BufReader::new(client.try_clone().unwrap());
    let (reader, writer) = websocket::from_stream(client, reader);
    (reader, writer, server)
}

#[test]
fn test_websocket_extended_length_and_fragment_rejection() {
    // masked extended length
    {
        let (_reader, writer, server) = ws_pair();
        let payload = "x".repeat(300).into_bytes();
        let sent = payload.clone();
        let handle = thread::spawn(move || writer.write_message(&sent).map(|()| writer));
        let (got, opcode) = read_client_frame(&mut BufReader::new(server)).unwrap();
        assert_eq!(opcode, OP_TEXT);
        assert_eq!(got, payload);
        let writer = handle.join().unwrap().unwrap();
        writer.close();
    }
    // fragmented server frame
    {
        let (mut reader, _writer, mut server) = ws_pair();
        server.write_all(&[OP_TEXT, 2, b'o', b'k']).unwrap();
        drop(server);
        let err = reader.read_message().unwrap_err();
        assert!(err.to_string().contains("fragmented"), "{err}");
    }
    // close
    {
        let (mut reader, writer, server) = ws_pair();
        let mut server_writer = server.try_clone().unwrap();
        let handle = thread::spawn(move || -> Result<(), String> {
            write_server_frame(&mut server_writer, OP_CLOSE, &[]).map_err(|e| e.to_string())?;
            let (_, opcode) = read_client_frame(&mut BufReader::new(server))?;
            if opcode != OP_CLOSE {
                return Err(format!("opcode={opcode}"));
            }
            Ok(())
        });
        let err = reader.read_message().unwrap_err();
        assert_eq!(err, Error::Eof);
        handle.join().unwrap().unwrap();
        writer.close();
    }
}

#[test]
fn test_thread_read_does_not_resume_or_generate() {
    let ((client_out, mut server_out), (server_in, client_in)) = pipes();
    let (done_tx, done_rx) = mpsc::channel::<Result<(), String>>();
    thread::spawn(move || {
        let result = (|| -> Result<(), String> {
            let mut lines = BufReader::new(server_in).split(b'\n');
            for method in ["initialize", "initialized", "thread/read"] {
                let line = lines
                    .next()
                    .ok_or(format!("missing {method}"))?
                    .map_err(|e| e.to_string())?;
                let request = parse_request(&line);
                if request.method != method {
                    return Err(format!(
                        "unexpected method {}, expected {method}",
                        request.method
                    ));
                }
                if method == "initialized" {
                    continue;
                }
                let mut result = json!({});
                if method == "thread/read" {
                    let params = params_value(&request);
                    if params["threadId"] != "thread-a" || params["includeTurns"] != false {
                        return Err("wrong read params".into());
                    }
                    result["thread"] = json!({"id": "thread-a", "cwd": "/work", "model": "gpt-6-astra", "reasoningEffort": "medium", "status": {"type": "active"}});
                }
                write_line(
                    &mut server_out,
                    &json!({"id": request.id, "result": result}),
                )
                .map_err(|e| e.to_string())?;
            }
            Ok(())
        })();
        let _ = done_tx.send(result);
    });
    let client = connect_stdio(client_out, client_in, deadline(1)).unwrap();
    let thread = client.thread_read(deadline(1), "thread-a").unwrap();
    assert_eq!(thread.id, "thread-a");
    assert_eq!(thread.status.kind, "active");
    assert_eq!(thread.reasoning_effort, "medium");
    done_rx
        .recv_timeout(Duration::from_secs(1))
        .unwrap()
        .unwrap();
}

/// Not in Go: pending calls fail with the close error when the server goes away,
/// and a deadline returns the context error without leaking the pending id.
#[test]
fn test_close_and_deadline() {
    let ((client_out, mut server_out), (server_in, client_in)) = pipes();
    thread::spawn(move || {
        let mut lines = BufReader::new(server_in).split(b'\n');
        let Some(Ok(line)) = lines.next() else { return };
        let request = parse_request(&line);
        let _ = write_line(&mut server_out, &json!({"id": request.id, "result": {}}));
        // Swallow the rest; never answer.
        for _ in lines {}
        drop(server_out);
    });
    let client = connect_stdio(client_out, client_in, deadline(3)).unwrap();
    let start = Instant::now();
    let err = client
        .thread_read(Some(Instant::now() + Duration::from_millis(50)), "x")
        .unwrap_err();
    assert_eq!(err, Error::DeadlineExceeded);
    assert!(start.elapsed() < Duration::from_secs(2));
    client.close();
    assert_eq!(client.thread_read(None, "x").unwrap_err(), Error::Closed);
}
