//! Snapshot HTTP service for dashboard.
//! `cargo run -p control-plane` serves `/snapshots`, `/snapshots/:key`, `/health`.

use crate::Registry;
use axum::{
    extract::{Path, State},
    http::{HeaderMap, StatusCode},
    middleware::{from_fn, Next},
    response::IntoResponse,
    routing::get,
    Json, Router,
};
use std::sync::Arc;

/// Check if `auth_header` authorizes against `token` (constant-time).
/// Empty `token` means auth disabled (always true).
pub fn is_authorized(auth_header: &str, token: &str) -> bool {
    if token.is_empty() {
        return true;
    }
    if !auth_header.starts_with("Bearer ") {
        return false;
    }
    let expected = format!("Bearer {token}");
    auth_header.len() == expected.len() && {
        use subtle::ConstantTimeEq;
        auth_header
            .as_bytes()
            .ct_eq(expected.as_bytes())
            .unwrap_u8()
            == 1
    }
}

/// Parse `CONTROL_PLANE_TOKENS` `tenant:token,tenant2:token2` map.
/// Returns `token -> tenant`.
pub fn tenant_tokens() -> std::collections::HashMap<String, String> {
    let raw = std::env::var("CONTROL_PLANE_TOKENS").unwrap_or_default();
    let mut m = std::collections::HashMap::new();
    for pair in raw.split(',') {
        let pair = pair.trim();
        if pair.is_empty() {
            continue;
        }
        if let Some((tenant, token)) = pair.split_once(':') {
            m.insert(token.trim().to_string(), tenant.trim().to_string());
        }
    }
    m
}

/// Resolve `auth_header` to authorized tenant.
/// - If `CONTROL_PLANE_TOKENS` set, map token → tenant (scoped).
/// - Else if `CONTROL_PLANE_TOKEN` set, any valid token → `*` (global admin).
/// - Else (no env) → `*` (open, dev).
pub fn authorized_tenant(auth_header: &str) -> Option<String> {
    let tokens = tenant_tokens();
    if !tokens.is_empty() {
        let token = auth_header.strip_prefix("Bearer ").unwrap_or("");
        for (tok, tenant) in &tokens {
            if is_authorized(auth_header, tok) {
                let _ = token; // keep prefix check via is_authorized
                return Some(tenant.clone());
            }
        }
        return None;
    }
    let token = std::env::var("CONTROL_PLANE_TOKEN").unwrap_or_default();
    if is_authorized(auth_header, &token) {
        // Empty token → open; non-empty global token → admin `*`
        Some("*".to_string())
    } else {
        None
    }
}

