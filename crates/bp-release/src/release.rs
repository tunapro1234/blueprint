//! Port of internal/release/release.go.
//!
//! Verifies immutable, signed native releases and replaces the running binary.

use crate::fsutil;
use crate::gojson;
use ed25519_dalek::pkcs8::DecodePublicKey;
use ed25519_dalek::{Signature, VerifyingKey};
use serde_json::Value;
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::io::{self, Read, Write as _};
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc;
use std::time::{Duration, Instant};

pub const BASE_URL: &str = "https://bp.tunapro.xyz";

const VERSION_TEXT: &str = include_str!("../../../internal/release/version.txt");

/// The embedded release signing key (SPKI PEM).
pub const PUBLIC_KEY_PEM: &[u8] = include_bytes!("../../../internal/release/release.pub");

/// The bp version this binary was built as.
pub fn version() -> &'static str {
    VERSION_TEXT.trim()
}

/// Size limits of the three kinds of release responses.
pub const LATEST_LIMIT: i64 = 128;
pub const MANIFEST_LIMIT: i64 = 64 << 10;
pub const SIGNATURE_LIMIT: i64 = 128;
pub const BINARY_LIMIT: i64 = 64 << 20;

const DEFAULT_STALL: Duration = Duration::from_secs(20);
const DEFAULT_BACKOFF: Duration = Duration::from_millis(250);

/// Boxed error from injected callbacks (HTTP client, validate/setup steps).
pub type BoxError = Box<dyn std::error::Error + Send + Sync>;

#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// A failed GET, already formatted like Go (`GET <url> after <n> bytes: …`).
    #[error("{0}")]
    Http(String),
    #[error("invalid release version")]
    InvalidVersion,
    #[error("release signature verification failed")]
    Signature,
    #[error("{0}")]
    ManifestJson(String),
    #[error("release version mismatch")]
    VersionMismatch,
    #[error("invalid release checksum entry")]
    InvalidChecksum,
    #[error("encoding/hex: invalid byte: {0}")]
    InvalidHex(String),
    #[error("release does not contain {0}")]
    MissingAsset(String),
    #[error("download {url} canceled after attempt {attempt}: {cause}")]
    DownloadCanceled {
        url: String,
        attempt: u32,
        cause: ContextError,
    },
    #[error("download {url} failed after {attempts} attempts: {last}")]
    DownloadFailed {
        url: String,
        attempts: u32,
        last: Box<Error>,
    },
    #[error("target is not a regular file")]
    NotRegular,
    #[error(transparent)]
    Io(#[from] io::Error),
    /// The candidate's validation step failed (returned unwrapped, as in Go).
    #[error(transparent)]
    Validate(BoxError),
    #[error("setup failed; previous binary restored: {0}")]
    SetupRestored(BoxError),
    #[error("setup failed: {setup}; restore failed: {restore}; backup: {backup}")]
    SetupRestoreFailed {
        setup: BoxError,
        restore: io::Error,
        backup: String,
    },
}

/// Go `context` errors.
#[derive(Debug, Clone, Copy, PartialEq, Eq, thiserror::Error)]
pub enum ContextError {
    #[error("context deadline exceeded")]
    DeadlineExceeded,
    #[error("context canceled")]
    Canceled,
}

/// A minimal stand-in for Go's `context.Context`: an optional deadline and an
/// optional cancellation flag.
#[derive(Debug, Clone, Default)]
pub struct Context {
    deadline: Option<Instant>,
    cancel: Option<Arc<AtomicBool>>,
}

impl Context {
    pub fn background() -> Self {
        Self::default()
    }

    /// `context.WithTimeout`: the earlier of the existing and new deadline.
    pub fn with_timeout(&self, timeout: Duration) -> Self {
        let deadline = Instant::now() + timeout;
        Self {
            deadline: Some(self.deadline.map_or(deadline, |d| d.min(deadline))),
            cancel: self.cancel.clone(),
        }
    }

    /// Cancelled once `flag` is set.
    pub fn with_cancel_flag(&self, flag: Arc<AtomicBool>) -> Self {
        Self {
            deadline: self.deadline,
            cancel: Some(flag),
        }
    }

