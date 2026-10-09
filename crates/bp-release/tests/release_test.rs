//! Port of internal/release/release_test.go, download_fix_test.go,
//! install_host_test.go and issue_repro_test.go.

mod common;

use bp_release::release::{
    self, BoxError, Checker, Context, Manifest, ReqwestClient, newer, platform, replace, sha256_hex,
};
use common::{head, ok, serve};
use ed25519_dalek::{Signer, SigningKey};
use std::collections::BTreeMap;
use std::io::Write;
use std::path::Path;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, AtomicI32, Ordering};
use std::time::Duration;

fn client() -> Arc<ReqwestClient> {
    Arc::new(ReqwestClient::new().unwrap())
}

fn manifest_for(version: &str, payload: &[u8]) -> Manifest {
    Manifest {
        version: version.into(),
        sha256: BTreeMap::from([(platform(), sha256_hex(payload))]),
        ..Manifest::default()
    }
}

#[test]
fn signed_release_rejects_tampering() {
    let key = test_key(7);
    let payload = b"executable".to_vec();
    let data = manifest_for("1.6.0", &payload).to_json().into_bytes();
    let signature = key.sign(&data).to_bytes().to_vec();
    let corrupt_manifest = Arc::new(AtomicBool::new(false));
    let corrupt_binary = Arc::new(AtomicBool::new(false));
    let corrupt_signature = Arc::new(AtomicBool::new(false));
    let (cm, cb, cs) = (
        corrupt_manifest.clone(),
        corrupt_binary.clone(),
        corrupt_signature.clone(),
    );
    let server = serve(move |path, stream| match path {
        "/latest.version" => ok(stream, b"1.6.0\n"),
        "/releases/v1.6.0/manifest.json" => {
            if cm.load(Ordering::SeqCst) {
                ok(stream, br#"{"version":"1.6.0","sha256":{}}"#)
            } else {
                ok(stream, &data)
            }
        }
        "/releases/v1.6.0/manifest.sig" => {
            if cs.load(Ordering::SeqCst) {
                ok(stream, &[0u8; 64])
            } else {
                ok(stream, &signature)
            }
        }
        _ => {
            if cb.load(Ordering::SeqCst) {
                ok(stream, b"tampered")
            } else {
                ok(stream, &payload)
            }
        }
    });
    let mut checker = Checker::new(&server.url, client());
    checker.key = Some(key.verifying_key());
    let ctx = Context::background();
    let verified = checker.latest(&ctx).unwrap();
    let got = checker.download(&ctx, &verified, &platform()).unwrap();
    assert_eq!(got, b"executable");

    corrupt_manifest.store(true, Ordering::SeqCst);
    assert!(
        checker.latest(&ctx).is_err(),
        "unverified manifest accepted"
    );
    corrupt_manifest.store(false, Ordering::SeqCst);
    corrupt_signature.store(true, Ordering::SeqCst);
    let err = checker.latest(&ctx).unwrap_err();
    assert_eq!(err.to_string(), "release signature verification failed");
    corrupt_signature.store(false, Ordering::SeqCst);
    corrupt_binary.store(true, Ordering::SeqCst);
    assert!(
        checker.download(&ctx, &verified, &platform()).is_err(),
        "bad executable accepted"
    );
    let err = checker.manifest(&ctx, "../../escape").unwrap_err();
    assert_eq!(err.to_string(), "invalid release version");
    assert!(!newer("1.5.9", "1.6.0") && !newer("1.6.0", "1.6.0") && newer("1.10.0", "1.6.0"));
}

fn test_key(seed: u8) -> SigningKey {
    SigningKey::from_bytes(&[seed; 32])
}

#[test]
fn update_preserves_executable_on_validation_and_setup_failure() {
    for stage in ["validate", "setup", "success"] {
        let dir = tempfile::tempdir().unwrap();
        let target = dir.path().join("bp");
        std::fs::write(&target, "old").unwrap();
        let validated = AtomicBool::new(false);
        let setup = AtomicBool::new(false);
        let result = replace(
            &target,
            b"new",
            |_path: &Path| -> Result<(), BoxError> {
                validated.store(true, Ordering::SeqCst);
                assert_eq!(
                    std::fs::read(&target).unwrap(),
                    b"old",
                    "replaced before validation"
                );
                if stage == "validate" {
                    return Err("invalid candidate".into());
                }
                Ok(())
            },
            || -> Result<(), BoxError> {
                setup.store(true, Ordering::SeqCst);
                if stage == "setup" {
                    return Err("setup failed".into());
                }
                Ok(())
            },
        );
        let got = std::fs::read(&target).unwrap();
        let backup = match &result {
            Ok(backup) => Some(backup.clone()),
            Err(e) => e.backup.clone(),
        };
        if stage == "success" {
            assert!(result.is_ok(), "{stage}: {result:?}");
            assert_eq!(got, b"new");
        } else {
            assert!(result.is_err(), "{stage}");
            assert_eq!(got, b"old", "{stage}");
        }
        assert!(validated.load(Ordering::SeqCst));
        assert_eq!(
            setup.load(Ordering::SeqCst),
            stage != "validate",
            "wrong order"
        );
        if let Some(backup) = backup {
            assert_eq!(std::fs::read(&backup).unwrap(), b"old", "backup lost");
            let name = backup.file_name().unwrap().to_string_lossy().into_owned();
            assert!(name.starts_with("bp.before-update-"), "{name}");
        }
        if stage == "setup" {
            let err = result.unwrap_err().to_string();
            assert!(err.contains("restored"), "{err}");
            assert_eq!(err, "setup failed; previous binary restored: setup failed");
        }
        // The candidate temp file never survives, the lock file stays.
        let leftovers: Vec<String> = std::fs::read_dir(dir.path())
            .unwrap()
            .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
            .filter(|n| n.starts_with(".bp-update-") || n.starts_with(".bp-restore-"))
            .collect();
        assert!(leftovers.is_empty(), "{leftovers:?}");
        assert!(dir.path().join(".bp-update.lock").exists());
    }
}

#[test]
fn replace_follows_symlinks_and_keeps_mode() {
    use std::os::unix::fs::PermissionsExt;
    let dir = tempfile::tempdir().unwrap();
    let real = dir.path().join("bp-real");
    std::fs::write(&real, "old").unwrap();
    std::fs::set_permissions(&real, std::fs::Permissions::from_mode(0o750)).unwrap();
    let link = dir.path().join("bp");
    std::os::unix::fs::symlink(&real, &link).unwrap();
    let backup = replace(&link, b"new", |_| Ok(()), || Ok(())).unwrap();
    assert!(
        std::fs::symlink_metadata(&link)
            .unwrap()
            .file_type()
            .is_symlink()
    );
    assert_eq!(std::fs::read(&real).unwrap(), b"new");
    let mode = std::fs::metadata(&real).unwrap().permissions().mode() & 0o777;
    assert_eq!(mode, 0o750);
    let mode = std::fs::metadata(&backup).unwrap().permissions().mode() & 0o777;
    assert_eq!(mode, 0o750);

    let not_regular = dir.path().join("d");
    std::fs::create_dir(&not_regular).unwrap();
    let err = replace(&not_regular, b"x", |_| Ok(()), || Ok(())).unwrap_err();
    assert_eq!(err.to_string(), "target is not a regular file");
}

// download_fix_test.go

#[test]
fn download_retries_body_stall_and_reports_attempt_bytes() {
    let payload = b"complete binary".to_vec();
    let manifest = manifest_for("1.0.0", &payload);
    let hits = Arc::new(AtomicI32::new(0));
    let counter = hits.clone();
    let body = payload.clone();
    let server = serve(move |_, stream| {
        if counter.fetch_add(1, Ordering::SeqCst) == 0 {
            head(stream, "200 OK", None, "");
            let _ = stream.write_all(b"part");
            std::thread::sleep(Duration::from_millis(80));
            let _ = stream.write_all(&body);
            return;
        }
        ok(stream, &body);
    });
    let mut checker = Checker::new(&server.url, client());
    checker.stall_timeout = Duration::from_millis(20);
    checker.download_retry = 1;
    checker.retry_backoff = Duration::from_millis(1);
    let got = checker
        .download(&Context::background(), &manifest, &platform())
        .unwrap();
    assert_eq!(got, payload);
    assert_eq!(
        hits.load(Ordering::SeqCst),
        2,
        "want retry after first stalled body"
    );
}

#[test]
fn download_uses_per_read_stall_limit_and_reports_url_and_bytes() {
    let payload = b"abc".to_vec();
    let mut manifest = manifest_for("1.0.0", &payload);
    let server = serve(|_, stream| {
        head(stream, "200 OK", None, "");
        for part in [b"a", b"b", b"c"] {
            let _ = stream.write_all(part);
            std::thread::sleep(Duration::from_millis(20));
        }
    });
    let mut checker = Checker::new(&server.url, client());
    checker.stall_timeout = Duration::from_millis(250);
    let got = checker
        .download(&Context::background(), &manifest, &platform())
        .unwrap();
    assert_eq!(got, payload, "progressing body failed");

    let stalling = serve(|_, stream| {
        head(stream, "200 OK", None, "");
        let _ = stream.write_all(b"four");
        std::thread::sleep(Duration::from_millis(100));
    });
    checker.base = stalling.url.clone();
    checker.stall_timeout = Duration::from_millis(15);
    manifest.sha256.insert(platform(), sha256_hex(b"wrong"));
    let err = checker
        .download(&Context::background(), &manifest, &platform())
        .unwrap_err()
        .to_string();
    let url = format!("{}/releases/v1.0.0/{}", stalling.url, platform());
    assert!(
        err.contains(&url) && err.contains("after 4 bytes"),
        "stall error={err}"
    );
    assert!(
        err.starts_with(&format!(
            "download {url} failed after 1 attempts: GET {url} after 4 bytes: stalled after 15ms"
        )),
        "{err}"
    );
}

#[test]
fn download_reports_content_length_progress_when_body_stalls() {
    const TOTAL: usize = 35_000_000;
    let payload = vec![b'x'; 2_000_000];
    let manifest = manifest_for("1.0.0", &payload);
    let body = payload.clone();
    let server = serve(move |_, stream| {
        head(stream, "200 OK", Some(TOTAL), "");
        if stream.write_all(&body).is_err() {
            return;
        }
        std::thread::sleep(Duration::from_secs(2));
    });
    let mut checker = Checker::new(&server.url, client());
    checker.stall_timeout = Duration::from_millis(500);
    let err = checker
        .download(&Context::background(), &manifest, &platform())
        .unwrap_err()
        .to_string();
    assert!(
        err.contains("2 MB of 35 MB") && err.contains("stalled after 500ms"),
        "download error={err}, want stalled transfer progress"
    );
}

// install_host_test.go

#[test]
fn live_release_references_use_current_host() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let legacy_host = format!("bp.{}", "trasumanar.ai");
    let mut legacy = Vec::new();
    walk(&root, &root, legacy_host.as_bytes(), &mut legacy);
    assert!(
        legacy.is_empty(),
        "live files still reference the retired release host {legacy_host}: {}",
        legacy.join(", ")
    );
    for (index, relative) in ["install.sh", "site/install.sh"].iter().enumerate() {
        let data = match std::fs::read(root.join(relative)) {
            Ok(data) => data,
            Err(e) if index == 1 && e.kind() == std::io::ErrorKind::NotFound => continue,
            Err(e) => panic!("{relative}: {e}"),
        };
        let needle = format!("local_base={}", release::BASE_URL);
        assert!(
            data.windows(needle.len()).any(|w| w == needle.as_bytes()),
            "{relative} does not use the release host {}",
            release::BASE_URL
        );
    }
}

