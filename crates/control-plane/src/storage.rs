//! Pluggable snapshot persistence for Registry.
//! Thin abstraction so `control-plane` stays memory-first (thin waist).
//! `FileStore` is the only durable backend; S3/Postgres live outside this crate.

use crate::Registry;
use std::sync::Arc;
use thompson_sampling::policy::Snapshot;

/// Backend for durable snapshot storage.
pub trait RegistryStorage: Send + Sync + std::fmt::Debug {
    /// Persist all tenants (bulk save).
    fn save_all(&self, registry: &Registry) -> Result<(), String>;
    /// Load single tenant.
    fn load(&self, tenant: &str) -> Result<Option<Snapshot>, String>;
}

/// In-memory storage — wraps `Registry` itself (no durability).
#[derive(Debug, Default)]
pub struct MemoryStorage;

impl RegistryStorage for MemoryStorage {
    fn save_all(&self, _registry: &Registry) -> Result<(), String> {
        Ok(())
    }
    fn load(&self, _tenant: &str) -> Result<Option<Snapshot>, String> {
        Ok(None)
    }
}

/// File-per-tenant storage backed by `thompson_sampling::FileStore`.
#[derive(Debug)]
pub struct FileStorage {
    dir: std::path::PathBuf,
}

impl FileStorage {
    /// Create storage rooted at `dir` (one file per tenant: `<dir>/<tenant>.json`).
    pub fn new(dir: impl Into<std::path::PathBuf>) -> Self {
        Self { dir: dir.into() }
    }
}

impl RegistryStorage for FileStorage {
    fn save_all(&self, registry: &Registry) -> Result<(), String> {
        std::fs::create_dir_all(&self.dir).map_err(|e| e.to_string())?;
        for tenant in registry.list() {
            if let Some(snap) = registry.get(&tenant) {
                let path = self.dir.join(format!("{tenant}.json"));
                let store = thompson_sampling::FileStore::new(&path);
                thompson_sampling::SnapshotStore::save(&store, &snap).map_err(|e| e.to_string())?;
            }
        }
        Ok(())
    }

    fn load(&self, tenant: &str) -> Result<Option<Snapshot>, String> {
        let path = self.dir.join(format!("{tenant}.json"));
        let store = thompson_sampling::FileStore::new(&path);
        thompson_sampling::SnapshotStore::load(&store).map_err(|e| e.to_string())
    }
}

/// Shared S3 backend — enabled with `features=["s3"]`, thin default keeps zero dep.
/// Stores `bucket/prefix/tenant.json` via `aws-sdk-s3::Client::put_object/get_object`.
#[cfg(feature = "s3")]
#[derive(Debug)]
pub struct S3Storage {
    bucket: String,
    prefix: String,
}

#[cfg(feature = "s3")]
impl S3Storage {
    /// Create with bucket.
    pub fn new(bucket: impl Into<String>) -> Self {
        Self {
            bucket: bucket.into(),
            prefix: String::new(),
        }
    }
    /// With prefix.
    pub fn with_prefix(mut self, prefix: impl Into<String>) -> Self {
        self.prefix = prefix.into();
        self
    }
    fn key_for(&self, tenant: &str) -> String {
        if self.prefix.is_empty() {
            format!("{tenant}.json")
        } else {
            format!("{}/{tenant}.json", self.prefix.trim_end_matches('/'))
        }
    }
}

#[cfg(feature = "s3")]
impl RegistryStorage for S3Storage {
    fn save_all(&self, registry: &Registry) -> Result<(), String> {
        let json = registry.to_json();
        let _ = (&self.bucket, self.key_for("_bulk"), json);
        // Future: with `aws-sdk-s3 1` (requires rustc >=1.70, latest 1.94) do:
        //   let cfg = aws_config::load_from_env().await;
        //   Client::new(&cfg).put_object().bucket(&bucket).key(&key).body(json).send().await
        // Keep stub to stay thin on rustc 1.75. Fail loudly so callers cannot
        // mistake the no-op for durability.
        Err("s3 storage not implemented: enable and wire aws-sdk-s3 first".to_string())
    }
    fn load(&self, tenant: &str) -> Result<Option<Snapshot>, String> {
        let _ = self.key_for(tenant);
        Err("s3 storage not implemented: enable and wire aws-sdk-s3 first".to_string())
    }
}

/// Postgres backend — enabled with `features=["postgres"]`, thin default keeps zero dep.
#[cfg(feature = "postgres")]
#[derive(Debug)]
pub struct PostgresStorage {
    /// DSN.
    pub dsn: String,
}

#[cfg(feature = "postgres")]
impl PostgresStorage {
    /// Create with DSN.
    pub fn new(dsn: impl Into<String>) -> Self {
        Self { dsn: dsn.into() }
    }
}

#[cfg(feature = "postgres")]
impl RegistryStorage for PostgresStorage {
    fn save_all(&self, registry: &Registry) -> Result<(), String> {
        let _ = registry.to_json();
        // Stub only: fail loudly rather than silently discarding snapshots.
        Err(
            "postgres storage not implemented: wire a sqlx/tokio-postgres backend first"
                .to_string(),
        )
    }
    fn load(&self, _tenant: &str) -> Result<Option<Snapshot>, String> {
        Err(
            "postgres storage not implemented: wire a sqlx/tokio-postgres backend first"
                .to_string(),
        )
    }
}

/// Background persister that periodically flushes Registry to storage.
pub struct Persister {
    registry: Arc<Registry>,
    storage: Arc<dyn RegistryStorage>,
    interval: std::time::Duration,
}

impl Persister {
    /// Create persister.
    pub fn new(
        registry: Arc<Registry>,
        storage: Arc<dyn RegistryStorage>,
        interval: std::time::Duration,
    ) -> Self {
        Self {
            registry,
            storage,
            interval,
        }
    }
    /// Spawn background task (tokio).
    pub fn spawn(self) -> tokio::task::JoinHandle<()> {
        tokio::spawn(async move {
            let mut ticker = tokio::time::interval(self.interval);
            loop {
                ticker.tick().await;
                // Surfaced, not swallowed: a persistently failing store
                // means snapshots are not durable, and the operator must
                // see it in logs, not discover it after a restart.
                if let Err(e) = self.storage.save_all(&self.registry) {
                    tracing::warn!(error=%e, "background snapshot persist failed");
                }
            }
        })
    }
}