    pub fn err(&self) -> Option<ContextError> {
        if self
            .cancel
            .as_ref()
            .is_some_and(|f| f.load(Ordering::SeqCst))
        {
            return Some(ContextError::Canceled);
        }
        match self.deadline {
            Some(d) if Instant::now() >= d => Some(ContextError::DeadlineExceeded),
            _ => None,
        }
    }

    pub fn remaining(&self) -> Option<Duration> {
        self.deadline
            .map(|d| d.saturating_duration_since(Instant::now()))
    }

    /// Sleeps for `delay` unless the context ends first.
    pub fn sleep(&self, delay: Duration) -> Result<(), ContextError> {
        let end = Instant::now() + delay;
        loop {
            if let Some(err) = self.err() {
                return Err(err);
            }
            let now = Instant::now();
            if now >= end {
                return Ok(());
            }
            let mut slice = (end - now).min(Duration::from_millis(25));
            if let Some(rem) = self.remaining() {
                slice = slice.min(rem.max(Duration::from_millis(1)));
            }
            std::thread::sleep(slice);
        }
    }
}

/// An HTTP response with a streaming body.
pub struct HttpResponse {
    pub status: u16,
    /// Go `Response.ContentLength`: -1 when unknown.
    pub content_length: i64,
    pub body: Box<dyn Read + Send>,
}

/// HTTP seam (Go's `Checker.HTTP *http.Client`).
pub trait HttpClient: Send + Sync {
    /// Issues a GET. `timeout` is the caller's remaining context deadline.
    fn get(&self, url: &str, timeout: Option<Duration>) -> Result<HttpResponse, BoxError>;
}

/// The production client: reqwest (blocking, rustls), https-only redirects,
/// no whole-body deadline.
pub struct ReqwestClient {
    client: reqwest::blocking::Client,
}

impl ReqwestClient {
    pub fn new() -> Result<Self, BoxError> {
        // The ring provider is ours; another crate may already have installed one.
        let _ = rustls::crypto::ring::default_provider().install_default();
        let policy = reqwest::redirect::Policy::custom(|attempt| {
            if attempt.url().scheme() != "https" {
                return attempt.error("non-HTTPS redirect refused");
            }
            if attempt.previous().len() > 5 {
                return attempt.error("too many redirects");
            }
            attempt.follow()
        });
        let client = reqwest::blocking::Client::builder()
            .redirect(policy)
            .connect_timeout(Duration::from_secs(10))
            .tcp_keepalive(Duration::from_secs(30))
            .timeout(Self::TOTAL_TIMEOUT)
            .build()?;
        Ok(Self { client })
    }

    /// The client-wide total request deadline. Large asset bodies may take as
    /// long as they need while bytes keep arriving; stalls are bounded per read.
    pub const TOTAL_TIMEOUT: Option<Duration> = None;
}

impl HttpClient for ReqwestClient {
    fn get(&self, url: &str, timeout: Option<Duration>) -> Result<HttpResponse, BoxError> {
        let mut request = self.client.get(url);
        if let Some(timeout) = timeout {
            request = request.timeout(timeout);
        }
        let response = request.send().map_err(|e| go_url_error(url, &e))?;
        let status = response.status().as_u16();
        let content_length = response
            .content_length()
            .and_then(|n| i64::try_from(n).ok())
            .unwrap_or(-1);
        Ok(HttpResponse {
            status,
            content_length,
            body: Box::new(response),
        })
    }
}

/// Formats a reqwest error like Go's `*url.Error` (`Get "<url>": <cause>`),
/// using the innermost causes instead of reqwest's generic summary.
fn go_url_error(url: &str, err: &reqwest::Error) -> BoxError {
    let mut causes = Vec::new();
    let mut source = std::error::Error::source(err);
    while let Some(cause) = source {
        causes.push(cause.to_string());
        source = cause.source();
    }
    let cause = if causes.is_empty() {
        err.to_string()
    } else {
        causes.join(": ")
    };
    format!("Get \"{url}\": {cause}").into()
}

/// A verified release manifest.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Manifest {
    pub version: String,
    pub revision: String,
    pub published: String,
    pub sha256: BTreeMap<String, String>,
}

