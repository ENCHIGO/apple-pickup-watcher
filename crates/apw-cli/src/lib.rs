//! CLI transport and input validation. Inventory decisions remain in apw-core.

pub mod args;
mod schema;

use std::collections::BTreeSet;
use std::future::Future;
use std::io;
use std::time::Duration;

use apw_core::apple::{AppleClient, ClientConfig, Fetcher};
use apw_core::catalog::Catalog;
use apw_core::model::{REGIONS, Region, Target, region_by_locale};
use apw_core::watcher::{Event, Watcher, WatcherConfig};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};

use args::{Cli, Command, TargetArgs};

pub const SCHEMA_VERSION: u32 = 1;
pub const MAX_TARGETS: usize = 256;
pub const MAX_INPUT_BYTES: u64 = 1 << 20;

#[derive(Debug, thiserror::Error)]
#[error("{message}")]
pub struct CliError {
    pub exit_code: u8,
    pub kind: &'static str,
    pub message: String,
}

impl CliError {
    pub fn new(exit_code: u8, kind: &'static str, message: impl Into<String>) -> Self {
        Self {
            exit_code,
            kind,
            message: message.into(),
        }
    }

    pub fn invalid(message: impl Into<String>) -> Self {
        Self::new(2, "invalid_input", message)
    }

    pub fn json(&self) -> Value {
        json!({"schemaVersion": SCHEMA_VERSION, "error": {
            "kind": self.kind, "message": self.message, "exitCode": self.exit_code
        }})
    }
}

impl From<io::Error> for CliError {
    fn from(error: io::Error) -> Self {
        if error.kind() == io::ErrorKind::BrokenPipe {
            Self::new(141, "broken_pipe", "Output consumer closed the pipe")
        } else {
            Self::new(1, "io_error", error.to_string())
        }
    }
}

pub async fn json_line<W: AsyncWrite + Unpin, T: Serialize>(
    out: &mut W,
    value: &T,
) -> Result<(), CliError> {
    let mut bytes = serde_json::to_vec(value)
        .map_err(|e| CliError::new(1, "serialization_error", e.to_string()))?;
    bytes.push(b'\n');
    out.write_all(&bytes).await?;
    out.flush().await?;
    Ok(())
}

/// Deadline covers the entire operation, including stdin and output backpressure.
/// Dropping the operation cancels its watcher via EngineTask below.
pub async fn bounded<F, S>(operation: F, seconds: u64, shutdown: S) -> Result<u8, CliError>
where
    F: Future<Output = Result<u8, CliError>>,
    S: Future<Output = Result<u8, CliError>>,
{
    let deadline = async {
        if seconds == 0 {
            std::future::pending::<()>().await;
        } else {
            tokio::time::sleep(Duration::from_secs(seconds)).await;
        }
    };
    tokio::select! {
        biased;
        code = shutdown => Err(CliError::new(code?, "interrupted", "Monitoring interrupted")),
        _ = deadline => Err(CliError::new(4, "timeout", format!("Deadline of {seconds} seconds exceeded"))),
        result = operation => result,
    }
}

fn region(locale: &str) -> Result<&'static Region, CliError> {
    region_by_locale(locale)
        .ok_or_else(|| CliError::invalid(format!("Unsupported locale {locale:?}; run apw regions")))
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct InputTarget {
    pub locale: String,
    pub store_number: String,
    pub part_number: String,
    #[serde(default)]
    pub store_title: String,
    #[serde(default)]
    pub product_name: String,
    /// Apple Watch 表壳的搭档表带零件号（见 `apw_core::model::Product::companion_part`）。
    /// 缺省时按零件号回查目录补齐；目录里没有就不带。
    #[serde(default)]
    pub companion_part: Option<String>,
}

pub async fn read_targets<R: AsyncRead + Unpin>(reader: R) -> Result<Vec<InputTarget>, CliError> {
    let mut bytes = Vec::new();
    reader
        .take(MAX_INPUT_BYTES + 1)
        .read_to_end(&mut bytes)
        .await?;
    if bytes.len() as u64 > MAX_INPUT_BYTES {
        return Err(CliError::invalid("Target input exceeds 1 MiB"));
    }
    serde_json::from_slice(&bytes)
        .map_err(|e| CliError::invalid(format!("Invalid target JSON: {e}")))
}

