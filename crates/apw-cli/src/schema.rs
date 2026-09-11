use apw_core::model::REGIONS;
use clap::CommandFactory;
use serde_json::{Value, json};

use crate::{SCHEMA_VERSION, args::Cli};

pub fn document() -> Value {
    let mut cli = Cli::command();
    cli.build();
    let commands: Vec<_> = cli.get_subcommands().map(|command| {
        let arguments: Vec<_> = command.get_arguments().map(|arg| {
            let values = arg.get_value_parser().possible_values().map(|values|
                values.map(|v| v.get_name().to_owned()).collect::<Vec<_>>());
            json!({"name": arg.get_id().as_str(), "long": arg.get_long(),
                "help": arg.get_help().map(ToString::to_string), "required": arg.is_required_set(),
                "action": format!("{:?}", arg.get_action()),
                "defaultValues": arg.get_default_values().iter().map(|v| v.to_string_lossy()).collect::<Vec<_>>(),
                "possibleValues": values})
        }).collect();
        json!({"name": command.get_name(), "description": command.get_about().map(ToString::to_string), "arguments": arguments,
            "help": command.clone().render_long_help().to_string()})
    }).collect();
    let locales: Vec<_> = REGIONS.iter().map(|r| r.locale).collect();
    let mut data_schema: Value =
        serde_json::from_str(include_str!("data-schema.json")).expect("embedded JSON schema");
    data_schema["$defs"]["inputTarget"]["properties"]["locale"]["enum"] = json!(locales);
    json!({"schemaVersion": SCHEMA_VERSION, "command": "schema", "version": env!("CARGO_PKG_VERSION"),
        "commands": commands, "dataSchema": data_schema,
        "exitCodes": {"0": "Known check result (including out of stock), catalog result, or watch condition satisfied",
            "1": "Internal or I/O failure", "2": "Invalid arguments or input", "3": "Unknown stock or incomplete catalog refresh",
            "4": "Overall deadline exceeded", "130": "Interrupted (Ctrl-C)", "141": "Output pipe closed", "143": "Terminated (SIGTERM)"},
        "transport": {"stdout": "JSON; watch emits one flushed JSON object per line (NDJSON)",
            "stderr": "JSON error object; help/version are human-readable exceptions",
            "timeUnit": "seconds for flags; Unix milliseconds for lastCheckedMs", "maxTargets": 256, "maxInputBytes": 1048576},
        "semantics": {"watchUntilInStock": "Any target; first confirmed inStock event exits 0",
            "watchDeadline": "Exit 4 even if earlier cycles were healthy; prior rows remain historical observations",
            "targetFlags": "--locale/--store/--part together, or --targets FILE/- exclusively; repeated stores and parts form a Cartesian product",
            "refresh": "Requires --category; in-memory only; failed pages keep embedded data and exit 3",
            "availability": "Only in_stock confirms stock. unknown never means out_of_stock; not_yet_checked is pending",
            "limits": "--interval >= 5; bounded by default; only watch --timeout 0 is unlimited"}})
}
