//! Port of internal/codexrpc/client.go: reads thread state from a Codex
//! app-server over JSON-RPC.
//!
//! Same design as Go: one reader thread (the Go `readLoop` goroutine) routes
//! responses to waiting callers by request id and forwards notifications;
//! callers block on a channel with an optional deadline (Go's context).

mod websocket;

#[cfg(test)]
mod tests;

use std::collections::{HashMap, HashSet};
use std::io::{BufRead, BufReader, Read, Write};
use std::sync::mpsc::{self, Receiver, RecvTimeoutError, SyncSender};
use std::sync::{Arc, Mutex, MutexGuard};
use std::thread;
use std::time::Instant;

use serde::Serialize;
use serde_json::{Value, json};

use crate::go_struct;
use crate::godecode::{GoDecode, RawJson, unmarshal};

use websocket::dial_websocket_unix;

/// Largest message (frame or stdio line) the client accepts.
pub const MAX_MESSAGE_SIZE: usize = 32 << 20;

/// Errors, with Go's message texts.
#[derive(Debug, Clone, PartialEq, thiserror::Error)]
pub enum Error {
    /// `ErrClosed`.
    #[error("codex app-server connection is closed")]
    Closed,
    /// `context.DeadlineExceeded`.
    #[error("context deadline exceeded")]
    DeadlineExceeded,
    /// End of stream (Go's `io.EOF`); the client reports it as [`Error::Closed`].
    #[error("EOF")]
    Eof,
    #[error("{0}")]
    Rpc(RpcError),
    #[error("send {method} request: {source}")]
    Send { method: String, source: Box<Error> },
    #[error("decode {method} response: {detail}")]
    Decode { method: String, detail: String },
    #[error("initialize codex app-server: {0}")]
    Initialize(Box<Error>),
    #[error("connect to codex socket {path}: {detail}")]
    Connect { path: String, detail: String },
    #[error("upgrade codex socket {path}: {source}")]
    Upgrade { path: String, source: Box<Error> },
    /// Protocol or transport failure; the text is Go's.
    #[error("{0}")]
    Other(String),
}

impl Error {
    pub(crate) fn io(err: &std::io::Error) -> Error {
        match err.kind() {
            std::io::ErrorKind::UnexpectedEof => Error::Eof,
            std::io::ErrorKind::TimedOut | std::io::ErrorKind::WouldBlock => {
                Error::Other("i/o timeout".to_owned())
            }
            _ => Error::Other(err.to_string()),
        }
    }
}

/// An app-server notification not associated with a request id.
#[derive(Debug, Clone, PartialEq)]
pub struct Notification {
    pub method: String,
    pub params: RawJson,
}

/// An error returned by the app-server.
#[derive(Debug, Clone, Default, PartialEq, thiserror::Error)]
#[error("codex app-server error {code}: {message}")]
pub struct RpcError {
    pub code: i64,
    pub message: String,
    pub data: RawJson,
}
go_struct!(RpcError {
    code: "code",
    message: "message",
    data: "data"
});

/// The read-only part of a thread/list entry used by blueprint.
#[derive(Debug, Clone, Default, PartialEq, Serialize)]
pub struct Thread {
    pub id: String,
    pub name: String,
    #[serde(rename = "agentNickname")]
    pub agent_nickname: String,
    pub path: String,
    pub model: String,
    #[serde(rename = "reasoningEffort")]
    pub reasoning_effort: String,
    pub cwd: String,
    pub status: ThreadStatus,
    #[serde(rename = "tokenUsage", skip_serializing_if = "Option::is_none")]
    pub token_usage: Option<ThreadTokenUsage>,
}
go_struct!(Thread {
    id: "id",
    name: "name",
    agent_nickname: "agentNickname",
    path: "path",
    model: "model",
    reasoning_effort: "reasoningEffort",
    cwd: "cwd",
    status: "status",
    token_usage: "tokenUsage",
});

#[derive(Debug, Clone, Default, PartialEq, Serialize)]
pub struct ThreadStatus {
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(rename = "activeFlags", skip_serializing_if = "Vec::is_empty")]
    pub active_flags: Vec<String>,
}
go_struct!(ThreadStatus {
    kind: "type",
    active_flags: "activeFlags"
});

#[derive(Debug, Clone, Default, PartialEq, Serialize)]
pub struct ThreadTokenUsage {
    pub total: TokenUsage,
    pub last: TokenUsage,
    #[serde(rename = "modelContextWindow")]
    pub model_context_window: Option<i64>,
}
go_struct!(ThreadTokenUsage {
    total: "total",
    last: "last",
    model_context_window: "modelContextWindow"
});

