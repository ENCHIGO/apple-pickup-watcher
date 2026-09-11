use std::path::PathBuf;

use apw_core::model::Category;
use clap::{Args, Parser, Subcommand, ValueEnum};

#[derive(Debug, Parser)]
#[command(
    name = "apw",
    version,
    about = "Apple pickup availability for agents. JSON output; watch emits NDJSON."
)]
pub struct Cli {
    #[command(subcommand)]
    pub command: Command,
}

#[derive(Debug, Subcommand)]
pub enum Command {
    /// List supported region identifiers (offline).
    Regions,
    /// List retail stores (offline).
    Stores {
        #[arg(long, default_value = "zh_CN")]
        locale: String,
        /// Match store number or name, case-insensitively.
        #[arg(long)]
        search: Option<String>,
    },
    /// List products; explicitly refresh to fetch current buying pages.
    Products {
        #[arg(long, default_value = "zh_CN")]
        locale: String,
        #[arg(long, value_enum, required_if_eq("refresh", "true"))]
        category: Option<ProductCategory>,
        /// Match SKU, title, family, capacity or color, case-insensitively.
        #[arg(long)]
        search: Option<String>,
        /// Refresh this category in memory; failed pages retain embedded data.
        #[arg(long)]
        refresh: bool,
        /// Overall deadline in seconds, including refresh and output.
        #[arg(long, default_value_t = 120, value_parser = clap::value_parser!(u64).range(1..=86400))]
        timeout: u64,
    },
    /// Complete one polling cycle. Exit 0 means known, not necessarily in stock.
    Check {
        #[command(flatten)]
        targets: TargetArgs,
        /// Overall deadline in seconds, including reading target input.
        #[arg(long, default_value_t = 60, value_parser = clap::value_parser!(u64).range(1..=86400))]
        timeout: u64,
    },
    /// Emit watcher events as NDJSON. No desktop, sound or push side effects.
    Watch {
        #[command(flatten)]
        targets: TargetArgs,
        /// Base interval in seconds; engine jitter and failure backoff still apply.
        #[arg(long, default_value_t = 30, value_parser = clap::value_parser!(u64).range(5..=86400))]
        interval: u64,
        /// Exit successfully on the first confirmed in-stock event (any target).
        #[arg(long)]
        until_in_stock: bool,
        /// Overall deadline in seconds; 0 explicitly enables unlimited monitoring.
        #[arg(long, default_value_t = 300, value_parser = clap::value_parser!(u64).range(0..=604800))]
        timeout: u64,
    },
    /// Describe commands, exit codes and the versioned JSON data contract (offline).
    Schema,
}

impl Cli {
    pub fn timeout_seconds(&self) -> u64 {
        match &self.command {
            Command::Products { timeout, .. }
            | Command::Check { timeout, .. }
            | Command::Watch { timeout, .. } => *timeout,
            _ => 60,
        }
    }
}

#[derive(Debug, Args)]
pub struct TargetArgs {
    /// Explicit locale for --store/--part, e.g. zh_CN.
    #[arg(long, requires_all = ["store", "part"])]
    pub locale: Option<String>,
    /// Store number. Repeat to query every store/part combination.
    #[arg(long, requires_all = ["locale", "part"], value_delimiter = ',')]
    pub store: Vec<String>,
    /// Apple part number (SKU), e.g. MWUC3CH/A. Repeat for multiple products.
    #[arg(long, requires_all = ["locale", "store"], value_delimiter = ',')]
    pub part: Vec<String>,
    /// JSON array of targets from a file; '-' reads stdin. Maximum 256 targets / 1 MiB.
    #[arg(long, conflicts_with_all = ["locale", "store", "part"])]
    pub targets: Option<PathBuf>,
}

#[derive(Debug, Clone, Copy, ValueEnum)]
pub enum ProductCategory {
    Iphone,
    Ipad,
    Mac,
    Watch,
}

impl From<ProductCategory> for Category {
    fn from(value: ProductCategory) -> Self {
        match value {
            ProductCategory::Iphone => Self::Iphone,
            ProductCategory::Ipad => Self::Ipad,
            ProductCategory::Mac => Self::Mac,
            ProductCategory::Watch => Self::Watch,
        }
    }
}
