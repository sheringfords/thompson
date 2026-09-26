//! Control-plane binary — serves Registry via axum.
//! Listens on `PORT` (default 8080), exposes `/snapshots`, `/snapshots/:key`, `/health`.

use control_plane::{
    server::router,
    storage::{FileStorage, MemoryStorage, RegistryStorage},
    Registry,
};
use std::{net::SocketAddr, sync::Arc, time::Duration};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .init();

    let port: u16 = std::env::var("PORT")
        .ok()
        .and_then(|p| p.parse().ok())
        .unwrap_or(8080);
    let storage_kind = std::env::var("STORAGE").unwrap_or_else(|_| "memory".to_string());
    let registry = Arc::new(Registry::new());

    // Optional file storage for local durability. Unknown backends fail
    // fast: silently falling back to memory (as STORAGE=s3 once did) loses
    // snapshots on restart while the operator believes they are durable.
    let storage: Arc<dyn RegistryStorage> = match storage_kind.as_str() {
        "file" => {
            let dir = std::env::var("STORAGE_DIR").unwrap_or_else(|_| "/tmp/traverse".to_string());
            tracing::info!(dir=%dir, "using FileStorage");
            Arc::new(FileStorage::new(dir))
        }
        "memory" => {
            tracing::warn!("using MemoryStorage: snapshots are NOT durable across restarts");
            Arc::new(MemoryStorage)
        }
        other => {
            anyhow::bail!("unknown STORAGE={other:?}: want \"file\" or \"memory\" (s3/postgres are not implemented)");
        }
    };

    // Background persist every 30s (best-effort)
    let persister = control_plane::storage::Persister::new(
        Arc::clone(&registry),
        Arc::clone(&storage),
        Duration::from_secs(30),
    );
    let _handle = persister.spawn();

    let app = router(Arc::clone(&registry)).layer(tower_http::trace::TraceLayer::new_for_http());

    let addr = SocketAddr::from(([0, 0, 0, 0], port));
    tracing::info!(%addr, "control-plane listening");
    let listener = tokio::net::TcpListener::bind(addr).await?;
    axum::serve(listener, app)
        .with_graceful_shutdown(shutdown_signal())
        .await?;
    Ok(())
}

async fn shutdown_signal() {
    let ctrl_c = async {
        tokio::signal::ctrl_c().await.expect("install ctrl_c");
    };
    #[cfg(unix)]
    let terminate = async {
        tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .expect("install terminate")
            .recv()
            .await;
    };
    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();
    tokio::select! { _ = ctrl_c => {}, _ = terminate => {} }
}