#[derive(Debug, Clone, Default, PartialEq, Serialize)]
pub struct TokenUsage {
    #[serde(rename = "inputTokens")]
    pub input_tokens: i64,
    #[serde(rename = "cachedInputTokens")]
    pub cached_input_tokens: i64,
    #[serde(rename = "outputTokens")]
    pub output_tokens: i64,
    #[serde(rename = "reasoningOutputTokens")]
    pub reasoning_output_tokens: i64,
    #[serde(rename = "totalTokens")]
    pub total_tokens: i64,
}
go_struct!(TokenUsage {
    input_tokens: "inputTokens",
    cached_input_tokens: "cachedInputTokens",
    output_tokens: "outputTokens",
    reasoning_output_tokens: "reasoningOutputTokens",
    total_tokens: "totalTokens",
});

/// Reading half of a message transport, owned by the reader thread.
pub(crate) trait MessageReader: Send {
    fn read_message(&mut self) -> Result<Vec<u8>, Error>;
}

/// Writing half of a message transport, shared by callers.
pub(crate) trait MessageWriter: Send + Sync {
    fn write_message(&self, data: &[u8]) -> Result<(), Error>;
    fn close(&self);
}

type CallResult = Result<RawJson, Error>;

struct ClientState {
    next_id: u64,
    pending: HashMap<u64, SyncSender<CallResult>>,
    closed: bool,
    close_err: Option<Error>,
    statuses: HashMap<String, ThreadStatus>,
    token_usage: HashMap<String, ThreadTokenUsage>,
}

struct Inner {
    writer: Box<dyn MessageWriter>,
    state: Mutex<ClientState>,
}

impl Inner {
    fn lock(&self) -> MutexGuard<'_, ClientState> {
        self.state
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }
}

/// A JSON-RPC client connected to one app-server.
pub struct Client {
    inner: Arc<Inner>,
    notifications: Mutex<Receiver<Notification>>,
}

/// Connects to an app-server WebSocket on a Unix domain socket and completes
/// the initialize handshake. `deadline` plays the role of Go's context.
pub fn dial_unix(path: &str, deadline: Option<Instant>) -> Result<Client, Error> {
    let (reader, writer) = dial_websocket_unix(path, deadline)?;
    connect(Box::new(reader), Box::new(writer), deadline)
}

/// Attaches to the stdout and stdin pipes of a running app-server process and
/// completes the initialize handshake.
///
/// Closing the client closes `stdin`; `stdout` stays with the reader thread,
/// which ends when the app-server closes its side.
pub fn connect_stdio<R, W>(stdout: R, stdin: W, deadline: Option<Instant>) -> Result<Client, Error>
where
    R: Read + Send + 'static,
    W: Write + Send + 'static,
{
    let reader = StdioReader {
        reader: BufReader::with_capacity(64 << 10, stdout),
    };
    let writer = StdioWriter {
        writer: Mutex::new(Some(Box::new(stdin))),
    };
    connect(Box::new(reader), Box::new(writer), deadline)
}

fn connect(
    reader: Box<dyn MessageReader>,
    writer: Box<dyn MessageWriter>,
    deadline: Option<Instant>,
) -> Result<Client, Error> {
    let (notify_tx, notify_rx) = mpsc::sync_channel(128);
    let inner = Arc::new(Inner {
        writer,
        state: Mutex::new(ClientState {
            next_id: 0,
            pending: HashMap::new(),
            closed: false,
            close_err: None,
            statuses: HashMap::new(),
            token_usage: HashMap::new(),
        }),
    });
    let loop_inner = Arc::clone(&inner);
    thread::Builder::new()
        .name("codexrpc-read".to_owned())
        .spawn(move || read_loop(&loop_inner, reader, &notify_tx))
        .map_err(|err| Error::io(&err))?;
    let client = Client {
        inner,
        notifications: Mutex::new(notify_rx),
    };
    let mut initialized = RawJson::default();
    let params =
        json!({"clientInfo": {"name": "blueprint", "title": "Blueprint bp CLI", "version": "dev"}});
    if let Err(err) = client.call(deadline, "initialize", params, Some(&mut initialized)) {
        client.close();
        return Err(Error::Initialize(Box::new(err)));
    }
    if let Err(err) = client
        .inner
        .writer
        .write_message(br#"{"method":"initialized"}"#)
    {
        client.close();
        return Err(err);
    }
    Ok(client)
}

#[derive(Default)]
struct Page {
    data: Vec<Thread>,
    next_cursor: Option<String>,
}
go_struct!(Page {
    data: "data",
    next_cursor: "nextCursor"
});

#[derive(Default)]
struct ReadResult {
    thread: Thread,
}
go_struct!(ReadResult { thread: "thread" });

#[derive(Serialize)]
struct Request<'a> {
    jsonrpc: &'static str,
    id: u64,
    method: &'a str,
    params: Value,
}

