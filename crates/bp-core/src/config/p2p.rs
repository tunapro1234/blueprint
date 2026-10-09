//! Port of internal/p2p/config.go (the configuration half), plus the parts of
//! go-libp2p `peer.Decode` and go-multiaddr `NewMultiaddr` that its
//! `Validate` relies on, so `bp-core` can validate a config without libp2p.

use std::collections::{BTreeMap, HashSet};
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};

use serde::{Deserialize, Serialize};

/// The `p2p:` config block.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Config {
    pub enabled: bool,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub listen: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub advertise: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub rendezvous: Vec<String>,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub relay: bool,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub mdns: bool,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub peers: BTreeMap<String, Peer>,
}

/// One allowed peer.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Peer {
    pub id: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub addresses: Vec<String>,
    /// Empty exposes nothing. Discovery and connectivity never grant agent access.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub expose: Vec<String>,
}

/// Go `p2p.ValidName`.
pub fn valid_name(s: &str) -> bool {
    if s.is_empty() || s == "." || s == ".." || s.len() > 64 {
        return false;
    }
    s.chars()
        .all(|c| c.is_ascii_alphanumeric() || c == '.' || c == '_' || c == '-')
}

impl Config {
    /// Go `Config.Validate`. Peers are checked in sorted alias order (Go
    /// iterates a map, so with several bad peers the reported one may differ).
    pub fn validate(&self) -> Result<(), String> {
        let mut ids = HashSet::new();
        for (name, p) in &self.peers {
            if !valid_name(name) {
                return Err(format!("invalid p2p peer alias {}", crate::gojson::time::go_quote(name)));
            }
            let id = decode_peer_id(&p.id).map_err(|_| format!("peer {name}: invalid Peer ID"))?;
            if !ids.insert(id) {
                return Err(format!("duplicate p2p peer identity: {name}"));
            }
            if p.expose.iter().any(|a| !valid_name(a)) {
                return Err(format!("peer {name}: invalid exposed agent"));
            }
            for a in &p.addresses {
                parse_multiaddr(a).map_err(|e| format!("peer {name}: invalid multiaddress: {e}"))?;
            }
        }
        for a in self.listen.iter().chain(&self.advertise) {
            parse_multiaddr(a).map_err(|e| format!("invalid p2p address: {e}"))?;
        }
        for a in &self.rendezvous {
            addr_info_from_string(a).map_err(|e| {
                format!("rendezvous needs a full multiaddress including /p2p/PeerID: {e}")
            })?;
        }
        Ok(())
    }
}

// ---- peer IDs (go-libp2p core/peer.Decode) ----

/// Decodes a textual peer ID (base58 multihash or CIDv1 `libp2p-key`) to its
/// multihash bytes, like go-libp2p `peer.Decode`.
pub fn decode_peer_id(s: &str) -> Result<Vec<u8>, String> {
    if s.starts_with("Qm") || s.starts_with('1') {
        let bytes = base58_decode(s).ok_or("failed to parse peer ID: input isn't valid multihash")?;
        multihash_cast(&bytes).map_err(|e| format!("failed to parse peer ID: {e}"))?;
        return Ok(bytes);
    }
    let (codec, hash) = cid_decode(s).map_err(|e| format!("failed to parse peer ID: {e}"))?;
    if codec != 0x72 {
        return Err(format!("can't convert CID of type {codec:#x} to a peer ID"));
    }
    Ok(hash)
}

fn base58_decode(s: &str) -> Option<Vec<u8>> {
    const ALPHABET: &[u8] = b"123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz";
    if s.is_empty() {
        return None;
    }
    let mut out: Vec<u8> = Vec::new();
    for c in s.bytes() {
        let mut carry = ALPHABET.iter().position(|&a| a == c)? as u32;
        for b in out.iter_mut().rev() {
            carry += u32::from(*b) * 58;
            *b = (carry & 0xff) as u8;
            carry >>= 8;
        }
        while carry > 0 {
            out.insert(0, (carry & 0xff) as u8);
            carry >>= 8;
        }
    }
    let zeros = s.bytes().take_while(|&c| c == b'1').count();
    let mut result = vec![0u8; zeros];
    result.extend(out);
    Some(result)
}

