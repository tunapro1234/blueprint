//! Port of internal/cache/codex.go: Codex rollout metrics, the reverse row
//! scanner, the incremental scan cache and the rollout-by-id index.

use std::collections::HashMap;
use std::fs::{self, File};
use std::io::{BufRead, BufReader, Read};
use std::sync::{LazyLock, Mutex, MutexGuard};
use std::time::{Duration as StdDuration, SystemTime};

use chrono::TimeDelta;

use super::glob;
use super::jsonrow::{
    definitely_other_compact_record_type, has_compact_record_type_prefix, may_have_record_type,
};
use super::{
    MAX_TAIL_SIZE, State, age, automatic, content_text, decode_codex_record, folder_path, read_at,
};
use crate::go_struct;
use crate::godecode::{RawJson, unmarshal_ok};
use crate::gotime::{self, Time};

const STATE_TYPES: &[&str] = &[
    "session_meta",
    "turn_context",
    "compacted",
    "event_msg",
    "response_item",
];

/// Reads the latest interactive session in a workspace. A known thread id
/// should be passed to [`read_codex_session`] so another session cannot replace it.
pub fn read_codex(codex_home: &str, folder: &str) -> State {
    read_codex_session(codex_home, folder, "")
}

pub fn read_codex_session(home: &str, folder: &str, id: &str) -> State {
    match codex_path(home, folder, id) {
        Some(path) => read_codex_path(&path),
        None => State::unknown(),
    }
}

/// Searches backwards for each metric independently. Tool output can separate
/// token_count and turn_context by megabytes; a fixed tail loses valid
/// measurements every time that happens. No model call or database write.
pub fn read_codex_path(path: &str) -> State {
    read_codex_path_at(path, &gotime::now())
}

/// [`read_codex_path`] with an injected clock.
pub fn read_codex_path_at(path: &str, now: &Time) -> State {
    let scan = cached_codex_scan(path);
    let mut result = scan.state;
    if result.known {
        result.age = age(now, &result.usage_at);
    }
    if !gotime::is_zero(&scan.human) {
        result.last_human_age = age(now, &scan.human);
    }
    // A crashed session must not keep a new pane busy indefinitely.
    if result.busy && age(now, &result.turn_at) > TimeDelta::hours(24) {
        result.busy = false;
    }
    result
}

/// Everything [`read_codex_path`] learns from the file; ages are derived at
/// call time so a cached scan never serves a stale age.
#[derive(Debug, Clone, PartialEq)]
pub(crate) struct CodexScan {
    pub(crate) state: State,
    pub(crate) human: Time,
}

/// One reverse scan's findings. Each metric is decided by the newest row that
/// carries it, independently of the others, so a scan of the appended rows
/// merged field by field with the previous findings equals a fresh scan of the
/// whole file. `*_at` is the byte offset of the deciding row; -1 means none.
#[derive(Debug, Clone)]
pub(crate) struct CodexFields {
    model_at: i64,
    usage_at: i64,
    turn_at: i64,
    human_at: i64,
    model: String,
    effort: String,
    tier: String,
    known: bool,
    ctx: i64,
    window: i64,
    usage_time: Time,
    turn_time: Time,
    busy: bool,
    human_time: Time,
    metas: Vec<CodexMeta>,
}

/// A session_meta row the scan passed. Only metas newer than the row that
/// completed the scan count, and the oldest of those names the thread.
#[derive(Debug, Clone)]
struct CodexMeta {
    at: i64,
    id: String,
}

impl CodexFields {
    pub(crate) fn new() -> CodexFields {
        CodexFields {
            model_at: -1,
            usage_at: -1,
            turn_at: -1,
            human_at: -1,
            model: String::new(),
            effort: String::new(),
            tier: String::new(),
            known: false,
            ctx: 0,
            window: 0,
            usage_time: gotime::zero(),
            turn_time: gotime::zero(),
            busy: false,
            human_time: gotime::zero(),
            metas: Vec::new(),
        }
    }