fn walk(root: &Path, dir: &Path, needle: &[u8], found: &mut Vec<String>) {
    for entry in std::fs::read_dir(dir).unwrap() {
        let entry = entry.unwrap();
        let path = entry.path();
        let relative = path
            .strip_prefix(root)
            .unwrap()
            .to_string_lossy()
            .into_owned();
        let kind = entry.file_type().unwrap();
        if kind.is_dir() {
            // `target` holds Cargo build output (Rust-only addition).
            if relative == ".git" || relative == "site/releases" || relative == "target" {
                continue;
            }
            walk(root, &path, needle, found);
        } else if kind.is_file() {
            let data = std::fs::read(&path).unwrap();
            if data.windows(needle.len()).any(|w| w == needle) {
                found.push(relative);
            }
        }
    }
}

// issue_repro_test.go: a client-wide timeout is a total request deadline and
// cancels the body read even while the server makes progress.
#[test]
fn issue04_release_download_has_no_whole_body_deadline() {
    assert_eq!(ReqwestClient::TOTAL_TIMEOUT, None);
    // Behavioral check: a body slower than any short total deadline but never
    // stalling for longer than the stall limit still downloads.
    let payload: Vec<u8> = b"0123456789".to_vec();
    let manifest = manifest_for("1.0.0", &payload);
    let body = payload.clone();
    let server = serve(move |_, stream| {
        head(stream, "200 OK", Some(body.len()), "");
        for b in &body {
            let _ = stream.write_all(&[*b]);
            std::thread::sleep(Duration::from_millis(30));
        }
    });
    let mut checker = Checker::new(&server.url, client());
    checker.stall_timeout = Duration::from_millis(200);
    let got = checker
        .download(&Context::background(), &manifest, &platform())
        .unwrap();
    assert_eq!(got, payload);
}

