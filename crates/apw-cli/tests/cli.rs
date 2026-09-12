use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

use apw_cli::args::Cli;
use apw_cli::{InputTarget, MAX_INPUT_BYTES, read_targets, resolve_targets};
use apw_core::catalog::Catalog;
use apw_core::model::Category;
use clap::Parser;
use serde_json::{Value, json};

fn run(args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_apw"))
        .args(args)
        .output()
        .unwrap()
}
fn result(output: &Output) -> Value {
    assert!(
        output.status.success(),
        "{}",
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(output.stderr.is_empty());
    serde_json::from_slice(&output.stdout).unwrap()
}

#[test]
fn offline_discovery_and_search_work_from_an_unrelated_directory() {
    let output = Command::new(env!("CARGO_BIN_EXE_apw"))
        .arg("regions")
        .current_dir(std::env::temp_dir())
        .output()
        .unwrap();
    let regions = result(&output);
    assert_eq!(regions["regions"].as_array().unwrap().len(), 7);
    let stores = result(&run(&["stores", "--locale", "zh_CN", "--search", "r359"]));
    assert_eq!(stores["stores"].as_array().unwrap().len(), 1);
    assert_eq!(stores["stores"][0]["number"], "R359");
    let products = result(&run(&[
        "products",
        "--locale",
        "zh_CN",
        "--category",
        "iphone",
        "--search",
        "512",
    ]));
    assert_eq!(products["source"], "embedded");
    assert!(!products["products"].as_array().unwrap().is_empty());
    assert!(
        products["products"]
            .as_array()
            .unwrap()
            .iter()
            .all(|p| p["category"] == "iphone")
    );
}

#[test]
fn schema_is_machine_readable_and_matches_discovered_commands() {
    let schema = result(&run(&["schema"]));
    assert_eq!(schema["schemaVersion"], 1);
    assert_eq!(
        schema["dataSchema"]["$defs"]["inputTarget"]["properties"]["locale"]["enum"]
            .as_array()
            .unwrap()
            .len(),
        7
    );
    for command in ["regions", "stores", "products", "check", "watch", "schema"] {
        assert!(
            schema["commands"]
                .as_array()
                .unwrap()
                .iter()
                .any(|c| c["name"] == command)
        );
    }
}

#[test]
fn invalid_arguments_have_json_stderr_and_empty_stdout() {
    for args in [
        vec!["check"],
        vec!["check", "--locale", "zh_CN", "--store", "R359"],
        vec!["check", "--targets", "-", "--locale", "zh_CN"],
        vec!["watch", "--interval", "1"],
        vec!["check", "--timeout", "0"],
        vec!["products", "--refresh"],
        vec!["stores", "--locale", "de_DE"],
        vec!["check", "--targets", "/apw-nonexistent-targets.json"],
        vec!["invalid-command"],
    ] {
        let output = run(&args);
        assert_eq!(output.status.code(), Some(2), "{args:?}: {output:?}");
        assert!(output.stdout.is_empty(), "{args:?}");
        let error: Value = serde_json::from_slice(&output.stderr).unwrap();
        assert_eq!(error["error"]["exitCode"], 2);
    }
}

#[test]
fn help_and_version_are_successful_text_exceptions() {
    for args in [vec!["--help"], vec!["check", "--help"], vec!["--version"]] {
        let output = run(&args);
        assert!(output.status.success());
        assert!(output.stderr.is_empty());
        assert!(!output.stdout.is_empty());
    }
}

#[test]
fn repeated_flags_and_explicit_unlimited_watch_are_accepted() {
    assert!(
        Cli::try_parse_from([
            "apw",
            "watch",
            "--locale",
            "zh_CN",
            "--store",
            "R359",
            "--store",
            "R683",
            "--part",
            "MWUC3CH/A",
            "--part",
            "MWUD3CH/A",
            "--timeout",
            "0"
        ])
        .is_ok()
    );
}

fn input(value: Value) -> InputTarget {
    serde_json::from_value(value).unwrap()
}

#[test]
fn exact_ids_are_validated_but_new_skus_are_not_rejected_by_stale_catalog() {
    let catalog = Catalog::new();
    let new =
        || input(json!({"locale":"zh_CN", "storeNumber":"R999999", "partNumber":"NEW123CH/A"}));
    let targets = resolve_targets(vec![new(), new()], &catalog).unwrap();
    assert_eq!(targets.len(), 1);
    assert_eq!(targets[0].product_name, "NEW123CH/A");
    assert_eq!(targets[0].store_title, "R999999");
    for bad in [
        json!({"locale":"de_DE", "storeNumber":"R359", "partNumber":"MWUC3CH/A"}),
        json!({"locale":"zh_CN", "storeNumber":"上海", "partNumber":"MWUC3CH/A"}),
        json!({"locale":"zh_CN", "storeNumber":"R359", "partNumber":"a|b"}),
        json!({"locale":"zh_CN", "storeNumber":"R359", "partNumber":"/A"}),
        json!({"locale":"zh_CN", "storeNumber":"R359", "partNumber":"A//B"}),
        json!({"locale":"zh_CN", "storeNumber":"R359", "partNumber":""}),
    ] {
        assert_eq!(
            resolve_targets(vec![input(bad)], &catalog)
                .unwrap_err()
                .exit_code,
            2
        );
    }
    assert!(resolve_targets(Vec::new(), &catalog).is_err());
    assert!(resolve_targets((0..257).map(|_| new()).collect(), &catalog).is_err());
}

#[test]
fn companion_part_is_accepted_validated_and_filled_from_the_catalog() {
    let catalog = Catalog::new();
    // Explicit companion survives resolution untouched.
    let explicit = input(
        json!({"locale":"zh_CN", "storeNumber":"R359", "partNumber":"MEHW4CH/B",
        "companionPart":"MJUA4FE/A"}),
    );
    let targets = resolve_targets(vec![explicit], &catalog).unwrap();
    assert_eq!(targets[0].companion_part.as_deref(), Some("MJUA4FE/A"));
    // A malformed companion is an input error, like a malformed part number.
    let bad = input(
        json!({"locale":"zh_CN", "storeNumber":"R359", "partNumber":"MEHW4CH/B",
        "companionPart":"a|b"}),
    );
    assert_eq!(
        resolve_targets(vec![bad], &catalog).unwrap_err().exit_code,
        2
    );
    // Apple Watch cases from the embedded catalog get their band automatically;
    // other categories never do, and unknown SKUs are left alone.
    let watch = catalog
        .products("zh_CN")
        .unwrap()
        .into_iter()
        .find(|p| p.category == Category::Watch)
        .expect("embedded catalog has Apple Watch");
    let iphone = catalog
        .products("zh_CN")
        .unwrap()
        .into_iter()
        .find(|p| p.category == Category::Iphone)
        .expect("embedded catalog has iPhone");
    let targets = resolve_targets(
        vec![
            input(json!({"locale":"zh_CN", "storeNumber":"R359", "partNumber": watch.part_number})),
            input(
                json!({"locale":"zh_CN", "storeNumber":"R359", "partNumber": iphone.part_number}),
            ),
            input(json!({"locale":"zh_CN", "storeNumber":"R359", "partNumber":"NEW123CH/B"})),
        ],
        &catalog,
    )
    .unwrap();
    assert!(
        targets[0].companion_part.is_some(),
        "watch case needs its band: {:?}",
        targets[0]
    );
    assert_eq!(targets[0].companion_part, watch.companion_part);
    assert_eq!(targets[1].companion_part, None);
    assert_eq!(targets[2].companion_part, None);
    // The wire format only carries the field when it is set.
    let v = serde_json::to_value(&targets[1]).unwrap();
    assert!(v.get("companionPart").is_none(), "{v}");
    let v = serde_json::to_value(&targets[0]).unwrap();
    assert!(v.get("companionPart").is_some(), "{v}");
}

#[tokio::test]
async fn json_input_rejects_typos_and_oversized_documents() {
    assert!(
        read_targets(&br#"[{"locale":"zh_CN","storeNumber":"R359","partNumer":"MWUC3CH/A"}]"#[..])
            .await
            .is_err()
    );
    let bytes = vec![b' '; MAX_INPUT_BYTES as usize + 1];
    assert_eq!(
        read_targets(bytes.as_slice()).await.unwrap_err().exit_code,
        2
    );
    let targets =
        read_targets(&br#"[{"locale":"zh_CN","storeNumber":"R359","partNumber":"MWUC3CH/A"}]"#[..])
            .await
            .unwrap();
    assert_eq!(targets.len(), 1);
}

#[test]
fn unclosed_stdin_does_not_defeat_process_deadline() {
    let mut child = Command::new(env!("CARGO_BIN_EXE_apw"))
        .args(["check", "--targets", "-", "--timeout", "1"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    // Keep the pipe open and send no input. This must not reach Apple.
    let input = child.stdin.take().unwrap();
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        if child.try_wait().unwrap().is_some() {
            break;
        }
        if Instant::now() > deadline {
            child.kill().unwrap();
            let _ = child.wait();
            panic!("CLI did not honor deadline while reading stdin");
        }
        std::thread::sleep(Duration::from_millis(20));
    }
    drop(input);
    let output = child.wait_with_output().unwrap();
    assert_eq!(output.status.code(), Some(4));
    assert!(output.stdout.is_empty());
    let error: Value = serde_json::from_slice(&output.stderr).unwrap();
    assert_eq!(error["error"]["kind"], "timeout");
}