    fn complete(&self) -> bool {
        self.model_at >= 0 && self.usage_at >= 0 && self.turn_at >= 0 && self.human_at >= 0
    }

    /// The offset of the oldest row the scan had to read, or 0 when some
    /// metric was never found.
    pub(crate) fn stop(&self) -> i64 {
        if !self.complete() {
            return 0;
        }
        self.model_at
            .min(self.usage_at)
            .min(self.turn_at)
            .min(self.human_at)
    }

    /// Applies one row, newest first. Reports whether the scan must go on.
    fn visit(&mut self, at: i64, line: &[u8]) -> bool {
        if definitely_other_compact_record_type(line, STATE_TYPES) {
            return !self.complete();
        }
        if line.len() > 4 * 1024
            && !has_compact_record_type_prefix(line, STATE_TYPES)
            && !may_have_record_type(line, STATE_TYPES)
        {
            return !self.complete();
        }
        let (r, ok) = decode_codex_record(line);
        if !ok {
            return true;
        }
        if r.kind == "session_meta" {
            self.metas.push(CodexMeta {
                at,
                id: r.payload.id.clone(),
            });
        }
        if r.kind == "turn_context" && self.model_at < 0 {
            self.model.clone_from(&r.payload.model);
            self.effort.clone_from(&r.payload.effort);
            self.tier.clone_from(&r.payload.service_tier);
            self.model_at = at;
        }
        // Compaction invalidates the pre-compact context measurement. Wait for
        // a new token_count rather than showing the old full context or a guess.
        if self.usage_at < 0
            && (r.kind == "compacted"
                || (r.kind == "event_msg" && r.payload.kind == "context_compacted"))
        {
            self.usage_at = at;
        }
        if r.kind == "event_msg" {
            if r.payload.kind == "token_count"
                && self.usage_at < 0
                && let Some(info) = &r.payload.info
                && let Some(last) = &info.last
            {
                self.known = true;
                self.usage_at = at;
                self.ctx = last.total;
                self.window = info.window;
                self.usage_time = r.timestamp;
            }
            if self.turn_at < 0 {
                match r.payload.kind.as_str() {
                    "task_started" => {
                        self.busy = true;
                        self.turn_at = at;
                        self.turn_time = r.timestamp;
                    }
                    "task_complete" | "turn_aborted" => {
                        self.busy = false;
                        self.turn_at = at;
                        self.turn_time = r.timestamp;
                    }
                    _ => {}
                }
            }
        }
        if self.human_at < 0
            && r.kind == "response_item"
            && r.payload.kind == "message"
            && r.payload.role == "user"
            && !automatic(&content_text(r.payload.content.as_bytes()))
        {
            self.human_time = r.timestamp;
            self.human_at = at;
        }
        !self.complete()
    }

    /// Completes the findings of a scan over newer rows with an earlier scan
    /// of the rows before them.
    fn merge(mut self, older: &CodexFields) -> CodexFields {
        if self.model_at < 0 {
            self.model_at = older.model_at;
            self.model.clone_from(&older.model);
            self.effort.clone_from(&older.effort);
            self.tier.clone_from(&older.tier);
        }
        if self.usage_at < 0 {
            self.usage_at = older.usage_at;
            self.known = older.known;
            self.ctx = older.ctx;
            self.window = older.window;
            self.usage_time = older.usage_time;
        }
        if self.turn_at < 0 {
            self.turn_at = older.turn_at;
            self.busy = older.busy;
            self.turn_time = older.turn_time;
        }
        if self.human_at < 0 {
            self.human_at = older.human_at;
            self.human_time = older.human_time;
        }
        let stop = self.stop();
        let metas = self
            .metas
            .iter()
            .chain(older.metas.iter())
            .filter(|m| m.at >= stop)
            .cloned()
            .collect();
        self.metas = metas;
        self
    }