fn uvarint(buf: &[u8]) -> Result<(u64, &[u8]), String> {
    let mut x: u64 = 0;
    let mut s = 0u32;
    for (i, &b) in buf.iter().enumerate() {
        if i == 9 && b > 1 {
            return Err("varints larger than uint63 not supported".into());
        }
        if b < 0x80 {
            if i > 0 && b == 0 {
                return Err("varint not minimally encoded".into());
            }
            return Ok((x | (u64::from(b) << s), &buf[i + 1..]));
        }
        x |= u64::from(b & 0x7f) << s;
        s += 7;
    }
    Err("varints malformed, could not reach the end".into())
}

/// go-multihash `Cast`: returns (code, digest length).
fn multihash_cast(buf: &[u8]) -> Result<(u64, usize), String> {
    if buf.len() < 2 {
        return Err("multihash too short. must be >= 2 bytes".into());
    }
    let (code, rest) = uvarint(buf)?;
    let (length, rest) = uvarint(rest)?;
    if length > i32::MAX as u64 {
        return Err("digest too long, supporting only <= 2^31-1".into());
    }
    if length as usize > rest.len() {
        return Err("length greater than remaining number of bytes in buffer".into());
    }
    if length as usize != rest.len() {
        return Err("multihash length inconsistent".into());
    }
    Ok((code, length as usize))
}

fn multibase_decode(s: &str) -> Result<Vec<u8>, String> {
    let mut chars = s.chars();
    let prefix = chars.next().ok_or("cannot decode multibase for zero length string")?;
    let body = chars.as_str();
    match prefix {
        'z' => base58_decode(body).ok_or_else(|| "invalid base58".to_string()),
        'b' => base32_decode(&body.to_ascii_uppercase()),
        'B' => base32_decode(body),
        'f' | 'F' => hex_decode(body),
        'k' | 'K' => base36_decode(&body.to_ascii_lowercase()),
        'm' => base64_decode(body, false),
        'u' => base64_decode(body, true),
        _ => Err(format!("selected encoding not supported")),
    }
}

fn base32_decode(s: &str) -> Result<Vec<u8>, String> {
    const ALPHABET: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
    let mut bits = 0u32;
    let mut acc = 0u64;
    let mut out = Vec::new();
    for c in s.bytes() {
        let v = ALPHABET.iter().position(|&a| a == c).ok_or("illegal base32 data")? as u64;
        acc = (acc << 5) | v;
        bits += 5;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
            acc &= (1 << bits) - 1;
        }
    }
    if bits >= 5 || acc != 0 {
        return Err("illegal base32 data".into());
    }
    Ok(out)
}

fn base36_decode(s: &str) -> Result<Vec<u8>, String> {
    const ALPHABET: &[u8] = b"0123456789abcdefghijklmnopqrstuvwxyz";
    if s.is_empty() {
        return Err("can not decode zero-length string".into());
    }
    let mut out: Vec<u8> = Vec::new();
    for c in s.bytes() {
        let mut carry = ALPHABET.iter().position(|&a| a == c).ok_or("invalid base36")? as u32;
        for b in out.iter_mut().rev() {
            carry += u32::from(*b) * 36;
            *b = (carry & 0xff) as u8;
            carry >>= 8;
        }
        while carry > 0 {
            out.insert(0, (carry & 0xff) as u8);
            carry >>= 8;
        }
    }
    let zeros = s.bytes().take_while(|&c| c == b'0').count();
    let mut result = vec![0u8; zeros];
    result.extend(out);
    Ok(result)
}

