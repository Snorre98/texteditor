//! Port discovery (ADR-0021 §1, ADR-0046 §7).
//!
//! The engine's base URL is resolved in precedence order:
//!   1. `ENGINE_URL`  — a full base URL override (web/LAN target, ADR-0014).
//!   2. `ENGINE_PORT` — a fixed numeric port on `127.0.0.1` (ADR-0021 §1 fixed mode).
//!   3. the spec's `servers[0]` default (`http://127.0.0.1:9100`, api/openapi.yaml).
//!
//! The resolved URL is then verified with a `GET /health` probe. When the engine
//! advertises a `baseUrl` (dynamic mode, ADR-0021 §1) the advertised URL is
//! adopted, so the client discovers rather than assumes. This mirrors the
//! OpenTUI client's `api/discovery.ts` semantics (the reference implementation).
//!
//! The probe is generic over a closure so URL precedence and adoption are
//! unit-testable with no live engine and no mock HTTP dependency.

use std::time::Duration;

use crate::gen::{HealthStatus, HttpClient};

/// The spec's `servers[0]` default (api/openapi.yaml).
pub const DEFAULT_BASE_URL: &str = "http://127.0.0.1:9100";
/// The health-probe timeout, matching the OpenTUI client's 1500 ms.
pub const DEFAULT_PROBE_TIMEOUT_MS: u64 = 1500;

/// The two relevant discovery env keys.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct EngineEnv {
    pub engine_url: Option<String>,
    pub engine_port: Option<String>,
}

impl EngineEnv {
    /// Read `ENGINE_URL`/`ENGINE_PORT` from the process environment.
    pub fn from_process_env() -> Self {
        Self {
            engine_url: std::env::var("ENGINE_URL").ok(),
            engine_port: std::env::var("ENGINE_PORT").ok(),
        }
    }
}

/// A labeled discovery failure — never a silent fallback.
#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum DiscoveryError {
    #[error("ENGINE_PORT must be a numeric port, got {0:?}")]
    NonNumericPort(String),
    #[error(
        "engine unreachable at {base_url} — is it running? \
         (start it with `go run ./cmd/texteditor`, or set ENGINE_PORT/ENGINE_URL \
         to the actual endpoint)"
    )]
    Unreachable { base_url: String },
}

/// Resolve the candidate base URL from the environment (no network).
pub fn resolve_base_url(env: &EngineEnv) -> Result<String, DiscoveryError> {
    if let Some(raw) = env.engine_url.as_deref() {
        let url = raw.trim();
        if !url.is_empty() {
            return Ok(strip_trailing_slash(url));
        }
    }
    if let Some(raw) = env.engine_port.as_deref() {
        let port = raw.trim();
        if !port.is_empty() {
            if !port.bytes().all(|b| b.is_ascii_digit()) {
                return Err(DiscoveryError::NonNumericPort(port.to_string()));
            }
            return Ok(format!("http://127.0.0.1:{port}"));
        }
    }
    Ok(DEFAULT_BASE_URL.to_string())
}

/// Resolve and verify the base URL using `probe`. `probe` receives the resolved
/// candidate and returns the base URL to adopt (`Some`) when the engine answers
/// `/health` with `status: ok`, or `None` otherwise. When the engine advertises
/// its own `baseUrl`, that is the adopted value (ADR-0021 §1).
pub async fn discover_with<F, Fut>(env: &EngineEnv, probe: F) -> Result<String, DiscoveryError>
where
    F: FnOnce(String) -> Fut,
    Fut: std::future::Future<Output = Option<String>>,
{
    let base_url = resolve_base_url(env)?;
    match probe(base_url.clone()).await {
        Some(adopted) => Ok(adopted),
        None => Err(DiscoveryError::Unreachable { base_url }),
    }
}

/// Discover with the real generated client: resolve, then probe `/health`.
pub async fn discover(env: &EngineEnv) -> Result<String, DiscoveryError> {
    discover_with(env, |base_url| async move {
        probe_health(&base_url, DEFAULT_PROBE_TIMEOUT_MS).await
    })
    .await
}

/// Probe `/health` through the generated client, bounded by `timeout_ms`.
/// Returns the adopted base URL when the engine is healthy.
pub async fn probe_health(base_url: &str, timeout_ms: u64) -> Option<String> {
    let fut = probe_health_inner(base_url.to_string());
    tokio::time::timeout(Duration::from_millis(timeout_ms), fut)
        .await
        .unwrap_or_default()
}

async fn probe_health_inner(base_url: String) -> Option<String> {
    let client = HttpClient::new().with_base_url(base_url.clone());
    match client.get_health().await {
        Ok(health) => {
            if health.status != HealthStatus::Ok {
                return None;
            }
            match health.base_url {
                Some(advertised) if !advertised.trim().is_empty() => {
                    Some(strip_trailing_slash(advertised.trim()))
                }
                // A healthy engine with no advertised URL: keep the resolved one.
                _ => Some(base_url),
            }
        }
        Err(_) => None,
    }
}

fn strip_trailing_slash(url: &str) -> String {
    url.strip_suffix('/').unwrap_or(url).to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn env(url: Option<&str>, port: Option<&str>) -> EngineEnv {
        EngineEnv {
            engine_url: url.map(str::to_string),
            engine_port: port.map(str::to_string),
        }
    }

    #[test]
    fn engine_url_wins_and_strips_trailing_slash() {
        let got = resolve_base_url(&env(Some("http://lan:9100/"), Some("1234"))).unwrap();
        assert_eq!(got, "http://lan:9100");
    }

    #[test]
    fn engine_url_whitespace_falls_through() {
        let got = resolve_base_url(&env(Some("   "), Some("1234"))).unwrap();
        assert_eq!(got, "http://127.0.0.1:1234");
    }

    #[test]
    fn numeric_engine_port_uses_loopback() {
        let got = resolve_base_url(&env(None, Some(" 9100 "))).unwrap();
        assert_eq!(got, "http://127.0.0.1:9100");
    }

    #[test]
    fn non_numeric_engine_port_is_labeled() {
        let err = resolve_base_url(&env(None, Some("http://x"))).unwrap_err();
        assert_eq!(err, DiscoveryError::NonNumericPort("http://x".to_string()));
    }

    #[test]
    fn default_when_unset() {
        assert_eq!(
            resolve_base_url(&env(None, None)).unwrap(),
            DEFAULT_BASE_URL
        );
    }

    #[tokio::test]
    async fn discover_adopts_advertised_base_url() {
        let got = discover_with(&env(None, None), |_url| async {
            Some("http://127.0.0.1:9500".to_string())
        })
        .await
        .unwrap();
        assert_eq!(got, "http://127.0.0.1:9500");
    }

    #[tokio::test]
    async fn discover_unreachable_is_labeled_with_resolved_url() {
        let err = discover_with(&env(Some("http://127.0.0.1:9999"), None), |_url| async {
            None
        })
        .await
        .unwrap_err();
        assert_eq!(
            err,
            DiscoveryError::Unreachable {
                base_url: "http://127.0.0.1:9999".to_string()
            }
        );
    }

    #[tokio::test]
    async fn discover_rejects_non_numeric_port_before_probing() {
        // The probe is never called when resolution fails.
        let err = discover_with(&env(None, Some("nope")), |_url| async {
            panic!("probe must not run when resolution fails")
        })
        .await
        .unwrap_err();
        assert_eq!(err, DiscoveryError::NonNumericPort("nope".to_string()));
    }
}