    fn scan(&self, path: &str) -> CodexScan {
        let mut result = State {
            path: path.to_owned(),
            thread_id: codex_id(path),
            ..State::unknown()
        };
        // A full scan meets session_meta rows newest first and keeps the last
        // one it passes, which is the oldest.
        let (mut oldest, stop) = (-1i64, self.stop());
        for meta in &self.metas {
            if meta.at >= stop && (oldest < 0 || meta.at < oldest) {
                oldest = meta.at;
                result.thread_id.clone_from(&meta.id);
            }
        }
        if self.model_at >= 0 {
            result.model.clone_from(&self.model);
            result.effort.clone_from(&self.effort);
            result.service_tier.clone_from(&self.tier);
        }
        result.known = self.known;
        result.ctx_tokens = self.ctx;
        result.window = self.window;
        result.busy = self.busy;
        result.usage_at = self.usage_time;
        result.turn_at = self.turn_time;
        result.turn_known = self.turn_at >= 0;
        CodexScan {
            state: result,
            human: self.human_time,
        }
    }
}

pub(crate) struct CodexScanEntry {
    size: u64,
    modified: Option<SystemTime>,
    /// The length of the newline-terminated prefix the fields cover; `seam`
    /// holds its last bytes, so a rewritten file is not mistaken for the one
    /// that was scanned.
    pub(crate) rows: i64,
    seam: Option<Vec<u8>>,
    fields: CodexFields,
}

/// Caches scans by path. An idle rollout does not change, and a working one
/// only grows, so only the appended rows are read again.
pub(crate) static CODEX_SCANS: LazyLock<Mutex<HashMap<String, CodexScanEntry>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));

const CODEX_SEAM_SIZE: i64 = 256;

fn scans() -> MutexGuard<'static, HashMap<String, CodexScanEntry>> {
    CODEX_SCANS
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
}

pub(crate) fn cached_codex_scan(path: &str) -> CodexScan {
    let unreadable = || CodexScan {
        state: State {
            path: path.to_owned(),
            thread_id: codex_id(path),
            ..State::unknown()
        },
        human: gotime::zero(),
    };
    let Ok(file) = File::open(path) else {
        return unreadable();
    };
    let Ok(info) = file.metadata() else {
        return unreadable();
    };
    let size = info.len();
    let modified = info.modified().ok();
    let mut fields = CodexFields::new();
    let mut floor = 0i64;
    let mut older: Option<CodexFields> = None;
    {
        let entries = scans();
        if let Some(entry) = entries.get(path) {
            if entry.size == size && entry.modified == modified {
                let fields = entry.fields.clone();
                drop(entries);
                return fields.scan(path);
            }
            if entry.rows <= size as i64
                && entry.fields.stop() < entry.rows
                && same_seam(&file, entry.rows, entry.seam.as_deref())
            {
                floor = entry.rows;
                older = Some(entry.fields.clone());
            }
        }
    }
    scan_rows_reverse(&file, floor, size as i64, &mut |at, row| {
        fields.visit(at, row)
    });
    if floor > 0
        && let Some(older) = &older
    {
        fields = fields.merge(older);
    }
    let rows = last_row_end(&file, size as i64);
    let mut entries = scans();
    if entries.len() > 256 {
        entries.clear();
    }
    if fields.stop() < rows && decided_before(&fields, rows) {
        entries.insert(
            path.to_owned(),
            CodexScanEntry {
                size,
                modified,
                rows,
                seam: read_seam(&file, rows),
                fields: fields.clone(),
            },
        );
    } else {
        entries.remove(path);
    }
    drop(entries);
    fields.scan(path)
}

/// No metric came from an unterminated final row, which the writer may still
/// extend into a different record.
fn decided_before(f: &CodexFields, rows: i64) -> bool {
    [f.model_at, f.usage_at, f.turn_at, f.human_at]
        .iter()
        .all(|&at| at < rows)
        && f.metas.iter().all(|meta| meta.at < rows)
}