pub fn resolve_targets(
    inputs: Vec<InputTarget>,
    catalog: &Catalog,
) -> Result<Vec<Target>, CliError> {
    if inputs.is_empty() || inputs.len() > MAX_TARGETS {
        return Err(CliError::invalid("Supply between 1 and 256 targets"));
    }
    let mut seen = BTreeSet::new();
    let mut targets = Vec::new();
    for input in inputs {
        region(&input.locale)?;
        let store = &input.store_number;
        if !(2..=12).contains(&store.len())
            || !store.starts_with('R')
            || !store[1..].bytes().all(|b| b.is_ascii_digit())
        {
            return Err(CliError::invalid(format!(
                "Invalid store number {store:?}; expected R followed by digits"
            )));
        }
        let part = &input.part_number;
        if !valid_part_number(part) {
            return Err(CliError::invalid(format!(
                "Invalid part number {part:?}; use the SKU returned by apw products"
            )));
        }
        let companion_input = input
            .companion_part
            .as_deref()
            .map(str::trim)
            .filter(|c| !c.is_empty())
            .map(str::to_string);
        if let Some(companion) = &companion_input
            && !valid_part_number(companion)
        {
            return Err(CliError::invalid(format!(
                "Invalid companionPart {companion:?}; use the value returned by apw products"
            )));
        }
        // Catalog absence is not proof of an invalid SKU/store: embedded data can lag.
        let store_title = if input.store_title.is_empty() {
            catalog
                .store_by_number(&input.locale, store)
                .map_or_else(|| store.clone(), |s| s.title)
        } else {
            input.store_title
        };
        let known = catalog.product_by_part(&input.locale, part);
        let product_name = if input.product_name.is_empty() {
            known
                .as_ref()
                .map_or_else(|| part.clone(), |p| p.title.clone())
        } else {
            input.product_name
        };
        // Apple Watch cases only answer truthfully when queried together with a
        // band from the same buy page; the catalog knows which one.
        let companion_part =
            companion_input.or_else(|| known.as_ref().and_then(|p| p.companion_part.clone()));
        let target = Target {
            locale: input.locale,
            store_number: input.store_number,
            part_number: input.part_number,
            store_title,
            product_name,
            companion_part,
        };
        if seen.insert(target.key()) {
            targets.push(target);
        }
    }
    Ok(targets)
}

async fn load_targets(args: TargetArgs, catalog: &Catalog) -> Result<Vec<Target>, CliError> {
    let inputs = if let Some(path) = args.targets {
        if path.as_os_str() == "-" {
            read_targets(tokio::io::stdin()).await?
        } else {
            let file = tokio::fs::File::open(&path)
                .await
                .map_err(|e| CliError::invalid(format!("Cannot read {}: {e}", path.display())))?;
            read_targets(file).await?
        }
    } else {
        let locale = args.locale.ok_or_else(|| {
            CliError::invalid("Supply --locale, --store and --part, or --targets FILE/-")
        })?;
        if args.store.len().saturating_mul(args.part.len()) > MAX_TARGETS {
            return Err(CliError::invalid(
                "Store/part combinations exceed 256 targets",
            ));
        }
        args.store
            .iter()
            .flat_map(|store| {
                args.part.iter().map(|part| InputTarget {
                    locale: locale.clone(),
                    store_number: store.clone(),
                    part_number: part.clone(),
                    store_title: String::new(),
                    product_name: String::new(),
                    companion_part: None,
                })
            })
            .collect()
    };
    resolve_targets(inputs, catalog)
}

/// Apple part numbers look like `MJTF4CH/A`: alphanumerics with at most one slash.
fn valid_part_number(part: &str) -> bool {
    !part.is_empty()
        && part.len() <= 64
        && part.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'/')
        && part.as_bytes()[0].is_ascii_alphanumeric()
        && part.as_bytes()[part.len() - 1].is_ascii_alphanumeric()
        && part.bytes().filter(|b| *b == b'/').count() <= 1
}

fn matches_search(search: &Option<String>, values: &[&str]) -> bool {
    search.as_ref().is_none_or(|q| {
        let q = q.to_lowercase();
        values.iter().any(|v| v.to_lowercase().contains(&q))
    })
}