// Rust-only coverage of Go behavior not pinned by Go tests.

#[test]
fn default_checker_uses_embedded_key_and_go_defaults() {
    let checker = Checker::default_checker();
    assert_eq!(checker.base, "https://bp.tunapro.xyz");
    assert_eq!(checker.stall_timeout, Duration::from_secs(20));
    assert_eq!(checker.download_retry, 2);
    assert_eq!(checker.retry_backoff, Duration::from_millis(250));
    assert_eq!(
        hex::encode(checker.key.unwrap().to_bytes()),
        "b3bd8247941376b2c241f0d2e9d1e1fe281b3882fa830e5ac86b9f0879c00d4c"
    );
}

#[test]
fn redirects_must_stay_on_https() {
    let target = serve(|_, stream| ok(stream, b"1.0.0"));
    let location = format!("Location: {}/latest.version\r\n", target.url);
    let server = serve(move |_, stream| head(stream, "302 Found", Some(0), &location));
    let checker = Checker::new(&server.url, client());
    let err = checker
        .latest(&Context::background())
        .unwrap_err()
        .to_string();
    assert!(err.contains("non-HTTPS redirect refused"), "{err}");
    assert!(
        err.starts_with(&format!(
            "GET {}/latest.version after 0 bytes: ",
            server.url
        )),
        "{err}"
    );
}