/// The offset just past the last newline before `size`.
fn last_row_end(file: &File, size: i64) -> i64 {
    let mut block = vec![0u8; 64 * 1024];
    let mut end = size;
    while end > 0 {
        let start = (end - block.len() as i64).max(0);
        let want = (end - start) as usize;
        let (n, failed) = read_at(file, &mut block[..want], start as u64);
        if failed && n < want {
            return 0;
        }
        if let Some(i) = block[..want].iter().rposition(|&c| c == b'\n') {
            return start + i as i64 + 1;
        }
        end = start;
    }
    0
}

fn read_seam(file: &File, end: i64) -> Option<Vec<u8>> {
    let start = (end - CODEX_SEAM_SIZE).max(0);
    let mut seam = vec![0u8; (end - start) as usize];
    let (n, failed) = read_at(file, &mut seam, start as u64);
    if failed && n < seam.len() {
        return None;
    }
    Some(seam)
}

fn same_seam(file: &File, end: i64, seam: Option<&[u8]>) -> bool {
    let Some(seam) = seam else {
        return false;
    };
    read_seam(file, end).is_some_and(|current| current == seam)
}

/// Visits complete JSONL rows, newest first. A partial final row is ignored
/// until the writer completes it; giant unrelated records are skipped without
/// imposing a tail limit on the metrics behind them.
pub fn scan_codex_reverse(path: &str, visit: &mut dyn FnMut(&[u8]) -> bool) {
    let Ok(file) = File::open(path) else {
        return;
    };
    let Ok(info) = file.metadata() else {
        return;
    };
    scan_rows_reverse(&file, 0, info.len() as i64, &mut |_, row| visit(row));
}

/// Visits the rows between `floor`, which must be a row start, and `size`,
/// newest first, with each row's starting offset.
fn scan_rows_reverse(
    file: &File,
    floor: i64,
    size: i64,
    visit: &mut dyn FnMut(i64, &[u8]) -> bool,
) {
    let mut offset = size;
    let mut fragments: Vec<Vec<u8>> = Vec::new();
    let mut fragment_size: usize = 0;
    let mut discard = false;
    let emit = |at: i64,
                prefix: &[u8],
                fragments: &[Vec<u8>],
                fragment_size: usize,
                discard: bool,
                visit: &mut dyn FnMut(i64, &[u8]) -> bool|
     -> bool {
        if discard {
            return true;
        }
        if fragment_size == 0 {
            return prefix.is_empty() || visit(at, prefix);
        }
        let mut row = Vec::with_capacity(prefix.len() + fragment_size);
        row.extend_from_slice(prefix);
        for fragment in fragments.iter().rev() {
            row.extend_from_slice(fragment);
        }
        visit(at, &row)
    };
    while offset > floor {
        let n = (64 * 1024i64).min(offset - floor);
        offset -= n;
        let mut block = vec![0u8; n as usize];
        let (got, failed) = read_at(file, &mut block, offset as u64);
        if failed && got == 0 {
            return;
        }
        let mut end = got;
        let mut i = got;
        while i > 0 {
            i -= 1;
            if block[i] != b'\n' {
                continue;
            }
            if !emit(
                offset + i as i64 + 1,
                &block[i + 1..end],
                &fragments,
                fragment_size,
                discard,
                visit,
            ) {
                return;
            }
            fragments.clear();
            fragment_size = 0;
            discard = false;
            end = i;
        }
        if !discard && end > 0 {
            block.truncate(end);
            fragments.push(block);
            fragment_size += end;
            if fragment_size as u64 > MAX_TAIL_SIZE {
                fragments.clear();
                fragment_size = 0;
                discard = true;
            }
        }
    }
    emit(floor, &[], &fragments, fragment_size, discard, visit);
}

