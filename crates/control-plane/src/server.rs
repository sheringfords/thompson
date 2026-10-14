//! Snapshot HTTP service for dashboard.
//! `cargo run -p control-plane` serves `/snapshots`, `/snapshots/:key`, `/health`.

use crate::Registry;
use axum::{
    extract::{Path, State},
    http::StatusCode,
    middleware::{from_fn_with_state, Next},
    response::IntoResponse,
    routing::get,
    Extension, Json, Router,
};
use std::collections::HashMap;
use std::sync::Arc;

/// Check if `auth_header` authorizes against `token` (constant-time).
/// Empty `token` means auth disabled (always true).
pub fn is_authorized(auth_header: &str, token: &str) -> bool {
    if token.is_empty() {
        return true;
    }
    let provided = auth_header.strip_prefix("Bearer ").unwrap_or("");
    // Compare only the token portion in constant time. On length mismatch do
    // dummy equal-length compares so the early return doesn't leak length via
    // timing (previous version short-circuited on `len !=` without comparing).
    use subtle::ConstantTimeEq;
    let a = provided.as_bytes();
    let b = token.as_bytes();
    if a.len() != b.len() {
        let _ = a.ct_eq(a);
        let _ = b.ct_eq(b);
        return false;
    }
    a.ct_eq(b).unwrap_u8() == 1
}

/// Parse `CONTROL_PLANE_TOKENS` `tenant:token,tenant2:token2` map.
/// Returns `token -> tenant`.
pub fn tenant_tokens() -> std::collections::HashMap<String, String> {
    let raw = std::env::var("CONTROL_PLANE_TOKENS").unwrap_or_default();
    parse_tenant_tokens(&raw)
}

