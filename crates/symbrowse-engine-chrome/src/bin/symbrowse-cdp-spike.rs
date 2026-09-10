use std::{env, path::PathBuf, process::ExitCode, time::Duration};

use symbrowse_engine_chrome::{BrowserMode, ProbeConfig, run_probe};

fn main() -> ExitCode {
    let args: Vec<String> = env::args().skip(1).collect();
    if args.as_slice() == ["--help"] || args.as_slice() == ["-h"] {
        println!("{}", usage());
        return ExitCode::SUCCESS;
    }
    if args.as_slice() == ["--version"] {
        println!("symbrowse version v0.8.0");
        return ExitCode::SUCCESS;
    }
    if args.as_slice() == ["version", "--json"] {
        println!(r#"{{"tool":"symbrowse","version":"v0.8.0","schema_version":8}}"#);
        return ExitCode::SUCCESS;
    }
    let config = match parse(&args) {
        Ok(config) => config,
        Err(error) => {
            eprintln!("symbrowse-cdp-spike: {error}");
            return ExitCode::from(1);
        }
    };
    match run(config) {
        Ok(output) => {
            println!("{output}");
            ExitCode::SUCCESS
        }
        Err(error) => {
            eprintln!("symbrowse-cdp-spike: {error}");
            ExitCode::from(1)
        }
    }
}

fn parse(args: &[String]) -> Result<ProbeConfig, String> {
    let mut executable = env::var_os("SYMBROWSE_CHROME_EXECUTABLE")
        .map(PathBuf::from)
        .unwrap_or_else(|| {
            PathBuf::from("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
        });
    let mut endpoint = None;
    let mut url = String::from("data:text/html,<title>symbrowse-cdp</title><h1>CDP fixture</h1>");
    let mut headless = false;
    let mut timeout = Duration::from_secs(10);
    let mut index = 0;
    while index < args.len() {
        match args[index].as_str() {
            "--chrome" => {
                index += 1;
                executable = PathBuf::from(args.get(index).ok_or("--chrome needs a path")?);
            }
            "--attach" => {
                index += 1;
                endpoint = Some(args.get(index).ok_or("--attach needs an endpoint")?.clone());
            }
            "--url" => {
                index += 1;
                url = args.get(index).ok_or("--url needs a URL")?.clone();
            }
            "--timeout-seconds" => {
                index += 1;
                let seconds = args
                    .get(index)
                    .ok_or("--timeout-seconds needs a number")?
                    .parse::<u64>()
                    .map_err(|_| "--timeout-seconds must be an integer")?;
                timeout = Duration::from_secs(seconds.max(1));
            }
            "--headless" => headless = true,
            "--help" | "-h" => return Err(usage()),
            value => return Err(format!("unknown argument {value:?}\n\n{}", usage())),
        }
        index += 1;
    }
    let mode = endpoint.map_or_else(
        || BrowserMode::Launch {
            executable,
            user_data_dir: temp_profile(),
            headless,
        },
        |endpoint| BrowserMode::Attach { endpoint },
    );
    Ok(ProbeConfig { mode, url, timeout })
}

fn temp_profile() -> PathBuf {
    let path = env::temp_dir().join(format!("symbrowse-cdp-spike-{}", std::process::id()));
    let _ = std::fs::create_dir_all(&path);
    path
}

fn run(config: ProbeConfig) -> Result<String, Box<dyn std::error::Error + Send + Sync>> {
    let cleanup_profile = match &config.mode {
        BrowserMode::Launch { user_data_dir, .. } => Some(user_data_dir.clone()),
        BrowserMode::Attach { .. } => None,
    };
    let result = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()?
        .block_on(run_probe(config));
    if let Some(profile) = cleanup_profile {
        let _ = std::fs::remove_dir_all(profile);
    }
    let report = result?;
    Ok(serde_json::to_string(&report)?)
}

fn usage() -> String {
    "usage: symbrowse-cdp-spike [--chrome PATH] [--headless] [--attach ENDPOINT] [--url URL] [--timeout-seconds N]".to_owned()
}