impl Client {
    /// Server notifications received while the client is open. The channel
    /// keeps the newest 128; it disconnects when the connection ends.
    pub fn notifications(&self) -> MutexGuard<'_, Receiver<Notification>> {
        self.notifications
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }

    /// All non-archived interactive threads, following pagination.
    pub fn thread_list(&self, deadline: Option<Instant>) -> Result<Vec<Thread>, Error> {
        let mut params = serde_json::Map::new();
        params.insert("limit".to_owned(), json!(100));
        let mut threads = Vec::new();
        let mut seen = HashSet::new();
        loop {
            let mut response = Page::default();
            self.call(
                deadline,
                "thread/list",
                Value::Object(params.clone()),
                Some(&mut response),
            )?;
            threads.append(&mut response.data);
            let cursor = match response.next_cursor {
                Some(cursor) if !cursor.is_empty() => cursor,
                _ => return Ok(self.with_notification_state(threads)),
            };
            if !seen.insert(cursor.clone()) {
                return Err(Error::Other(
                    "thread/list returned repeated cursor".to_owned(),
                ));
            }
            params.insert("cursor".to_owned(), Value::String(cursor));
        }
    }

    /// Observes a thread without resuming, subscribing, or creating a writer.
    pub fn thread_read(&self, deadline: Option<Instant>, id: &str) -> Result<Thread, Error> {
        let mut result = ReadResult::default();
        let outcome = self.call(
            deadline,
            "thread/read",
            json!({"threadId": id, "includeTurns": false}),
            Some(&mut result),
        );
        outcome.map(|()| result.thread)
    }

    /// Sends one request and waits for its response, decoding `result` into
    /// `target` (Go's `call`).
    pub(crate) fn call<T: GoDecode>(
        &self,
        deadline: Option<Instant>,
        method: &str,
        params: Value,
        target: Option<&mut T>,
    ) -> Result<(), Error> {
        if deadline.is_some_and(|d| Instant::now() >= d) {
            return Err(Error::DeadlineExceeded);
        }
        let (id, wait) = {
            let mut state = self.inner.lock();
            if state.closed {
                return Err(state.close_err.clone().unwrap_or(Error::Closed));
            }
            state.next_id += 1;
            let id = state.next_id;
            let (tx, rx) = mpsc::sync_channel(1);
            state.pending.insert(id, tx);
            (id, rx)
        };
        let request = serde_json::to_vec(&Request {
            jsonrpc: "2.0",
            id,
            method,
            params,
        })
        .map_err(|err| Error::Other(err.to_string()))
        .and_then(|request| self.inner.writer.write_message(&request));
        if let Err(err) = request {
            self.remove_pending(id);
            return Err(Error::Send {
                method: method.to_owned(),
                source: Box::new(err),
            });
        }
        let response = match deadline {
            None => wait.recv().unwrap_or(Err(Error::Closed)),
            Some(deadline) => {
                match wait.recv_timeout(deadline.saturating_duration_since(Instant::now())) {
                    Ok(response) => response,
                    Err(RecvTimeoutError::Timeout) => {
                        self.remove_pending(id);
                        return Err(Error::DeadlineExceeded);
                    }
                    Err(RecvTimeoutError::Disconnected) => Err(Error::Closed),
                }
            }
        };
        let result = response?;
        let Some(target) = target else {
            return Ok(());
        };
        unmarshal(result.as_bytes(), target).map_err(|detail| Error::Decode {
            method: method.to_owned(),
            detail,
        })
    }

    fn remove_pending(&self, id: u64) {
        self.inner.lock().pending.remove(&id);
    }

    fn with_notification_state(&self, mut threads: Vec<Thread>) -> Vec<Thread> {
        let state = self.inner.lock();
        for thread in &mut threads {
            if let Some(status) = state.statuses.get(&thread.id) {
                thread.status = status.clone();
            }
            if let Some(usage) = state.token_usage.get(&thread.id) {
                thread.token_usage = Some(usage.clone());
            }
        }
        threads
    }

    /// Closes the connection; pending calls fail with [`Error::Closed`].
    pub fn close(&self) {
        shutdown(&self.inner, Error::Closed);
    }
}

impl Drop for Client {
    fn drop(&mut self) {
        self.close();
    }
}

#[derive(Default)]
struct WireMessage {
    jsonrpc: String,
    id: RawJson,
    method: String,
    params: RawJson,
    result: RawJson,
    error: Option<RpcError>,
}
go_struct!(WireMessage {
    jsonrpc: "jsonrpc",
    id: "id",
    method: "method",
    params: "params",
    result: "result",
    error: "error",
});