/// Pure parser for `tenant:token,...` (no env access, test-friendly).
pub fn parse_tenant_tokens(raw: &str) -> std::collections::HashMap<String, String> {
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

/// Snapshot of auth configuration, captured once at router construction.
///
/// Reading `CONTROL_PLANE_TOKENS`/`CONTROL_PLANE_TOKEN` per request from the
/// process env is a race: tests (and dynamic config reloads) mutate env while
/// requests are in flight, so the middleware check and the handler check can
/// observe different configs. Capturing once makes every request served by a
/// router observe the same config.
#[derive(Debug, Clone, Default)]
pub struct AuthConfig {
    /// `token -> tenant` when per-tenant mode is on.
    pub tenants: HashMap<String, String>,
    /// Global admin token when per-tenant mode is off.
    pub global: String,
}

impl AuthConfig {
    /// Read from process env once.
    pub fn from_env() -> Self {
        Self {
            tenants: tenant_tokens(),
            global: std::env::var("CONTROL_PLANE_TOKEN").unwrap_or_default(),
        }
    }
    /// Open auth (no tokens) — dev mode.
    pub fn open() -> Self {
        Self {
            tenants: HashMap::new(),
            global: String::new(),
        }
    }
    /// Single global admin token.
    pub fn with_global(token: impl Into<String>) -> Self {
        Self {
            tenants: HashMap::new(),
            global: token.into(),
        }
    }
    /// Per-tenant `tenant:token` pairs.
    pub fn with_tenants<T, K>(pairs: impl IntoIterator<Item = (T, K)>) -> Self
    where
        T: Into<String>,
        K: Into<String>,
    {
        let mut tenants = HashMap::new();
        for (tenant, token) in pairs {
            tenants.insert(token.into(), tenant.into());
        }
        Self {
            tenants,
            global: String::new(),
        }
    }

    /// Resolve `auth_header` to authorized tenant.
    /// - Per-tenant map non-empty: token → tenant (scoped).
    /// - Else global token: any valid token → `*` (admin), empty token → `*` (open).
    /// - Else `None`.
    pub fn authorize(&self, auth_header: &str) -> Option<String> {
        if !self.tenants.is_empty() {
            for (tok, tenant) in &self.tenants {
                if is_authorized(auth_header, tok) {
                    return Some(tenant.clone());
                }
            }
            return None;
        }
        if is_authorized(auth_header, &self.global) {
            Some("*".to_string())
        } else {
            None
        }
    }
}

/// Authenticated tenant injected by [`auth_middleware`] for handlers.
/// Handlers must read this instead of re-reading env (fail-closed).
#[derive(Debug, Clone)]
pub struct Tenant(pub String);

#[derive(Clone)]
pub struct AppState {
    pub registry: Arc<Registry>,
    pub auth: AuthConfig,
}

/// Resolve `auth_header` to authorized tenant.
/// - If `CONTROL_PLANE_TOKENS` set, map token → tenant (scoped).
/// - Else if `CONTROL_PLANE_TOKEN` set, any valid token → `*` (global admin).
/// - Else (no env) → `*` (open, dev).
///
/// Prefer [`AuthConfig::authorize`] on a captured config; this wrapper reads
/// env per call for backwards compatibility.
pub fn authorized_tenant(auth_header: &str) -> Option<String> {
    AuthConfig::from_env().authorize(auth_header)
}

/// Bearer auth middleware for `/snapshots` — uses the [`AuthConfig`] captured
/// in [`AppState`] at router construction, not env per request.
/// If neither token var was set at construction, auth is disabled (dev).
pub async fn auth_middleware(
    State(state): State<AppState>,
    req: axum::http::Request<axum::body::Body>,
    next: Next,
) -> impl IntoResponse {
    let auth = req
        .headers()
        .get(axum::http::header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .to_string();
    match state.auth.authorize(&auth) {
        Some(tenant) => {
            let mut req = req;
            req.extensions_mut().insert(Tenant(tenant));
            next.run(req).await
        }
        None => (
            StatusCode::UNAUTHORIZED,
            axum::Json(serde_json::json!({"error":"unauthorized"})),
        )
            .into_response(),
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

async fn list(
    State(state): State<AppState>,
    Extension(tenant): Extension<Tenant>,
) -> impl IntoResponse {
    // The middleware always injects `Tenant` and 401s before reaching here.
    // A missing extension is a wiring bug and yields 500 (fail closed), never
    // an unrestricted default.
    let tenant = tenant.0;
    let body = if tenant == "*" {
        state.registry.to_json()
    } else {
        // Scoped tenant only sees its own snapshot
        match state.registry.to_json_for(&tenant) {
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
    State(state): State<AppState>,
    Extension(tenant): Extension<Tenant>,
    Path(key): Path<String>,
) -> impl IntoResponse {
    let tenant = tenant.0;
    // If scoped, only allow own tenant key
    if tenant != "*" && tenant != key {
        return (
            StatusCode::FORBIDDEN,
            axum::Json(serde_json::json!({"error":"forbidden"})),
        )
            .into_response();
    }
    match state.registry.to_json_for(&key) {
        Some(body) => (
            StatusCode::OK,
            [(axum::http::header::CONTENT_TYPE, "application/json")],
            body,
        )
            .into_response(),
        None => (
            StatusCode::NOT_FOUND,
            axum::Json(serde_json::json!({"error":"unknown tenant"})),
        )
            .into_response(),
    }
}

async fn health() -> impl IntoResponse {
    Json(serde_json::json!({"status":"ok"}))
}

async fn metrics(State(state): State<AppState>) -> impl IntoResponse {
    // Prometheus text format: per-tenant + per-arm stats + billing.
    //
    // EXPOSURE BOUNDARY (reviewed, intentional): this endpoint is public and
    // reveals tenant IDs, arm IDs, posterior means, pull counts, and derived
    // cost estimates. It never carries prompts, responses, or outcome
    // payloads. In multi-tenant deployments put network controls in front
    // (see helm/traverse/values.yaml) — tenant enumeration here is by
    // design, not by accident.
    let dash = crate::dashboard::build_dashboard(&state.registry);
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
/// Auth config is snapshotted from env once here (see [`AuthConfig::from_env`]).
pub fn router(registry: Arc<Registry>) -> Router {
    router_with_auth(registry, AuthConfig::from_env())
}

/// Build router with an explicit auth config (no env access — test-friendly).
pub fn router_with_auth(registry: Arc<Registry>, auth: AuthConfig) -> Router {
    let state = AppState { registry, auth };
    let snapshots = Router::new()
        .route("/snapshots", get(list))
        .route("/snapshots/:key", get(get_one))
        .route_layer(from_fn_with_state(state.clone(), auth_middleware))
        .with_state(state.clone());
    Router::new()
        .route("/health", get(health))
        .route("/metrics", get(metrics))
        .merge(snapshots)
        .with_state(state)
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::http::StatusCode;
    use thompson_sampling::ThompsonSampling;

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
        // Pure parser — no env access, so parallel tests cannot race.
        let m = parse_tenant_tokens("tenant-a:tok-a, tenant-b:tok-b ");
        assert_eq!(m.get("tok-a").unwrap(), "tenant-a");
        assert_eq!(m.get("tok-b").unwrap(), "tenant-b");
    }

    #[test]
    fn per_tenant_authorized_tenant() {
        // Explicit config — no env access, parallel-safe.
        let auth = AuthConfig::with_tenants([("t1", "tok1"), ("t2", "tok2")]);
        assert_eq!(auth.authorize("Bearer tok1").unwrap(), "t1");
        assert_eq!(auth.authorize("Bearer tok2").unwrap(), "t2");
        assert!(auth.authorize("Bearer bad").is_none());
        assert!(auth.authorize("").is_none());
        // Fallback to global token when per-tenant not set
        let global = AuthConfig::with_global("global");
        assert_eq!(global.authorize("Bearer global").unwrap(), "*");
        assert!(global.authorize("Bearer wrong").is_none());
    }

    #[tokio::test]
    async fn health_is_unauthenticated() {
        let reg = Arc::new(Registry::new());
        let app = router_with_auth(reg, AuthConfig::with_global("test-token"));
        let req = axum::http::Request::builder()
            .uri("/health")
            .body(axum::body::Body::empty())
            .unwrap();
        let resp = tower::ServiceExt::oneshot(app, req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
    }

    #[tokio::test]
    async fn snapshots_gated_by_auth() {
        let reg = Arc::new(Registry::new());
        let app = router_with_auth(reg, AuthConfig::with_global("test-token"));
        let req = axum::http::Request::builder()
            .uri("/snapshots")
            .body(axum::body::Body::empty())
            .unwrap();
        let resp = tower::ServiceExt::oneshot(app, req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::UNAUTHORIZED);
    }

    #[tokio::test]
    async fn per_tenant_scoping_filters_list() {
        let reg = Arc::new(Registry::new());
        let policy = ThompsonSampling::with_defaults(["a"]);
        reg.put("t1".to_string(), policy.snapshot());
        reg.put("t2".to_string(), policy.snapshot());
        let app = router_with_auth(reg, AuthConfig::with_tenants([("t1", "tok1"), ("t2", "tok2")]));
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
    }

    #[tokio::test]
    async fn per_tenant_get_one_forbidden() {
        let reg = Arc::new(Registry::new());
        let policy = ThompsonSampling::with_defaults(["a"]);
        reg.put("t1".to_string(), policy.snapshot());
        reg.put("t2".to_string(), policy.snapshot());
        let app = router_with_auth(reg, AuthConfig::with_tenants([("t1", "tok1")]));
        let req = axum::http::Request::builder()
            .uri("/snapshots/t2")
            .header("Authorization", "Bearer tok1")
            .body(axum::body::Body::empty())
            .unwrap();
        let resp = tower::ServiceExt::oneshot(app, req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::FORBIDDEN);
    }

    #[tokio::test]
    async fn bad_token_yields_unauthorized_not_data() {
        // Regression guard: an invalid token must produce the unauthorized
        // body, never a tenant dump, even if the middleware layer is ever
        // bypassed.
        let reg = Arc::new(Registry::new());
        let policy = ThompsonSampling::with_defaults(["a"]);
        reg.put("t1".to_string(), policy.snapshot());
        for uri in ["/snapshots", "/snapshots/t1"] {
            let app = router_with_auth(Arc::clone(&reg), AuthConfig::with_tenants([("t1", "tok1")]));
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
    }
}