impl Manifest {
    /// `json.Marshal(manifest)` (map keys sorted, as Go does).
    pub fn to_json(&self) -> String {
        let mut out = String::from("{\"version\":");
        gojson::write_string(&mut out, &self.version);
        out.push_str(",\"revision\":");
        gojson::write_string(&mut out, &self.revision);
        out.push_str(",\"published\":");
        gojson::write_string(&mut out, &self.published);
        out.push_str(",\"sha256\":{");
        for (i, (name, sum)) in self.sha256.iter().enumerate() {
            if i > 0 {
                out.push(',');
            }
            gojson::write_string(&mut out, name);
            out.push(':');
            gojson::write_string(&mut out, sum);
        }
        out.push_str("}}");
        out
    }

    /// Decodes like Go's `json.Unmarshal` into `Manifest`.
    pub fn from_json(data: &[u8]) -> Result<Self, Error> {
        let bad = |what: &str| Error::ManifestJson(format!("json: cannot unmarshal {what}"));
        let value = gojson::parse(data)
            .ok_or_else(|| Error::ManifestJson("invalid JSON in release manifest".into()))?;
        let map = match value {
            Value::Object(map) => map,
            Value::Null => return Ok(Self::default()),
            _ => return Err(bad("into Go value of type release.Manifest")),
        };
        let string = |name: &str| {
            gojson::string_field(&map, name)
                .map(Option::unwrap_or_default)
                .map_err(|()| {
                    bad(&format!(
                        "into Go struct field Manifest.{name} of type string"
                    ))
                })
        };
        let mut m = Self {
            version: string("version")?,
            revision: string("revision")?,
            published: string("published")?,
            sha256: BTreeMap::new(),
        };
        match gojson::field(&map, "sha256") {
            None | Some(Value::Null) => {}
            Some(Value::Object(sums)) => {
                for (name, sum) in sums {
                    let sum = match sum {
                        Value::String(s) => s.clone(),
                        Value::Null => String::new(),
                        _ => {
                            return Err(bad("into Go struct field Manifest.sha256 of type string"));
                        }
                    };
                    m.sha256.insert(name.clone(), sum);
                }
            }
            Some(_) => {
                return Err(bad(
                    "into Go struct field Manifest.sha256 of type map[string]string",
                ));
            }
        }
        Ok(m)
    }
}

/// Fetches and verifies releases.
#[derive(Clone)]
pub struct Checker {
    pub base: String,
    /// `None` uses the default client (Go: nil `HTTP` → `Default().HTTP`).
    pub http: Option<Arc<dyn HttpClient>>,
    /// `None` rejects every signature.
    pub key: Option<VerifyingKey>,
    /// Per-read stall limit; zero means 20s.
    pub stall_timeout: Duration,
    pub download_retry: i32,
    /// Zero means 250ms.
    pub retry_backoff: Duration,
}

impl std::fmt::Debug for Checker {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Checker")
            .field("base", &self.base)
            .field("key", &self.key)
            .field("stall_timeout", &self.stall_timeout)
            .field("download_retry", &self.download_retry)
            .field("retry_backoff", &self.retry_backoff)
            .finish_non_exhaustive()
    }
}

/// The embedded release verification key.
pub fn public_key() -> VerifyingKey {
    let pem = std::str::from_utf8(PUBLIC_KEY_PEM).expect("release.pub is ASCII");
    VerifyingKey::from_public_key_pem(pem)
        .expect("embedded release.pub is a valid Ed25519 SPKI key")
}

fn default_http() -> Arc<dyn HttpClient> {
    Arc::new(ReqwestClient::new().expect("build release HTTP client"))
}

impl Checker {
    /// Go `release.Default()`.
    pub fn default_checker() -> Self {
        Self {
            base: BASE_URL.to_string(),
            http: Some(default_http()),
            key: Some(public_key()),
            stall_timeout: DEFAULT_STALL,
            download_retry: 2,
            retry_backoff: DEFAULT_BACKOFF,
        }
    }

    /// A checker with Go's zero values for everything except `base` and `http`.
    pub fn new(base: impl Into<String>, http: Arc<dyn HttpClient>) -> Self {
        Self {
            base: base.into(),
            http: Some(http),
            key: None,
            stall_timeout: Duration::ZERO,
            download_retry: 0,
            retry_backoff: Duration::ZERO,
        }
    }

    fn url(&self, path: &str) -> String {
        format!("{}/{path}", self.base.trim_end_matches('/'))
    }

