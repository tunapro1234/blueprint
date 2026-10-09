//! Port of internal/codexrpc/websocket.go: a minimal client WebSocket over a
//! Unix domain socket.
//!
//! Hand-rolled like the Go original instead of `tungstenite`: the Go client
//! rejects fragmented frames, non-minimal lengths, extensions and frames over
//! 32 MiB, and reads on one goroutine while callers write on others.
//! tungstenite reassembles fragments, accepts non-minimal lengths and cannot
//! split a blocking stream into independent read and write halves, so using it
//! would change both the accepted wire format and the threading design.

use std::fs::File;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::Shutdown;
use std::os::unix::net::UnixStream;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use base64::Engine as _;
use base64::engine::general_purpose::STANDARD;
use sha1::{Digest, Sha1};

use super::{Error, MAX_MESSAGE_SIZE, MessageReader, MessageWriter};

pub(crate) const OP_TEXT: u8 = 0x1;
pub(crate) const OP_CLOSE: u8 = 0x8;
pub(crate) const OP_PING: u8 = 0x9;
pub(crate) const OP_PONG: u8 = 0xa;

const ACCEPT_GUID: &str = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";

/// Shared writing state (Go's `mu`, `closeSent`, `closed`).
pub(crate) struct WsWriter {
    inner: Arc<Mutex<WriterState>>,
}

struct WriterState {
    conn: UnixStream,
    close_sent: bool,
    closed: bool,
}

/// The reading half; it writes pongs and close echoes through the writer.
pub(crate) struct WsReader {
    reader: BufReader<UnixStream>,
    writer: WsWriter,
}

fn random_bytes<const N: usize>() -> Result<[u8; N], Error> {
    let mut bytes = [0u8; N];
    File::open("/dev/urandom")
        .and_then(|mut f| f.read_exact(&mut bytes))
        .map_err(|err| Error::io(&err))?;
    Ok(bytes)
}

/// Connects and upgrades; the deadline bounds the handshake only.
pub(crate) fn dial_websocket_unix(
    path: &str,
    deadline: Option<Instant>,
) -> Result<(WsReader, WsWriter), Error> {
    let conn = UnixStream::connect(path).map_err(|err| Error::Connect {
        path: path.to_owned(),
        detail: go_dial_error(path, &err),
    })?;
    let set_deadline = |timeout: Option<Duration>| {
        let _ = conn.set_read_timeout(timeout);
        let _ = conn.set_write_timeout(timeout);
    };
    if let Some(deadline) = deadline {
        // A zero timeout is rejected by std; the smallest positive one is
        // already in the past for practical purposes.
        set_deadline(Some(
            deadline
                .saturating_duration_since(Instant::now())
                .max(Duration::from_nanos(1)),
        ));
    }
    match upgrade_websocket(&conn) {
        Ok(pair) => {
            set_deadline(None);
            Ok(pair)
        }
        Err(err) => {
            let _ = conn.shutdown(Shutdown::Both);
            Err(Error::Upgrade {
                path: path.to_owned(),
                source: Box::new(err),
            })
        }
    }
}

fn go_dial_error(path: &str, err: &std::io::Error) -> String {
    let text = match err.kind() {
        std::io::ErrorKind::NotFound => "no such file or directory".to_owned(),
        std::io::ErrorKind::ConnectionRefused => "connection refused".to_owned(),
        std::io::ErrorKind::PermissionDenied => "permission denied".to_owned(),
        _ => err.to_string(),
    };
    format!("dial unix {path}: connect: {text}")
}

/// Builds a reader/writer pair over an already upgraded stream.
pub(crate) fn from_stream(conn: UnixStream, reader: BufReader<UnixStream>) -> (WsReader, WsWriter) {
    let writer = WsWriter {
        inner: Arc::new(Mutex::new(WriterState {
            conn,
            close_sent: false,
            closed: false,
        })),
    };
    let reader = WsReader {
        reader,
        writer: WsWriter {
            inner: Arc::clone(&writer.inner),
        },
    };
    (reader, writer)
}

fn upgrade_websocket(conn: &UnixStream) -> Result<(WsReader, WsWriter), Error> {
    let key = STANDARD.encode(random_bytes::<16>()?);
    // The bytes Go's http.Request.Write produces for this request.
    let request = format!(
        "GET / HTTP/1.1\r\nHost: localhost\r\nUser-Agent: Go-http-client/1.1\r\nConnection: Upgrade\r\nSec-Websocket-Key: {key}\r\nSec-Websocket-Version: 13\r\nUpgrade: websocket\r\n\r\n"
    );
    let mut writer = conn.try_clone().map_err(|err| Error::io(&err))?;
    write_all(&mut writer, request.as_bytes())?;

    let mut reader = BufReader::new(conn.try_clone().map_err(|err| Error::io(&err))?);
    let response = read_response(&mut reader)?;
    if response.code != 101 {
        return Err(Error::Other(format!(
            "unexpected HTTP status {}",
            response.status
        )));
    }
    if !header_has_token(&response.headers, "Connection", "upgrade")
        || !header_has_token(&response.headers, "Upgrade", "websocket")
    {
        return Err(Error::Other("invalid WebSocket upgrade headers".to_owned()));
    }
    if !header_get(&response.headers, "Sec-WebSocket-Extensions").is_empty() {
        return Err(Error::Other(
            "WebSocket extensions are not supported".to_owned(),
        ));
    }
    let want = Sha1::digest(format!("{key}{ACCEPT_GUID}").as_bytes());
    if header_get(&response.headers, "Sec-WebSocket-Accept") != STANDARD.encode(want) {
        return Err(Error::Other(
            "invalid Sec-WebSocket-Accept header".to_owned(),
        ));
    }
    Ok(from_stream(writer, reader))
}