#[test]
fn http_status_and_size_limits() {
    let server = serve(|path, stream| match path {
        "/latest.version" => ok(stream, &[b'1'; 129]),
        _ => head(stream, "404 Not Found", Some(0), ""),
    });
    let checker = Checker::new(&server.url, client());
    let ctx = Context::background();
    let err = checker.latest(&ctx).unwrap_err().to_string();
    assert_eq!(
        err,
        format!(
            "GET {}/latest.version after 129 bytes: release response too large",
            server.url
        )
    );
    let err = checker.manifest(&ctx, "1.2.3").unwrap_err().to_string();
    assert_eq!(
        err,
        format!(
            "GET {}/releases/v1.2.3/manifest.json after 0 bytes: HTTP 404",
            server.url
        )
    );
    let missing = Manifest {
        version: "1.0.0".into(),
        ..Manifest::default()
    };
    let err = checker
        .download(&ctx, &missing, "bp-x")
        .unwrap_err()
        .to_string();
    assert_eq!(err, "release does not contain bp-x");
}

#[test]
fn manifest_rejects_bad_checksum_entries_and_version_mismatch() {
    let key = test_key(9);
    let cases: Vec<(&str, &str)> = vec![
        (
            r#"{"version":"1.0.1","sha256":{}}"#,
            "release version mismatch",
        ),
        (
            r#"{"version":"1.0.0","sha256":{"a/b":"00"}}"#,
            "invalid release checksum entry",
        ),
        (
            r#"{"version":"1.0.0","sha256":{"bp":"zz00000000000000000000000000000000000000000000000000000000000000"}}"#,
            "encoding/hex: invalid byte: U+007A 'z'",
        ),
        (
            r#"{"Version":"1.0.0","SHA256":{"bp":"AA00000000000000000000000000000000000000000000000000000000000000"}}"#,
            "",
        ),
    ];
    for (body, want) in cases {
        let data = body.as_bytes().to_vec();
        let signature = key.sign(&data).to_bytes().to_vec();
        let server = serve(move |path, stream| {
            if path.ends_with(".sig") {
                ok(stream, &signature)
            } else {
                ok(stream, &data)
            }
        });
        let mut checker = Checker::new(&server.url, client());
        checker.key = Some(key.verifying_key());
        let result = checker.manifest(&Context::background(), "1.0.0");
        if want.is_empty() {
            let m = result.unwrap();
            assert_eq!(m.sha256.len(), 1, "case-insensitive keys like Go");
        } else {
            assert_eq!(result.unwrap_err().to_string(), want, "{body}");
        }
    }
}