    fn body_stall_timeout(&self) -> Duration {
        if self.stall_timeout > Duration::ZERO {
            self.stall_timeout
        } else {
            DEFAULT_STALL
        }
    }

    fn get(&self, ctx: &Context, path: &str, limit: i64) -> Result<Vec<u8>, Error> {
        let url = self.url(path);
        if let Some(err) = ctx.err() {
            return Err(Error::Http(format!("GET {url} after 0 bytes: {err}")));
        }
        let http = self.http.clone().unwrap_or_else(default_http);
        let response = http
            .get(&url, ctx.remaining())
            .map_err(|e| Error::Http(format!("GET {url} after 0 bytes: {e}")))?;
        if response.status != 200 {
            return Err(Error::Http(format!(
                "GET {url} after 0 bytes: HTTP {}",
                response.status
            )));
        }
        let total = response.content_length;
        let (data, err) = read_with_stall(ctx, response.body, limit + 1, self.body_stall_timeout());
        if let Some(err) = err {
            return Err(Error::Http(format!(
                "GET {url} after {}: {err}",
                body_progress(data.len() as i64, total)
            )));
        }
        if data.len() as i64 > limit {
            return Err(Error::Http(format!(
                "GET {url} after {} bytes: release response too large",
                data.len()
            )));
        }
        Ok(data)
    }

    /// Fetches `latest.version` and its verified manifest.
    pub fn latest(&self, ctx: &Context) -> Result<Manifest, Error> {
        let data = self.get(ctx, "latest.version", LATEST_LIMIT)?;
        let version = String::from_utf8_lossy(&data);
        self.manifest(ctx, version.trim())
    }

    /// Fetches and verifies the signed manifest of `version`.
    pub fn manifest(&self, ctx: &Context, version: &str) -> Result<Manifest, Error> {
        if !valid_version(version) {
            return Err(Error::InvalidVersion);
        }
        let base = format!("releases/v{version}/");
        let data = self.get(ctx, &format!("{base}manifest.json"), MANIFEST_LIMIT)?;
        let signature = self.get(ctx, &format!("{base}manifest.sig"), SIGNATURE_LIMIT)?;
        if !verify(self.key.as_ref(), &data, &signature) {
            return Err(Error::Signature);
        }
        let m = Manifest::from_json(&data)?;
        if m.version != version {
            return Err(Error::VersionMismatch);
        }
        for (name, sum) in &m.sha256 {
            if go_base(name) != name || sum.len() != 64 {
                return Err(Error::InvalidChecksum);
            }
            if let Some(b) = sum.bytes().find(|b| !b.is_ascii_hexdigit()) {
                return Err(Error::InvalidHex(go_rune_u(b)));
            }
        }
        Ok(m)
    }

    /// Downloads `name` from the release, retrying stalls and checksum
    /// mismatches with exponential backoff.
    pub fn download(&self, ctx: &Context, m: &Manifest, name: &str) -> Result<Vec<u8>, Error> {
        let Some(sum) = m.sha256.get(name) else {
            return Err(Error::MissingAsset(name.to_string()));
        };
        let path = format!("releases/v{}/{name}", m.version);
        let url = self.url(&path);
        let retries = self.download_retry.max(0) as u32;
        let backoff = if self.retry_backoff > Duration::ZERO {
            self.retry_backoff
        } else {
            DEFAULT_BACKOFF
        };
        let mut last = None;
        for attempt in 0..=retries {
            let err = match self.get(ctx, &path, BINARY_LIMIT) {
                Ok(data) if sha256_hex(&data) == *sum => return Ok(data),
                Ok(data) => Error::Http(format!(
                    "GET {url} after {} bytes: SHA-256 mismatch",
                    data.len()
                )),
                Err(err) => err,
            };
            last = Some(err);
            if attempt == retries {
                break;
            }
            let delay = backoff
                .checked_mul(1u32.checked_shl(attempt).unwrap_or(u32::MAX))
                .unwrap_or(Duration::MAX);
            if let Err(cause) = ctx.sleep(delay) {
                return Err(Error::DownloadCanceled {
                    url,
                    attempt: attempt + 1,
                    cause,
                });
            }
        }
        Err(Error::DownloadFailed {
            url,
            attempts: retries + 1,
            last: Box::new(last.expect("at least one attempt")),
        })
    }
}