struct Response {
    code: u16,
    status: String,
    headers: Vec<(String, String)>,
}

fn read_line(reader: &mut BufReader<UnixStream>) -> Result<String, Error> {
    let mut line = Vec::new();
    let n = reader
        .read_until(b'\n', &mut line)
        .map_err(|err| Error::io(&err))?;
    if n == 0 || line.last() != Some(&b'\n') {
        return Err(Error::Other("unexpected EOF".to_owned()));
    }
    line.pop();
    if line.last() == Some(&b'\r') {
        line.pop();
    }
    Ok(String::from_utf8_lossy(&line).into_owned())
}

/// The parts of `http.ReadResponse` the upgrade looks at.
fn read_response(reader: &mut BufReader<UnixStream>) -> Result<Response, Error> {
    let line = read_line(reader)?;
    let Some((proto, status)) = line.split_once(' ') else {
        return Err(Error::Other(format!("malformed HTTP response {line:?}")));
    };
    let status = status.trim_start_matches(' ').to_owned();
    let code_text = status.split(' ').next().unwrap_or_default();
    if code_text.len() != 3 {
        return Err(Error::Other(format!(
            "malformed HTTP status code {code_text:?}"
        )));
    }
    let code: u16 = code_text
        .parse()
        .map_err(|_| Error::Other(format!("malformed HTTP status code {code_text:?}")))?;
    let version_ok = proto
        .strip_prefix("HTTP/")
        .and_then(|v| v.split_once('.'))
        .is_some_and(|(major, minor)| {
            !major.is_empty()
                && !minor.is_empty()
                && major.bytes().all(|c| c.is_ascii_digit())
                && minor.bytes().all(|c| c.is_ascii_digit())
        });
    if !version_ok {
        return Err(Error::Other(format!("malformed HTTP version {proto:?}")));
    }
    let mut headers = Vec::new();
    loop {
        let line = read_line(reader)?;
        if line.is_empty() {
            break;
        }
        let Some((key, value)) = line.split_once(':') else {
            return Err(Error::Other(format!("malformed MIME header line: {line}")));
        };
        if key.is_empty() || key.contains(' ') || key.contains('\t') {
            return Err(Error::Other(format!("malformed MIME header line: {line}")));
        }
        headers.push((key.to_owned(), value.trim_matches([' ', '\t']).to_owned()));
    }
    Ok(Response {
        code,
        status,
        headers,
    })
}

fn header_values<'a>(
    headers: &'a [(String, String)],
    name: &'a str,
) -> impl Iterator<Item = &'a str> {
    headers
        .iter()
        .filter(move |(key, _)| key.eq_ignore_ascii_case(name))
        .map(|(_, value)| value.as_str())
}

fn header_get<'a>(headers: &'a [(String, String)], name: &'a str) -> &'a str {
    header_values(headers, name).next().unwrap_or_default()
}

pub(crate) fn header_has_token(headers: &[(String, String)], name: &str, want: &str) -> bool {
    header_values(headers, name).any(|value| {
        value
            .split(',')
            .any(|token| token.trim().eq_ignore_ascii_case(want))
    })
}

fn write_all(writer: &mut UnixStream, data: &[u8]) -> Result<(), Error> {
    writer.write_all(data).map_err(|err| Error::io(&err))
}

fn read_full(reader: &mut BufReader<UnixStream>, buf: &mut [u8]) -> Result<(), Error> {
    reader.read_exact(buf).map_err(|err| Error::io(&err))
}