fn read_loop(
    inner: &Inner,
    mut reader: Box<dyn MessageReader>,
    notifications: &SyncSender<Notification>,
) {
    loop {
        let data = match reader.read_message() {
            Ok(data) => data,
            Err(err) => {
                shutdown(inner, err);
                return;
            }
        };
        let mut message = WireMessage::default();
        // The app-server omits the "jsonrpc" field on the wire (verified
        // against 0.145.0), so only reject messages that carry a wrong version.
        if unmarshal(&data, &mut message).is_err()
            || (!message.jsonrpc.is_empty() && message.jsonrpc != "2.0")
        {
            shutdown(inner, Error::Other("invalid JSON-RPC message".to_owned()));
            return;
        }
        if !message.id.is_empty() && message.id.as_bytes() != b"null" {
            if !message.method.is_empty() {
                // Server requests are not responses to our ids.
                continue;
            }
            let mut id = 0u64;
            if unmarshal(message.id.as_bytes(), &mut id).is_err() {
                // Not one of our ids: a server-to-client request (approvals and
                // the like). A read-only client ignores those.
                continue;
            }
            let wait = inner.lock().pending.remove(&id);
            if let Some(wait) = wait {
                let _ = wait.try_send(match message.error {
                    Some(err) => Err(Error::Rpc(err)),
                    None => Ok(message.result),
                });
            }
            continue;
        }
        if !message.method.is_empty() {
            record_notification(inner, &message.method, message.params.as_bytes());
            let _ = notifications.try_send(Notification {
                method: message.method,
                params: message.params,
            });
        }
    }
}

#[derive(Default)]
struct StatusUpdate {
    thread_id: String,
    status: ThreadStatus,
}
go_struct!(StatusUpdate {
    thread_id: "threadId",
    status: "status"
});

#[derive(Default)]
struct UsageUpdate {
    thread_id: String,
    token_usage: ThreadTokenUsage,
}
go_struct!(UsageUpdate {
    thread_id: "threadId",
    token_usage: "tokenUsage"
});

fn record_notification(inner: &Inner, method: &str, params: &[u8]) {
    match method {
        "thread/status/changed" => {
            let mut update = StatusUpdate::default();
            if unmarshal(params, &mut update).is_ok() && !update.thread_id.is_empty() {
                inner
                    .lock()
                    .statuses
                    .insert(update.thread_id, update.status);
            }
        }
        "thread/tokenUsage/updated" => {
            let mut update = UsageUpdate::default();
            if unmarshal(params, &mut update).is_ok() && !update.thread_id.is_empty() {
                inner
                    .lock()
                    .token_usage
                    .insert(update.thread_id, update.token_usage);
            }
        }
        _ => {}
    }
}

fn shutdown(inner: &Inner, err: Error) {
    let pending = {
        let mut state = inner.lock();
        if state.closed {
            return;
        }
        state.closed = true;
        let err = if err == Error::Eof {
            Error::Closed
        } else {
            err
        };
        state.close_err = Some(err);
        std::mem::take(&mut state.pending)
    };
    inner.writer.close();
    let close_err = inner.lock().close_err.clone().unwrap_or(Error::Closed);
    for wait in pending.into_values() {
        let _ = wait.try_send(Err(close_err.clone()));
    }
}

/// Newline-delimited JSON over a pipe (bufio.Scanner with a 32 MiB limit).
struct StdioReader<R: Read> {
    reader: BufReader<R>,
}

impl<R: Read + Send> MessageReader for StdioReader<R> {
    fn read_message(&mut self) -> Result<Vec<u8>, Error> {
        let mut line = Vec::new();
        let n = (&mut self.reader)
            .take(MAX_MESSAGE_SIZE as u64 + 1)
            .read_until(b'\n', &mut line)
            .map_err(|err| Error::io(&err))?;
        if n == 0 {
            return Err(Error::Eof);
        }
        if line.last() == Some(&b'\n') {
            line.pop();
        }
        if line.len() >= MAX_MESSAGE_SIZE {
            return Err(Error::Other("bufio.Scanner: token too long".to_owned()));
        }
        if line.last() == Some(&b'\r') {
            line.pop();
        }
        Ok(line)
    }
}

struct StdioWriter {
    writer: Mutex<Option<Box<dyn Write + Send>>>,
}

impl MessageWriter for StdioWriter {
    fn write_message(&self, data: &[u8]) -> Result<(), Error> {
        let mut writer = self
            .writer
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        let Some(writer) = writer.as_mut() else {
            return Err(Error::Other("write |1: file already closed".to_owned()));
        };
        writer.write_all(data).map_err(|err| Error::io(&err))?;
        writer.write_all(b"\n").map_err(|err| Error::io(&err))?;
        writer.flush().map_err(|err| Error::io(&err))
    }

    fn close(&self) {
        let mut writer = self
            .writer
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        if let Some(mut writer) = writer.take() {
            let _ = writer.flush();
        }
    }
}