fn verify(key: Option<&VerifyingKey>, data: &[u8], signature: &[u8]) -> bool {
    let Some(key) = key else {
        return false;
    };
    let Ok(signature) = Signature::from_slice(signature) else {
        return false;
    };
    use ed25519_dalek::Verifier;
    key.verify(data, &signature).is_ok()
}

/// Lowercase hex SHA-256 (Go `fmt.Sprintf("%x", sha256.Sum256(data))`).
pub fn sha256_hex(data: &[u8]) -> String {
    hex::encode(Sha256::digest(data))
}

/// Go `%#U` of a byte as a rune.
fn go_rune_u(b: u8) -> String {
    let c = char::from(b);
    if c.is_control() {
        format!("U+{:04X}", b)
    } else {
        format!("U+{:04X} '{c}'", b)
    }
}

/// Go `filepath.Base` (Unix).
fn go_base(path: &str) -> &str {
    if path.is_empty() {
        return ".";
    }
    let trimmed = path.trim_end_matches('/');
    if trimmed.is_empty() {
        return "/";
    }
    match trimmed.rfind('/') {
        Some(i) => &trimmed[i + 1..],
        None => trimmed,
    }
}

/// Go's progress text for a partially read body.
pub fn body_progress(received: i64, total: i64) -> String {
    const MEGABYTE: f64 = 1_000_000.0;
    if total >= 1_000_000 {
        return format!(
            "{:.0} MB of {:.0} MB",
            received as f64 / MEGABYTE,
            total as f64 / MEGABYTE
        );
    }
    if total > 0 {
        return format!("{received} of {total} bytes");
    }
    format!("{received} bytes")
}

/// Why a body read stopped early.
#[derive(Debug, thiserror::Error)]
pub enum ReadError {
    #[error("stalled after {}", gojson::duration_string(*.0))]
    Stalled(Duration),
    #[error(transparent)]
    Context(ContextError),
    #[error(transparent)]
    Io(io::Error),
}

/// Bounds each individual body read rather than the total download time. A
/// progressing large file may take as long as it needs; a stalled one fails
/// with the bytes collected so far.
///
/// Reads run on a helper thread (Go: one goroutine per read). On a stall the
/// helper is abandoned and exits once its blocked read returns.
pub fn read_with_stall(
    ctx: &Context,
    mut body: Box<dyn Read + Send>,
    limit: i64,
    stall: Duration,
) -> (Vec<u8>, Option<ReadError>) {
    const CHUNK: i64 = 32 << 10;
    let limit = limit.max(0);
    let mut data = Vec::with_capacity(limit.min(64 << 10) as usize);
    let (request_tx, request_rx) = mpsc::channel::<usize>();
    let (reply_tx, reply_rx) = mpsc::channel::<(Vec<u8>, io::Result<usize>)>();
    std::thread::spawn(move || {
        while let Ok(n) = request_rx.recv() {
            let mut buf = vec![0u8; n];
            let result = loop {
                match body.read(&mut buf) {
                    Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
                    other => break other,
                }
            };
            if reply_tx.send((buf, result)).is_err() {
                return;
            }
        }
    });
    while (data.len() as i64) < limit {
        let next = CHUNK.min(limit - data.len() as i64) as usize;
        if request_tx.send(next).is_err() {
            return (data, Some(ReadError::Io(io::ErrorKind::BrokenPipe.into())));
        }
        let stall_at = Instant::now() + stall;
        let (buf, result) = loop {
            if let Some(err) = ctx.err() {
                return (data, Some(ReadError::Context(err)));
            }
            let now = Instant::now();
            if now >= stall_at {
                return (data, Some(ReadError::Stalled(stall)));
            }
            let mut wait = stall_at - now;
            if ctx.cancel.is_some() {
                wait = wait.min(Duration::from_millis(25));
            }
            if let Some(rem) = ctx.remaining() {
                wait = wait.min(rem);
            }
            match reply_rx.recv_timeout(wait) {
                Ok(reply) => break reply,
                Err(mpsc::RecvTimeoutError::Timeout) => continue,
                Err(mpsc::RecvTimeoutError::Disconnected) => {
                    return (data, Some(ReadError::Io(io::ErrorKind::BrokenPipe.into())));
                }
            }
        };
        match result {
            Ok(0) => return (data, None), // io.EOF
            Ok(n) => data.extend_from_slice(&buf[..n]),
            Err(e) => return (data, Some(ReadError::Io(e))),
        }
    }
    (data, None)
}