impl WsReader {
    fn read_frame(&mut self) -> Result<(u8, Vec<u8>), Error> {
        let mut header = [0u8; 2];
        read_full(&mut self.reader, &mut header)?;
        if header[0] & 0x70 != 0 {
            return Err(Error::Other(
                "WebSocket extensions are not supported".to_owned(),
            ));
        }
        if header[0] & 0x80 == 0 {
            return Err(Error::Other(
                "fragmented WebSocket frames are not supported".to_owned(),
            ));
        }
        let opcode = header[0] & 0x0f;
        if header[1] & 0x80 != 0 {
            return Err(Error::Other(
                "server WebSocket frames must not be masked".to_owned(),
            ));
        }
        let mut length = u64::from(header[1] & 0x7f);
        match length {
            126 => {
                let mut extended = [0u8; 2];
                read_full(&mut self.reader, &mut extended)?;
                length = u64::from(u16::from_be_bytes(extended));
                if length < 126 {
                    return Err(Error::Other(
                        "non-minimal WebSocket frame length".to_owned(),
                    ));
                }
            }
            127 => {
                let mut extended = [0u8; 8];
                read_full(&mut self.reader, &mut extended)?;
                length = u64::from_be_bytes(extended);
                if length < 65536 || length >> 63 != 0 {
                    return Err(Error::Other("invalid WebSocket frame length".to_owned()));
                }
            }
            _ => {}
        }
        if opcode >= OP_CLOSE && length > 125 {
            return Err(Error::Other(
                "WebSocket control frame is too large".to_owned(),
            ));
        }
        if length > MAX_MESSAGE_SIZE as u64 {
            return Err(Error::Other(format!(
                "WebSocket frame exceeds {MAX_MESSAGE_SIZE} bytes"
            )));
        }
        let mut payload = vec![0u8; length as usize];
        read_full(&mut self.reader, &mut payload)?;
        Ok((opcode, payload))
    }
}

impl MessageReader for WsReader {
    fn read_message(&mut self) -> Result<Vec<u8>, Error> {
        loop {
            let (opcode, payload) = self.read_frame()?;
            match opcode {
                OP_TEXT => {
                    if std::str::from_utf8(&payload).is_err() {
                        return Err(Error::Other("WebSocket text frame is not UTF-8".to_owned()));
                    }
                    return Ok(payload);
                }
                OP_PING => self.writer.write_frame(OP_PONG, &payload)?,
                OP_PONG => {}
                OP_CLOSE => {
                    if payload.len() == 1 {
                        return Err(Error::Other("invalid WebSocket close frame".to_owned()));
                    }
                    let _ = self.writer.write_frame(OP_CLOSE, &payload);
                    return Err(Error::Eof);
                }
                _ => {
                    return Err(Error::Other(format!(
                        "unsupported WebSocket opcode {opcode}"
                    )));
                }
            }
        }
    }
}

impl WsWriter {
    fn lock(&self) -> std::sync::MutexGuard<'_, WriterState> {
        self.inner
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }

    pub(crate) fn write_frame(&self, opcode: u8, payload: &[u8]) -> Result<(), Error> {
        if payload.len() > MAX_MESSAGE_SIZE {
            return Err(Error::Other(format!(
                "WebSocket frame exceeds {MAX_MESSAGE_SIZE} bytes"
            )));
        }
        if opcode >= OP_CLOSE && payload.len() > 125 {
            return Err(Error::Other(
                "WebSocket control frame is too large".to_owned(),
            ));
        }
        let mut state = self.lock();
        if state.closed {
            return Err(Error::Closed);
        }
        write_frame_locked(&mut state.conn, opcode, payload)?;
        if opcode == OP_CLOSE {
            state.close_sent = true;
        }
        Ok(())
    }
}

fn write_frame_locked(conn: &mut UnixStream, opcode: u8, payload: &[u8]) -> Result<(), Error> {
    let length = payload.len();
    let mut header = vec![0x80 | opcode];
    if length < 126 {
        header.push(0x80 | length as u8);
    } else if length <= 65535 {
        header.extend_from_slice(&[0x80 | 126, (length >> 8) as u8, length as u8]);
    } else {
        header.extend_from_slice(&[
            0x80 | 127,
            0,
            0,
            0,
            0,
            (length >> 24) as u8,
            (length >> 16) as u8,
            (length >> 8) as u8,
            length as u8,
        ]);
    }
    let mask = random_bytes::<4>()?;
    header.extend_from_slice(&mask);
    let masked: Vec<u8> = payload
        .iter()
        .enumerate()
        .map(|(i, b)| b ^ mask[i % 4])
        .collect();
    write_all(conn, &header)?;
    write_all(conn, &masked)
}

impl MessageWriter for WsWriter {
    fn write_message(&self, data: &[u8]) -> Result<(), Error> {
        if std::str::from_utf8(data).is_err() {
            return Err(Error::Other("WebSocket text frame is not UTF-8".to_owned()));
        }
        self.write_frame(OP_TEXT, data)
    }

    fn close(&self) {
        let mut state = self.lock();
        let _ = state
            .conn
            .set_write_timeout(Some(Duration::from_millis(100)));
        if state.closed {
            return;
        }
        if !state.close_sent {
            let _ = write_frame_locked(&mut state.conn, OP_CLOSE, &[]);
        }
        state.closed = true;
        // Shutting the socket down also wakes the reader thread.
        let _ = state.conn.shutdown(Shutdown::Both);
    }
}