pub async fn execute<W: AsyncWrite + Unpin>(cli: Cli, out: &mut W) -> Result<u8, CliError> {
    let catalog = Catalog::new();
    match cli.command {
        Command::Regions => {
            let regions: Vec<_> = REGIONS
                .iter()
                .map(|r| json!({"locale": r.locale, "title": r.title, "baseUrl": r.base_url}))
                .collect();
            json_line(
                out,
                &json!({"schemaVersion": SCHEMA_VERSION, "command": "regions", "regions": regions}),
            )
            .await?;
        }
        Command::Stores { locale, search } => {
            region(&locale)?;
            let stores: Vec<_> = catalog
                .stores(&locale)
                .map_err(catalog_error)?
                .into_iter()
                .filter(|s| matches_search(&search, &[&s.number, &s.name, &s.title]))
                .collect();
            json_line(out, &json!({"schemaVersion": SCHEMA_VERSION, "command": "stores", "locale": locale, "source": "embedded", "stores": stores})).await?;
        }
        Command::Products {
            locale,
            category,
            search,
            refresh,
            ..
        } => {
            let region = region(&locale)?;
            let category = category.map(Into::into);
            let mut warning = None;
            if refresh {
                let http = reqwest::Client::builder()
                    .timeout(Duration::from_secs(15))
                    .connect_timeout(Duration::from_secs(5))
                    .build()
                    .map_err(|e| CliError::new(1, "client_error", e.to_string()))?;
                if let Err(error) = catalog.refresh_products(region, category, &http).await {
                    warning = Some(error.to_string());
                }
            }
            let products: Vec<_> = catalog
                .products(&locale)
                .map_err(catalog_error)?
                .into_iter()
                .filter(|p| category.is_none_or(|c| c == p.category))
                .filter(|p| {
                    matches_search(
                        &search,
                        &[&p.part_number, &p.title, &p.family, &p.capacity, &p.color],
                    )
                })
                .collect();
            let complete = warning.is_none();
            let source = if !refresh {
                "embedded"
            } else if complete {
                "online"
            } else {
                "online_with_embedded_fallback"
            };
            json_line(out, &json!({"schemaVersion": SCHEMA_VERSION, "command": "products", "locale": locale,
                "source": source, "refreshComplete": if refresh { Some(complete) } else { None }, "warning": warning, "products": products})).await?;
            return Ok(if complete { 0 } else { 3 });
        }
        Command::Check { targets, .. } => {
            let targets = load_targets(targets, &catalog).await?;
            let client = AppleClient::new(ClientConfig::default())
                .map_err(|e| CliError::new(1, "client_error", e.to_string()))?;
            return monitor(client, targets, WatcherConfig::default(), Mode::Check, out).await;
        }
        Command::Watch {
            targets,
            interval,
            until_in_stock,
            ..
        } => {
            let targets = load_targets(targets, &catalog).await?;
            let client = AppleClient::new(ClientConfig::default())
                .map_err(|e| CliError::new(1, "client_error", e.to_string()))?;
            let config = WatcherConfig {
                interval: Duration::from_secs(interval),
                ..WatcherConfig::default()
            };
            return monitor(client, targets, config, Mode::Watch { until_in_stock }, out).await;
        }
        Command::Schema => json_line(out, &schema::document()).await?,
    }
    Ok(0)
}

fn catalog_error(error: apw_core::catalog::CatalogError) -> CliError {
    CliError::new(1, "catalog_error", error.to_string())
}

#[derive(Debug, Clone, Copy)]
pub enum Mode {
    Check,
    Watch { until_in_stock: bool },
}

struct EngineTask(tokio::task::JoinHandle<()>);
impl Drop for EngineTask {
    fn drop(&mut self) {
        self.0.abort();
    }
}

/// Shared adapter, tested using a fake Fetcher and the real core engine.
pub async fn monitor<F: Fetcher, W: AsyncWrite + Unpin>(
    client: F,
    targets: Vec<Target>,
    config: WatcherConfig,
    mode: Mode,
    out: &mut W,
) -> Result<u8, CliError> {
    if targets.is_empty() {
        return Err(CliError::invalid("No targets"));
    }
    let (watcher, mut events, engine) = Watcher::new(client, config);
    let _engine = EngineTask(tokio::spawn(engine));
    watcher.set_targets(targets).await;
    watcher.start().await;
    while let Some(event) = events.recv().await {
        match mode {
            Mode::Check => {
                if let Event::CycleComplete { healthy, snapshot } = event {
                    let healthy = healthy
                        && !snapshot.is_empty()
                        && snapshot.iter().all(|s| !s.availability.is_unknown());
                    let any_in_stock = snapshot.iter().any(|s| s.availability.is_in_stock());
                    watcher.stop().await;
                    json_line(
                        out,
                        &json!({"schemaVersion": SCHEMA_VERSION, "command": "check",
                        "healthy": healthy, "anyInStock": any_in_stock, "snapshot": snapshot}),
                    )
                    .await?;
                    return Ok(if healthy { 0 } else { 3 });
                }
            }
            Mode::Watch { until_in_stock } => {
                let hit = matches!(event, Event::InStock { .. });
                json_line(
                    out,
                    &json!({"schemaVersion": SCHEMA_VERSION, "command": "watch", "event": event}),
                )
                .await?;
                if until_in_stock && hit {
                    watcher.stop().await;
                    return Ok(0);
                }
            }
        }
    }
    Err(CliError::new(
        1,
        "engine_stopped",
        "Watcher event stream closed unexpectedly",
    ))
}