/// Go `runtime.GOOS`.
pub const GOOS: &str = if cfg!(target_os = "macos") {
    "darwin"
} else if cfg!(target_os = "linux") {
    "linux"
} else if cfg!(target_os = "freebsd") {
    "freebsd"
} else if cfg!(target_os = "windows") {
    "windows"
} else {
    std::env::consts::OS
};

/// Go `runtime.GOARCH`.
pub const GOARCH: &str = if cfg!(target_arch = "x86_64") {
    "amd64"
} else if cfg!(target_arch = "aarch64") {
    "arm64"
} else if cfg!(target_arch = "x86") {
    "386"
} else if cfg!(target_arch = "arm") {
    "arm"
} else {
    std::env::consts::ARCH
};

/// Release asset name of this build: `bp-<goos>-<goarch>`.
pub fn platform() -> String {
    format!("bp-{GOOS}-{GOARCH}")
}

/// `^[0-9]+\.[0-9]+\.[0-9]+$`
pub fn valid_version(version: &str) -> bool {
    let parts: Vec<&str> = version.split('.').collect();
    parts.len() == 3
        && parts
            .iter()
            .all(|p| !p.is_empty() && p.bytes().all(|b| b.is_ascii_digit()))
}

/// Whether `candidate` is a strictly newer release version than `current`.
pub fn newer(candidate: &str, current: &str) -> bool {
    if !valid_version(candidate) || !valid_version(current) {
        return false;
    }
    // strconv.ParseUint saturates at the maximum on overflow.
    let parse = |s: &str| s.parse::<u64>().unwrap_or(u64::MAX);
    for (a, b) in candidate.split('.').zip(current.split('.')) {
        let (x, y) = (parse(a), parse(b));
        if x != y {
            return x > y;
        }
    }
    false
}

/// [`replace`] failure, with the backup path when one was written.
#[derive(Debug, thiserror::Error)]
#[error("{error}")]
pub struct ReplaceError {
    pub backup: Option<PathBuf>,
    #[source]
    pub error: Error,
}

impl ReplaceError {
    fn new(backup: Option<&Path>, error: impl Into<Error>) -> Self {
        Self {
            backup: backup.map(Path::to_path_buf),
            error: error.into(),
        }
    }
}

struct RemoveOnDrop<'a>(&'a Path);

impl Drop for RemoveOnDrop<'_> {
    fn drop(&mut self) {
        let _ = std::fs::remove_file(self.0);
    }
}

fn write_copy(file: &mut std::fs::File, data: &[u8], perm: u32, sync: bool) -> io::Result<()> {
    file.write_all(data)?;
    file.set_permissions(std::fs::Permissions::from_mode(perm))?;
    if sync {
        file.sync_all()?;
    }
    Ok(())
}