fn hex_decode(s: &str) -> Result<Vec<u8>, String> {
    if s.len() % 2 != 0 {
        return Err("odd length hex".into());
    }
    (0..s.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(s.get(i..i + 2).ok_or("invalid hex")?, 16).map_err(|e| e.to_string()))
        .collect()
}

fn base64_decode(s: &str, url: bool) -> Result<Vec<u8>, String> {
    let alphabet: &[u8] = if url {
        b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
    } else {
        b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
    };
    let mut bits = 0u32;
    let mut acc = 0u32;
    let mut out = Vec::new();
    for c in s.bytes() {
        let v = alphabet.iter().position(|&a| a == c).ok_or("illegal base64 data")? as u32;
        acc = (acc << 6) | v;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
            acc &= (1 << bits) - 1;
        }
    }
    Ok(out)
}

/// go-cid `Decode`: returns (codec, multihash bytes).
fn cid_decode(s: &str) -> Result<(u64, Vec<u8>), String> {
    if s.len() < 2 {
        return Err("cid too short".into());
    }
    if s.len() == 46 && s.starts_with("Qm") {
        let hash = base58_decode(s).ok_or("invalid cid")?;
        multihash_cast(&hash)?;
        return Ok((0x70, hash));
    }
    let data = multibase_decode(s)?;
    if data.len() == 34 && data[0] == 0x12 && data[1] == 0x20 {
        return Ok((0x70, data));
    }
    let (version, rest) = uvarint(&data)?;
    if version != 1 {
        return Err(format!("expected 1 as the cid version number, got: {version}"));
    }
    let (codec, rest) = uvarint(rest)?;
    multihash_cast(rest)?;
    Ok((codec, rest.to_vec()))
}

// ---- multiaddrs (go-multiaddr NewMultiaddr) ----

#[derive(Clone, Copy, PartialEq)]
enum Value {
    None,
    Ip4,
    Ip6,
    Port,
    Dns,
    Ip6Zone,
    IpCidr,
    P2p,
    Unix,
    CertHash,
    HttpPath,
    Memory,
    Onion,
    Onion3,
    Garlic,
}

fn protocol(name: &str) -> Option<Value> {
    Some(match name {
        "ip4" => Value::Ip4,
        "ip6" => Value::Ip6,
        "tcp" | "udp" | "dccp" | "sctp" => Value::Port,
        "dns" | "dns4" | "dns6" | "dnsaddr" | "sni" => Value::Dns,
        "ip6zone" => Value::Ip6Zone,
        "ipcidr" => Value::IpCidr,
        "p2p" | "ipfs" => Value::P2p,
        "unix" => Value::Unix,
        "certhash" => Value::CertHash,
        "http-path" => Value::HttpPath,
        "memory" => Value::Memory,
        "onion" => Value::Onion,
        "onion3" => Value::Onion3,
        "garlic64" | "garlic32" => Value::Garlic,
        "p2p-circuit" | "utp" | "udt" | "quic" | "quic-v1" | "webtransport" | "http" | "https"
        | "p2p-webrtc-direct" | "tls" | "noise" | "plaintextv2" | "ws" | "wss" | "webrtc-direct"
        | "webrtc" => Value::None,
        _ => return None,
    })
}

/// A parsed multiaddr: the `(protocol, value)` components in order.
pub type Multiaddr = Vec<(String, String)>;

