---
name: apple-pickup-watcher
description: Query Apple Retail pickup availability and monitor specified stores and SKUs with the apw CLI. Use when the user asks about Apple in-store stock, available pickup stores, or a bounded stock watch.
---

# Apple Pickup Watcher

Use `apw` for inventory queries. It runs without the desktop application and emits JSON. It does not add to a bag, place orders, or send notifications itself.

## Locate and discover

Use `apw --version` and `apw schema` to identify the installed interface. If `apw` is absent, use an explicitly supplied executable path; in a source checkout the build is `cargo build --release --locked -p apw-cli` and the executable is `target/release/apw` (`apw.exe` on Windows). If neither binary nor source checkout is available, explain that the CLI must be installed; do not invent a download URL.

`apw schema` returns the supported flags, exit codes, limits and JSON Schema definitions. This skill covers `schemaVersion: 1`; inspect a different version's contract before relying on field names.

Resolve the user's request through the catalog:

```bash
apw regions
apw stores --locale zh_CN --search 上海
apw products --locale zh_CN --category iphone --search '512GB'
```

Use the returned `locale`, store `number`, and product `partNumber`. A store name or product family is not an identifier. If several variants fit and the user has not requested all of them, clarify the relevant capacity/color/model or store choice. Do not treat the example identifiers below as the user's selection.

Catalog commands use embedded snapshots by default; they are not live stock checks. When current models are needed, use `apw products --locale zh_CN --category iphone --refresh --timeout 120`. An incomplete refresh exits 3 and includes `warning` and fallback data; describe the data as incomplete. Refreshed data lives only in that invocation, but its returned SKUs can be passed to `check` directly.

## Query or watch

```bash
apw check --locale zh_CN --store R359 --part 'MWUC3CH/A' --timeout 60
apw watch --locale zh_CN --store R359 --part 'MWUC3CH/A' \
  --interval 30 --until-in-stock --timeout 300
```

`check` completes one cycle. For explicit target pairs or multiple regions, use a JSON array with `--targets FILE` or `--targets -` (stdin):

```json
[
  {"locale":"zh_CN","storeNumber":"R359","partNumber":"MWUC3CH/A"}
]
```

Up to 256 targets / 1 MiB are accepted. Repeated `--store` and `--part` flags query every store/part combination; use the JSON array when only particular pairs are intended. `--targets` is exclusive with those flags and `--locale`.

Apple Watch entries in `apw products` carry a `companionPart` (a band part number from the same buy page). Apple's pickup endpoint only answers truthfully for a watch case when a band is in the same query, so `check` and `watch` add it automatically for SKUs found in the embedded catalog. When a watch SKU came from `--refresh` output, pass its `companionPart` in the targets JSON (`{"locale":…,"storeNumber":…,"partNumber":…,"companionPart":…}`); a watch case queried without a band may come back empty or as a false negative.

Prefer one bounded `watch` process for repeated checks so the engine can share connections, apply backoff, and deduplicate arrival events. The default interval is 30 seconds (minimum 5). Keep the host tool's execution deadline longer than the CLI's deadline. An unlimited `--timeout 0` watch requires a host that can retain the process and a user request for ongoing monitoring. Ending a foreground command or receiving a timeout ends monitoring; do not claim it will continue or notify later.

## Interpret results

- `check.snapshot[]` contains the target, `availability`, `lastCheckedMs` (Unix milliseconds), and consecutive failure count. `anyInStock` means at least one target is confirmed; `healthy` means every target has a known result.
- `availability.kind`: `in_stock` confirms pickup availability; `out_of_stock` is a confirmed negative. `unknown` must retain its `reason` and relevant detail. `not_yet_checked` means pending. A timeout or missing result is not evidence of no stock.
- `watch` emits one JSON object per line; the event is under `event`. `inStock` is an arrival notification; `cycleComplete.snapshot` is the complete cycle view. Continuous stock is not repeatedly announced. `--until-in-stock` exits on **any** target's first confirmed arrival; it does not wait for all targets.
- Exit 0 means a successful query (including no stock), or a satisfied watch condition. Exit 3 means unknown stock or incomplete refresh; parse stdout even on this exit, because other rows may be known. Exit 4 means the overall deadline elapsed; earlier events are historical observations. Exit 2 is invalid input, 1 is an internal/I/O error, 130/143 is interruption, and 141 means the output consumer closed its pipe.
- stdout contains JSON/NDJSON; stderr contains structured fatal errors. `--help` and `--version` are text exceptions. Do not hide nonzero exits with `|| true`, discard partial stdout, or translate request errors into no stock.

Report the exact region/store/model, observation time, and any unknown or incomplete result. A fresh successful check demonstrates this invocation's result, not reliability on every network. HTTP 541 should be reported as blocked; avoid rapid retries or concurrent duplicate watchers. Let the user's requested workflow determine whether an arrival is simply reported or passed to an already authorized notification channel.