/// Replaces `target` with `data`, retaining the previous executable as
/// `<target>.before-update-*`. `validate` runs on the closed candidate before
/// anything changes; a failed `setup` restores the old binary atomically.
/// Holds `flock(.bp-update.lock)` beside the target throughout.
pub fn replace(
    target: &Path,
    data: &[u8],
    validate: impl FnOnce(&Path) -> Result<(), BoxError>,
    setup: impl FnOnce() -> Result<(), BoxError>,
) -> Result<PathBuf, ReplaceError> {
    let target = std::fs::canonicalize(target).map_err(|e| ReplaceError::new(None, e))?;
    let dir = target.parent().unwrap_or(Path::new("/")).to_path_buf();
    let _lock = fsutil::lock_exclusive(&dir.join(".bp-update.lock"), 0o600)
        .map_err(|e| ReplaceError::new(None, e))?;

    let info = std::fs::metadata(&target).map_err(|e| ReplaceError::new(None, e))?;
    if !info.is_file() {
        return Err(ReplaceError::new(None, Error::NotRegular));
    }
    let perm = info.permissions().mode() & 0o777;
    let (mut candidate, name) =
        fsutil::create_temp(&dir, ".bp-update-").map_err(|e| ReplaceError::new(None, e))?;
    let _cleanup = RemoveOnDrop(&name);
    let written = write_copy(&mut candidate, data, perm, true);
    drop(candidate);
    written.map_err(|e| ReplaceError::new(None, e))?;
    validate(&name).map_err(|e| ReplaceError::new(None, Error::Validate(e)))?;

    let old = std::fs::read(&target).map_err(|e| ReplaceError::new(None, e))?;
    let base = target
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_default();
    let (mut backup, backup_name) = fsutil::create_temp(&dir, &format!("{base}.before-update-"))
        .map_err(|e| ReplaceError::new(None, e))?;
    let written = write_copy(&mut backup, &old, perm, true);
    drop(backup);
    written.map_err(|e| ReplaceError::new(Some(&backup_name), e))?;
    std::fs::rename(&name, &target).map_err(|e| ReplaceError::new(Some(&backup_name), e))?;

    if let Err(setup_err) = setup() {
        let restored = (|| {
            let (mut rollback, rollback_name) = fsutil::create_temp(&dir, ".bp-restore-")?;
            let written = write_copy(&mut rollback, &old, perm, false);
            drop(rollback);
            written?;
            std::fs::rename(&rollback_name, &target)
        })();
        let error = match restored {
            Ok(()) => Error::SetupRestored(setup_err),
            Err(restore) => Error::SetupRestoreFailed {
                setup: setup_err,
                restore,
                backup: backup_name.to_string_lossy().into_owned(),
            },
        };
        return Err(ReplaceError::new(Some(&backup_name), error));
    }
    Ok(backup_name)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn newer_compares_numerically() {
        for (a, b, want) in [
            ("1.5.9", "1.6.0", false),
            ("1.6.0", "1.6.0", false),
            ("1.10.0", "1.6.0", true),
            ("2.0.0", "1.99.99", true),
            ("1.6.1", "1.6.0", true),
            ("v1.6.1", "1.6.0", false),
            ("1.6", "1.5.0", false),
            ("1.6.0\n", "1.5.0", false),
            ("99999999999999999999.0.0", "1.0.0", true),
        ] {
            assert_eq!(newer(a, b), want, "{a} vs {b}");
        }
    }

    #[test]
    fn version_is_trimmed_release_text() {
        assert!(valid_version(version()), "{:?}", version());
        assert_eq!(version(), VERSION_TEXT.trim());
    }

    #[test]
    fn platform_uses_go_names() {
        let p = platform();
        assert!(p.starts_with("bp-"));
        #[cfg(all(target_os = "linux", target_arch = "x86_64"))]
        assert_eq!(p, "bp-linux-amd64");
        #[cfg(all(target_os = "macos", target_arch = "aarch64"))]
        assert_eq!(p, "bp-darwin-arm64");
    }

    #[test]
    fn go_base_matches_filepath_base() {
        for (p, want) in [
            ("", "."),
            ("/", "/"),
            ("a/", "a"),
            ("a/b", "b"),
            ("bp", "bp"),
            ("..", ".."),
        ] {
            assert_eq!(go_base(p), want, "{p}");
        }
    }

    #[test]
    fn body_progress_formats() {
        assert_eq!(body_progress(2_000_000, 35_000_000), "2 MB of 35 MB");
        assert_eq!(body_progress(4, 10), "4 of 10 bytes");
        assert_eq!(body_progress(4, -1), "4 bytes");
    }

    #[test]
    fn manifest_json_round_trip() {
        let m = Manifest {
            version: "1.2.3".into(),
            revision: "r".into(),
            published: "p".into(),
            sha256: BTreeMap::from([("b".into(), "2".into()), ("a".into(), "1".into())]),
        };
        assert_eq!(
            m.to_json(),
            r#"{"version":"1.2.3","revision":"r","published":"p","sha256":{"a":"1","b":"2"}}"#
        );
        assert_eq!(Manifest::from_json(m.to_json().as_bytes()).unwrap(), m);
        assert!(Manifest::from_json(br#"{"sha256":{"a":1}}"#).is_err());
        assert!(Manifest::from_json(br#"{"version":1}"#).is_err());
    }

    #[test]
    fn signature_must_be_64_bytes() {
        let key = public_key();
        assert!(!verify(Some(&key), b"x", &[0; 63]));
        assert!(!verify(Some(&key), b"x", &[0; 64]));
        assert!(!verify(None, b"x", &[0; 64]));
    }
}