/// Validates a textual multiaddr like go-multiaddr `NewMultiaddr`, with the
/// same error wording for the common protocols.
pub fn parse_multiaddr(input: &str) -> Result<Multiaddr, String> {
    let s = input.trim_end_matches('/');
    let mut sp: Vec<&str> = s.split('/').collect();
    if sp[0] != "" {
        return Err(format!("failed to parse multiaddr {s:?}: must begin with /"));
    }
    sp.remove(0);
    if sp.is_empty() {
        return Err(format!("failed to parse multiaddr {s:?}: empty multiaddr"));
    }
    let mut out = Vec::new();
    let mut i = 0;
    while i < sp.len() {
        let name = sp[i];
        let kind = protocol(name)
            .ok_or_else(|| format!("failed to parse multiaddr {s:?}: unknown protocol {name}"))?;
        i += 1;
        if kind == Value::None {
            out.push((name.to_string(), String::new()));
            continue;
        }
        if i >= sp.len() {
            return Err(format!("failed to parse multiaddr {s:?}: unexpected end of multiaddr"));
        }
        let value = if kind == Value::Unix {
            let v = format!("/{}", sp[i..].join("/"));
            i = sp.len();
            v
        } else {
            let v = sp[i].to_string();
            i += 1;
            v
        };
        check_value(kind, &value).map_err(|e| {
            format!("failed to parse multiaddr {s:?}: invalid value {value:?} for protocol {name}: {e}")
        })?;
        out.push((name.to_string(), value));
    }
    Ok(out)
}

fn check_value(kind: Value, v: &str) -> Result<(), String> {
    match kind {
        Value::None => Ok(()),
        Value::Ip4 => match parse_ip(v) {
            Some(IpAddr::V4(_)) => Ok(()),
            Some(IpAddr::V6(a)) if a.to_ipv4_mapped().is_some() => Ok(()),
            _ => Err(format!("failed to parse ip4 addr: {v}")),
        },
        Value::Ip6 => parse_ip(v)
            .map(|_| ())
            .ok_or_else(|| format!("failed to parse ip6 addr: {v}")),
        Value::Port => v
            .parse::<u16>()
            .map(|_| ())
            .map_err(|_| format!("failed to parse port addr: strconv.ParseUint: parsing {v:?}: invalid syntax")),
        Value::Dns => {
            if v.is_empty() {
                Err("empty dns addr".into())
            } else {
                Ok(())
            }
        }
        Value::Ip6Zone => {
            if v.is_empty() {
                Err("empty ip6zone".into())
            } else {
                Ok(())
            }
        }
        Value::IpCidr => v.parse::<u8>().map(|_| ()).map_err(|e| e.to_string()),
        Value::P2p => {
            let hash = if v.starts_with("Qm") || v.starts_with('1') {
                let b = base58_decode(v).ok_or_else(|| format!("failed to parse p2p addr: {v} invalid multihash"))?;
                multihash_cast(&b).map_err(|e| format!("failed to parse p2p addr: {v} {e}"))?;
                b
            } else {
                let (codec, hash) = cid_decode(v).map_err(|e| format!("failed to parse p2p addr: {v} {e}"))?;
                if codec != 0x72 {
                    return Err(format!("failed to parse p2p addr: {v} has the invalid codec {codec}"));
                }
                hash
            };
            let (code, len) = multihash_cast(&hash).map_err(|e| format!("invalid multihash: {e}"))?;
            if code != 0x12 && code != 0x00 {
                return Err(format!("invalid multihash code {code} expected sha-256 or identity"));
            }
            if code == 0x12 && len != 32 {
                return Err(format!("invalid digest length {len} for sha256 addr: expected 32"));
            }
            Ok(())
        }
        Value::Unix => {
            if v.len() < 2 {
                Err(format!("byte slice too short: {}", v.len()))
            } else if v.ends_with('/') {
                Err("unix socket path must not end in '/'".into())
            } else {
                Ok(())
            }
        }
        Value::CertHash => {
            let data = multibase_decode(v)?;
            multihash_cast(&data).map(|_| ())
        }
        Value::HttpPath => {
            let unescaped = query_unescape(v)?;
            if unescaped.is_empty() {
                Err("empty http path is not allowed".into())
            } else {
                Ok(())
            }
        }
        Value::Memory => v.parse::<u64>().map(|_| ()).map_err(|e| e.to_string()),
        Value::Onion => {
            let (host, port) = v
                .split_once(':')
                .filter(|(_, p)| !p.contains(':'))
                .ok_or_else(|| format!("failed to parse onion addr: {v} does not contain a port number"))?;
            if host.len() != 16 {
                return Err(format!("failed to parse onion addr: {v} not a Tor onion address."));
            }
            base32_decode(&host.to_ascii_uppercase())?;
            check_onion_port(v, port)
        }
        Value::Onion3 => {
            let (host, port) = v
                .split_once(':')
                .filter(|(_, p)| !p.contains(':'))
                .ok_or_else(|| format!("failed to parse onion addr: {v} does not contain a port number"))?;
            if host.len() != 56 {
                return Err(format!("failed to parse onion addr: {v} not a Tor onionv3 address. len == {}", host.len()));
            }
            base32_decode(&host.to_ascii_uppercase())?;
            check_onion_port(v, port)
        }
        Value::Garlic => {
            if v.is_empty() {
                Err("empty garlic address".into())
            } else {
                Ok(())
            }
        }
    }
}