/// Finds the most recently WRITTEN rollout whose session started in `folder`.
/// Every date directory is listed: a long-running or resumed session lives in
/// the day directory it was born in, weeks behind today.
fn newest_rollout(sessions_dir: &str, folder: &str, id: &str) -> Option<String> {
    let mut files: Vec<(String, SystemTime)> = Vec::new();
    for day in recent_day_dirs(sessions_dir) {
        let Ok(entries) = fs::read_dir(&day) else {
            continue;
        };
        let mut entries: Vec<_> = entries.filter_map(Result::ok).collect();
        entries.sort_by_key(|e| e.file_name());
        for entry in entries {
            let name = entry.file_name().to_string_lossy().into_owned();
            let is_dir = entry.file_type().is_ok_and(|t| t.is_dir());
            if is_dir
                || !name.ends_with(".jsonl")
                || (!id.is_empty() && !name.ends_with(&format!("-{id}.jsonl")))
            {
                continue;
            }
            let Ok(info) = entry.metadata() else {
                continue;
            };
            files.push((
                glob::join(&[&day, &name]),
                info.modified().unwrap_or(SystemTime::UNIX_EPOCH),
            ));
        }
    }
    // Go's sort.Slice is not stable; ties on mtime are rare and keep
    // directory order here.
    files.sort_by_key(|f| std::cmp::Reverse(f.1));
    files
        .into_iter()
        .map(|(path, _)| path)
        .find(|path| rollout_cwd(path) == folder && (id.is_empty() || codex_id(path) == id))
}

/// sessions/YYYY/MM/DD directories, newest day first.
fn recent_day_dirs(sessions_dir: &str) -> Vec<String> {
    let mut days = Vec::new();
    for year in sorted_dirs_desc(sessions_dir) {
        let year_dir = glob::join(&[sessions_dir, &year]);
        for month in sorted_dirs_desc(&year_dir) {
            let month_dir = glob::join(&[sessions_dir, &year, &month]);
            for day in sorted_dirs_desc(&month_dir) {
                days.push(glob::join(&[sessions_dir, &year, &month, &day]));
            }
        }
    }
    days
}

fn sorted_dirs_desc(path: &str) -> Vec<String> {
    let Ok(entries) = fs::read_dir(path) else {
        return Vec::new();
    };
    let mut names: Vec<String> = entries
        .filter_map(Result::ok)
        .filter(|e| e.file_type().is_ok_and(|t| t.is_dir()))
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .collect();
    names.sort_by(|a, b| b.cmp(a));
    names
}

#[derive(Default)]
struct RolloutHead {
    payload: RolloutHeadPayload,
}
go_struct!(RolloutHead { payload: "payload" });

#[derive(Default)]
struct RolloutHeadPayload {
    cwd: String,
    source: RawJson,
    originator: String,
}
go_struct!(RolloutHeadPayload {
    cwd: "cwd",
    source: "source",
    originator: "originator"
});

fn rollout_cwd(path: &str) -> String {
    let Ok(file) = File::open(path) else {
        return String::new();
    };
    // The session_meta line embeds the model's full base instructions, ~20KB
    // today: the buffer must swallow the whole line or the JSON is truncated.
    let mut head = Vec::with_capacity(256 * 1024);
    if file.take(256 * 1024).read_to_end(&mut head).is_err() && head.is_empty() {
        return String::new();
    }
    if let Some(newline) = head.iter().position(|&c| c == b'\n') {
        head.truncate(newline);
    }
    let mut record = RolloutHead::default();
    if !unmarshal_ok(&head, &mut record) {
        return String::new();
    }
    let mut source = String::new();
    let _ = unmarshal_ok(record.payload.source.as_bytes(), &mut source);
    if !record.payload.source.is_empty()
        && !matches!(
            source.as_str(),
            "cli" | "vscode" | "appServer" | "app-server"
        )
    {
        return String::new();
    }
    if record.payload.originator.contains("exec") {
        return String::new();
    }
    record.payload.cwd
}

/// The live session file of a codex agent working in `folder`, if any. The
/// delivery witness uses it to read a codex agent's own record.
pub fn rollout_path(codex_home: &str, folder: &str) -> Option<String> {
    codex_path(codex_home, folder, "")
}