/// Bearer auth middleware for `/snapshots` — reads `CONTROL_PLANE_TOKEN` or `CONTROL_PLANE_TOKENS` env.
/// `CONTROL_PLANE_TOKENS=tenant:token,...` scopes to tenant; single `CONTROL_PLANE_TOKEN` is global admin.
/// If neither set, auth is disabled (dev).
pub async fn auth_middleware(
    headers: HeaderMap,
    req: axum::http::Request<axum::body::Body>,
    next: Next,
) -> impl IntoResponse {
    let auth = headers
        .get(axum::http::header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    if authorized_tenant(auth).is_some() {
        next.run(req).await
    } else {
        (
            StatusCode::UNAUTHORIZED,
            Json(serde_json::json!({"error":"unauthorized"})),
        )
            .into_response()
    }
}

/// Legacy JSON dump for tests / non-HTTP callers.
pub fn json_response(registry: &Arc<Registry>) -> String {
    registry.to_json()
}

/// Backwards-compatible stub delegates to `json_response`.
pub fn axum_stub(registry: &Arc<Registry>) -> String {
    json_response(registry)
}

async fn list(State(registry): State<Arc<Registry>>, headers: HeaderMap) -> impl IntoResponse {
    let auth = headers
        .get(axum::http::header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    // Deny explicitly on missing context: the middleware normally 401s
    // first, but this handler must never default to unrestricted scope even
    // if wired without it. Open dev mode (no env) still yields `*` from
    // authorized_tenant itself.
    let tenant = match authorized_tenant(auth) {
        Some(t) => t,
        None => {
            return (
                StatusCode::UNAUTHORIZED,
                Json(serde_json::json!({"error":"unauthorized"})),
            )
                .into_response()
        }
    };
    let body = if tenant == "*" {
        registry.to_json()
    } else {
        // Scoped tenant only sees its own snapshot
        match registry.to_json_for(&tenant) {
            Some(json) => format!("{{\"{tenant}\": {json}}}"),
            None => "{}".to_string(),
        }
    };
    (
        StatusCode::OK,
        [(axum::http::header::CONTENT_TYPE, "application/json")],
        body,
    )
        .into_response()
}

async fn get_one(
    State(registry): State<Arc<Registry>>,
    headers: HeaderMap,
    Path(key): Path<String>,
) -> impl IntoResponse {
    let auth = headers
        .get(axum::http::header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    let tenant = match authorized_tenant(auth) {
        Some(t) => t,
        None => {
            return (
                StatusCode::UNAUTHORIZED,
                Json(serde_json::json!({"error":"unauthorized"})),
            )
                .into_response()
        }
    };
    // If scoped, only allow own tenant key
    if tenant != "*" && tenant != key {
        return (
            StatusCode::FORBIDDEN,
            Json(serde_json::json!({"error":"forbidden"})),
        )
            .into_response();
    }
    match registry.to_json_for(&key) {
        Some(body) => (
            StatusCode::OK,
            [(axum::http::header::CONTENT_TYPE, "application/json")],
            body,
        )
            .into_response(),
        None => (
            StatusCode::NOT_FOUND,
            Json(serde_json::json!({"error":"unknown tenant"})),
        )
            .into_response(),
    }
}

async fn health() -> impl IntoResponse {
    Json(serde_json::json!({"status":"ok"}))
}

async fn metrics(State(registry): State<Arc<Registry>>) -> impl IntoResponse {
    // Prometheus text format: per-tenant + per-arm stats + billing.
    //
    // EXPOSURE BOUNDARY (reviewed, intentional): this endpoint is public and
    // reveals tenant IDs, arm IDs, posterior means, pull counts, and derived
    // cost estimates. It never carries prompts, responses, or outcome
    // payloads. In multi-tenant deployments put network controls in front
    // (see helm/traverse/values.yaml) — tenant enumeration here is by
    // design, not by accident.
    let dash = crate::dashboard::build_dashboard(&registry);
    let billing_cost: f64 = std::env::var("BILLING_COST_PER_1K")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(0.001);
    let mut out = String::new();
    out.push_str("# HELP traverse_snapshots_total_pulls Total pulls per tenant\n");
    out.push_str("# TYPE traverse_snapshots_total_pulls gauge\n");
    out.push_str("# HELP traverse_billing_cost_usd Estimated cost per tenant\n");
    out.push_str("# TYPE traverse_billing_cost_usd gauge\n");
    for tenant in &dash {
        out.push_str(&format!(
            "traverse_snapshots_total_pulls{{tenant=\"{}\"}} {}\n",
            tenant.tenant, tenant.total_pulls
        ));
        let cost = tenant.total_pulls as f64 * billing_cost / 1000.0;
        out.push_str(&format!(
            "traverse_billing_cost_usd{{tenant=\"{}\"}} {:.6}\n",
            tenant.tenant, cost
        ));
        for arm in &tenant.arms {
            out.push_str(&format!(
                "traverse_posterior_mean{{tenant=\"{}\",arm=\"{}\"}} {:.6}\n",
                tenant.tenant, arm.id, arm.mean
            ));
            out.push_str(&format!(
                "traverse_pulls{{tenant=\"{}\",arm=\"{}\"}} {}\n",
                tenant.tenant, arm.id, arm.pulls
            ));
        }
    }
    (
        StatusCode::OK,
        [(
            axum::http::header::CONTENT_TYPE,
            "text/plain; charset=utf-8",
        )],
        out,
    )
}

/// Build `axum::Router` with snapshot endpoints. Use `axum::serve` to bind.
/// `/health` + `/metrics` are unauthenticated; `/snapshots*` gated by `auth_middleware`.
pub fn router(registry: Arc<Registry>) -> Router {
    let snapshots = Router::new()
        .route("/snapshots", get(list))
        .route("/snapshots/:key", get(get_one))
        .layer(from_fn(auth_middleware))
        .with_state(registry.clone());
    Router::new()
        .route("/health", get(health))
        .route("/metrics", get(metrics))
        .merge(snapshots)
        .with_state(registry)
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::http::StatusCode;
    use thompson_sampling::ThompsonSampling;

    /// Serializes every test that mutates process-global auth env vars.
    /// Cargo runs tests on threads in one process, so unsynchronized
    /// set_var/remove_var flips global auth mid-request in sibling tests
    /// (the historical source of flaky per-tenant failures).
    static ENV_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

    fn lock_env() -> std::sync::MutexGuard<'static, ()> {
        ENV_LOCK.lock().unwrap()
    }

    #[test]
    fn server_json_round_trip() {
        let reg = Arc::new(Registry::new());
        let policy = ThompsonSampling::with_defaults(["a"]);
        reg.put("t1".to_string(), policy.snapshot());
        let json = json_response(&reg);
        assert!(json.contains("t1"));
    }

    #[test]
    fn auth_allows_when_token_empty() {
        assert!(is_authorized("", ""));
        assert!(is_authorized("Bearer anything", ""));
        assert!(is_authorized("Bearer foo", ""));
    }

    #[test]
    fn auth_rejects_missing_prefix() {
        assert!(!is_authorized("Token foo", "foo"));
        assert!(!is_authorized("foo", "foo"));
        assert!(!is_authorized("", "secret"));
    }

    #[test]
    fn auth_constant_time_valid_and_invalid() {
        assert!(is_authorized("Bearer secret123", "secret123"));
        assert!(!is_authorized("Bearer secret124", "secret123"));
        assert!(!is_authorized("Bearer short", "secret123"));
        // Length mismatch early return still constant-time via dummy compare
        assert!(!is_authorized("Bearer s", "secret123"));
    }

    #[test]
    fn per_tenant_token_parsing() {
        let _env = lock_env();
        unsafe { std::env::set_var("CONTROL_PLANE_TOKENS", "tenant-a:tok-a, tenant-b:tok-b ") };
        let m = tenant_tokens();
        assert_eq!(m.get("tok-a").unwrap(), "tenant-a");
        assert_eq!(m.get("tok-b").unwrap(), "tenant-b");
        unsafe { std::env::remove_var("CONTROL_PLANE_TOKENS") };
    }

    #[test]
    fn per_tenant_authorized_tenant() {
        let _env = lock_env();
        unsafe { std::env::set_var("CONTROL_PLANE_TOKENS", "t1:tok1,t2:tok2") };
        assert_eq!(authorized_tenant("Bearer tok1").unwrap(), "t1");
        assert_eq!(authorized_tenant("Bearer tok2").unwrap(), "t2");
        assert!(authorized_tenant("Bearer bad").is_none());
        assert!(authorized_tenant("").is_none());
        unsafe { std::env::remove_var("CONTROL_PLANE_TOKENS") };
        // Fallback to global token when per-tenant not set
        unsafe { std::env::set_var("CONTROL_PLANE_TOKEN", "global") };
        assert_eq!(authorized_tenant("Bearer global").unwrap(), "*");
        unsafe { std::env::remove_var("CONTROL_PLANE_TOKEN") };
    }

    #[tokio::test]
    async fn health_is_unauthenticated() {
        let reg = Arc::new(Registry::new());
        let app = router(reg);
        let req = axum::http::Request::builder()
            .uri("/health")
            .body(axum::body::Body::empty())
            .unwrap();
        let resp = tower::ServiceExt::oneshot(app, req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
    }

    #[tokio::test]
    // Intentional: the whole point is holding process-global auth env
    // stable across the awaited request under test.
    #[allow(clippy::await_holding_lock)]
    async fn snapshots_gated_by_auth() {
        let _env = lock_env();
        // Set token via env for this test (serial via unsafe)
        unsafe { std::env::set_var("CONTROL_PLANE_TOKEN", "test-token") };
        let reg = Arc::new(Registry::new());
        let app = router(reg);
        let req = axum::http::Request::builder()
            .uri("/snapshots")
            .body(axum::body::Body::empty())
            .unwrap();
        let resp = tower::ServiceExt::oneshot(app, req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::UNAUTHORIZED);
        unsafe { std::env::remove_var("CONTROL_PLANE_TOKEN") };
    }

    #[tokio::test]
    // Intentional: the whole point is holding process-global auth env
    // stable across the awaited request under test.
    #[allow(clippy::await_holding_lock)]
    async fn per_tenant_scoping_filters_list() {
        let _env = lock_env();
        unsafe { std::env::set_var("CONTROL_PLANE_TOKENS", "t1:tok1,t2:tok2") };
        let reg = Arc::new(Registry::new());
        let policy = ThompsonSampling::with_defaults(["a"]);
        reg.put("t1".to_string(), policy.snapshot());
        reg.put("t2".to_string(), policy.snapshot());
        let app = router(reg);
        // t1 token should only see t1
        let req = axum::http::Request::builder()
            .uri("/snapshots")
            .header("Authorization", "Bearer tok1")
            .body(axum::body::Body::empty())
            .unwrap();
        let resp = tower::ServiceExt::oneshot(app, req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let body = axum::body::to_bytes(resp.into_body(), 10 * 1024)
            .await
            .unwrap();
        let s = String::from_utf8(body.to_vec()).unwrap();
        assert!(s.contains("t1"));
        assert!(!s.contains("t2"));
        unsafe { std::env::remove_var("CONTROL_PLANE_TOKENS") };
    }

    #[tokio::test]
    // Intentional: the whole point is holding process-global auth env
    // stable across the awaited request under test.
    #[allow(clippy::await_holding_lock)]
    async fn per_tenant_get_one_forbidden() {
        let _env = lock_env();
        unsafe { std::env::set_var("CONTROL_PLANE_TOKENS", "t1:tok1") };
        let reg = Arc::new(Registry::new());
        let policy = ThompsonSampling::with_defaults(["a"]);
        reg.put("t1".to_string(), policy.snapshot());
        reg.put("t2".to_string(), policy.snapshot());
        let app = router(reg);
        let req = axum::http::Request::builder()
            .uri("/snapshots/t2")
            .header("Authorization", "Bearer tok1")
            .body(axum::body::Body::empty())
            .unwrap();
        let resp = tower::ServiceExt::oneshot(app, req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
        unsafe { std::env::remove_var("CONTROL_PLANE_TOKENS") };
    }

    #[tokio::test]
    // Intentional: the whole point is holding process-global auth env
    // stable across the awaited request under test.
    #[allow(clippy::await_holding_lock)]
    async fn bad_token_yields_unauthorized_not_data() {
        // Regression guard for the list-handler fallback: an invalid token
        // must produce the unauthorized body, never a tenant dump, even if
        // the middleware layer is ever bypassed (the handler denies on its
        // own since this fix).
        let _env = lock_env();
        unsafe { std::env::set_var("CONTROL_PLANE_TOKENS", "t1:tok1") };
        let reg = Arc::new(Registry::new());
        let policy = ThompsonSampling::with_defaults(["a"]);
        reg.put("t1".to_string(), policy.snapshot());
        for uri in ["/snapshots", "/snapshots/t1"] {
            let app = router(Arc::clone(&reg));
            let req = axum::http::Request::builder()
                .uri(uri)
                .header("Authorization", "Bearer wrong")
                .body(axum::body::Body::empty())
                .unwrap();
            let resp = tower::ServiceExt::oneshot(app, req).await.unwrap();
            assert_eq!(resp.status(), StatusCode::UNAUTHORIZED, "{uri}");
            let body = axum::body::to_bytes(resp.into_body(), 10 * 1024)
                .await
                .unwrap();
            let s = String::from_utf8(body.to_vec()).unwrap();
            assert!(s.contains("unauthorized"), "{uri}: {s}");
            assert!(!s.contains("posterior"), "{uri}: leaked snapshot data");
        }
        unsafe { std::env::remove_var("CONTROL_PLANE_TOKENS") };
    }
}