fn check_onion_port(v: &str, port: &str) -> Result<(), String> {
    match port.parse::<u16>() {
        Ok(0) => Err(format!("failed to parse onion addr: {v} port less than 1")),
        Ok(_) => Ok(()),
        Err(_) => Err(format!("failed to parse onion addr: {v} invalid port")),
    }
}

/// Go `net.ParseIP` (no zones, IPv4 dotted decimal without leading zeros).
pub fn parse_ip(s: &str) -> Option<IpAddr> {
    if let Ok(v4) = s.parse::<Ipv4Addr>() {
        return Some(IpAddr::V4(v4));
    }
    if s.contains(':') {
        return s.parse::<Ipv6Addr>().ok().map(IpAddr::V6);
    }
    None
}

fn query_unescape(s: &str) -> Result<String, String> {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        match b[i] {
            b'%' => {
                let hex = s.get(i + 1..i + 3).filter(|h| h.bytes().all(|c| c.is_ascii_hexdigit()));
                let Some(hex) = hex else {
                    let end = (i + 3).min(s.len());
                    return Err(format!("invalid URL escape {:?}", &s[i..end]));
                };
                out.push(u8::from_str_radix(hex, 16).expect("hex digits"));
                i += 3;
            }
            b'+' => {
                out.push(b' ');
                i += 1;
            }
            c => {
                out.push(c);
                i += 1;
            }
        }
    }
    Ok(String::from_utf8_lossy(&out).into_owned())
}

/// go-libp2p `peer.AddrInfoFromString`: the address must end in `/p2p/<id>`.
pub fn addr_info_from_string(s: &str) -> Result<(), String> {
    let addr = parse_multiaddr(s)?;
    match addr.last() {
        Some((name, _)) if name == "p2p" || name == "ipfs" => Ok(()),
        _ => Err("invalid p2p multiaddr".into()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn peer_ids() {
        assert!(decode_peer_id("12D3KooWD3eckifWpRn9wQpMG9R9hX3sD158z7EqHWmweQAJU5SA").is_ok());
        assert!(decode_peer_id("QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N").is_ok());
        assert!(decode_peer_id("not-a-peer-id").is_err());
        assert!(decode_peer_id("12D3KooWD3eckifWpRn9wQpMG9R9hX3sD158z7EqHWmweQAJU5S").is_err());
    }

    #[test]
    fn multiaddrs() {
        for ok in [
            "/ip4/127.0.0.1/tcp/0",
            "/ip6/::/udp/4001/quic-v1",
            "/dns4/example.com/tcp/443/wss",
            "/unix/tmp/sock",
            "/ip4/1.2.3.4/tcp/1/",
        ] {
            parse_multiaddr(ok).unwrap();
        }
        for bad in ["https://example.com", "/ip4/1.2.3/tcp/1", "/ip4/1.2.3.4/tcp/65536", "/", "/nope/1", "/ip4"] {
            assert!(parse_multiaddr(bad).is_err(), "{bad}");
        }
    }
}