pub fn codex_path(codex_home: &str, folder: &str, id: &str) -> Option<String> {
    let folder = folder_path(folder);
    if codex_home.is_empty() || folder.is_empty() {
        return None;
    }
    if !id.is_empty() {
        let paths = rollout_paths_by_id(codex_home, id);
        if paths.len() != 1 || codex_id(&paths[0]) != id || rollout_cwd(&paths[0]) != folder {
            return None;
        }
        return paths.into_iter().next();
    }
    newest_rollout(&glob::join(&[codex_home, "sessions"]), folder, id)
}

/// Remembers, per Codex home, which rollout files each thread id matched.
/// The answer can only change when a file is created, removed or renamed in a
/// date directory, which changes that directory's mtime (appends do not).
struct RolloutIndex {
    dirs: HashMap<String, SystemTime>,
    newest: SystemTime,
    by_id: HashMap<String, Vec<String>>,
}

static ROLLOUT_INDEXES: LazyLock<Mutex<HashMap<String, RolloutIndex>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));

/// Guards the mtime rule against coarse timestamps: an answer computed while
/// some date directory was modified within this window is not reused.
const ROLLOUT_RACY_WINDOW: StdDuration = StdDuration::from_secs(1);

fn newest_dir_time(dirs: &HashMap<String, SystemTime>) -> SystemTime {
    dirs.values()
        .copied()
        .max()
        .unwrap_or(SystemTime::UNIX_EPOCH)
}

fn has_glob_meta(id: &str) -> bool {
    id.bytes()
        .any(|c| matches!(c, b'*' | b'?' | b'[' | b'\\' | b'/'))
}

/// The rollout files named for thread `id` under `home`.
pub fn rollout_paths_by_id(home: &str, id: &str) -> Vec<String> {
    let sessions = glob::join(&[home, "sessions"]);
    let mut indexes = ROLLOUT_INDEXES
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    if let Some(index) = indexes.get(home)
        && let Some(dirs) = rollout_day_dirs(&sessions)
        && same_dir_times(&index.dirs, &dirs)
        && SystemTime::now()
            .checked_sub(ROLLOUT_RACY_WINDOW)
            .is_some_and(|limit| index.newest < limit)
    {
        if !has_glob_meta(id) {
            return index.by_id.get(id).cloned().unwrap_or_default();
        }
        // Keep filepath.Glob's pattern semantics for non-UUID callers too.
        return glob_rollout_paths(&sessions, id);
    }
    let Some(index) = scan_rollout_index(&sessions) else {
        return glob_rollout_paths(&sessions, id);
    };
    if indexes.len() > 16 {
        indexes.clear();
    }
    let answer = if has_glob_meta(id) {
        None
    } else {
        Some(index.by_id.get(id).cloned().unwrap_or_default())
    };
    indexes.insert(home.to_owned(), index);
    match answer {
        Some(paths) => paths,
        None => glob_rollout_paths(&sessions, id),
    }
}

fn glob_rollout_paths(sessions: &str, id: &str) -> Vec<String> {
    let name = format!("*-{id}.jsonl");
    glob::glob(&glob::join(&[sessions, "*", "*", "*", &name])).unwrap_or_default()
}

