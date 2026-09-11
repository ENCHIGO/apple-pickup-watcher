use std::io::Write;
use std::process::ExitCode;
use std::time::Duration;

use apw_cli::args::Cli;
use apw_cli::{CliError, bounded, execute};
use clap::Parser;

fn report(error: CliError) -> ExitCode {
    // A consumer such as head can close the pipe intentionally; don't print a panic.
    if error.exit_code != 141 {
        let _ = writeln!(std::io::stderr().lock(), "{}", error.json());
    }
    ExitCode::from(error.exit_code)
}

fn main() -> ExitCode {
    let cli = match Cli::try_parse() {
        Ok(cli) => cli,
        Err(error) => {
            if matches!(
                error.kind(),
                clap::error::ErrorKind::DisplayHelp | clap::error::ErrorKind::DisplayVersion
            ) {
                return match write!(std::io::stdout().lock(), "{error}") {
                    Ok(()) => ExitCode::SUCCESS,
                    Err(error) => report(error.into()),
                };
            }
            return report(CliError::new(2, "invalid_arguments", error.to_string()));
        }
    };
    let runtime = match tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
    {
        Ok(runtime) => runtime,
        Err(error) => return report(CliError::new(1, "runtime_error", error.to_string())),
    };
    let seconds = cli.timeout_seconds();
    let result = runtime.block_on(async {
        bounded(execute(cli, &mut tokio::io::stdout()), seconds, shutdown()).await
    });
    // Tokio's stdin/stdout use blocking workers. An unclosed input pipe must not
    // keep the process alive after its overall deadline or a termination signal.
    runtime.shutdown_timeout(Duration::from_millis(200));
    match result {
        Ok(code) => ExitCode::from(code),
        Err(error) => report(error),
    }
}

async fn shutdown() -> Result<u8, CliError> {
    #[cfg(unix)]
    {
        let mut terminate =
            tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())?;
        tokio::select! {
            result = tokio::signal::ctrl_c() => { result?; Ok(130) },
            _ = terminate.recv() => Ok(143),
        }
    }
    #[cfg(not(unix))]
    {
        tokio::signal::ctrl_c().await?;
        Ok(130)
    }
}