#[test]
fn context_deadline_stops_a_slow_body() {
    let server = serve(|_, stream| {
        head(stream, "200 OK", None, "");
        for _ in 0..50 {
            if stream.write_all(b"x").is_err() {
                return;
            }
            std::thread::sleep(Duration::from_millis(20));
        }
    });
    let checker = Checker::new(&server.url, client());
    let ctx = Context::background().with_timeout(Duration::from_millis(150));
    let err = checker.latest(&ctx).unwrap_err().to_string();
    assert!(err.contains("context deadline exceeded"), "{err}");
}

#[test]
fn download_canceled_during_backoff() {
    let server = serve(|_, stream| ok(stream, b"wrong"));
    let mut checker = Checker::new(&server.url, client());
    checker.download_retry = 3;
    checker.retry_backoff = Duration::from_secs(5);
    let manifest = manifest_for("1.0.0", b"right");
    let flag = Arc::new(AtomicBool::new(false));
    let ctx = Context::background().with_cancel_flag(flag.clone());
    let setter = std::thread::spawn(move || {
        std::thread::sleep(Duration::from_millis(100));
        flag.store(true, Ordering::SeqCst);
    });
    let err = checker
        .download(&ctx, &manifest, &platform())
        .unwrap_err()
        .to_string();
    setter.join().unwrap();
    let url = format!("{}/releases/v1.0.0/{}", server.url, platform());
    assert_eq!(
        err,
        format!("download {url} canceled after attempt 1: context canceled")
    );
}

#[test]
fn connection_errors_name_the_url_like_go() {
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let base = format!("http://{}", listener.local_addr().unwrap());
    drop(listener);
    let checker = Checker::new(&base, client());
    let err = checker
        .latest(&Context::background())
        .unwrap_err()
        .to_string();
    let url = format!("{base}/latest.version");
    assert!(
        err.starts_with(&format!("GET {url} after 0 bytes: Get \"{url}\": ")),
        "{err}"
    );
}