/// Walks sessions/YYYY/MM/DD once, recording directory mtimes for the
/// invalidation rule and each entry name for id lookup. It never opens
/// rollout files; directory entries named like rollout files are retained
/// because `filepath.Glob` returns them too.
fn scan_rollout_index(sessions: &str) -> Option<RolloutIndex> {
    let mut dirs = HashMap::new();
    let mut by_id: HashMap<String, Vec<String>> = HashMap::new();
    fn walk(
        dir: &str,
        depth: usize,
        dirs: &mut HashMap<String, SystemTime>,
        by_id: &mut HashMap<String, Vec<String>>,
    ) -> bool {
        let Ok(info) = fs::metadata(dir) else {
            return false;
        };
        if !info.is_dir() {
            return false;
        }
        dirs.insert(
            dir.to_owned(),
            info.modified().unwrap_or(SystemTime::UNIX_EPOCH),
        );
        let Ok(entries) = fs::read_dir(dir) else {
            return false;
        };
        let mut entries: Vec<_> = entries.filter_map(Result::ok).collect();
        entries.sort_by_key(|e| e.file_name());
        if depth < 3 {
            for entry in entries {
                if entry.file_type().is_ok_and(|t| t.is_dir()) {
                    let name = entry.file_name().to_string_lossy().into_owned();
                    if !walk(&glob::join(&[dir, &name]), depth + 1, dirs, by_id) {
                        return false;
                    }
                }
            }
            return true;
        }
        for entry in entries {
            let name = entry.file_name().to_string_lossy().into_owned();
            let Some(stem) = name.strip_suffix(".jsonl") else {
                continue;
            };
            for (at, _) in stem.match_indices('-') {
                by_id
                    .entry(stem[at + 1..].to_owned())
                    .or_default()
                    .push(glob::join(&[dir, &name]));
            }
        }
        true
    }
    if !walk(sessions, 0, &mut dirs, &mut by_id) {
        return None;
    }
    for paths in by_id.values_mut() {
        paths.sort();
    }
    Some(RolloutIndex {
        newest: newest_dir_time(&dirs),
        dirs,
        by_id,
    })
}

/// The mtime of sessions/ and of every directory below it down to the date
/// directories. Any read error disables the cache.
fn rollout_day_dirs(sessions: &str) -> Option<HashMap<String, SystemTime>> {
    fn walk(dir: &str, depth: usize, dirs: &mut HashMap<String, SystemTime>) -> bool {
        let Ok(info) = fs::metadata(dir) else {
            return false;
        };
        if !info.is_dir() {
            return false;
        }
        dirs.insert(
            dir.to_owned(),
            info.modified().unwrap_or(SystemTime::UNIX_EPOCH),
        );
        if depth == 3 {
            return true;
        }
        let Ok(entries) = fs::read_dir(dir) else {
            return false;
        };
        for entry in entries.filter_map(Result::ok) {
            if entry.file_type().is_ok_and(|t| t.is_dir()) {
                let name = entry.file_name().to_string_lossy().into_owned();
                if !walk(&glob::join(&[dir, &name]), depth + 1, dirs) {
                    return false;
                }
            }
        }
        true
    }
    let mut dirs = HashMap::new();
    walk(sessions, 0, &mut dirs).then_some(dirs)
}

fn same_dir_times(a: &HashMap<String, SystemTime>, b: &HashMap<String, SystemTime>) -> bool {
    a.len() == b.len() && a.iter().all(|(dir, at)| b.get(dir) == Some(at))
}

#[derive(Default)]
struct IdRow {
    payload: IdPayload,
}
go_struct!(IdRow { payload: "payload" });

#[derive(Default)]
struct IdPayload {
    id: String,
}
go_struct!(IdPayload { id: "id" });

/// Reads the identity, never instructions or credentials.
pub fn codex_id(path: &str) -> String {
    let Ok(file) = File::open(path) else {
        return String::new();
    };
    // bufio.Scanner with a 16 MiB token limit: the first line, CR dropped.
    let mut reader = BufReader::with_capacity(64 * 1024, file).take(MAX_TAIL_SIZE + 1);
    let mut line = Vec::new();
    if reader.read_until(b'\n', &mut line).is_err() || line.is_empty() {
        return String::new();
    }
    if line.last() == Some(&b'\n') {
        line.pop();
    }
    if line.len() as u64 >= MAX_TAIL_SIZE {
        return String::new();
    }
    if line.last() == Some(&b'\r') {
        line.pop();
    }
    let mut row = IdRow::default();
    if !unmarshal_ok(&line, &mut row) {
        return String::new();
    }
    row.payload.id
}
