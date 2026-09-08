#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    ffi::OsString,
    fs,
    io::{self, Read, Write},
    path::PathBuf,
    process::ExitCode,
    time::Duration,
};

use serde::Serialize;
use symbrowse_core::{
    batch::{self, ItemOutput},
    config::{FlagOverrides, LoadContext, load, render_show_text, render_show_yaml, show_fields},
    error::ErrorCode,
    flows,
    key_resolver::{KeyInitResult, KeyResolver},
    key_sources::SystemKeySources,
    output::{Envelope, Format},
};
use symbrowse_daemon::{
    Client, ClientOptions, Frame, PolicyStatus, Server, ServerOptions, SessionSpec,
    codes as daemon_codes, default_socket_path, dispatch_once,
};
use symbrowse_mcp::{ServeOptions, registry, serve_stdio};
use symbrowse_protocol::{render_root_version, render_version_json, render_version_text};

const VERSION: &str = match option_env!("SYMBROWSE_VERSION") {
    Some(version) => version,
    None => "dev",
};

#[derive(Debug, Eq, PartialEq)]
enum Action {
    RootVersion,
    Version {
        structured: bool,
    },
    ConfigShow {
        format: Format,
        flags: FlagOverrides,
    },
    Batch {
        format: Format,
        commands: Vec<String>,
        bail: bool,
        dry_run: bool,
    },
    StateKeyInit {
        format: Format,
    },
    DaemonRun {
        session: String,
        mode: String,
        engine: String,
        ssrf: Option<bool>,
        allow_private: Option<bool>,
    },
    DaemonLifecycle {
        session: String,
        command: String,
        format: Format,
    },
    StateOperation {
        session: String,
        command: String,
        name: Option<String>,
        older_than: Option<i64>,
        format: Format,
    },
    Mcp {
        session: String,
        profiles: String,
        allow_private: bool,
        engine: Option<String>,
    },
    McpListProfiles,
    ProfileList {
        format: Format,
    },
    ToolList {
        profiles: String,
        format: Format,
    },
    Dispatch {
        session: String,
        command: String,
        args: serde_json::Value,
        format: Format,
    },
    FlowList {
        format: Format,
    },
    FlowValidate {
        path: PathBuf,
        format: Format,
    },
    FlowRun {
        session: String,
        path: PathBuf,
        inputs: BTreeMap<String, String>,
        dry_run: bool,
        format: Format,
    },
}

#[derive(Debug, Eq, PartialEq)]
struct ParseError {
    message: String,
    exit_code: u8,
}

#[derive(Serialize)]
struct ConfigSuccess<T> {
    success: bool,
    data: T,
}

#[derive(Serialize)]
struct ConfigData {
    fields: std::collections::BTreeMap<String, symbrowse_core::config::Field>,
}

#[derive(Serialize)]
struct BatchSuccess<'a> {
    success: bool,
    data: &'a batch::Report,
}

#[derive(Serialize)]
struct StateSuccess<'a> {
    success: bool,
    data: &'a KeyInitResult,
}

fn main() -> ExitCode {
    let args: Vec<OsString> = std::env::args_os().skip(1).collect();
    match parse(&args) {
        Ok(Action::RootVersion) => write_stdout(&render_root_version(VERSION)),
        Ok(Action::Version { structured: true }) => match render_version_json(VERSION) {
            Ok(output) => write_stdout(&output),
            Err(_) => ExitCode::from(1),
        },
        Ok(Action::Version { structured: false }) => write_stdout(&render_version_text(VERSION)),
        Ok(Action::ConfigShow { format, flags }) => run_config_show(format, flags),
        Ok(Action::Batch {
            format,
            commands,
            bail,
            dry_run,
        }) => run_batch(format, commands, bail, dry_run),
        Ok(Action::StateKeyInit { format }) => run_state_key_init(format),
        Ok(Action::DaemonRun {
            session,
            mode,
            engine,
            ssrf,
            allow_private,
        }) => run_daemon(session, mode, engine, ssrf, allow_private),
        Ok(Action::DaemonLifecycle {
            session,
            command,
            format,
        }) => run_daemon_lifecycle(session, command, format),
        Ok(Action::StateOperation {
            session,
            command,
            name,
            older_than,
            format,
        }) => run_state_operation(session, command, name, older_than, format),
        Ok(Action::Mcp {
            session,
            profiles,
            allow_private,
            engine,
        }) => run_mcp(session, profiles, allow_private, engine),
        Ok(Action::McpListProfiles) => write_stdout(MCP_PROFILE_LIST),
        Ok(Action::ProfileList { format }) => run_profile_list(format),
        Ok(Action::ToolList { profiles, format }) => run_tool_list(profiles, format),
        Ok(Action::Dispatch {
            session,
            command,
            args,
            format,
        }) => run_dispatch(session, command, args, format),
        Ok(Action::FlowList { format }) => run_flow_list(format),
        Ok(Action::FlowValidate { path, format }) => run_flow_validate(path, format),
        Ok(Action::FlowRun {
            session,
            path,
            inputs,
            dry_run,
            format,
        }) => run_flow(session, path, inputs, dry_run, format),
        Err(error) => {
            let _ = writeln!(io::stderr(), "{}", error.message);
            ExitCode::from(error.exit_code)
        }
    }
}

const MCP_PROFILE_LIST: &str = "core     14 tools  Page interaction and reading: open, snapshot, click, fill, type, press, wait, read, get, find. The default profile.\n           tools: open, snapshot, click, fill, type, press, wait, read, get, find, fetch_url, fetch_batch, cache_get, wayback_snapshots\nnav       3 tools  History navigation: back, forward, reload.\n           tools: back, forward, reload\nstate     0 tools  Sessions, cookies, storage, state save/load, auth login. Tools land with the state milestone (v0.4.0).\nnetwork   0 tools  Routing, mocking, request inspection, HAR, headers, offline. Tools land with the network milestone (v1.0.0).\ndebug     0 tools  Console, errors, eval, a11y, diff, doctor. Tools land with the reach milestones (v1.0.0).\nflows     0 tools  Flow list/run/record. Tools land with the flows milestone (v0.6.0).\n";

fn run_profile_list(format: Format) -> ExitCode {
    if matches!(format, Format::Text) {
        return write_stdout(MCP_PROFILE_LIST);
    }
    let profiles = serde_json::json!([
        {"name":"core","tools":14,"description":"Page interaction and reading"},
        {"name":"nav","tools":3,"description":"History navigation"},
        {"name":"state","tools":0,"description":"Sessions and state"},
        {"name":"network","tools":0,"description":"Network controls"},
        {"name":"debug","tools":0,"description":"Diagnostics"},
        {"name":"flows","tools":0,"description":"Flow list/run/record"}
    ]);
    match Envelope::ok(profiles, Vec::new()).render(format) {
        Ok(output) => write_stdout(&output),
        Err(_) => ExitCode::from(1),
    }
}

fn run_tool_list(profiles: String, format: Format) -> ExitCode {
    let selected = match registry::validate_profile_selection(&profiles) {
        Ok(selected) => selected,
        Err(error) => return render_dispatch_error(format, daemon_codes::MALFORMED_REQUEST, error),
    };
    let tools = registry::specs()
        .iter()
        .filter(|spec| selected.contains(&spec.profile))
        .map(|spec| serde_json::json!({"name": spec.name, "command": spec.command, "profile": spec.profile}))
        .collect::<Vec<_>>();
    match Envelope::ok(serde_json::Value::Array(tools), Vec::new()).render(format) {
        Ok(output) => write_stdout(&output),
        Err(error) => {
            render_dispatch_error(format, daemon_codes::OPERATION_FAILED, error.to_string())
        }
    }
}

fn run_dispatch(
    session: String,
    command: String,
    args: serde_json::Value,
    format: Format,
) -> ExitCode {
    let frame = Frame {
        cmd: command,
        args: Some(args),
        session: session.clone(),
        ..Frame::default()
    };
    let direct = if matches!(frame.cmd.as_str(), "fetch.url" | "fetch.batch") {
        LoadContext::from_process(FlagOverrides::default())
            .ok()
            .and_then(|context| load(&context).ok())
            .filter(|result| result.config.engine == "static")
            .map(|result| {
                dispatch_once(
                    SessionSpec::from_config(&result.config, &session),
                    frame.clone(),
                )
            })
    } else {
        None
    };
    let response = match if let Some(response) = direct {
        Ok(response)
    } else {
        Client::new(ClientOptions {
            socket_path: default_socket_path(&session),
            session,
            ..ClientOptions::default()
        })
        .request(frame)
    } {
        Ok(response) => response,
        Err(error) => {
            return render_dispatch_error(
                format,
                daemon_codes::DAEMON_UNAVAILABLE,
                error.to_string(),
            );
        }
    };
    if response.success {
        let envelope = Envelope::ok(
            response.data.unwrap_or(serde_json::Value::Null),
            response
                .warnings
                .into_iter()
                .map(|warning| symbrowse_core::output::Warning {
                    kind: warning.kind,
                    severity: warning.severity,
                    message: warning.message,
                    r#ref: warning.r#ref,
                    excerpt: warning.excerpt,
                })
                .collect(),
        );
        match envelope.render(format) {
            Ok(output) => write_stdout(&output),
            Err(error) => {
                render_dispatch_error(format, daemon_codes::OPERATION_FAILED, error.to_string())
            }
        }
    } else {
        let error = response.error.unwrap_or_default();
        render_dispatch_error(format, &error.code, error.message)
    }
}

fn render_dispatch_error(format: Format, code: &str, message: String) -> ExitCode {
    let mapped = match code {
        daemon_codes::MALFORMED_REQUEST => ErrorCode::MalformedRequest,
        daemon_codes::UNKNOWN_COMMAND => ErrorCode::UnknownCommand,
        daemon_codes::OPERATION_TIMEOUT => ErrorCode::OperationTimeout,
        daemon_codes::PEER_DENIED => ErrorCode::PeerDenied,
        daemon_codes::DAEMON_UNAVAILABLE => ErrorCode::DaemonUnavailable,
        daemon_codes::INVALID_SESSION => ErrorCode::InvalidSession,
        _ => ErrorCode::OperationFailed,
    };
    match Envelope::failure(mapped, message).render(format) {
        Ok(output) => {
            let _ = io::stdout().write_all(output.as_bytes());
        }
        Err(_) => {
            let _ = writeln!(io::stderr(), "dispatch failed");
        }
    }
    ExitCode::from(mapped.exit_code())
}

fn run_flow_list(format: Format) -> ExitCode {
    let mut groups = Vec::new();
    let project = PathBuf::from(".symbrowse/flows");
    if let Ok(found) = flows::discover_directory(&project, "project") {
        groups.push(found);
    }
    if let Ok(home) = std::env::var("HOME") {
        let global = PathBuf::from(home).join(".config/symbrowse/flows");
        if let Ok(found) = flows::discover_directory(&global, "global") {
            groups.push(found);
        }
    }
    let data = serde_json::to_value(flows::merge_discovered(groups)).unwrap_or_default();
    match Envelope::ok(data, Vec::new()).render(format) {
        Ok(output) => write_stdout(&output),
        Err(error) => {
            render_dispatch_error(format, daemon_codes::OPERATION_FAILED, error.to_string())
        }
    }
}

fn run_flow_validate(path: PathBuf, format: Format) -> ExitCode {
    let source = match fs::read(&path) {
        Ok(source) => source,
        Err(error) => {
            return render_dispatch_error(
                format,
                "not_found",
                format!("read flow {}: {error}", path.display()),
            );
        }
    };
    match flows::parse(&source, path.to_string_lossy().to_string()) {
        Ok(flow) => {
            let data = serde_json::json!({
                "name": flow.name,
                "version": flow.version,
                "domains": flow.domains,
                "steps": flow.steps.len(),
                "outputs": flow.outputs.len(),
                "valid": true,
            });
            match Envelope::ok(data, Vec::new()).render(format) {
                Ok(output) => write_stdout(&output),
                Err(error) => {
                    render_dispatch_error(format, daemon_codes::OPERATION_FAILED, error.to_string())
                }
            }
        }
        Err(error) => render_dispatch_error(format, "malformed_request", error.to_string()),
    }
}

fn run_flow(
    session: String,
    path: PathBuf,
    inputs: BTreeMap<String, String>,
    dry_run: bool,
    format: Format,
) -> ExitCode {
    let source = match fs::read_to_string(&path) {
        Ok(source) => source,
        Err(error) => {
            return render_dispatch_error(
                format,
                "not_found",
                format!("read flow {}: {error}", path.display()),
            );
        }
    };
    let client = Client::new(ClientOptions {
        socket_path: default_socket_path(&session),
        session: session.clone(),
        ..ClientOptions::default()
    });
    let response = match client.request(Frame {
        cmd: "flow.run".into(),
        args: Some(serde_json::json!({"yaml": source, "inputs": inputs, "dry_run": dry_run})),
        session,
        ..Frame::default()
    }) {
        Ok(response) => response,
        Err(error) => {
            return render_dispatch_error(
                format,
                daemon_codes::DAEMON_UNAVAILABLE,
                error.to_string(),
            );
        }
    };
    if response.success {
        match Envelope::ok(response.data.unwrap_or_default(), Vec::new()).render(format) {
            Ok(output) => write_stdout(&output),
            Err(error) => {
                render_dispatch_error(format, daemon_codes::OPERATION_FAILED, error.to_string())
            }
        }
    } else {
        let error = response.error.unwrap_or_default();
        render_dispatch_error(format, &error.code, error.message)
    }
}

fn run_mcp(
    session: String,
    profiles: String,
    allow_private: bool,
    engine: Option<String>,
) -> ExitCode {
    if let Err(error) = registry::validate_profile_selection(&profiles) {
        let _ = writeln!(io::stderr(), "{error}");
        return ExitCode::from(1);
    }
    let context = match LoadContext::from_process(FlagOverrides::default()) {
        Ok(context) => context,
        Err(error) => {
            let _ = writeln!(io::stderr(), "{error}");
            return ExitCode::from(1);
        }
    };
    let config = match load(&context) {
        Ok(result) => result.config,
        Err(error) => {
            let _ = writeln!(io::stderr(), "{error}");
            return ExitCode::from(1);
        }
    };
    let daemon_log_path = config.daemon_log.clone();
    let engine = engine.unwrap_or(config.engine);
    let allow_private = allow_private || config.allow_private;
    if let Err(message) = validate_engine(&engine) {
        let _ = writeln!(io::stderr(), "{message}");
        return ExitCode::from(1);
    }
    let stdin = io::stdin();
    let stdout = io::stdout();
    match serve_stdio(
        stdin.lock(),
        stdout.lock(),
        ServeOptions {
            version: VERSION.to_owned(),
            session,
            profiles,
            executable: std::env::current_exe()
                .ok()
                .and_then(|path| path.into_os_string().into_string().ok())
                .unwrap_or_else(|| "symbrowse".to_owned()),
            allow_private,
            engine: Some(engine),
            daemon_log_path: Some(daemon_log_path),
        },
    ) {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            let _ = writeln!(io::stderr(), "mcp server: {error}");
            ExitCode::from(1)
        }
    }
}

fn validate_engine(value: &str) -> Result<(), String> {
    if matches!(value, "chrome" | "static" | "safari-attach" | "safari-bidi") {
        Ok(())
    } else {
        Err(format!(
            "invalid engine {value:?}: use one of chrome, static, safari-attach, safari-bidi"
        ))
    }
}

fn run_state_key_init(format: Format) -> ExitCode {
    let resolver = KeyResolver::new(SystemKeySources::default());
    match resolver.initialize() {
        Ok(result) => match render_state_key_init(format, &result) {
            Ok(output) => write_stdout(&output),
            Err(_) => ExitCode::from(1),
        },
        Err(error) => {
            if format == Format::Text {
                let _ = writeln!(io::stderr(), "{error}");
            } else if let Ok(output) =
                Envelope::failure(ErrorCode::Internal, error.to_string()).render(format)
            {
                let _ = io::stdout().write_all(output.as_bytes());
            }
            ExitCode::from(1)
        }
    }
}

fn render_state_key_init(
    format: Format,
    result: &KeyInitResult,
) -> Result<String, serde_json::Error> {
    match format {
        Format::Json => {
            let mut output = serde_json::to_string(&StateSuccess {
                success: true,
                data: result,
            })?;
            output.push('\n');
            Ok(output)
        }
        Format::Text if !result.instruction.is_empty() => Ok(format!(
            "No secure key store is available. Configure the generated key with:\n{}\n",
            result.instruction
        )),
        Format::Text => Ok(format!(
            "State encryption key {} via {}.\n",
            result.action, result.key_source
        )),
        Format::Yaml => Ok(format!(
            "success: true\ndata:\n    action: {}\n    configured: {}\n    keysource: {}\n    instruction: {}\nwarnings: []\nerror: null\n",
            result.action,
            result.configured,
            result.key_source,
            serde_json::to_string(&result.instruction)?
        )),
    }
}

fn run_daemon(
    session: String,
    mode: String,
    engine: String,
    ssrf: Option<bool>,
    allow_private: Option<bool>,
) -> ExitCode {
    let context = match LoadContext::from_process(FlagOverrides::default()) {
        Ok(context) => context,
        Err(error) => {
            let _ = writeln!(io::stderr(), "daemon configuration: {error}");
            return ExitCode::from(1);
        }
    };
    let config = match load(&context) {
        Ok(result) => result.config,
        Err(error) => {
            let _ = writeln!(io::stderr(), "daemon configuration: {error}");
            return ExitCode::from(1);
        }
    };
    let engine = if engine.is_empty() {
        if config.engine.is_empty() {
            "chrome".to_owned()
        } else {
            config.engine.clone()
        }
    } else {
        engine
    };
    let mode = if mode.is_empty() {
        config.mode.clone()
    } else {
        mode
    };
    if let Err(error) = symbrowse_core::config::resolve_selection(Some(&mode), Some(&engine)) {
        let _ = writeln!(
            io::stderr(),
            "invalid transport selection: {}",
            error.message
        );
        return ExitCode::from(1);
    }
    if let Err(error) = validate_engine(&engine) {
        let _ = writeln!(io::stderr(), "{error}");
        return ExitCode::from(1);
    }
    let idle_timeout = if config.idle_timeout == 0 {
        None
    } else {
        Some(Duration::from_secs(config.idle_timeout as u64))
    };
    let mut spec = SessionSpec::from_config(&config, session.clone());
    spec.mode = mode.clone();
    spec.engine = engine.clone();
    spec.ssrf_enabled = ssrf.unwrap_or(config.ssrf_enabled);
    spec.allow_private = allow_private.unwrap_or(config.allow_private);
    spec.socket_path = default_socket_path(&session);
    let policy = PolicyStatus {
        allowed_domains: config.allowed_domains.clone(),
        ssrf_enabled: spec.ssrf_enabled,
        fetch_ssrf_enabled: spec.ssrf_enabled,
        allow_private: spec.allow_private,
    };
    let options = ServerOptions {
        socket_path: spec.socket_path.clone(),
        session: session.clone(),
        engine: engine.clone(),
        mode: mode.clone(),
        policy,
        idle_timeout,
        operation_timeout: Duration::from_secs(config.operation_timeout as u64),
        read_timeout: Duration::from_secs(config.read_timeout as u64),
        session_spec: Some(spec),
        ..ServerOptions::default()
    };
    match Server::new(options).and_then(|server| server.listen_and_serve()) {
        Ok(()) | Err(symbrowse_daemon::ServerError::AlreadyRunning) => ExitCode::SUCCESS,
        Err(error) => {
            let _ = writeln!(io::stderr(), "daemon: {error}");
            ExitCode::from(1)
        }
    }
}

fn run_daemon_lifecycle(session: String, command: String, format: Format) -> ExitCode {
    let client = Client::new(ClientOptions {
        socket_path: default_socket_path(&session),
        session: session.clone(),
        autostart: false,
        ..ClientOptions::default()
    });
    let response = match client.request_without_autostart(Frame {
        cmd: command,
        session,
        ..Frame::default()
    }) {
        Ok(response) => response,
        Err(error) => {
            if format == Format::Text {
                let _ = writeln!(io::stderr(), "{error}");
            } else if let Ok(output) = serde_json::to_string(&symbrowse_daemon::error_response(
                daemon_codes::DAEMON_UNAVAILABLE,
                error.to_string(),
            )) {
                let _ = writeln!(io::stdout(), "{output}");
            }
            return ExitCode::from(1);
        }
    };
    if format == Format::Text {
        if response.success {
            let _ = writeln!(
                io::stdout(),
                "{}",
                response.data.as_ref().map_or_else(
                    || "ok".to_owned(),
                    |data| serde_json::to_string_pretty(data).unwrap_or_else(|_| "ok".to_owned())
                )
            );
            ExitCode::SUCCESS
        } else {
            let _ = writeln!(
                io::stderr(),
                "{}",
                response
                    .error
                    .map_or_else(|| "daemon request failed".to_owned(), |error| error.message)
            );
            ExitCode::from(1)
        }
    } else {
        serde_json::to_string(&response)
            .map(|output| write_stdout(&(output + "\n")))
            .unwrap_or_else(|_| ExitCode::from(1))
    }
}

fn run_state_operation(
    session: String,
    command: String,
    name: Option<String>,
    older_than: Option<i64>,
    format: Format,
) -> ExitCode {
    let args = match (name, older_than) {
        (Some(name), _) => Some(serde_json::json!({"name": name})),
        (None, Some(days)) => Some(serde_json::json!({"older_than_days": days})),
        (None, None) => None,
    };
    let client = Client::new(ClientOptions {
        socket_path: default_socket_path(&session),
        session: session.clone(),
        ..ClientOptions::default()
    });
    let response = match client.request(Frame {
        cmd: command.clone(),
        args,
        session,
        ..Frame::default()
    }) {
        Ok(response) => response,
        Err(error) => {
            let _ = writeln!(io::stderr(), "{error}");
            return ExitCode::from(1);
        }
    };
    if !response.success {
        let _ = writeln!(
            io::stderr(),
            "{}",
            response
                .error
                .map_or_else(|| "state request failed".to_owned(), |error| error.message)
        );
        return ExitCode::from(1);
    }
    if format != Format::Text {
        return serde_json::to_string(&response)
            .map(|output| write_stdout(&(output + "\n")))
            .unwrap_or_else(|_| ExitCode::from(1));
    }
    let data = response.data.unwrap_or(serde_json::Value::Null);
    let output = match command.as_str() {
        "state.save" => format!(
            "saved state {:?}\n",
            data.get("name")
                .and_then(serde_json::Value::as_str)
                .unwrap_or("")
        ),
        "state.load" => format!(
            "loaded state {:?}\n",
            data.get("name")
                .and_then(serde_json::Value::as_str)
                .unwrap_or("")
        ),
        "state.clear" => format!(
            "cleared state {:?}\n",
            data.get("name")
                .and_then(serde_json::Value::as_str)
                .unwrap_or("")
        ),
        "state.list" => data
            .get("states")
            .and_then(serde_json::Value::as_array)
            .map(|values| {
                values
                    .iter()
                    .filter_map(serde_json::Value::as_str)
                    .map(|s| format!("{s}\n"))
                    .collect()
            })
            .unwrap_or_default(),
        "state.clean" => format!(
            "removed {} expired state(s)\n",
            data.get("removed")
                .and_then(serde_json::Value::as_array)
                .map_or(0, Vec::len)
        ),
        _ => serde_json::to_string_pretty(&data).unwrap_or_default() + "\n",
    };
    write_stdout(&output)
}

fn run_batch(format: Format, mut commands: Vec<String>, bail: bool, dry_run: bool) -> ExitCode {
    if commands.is_empty() {
        let mut input = String::new();
        if let Err(error) = io::stdin().read_to_string(&mut input) {
            return write_batch_error(
                format,
                ErrorCode::Internal,
                &format!("read batch commands from stdin: {error}"),
                1,
            );
        }
        if input.trim().is_empty() {
            return write_batch_error(
                format,
                ErrorCode::Internal,
                "stdin must contain a JSON array of command strings: unexpected end of JSON input",
                1,
            );
        }
        commands = match decode_batch_commands(&input) {
            Ok(commands) => commands,
            Err(error) => {
                return write_batch_error(
                    format,
                    ErrorCode::Internal,
                    &format!("stdin must contain a JSON array of command strings: {error}"),
                    1,
                );
            }
        };
    }
    if commands.is_empty() {
        return write_batch_error(
            format,
            ErrorCode::InvalidArgs,
            "batch requires at least one command",
            2,
        );
    }

    let report = batch::run(&commands, dry_run, bail, execute_batch_item);
    if format == Format::Json {
        return match serde_json::to_string(&BatchSuccess {
            success: true,
            data: &report,
        }) {
            Ok(mut output) => {
                output.push('\n');
                write_stdout(&output)
            }
            Err(_) => ExitCode::from(1),
        };
    }
    if format == Format::Text {
        return match serde_json::to_string_pretty(&report) {
            Ok(mut output) => {
                output.push('\n');
                write_stdout(&output)
            }
            Err(_) => ExitCode::from(1),
        };
    }
    write_stdout(&batch::render_yaml(&report))
}

fn decode_batch_commands(input: &str) -> Result<Vec<String>, String> {
    let value: serde_json::Value = serde_json::from_str(input).map_err(|error| {
        if error.is_eof() {
            return "unexpected end of JSON input".to_owned();
        }
        let trimmed = input.trim_start();
        if trimmed.starts_with('n') && trimmed != "null" {
            let expected = "null";
            if let Some((index, actual)) = trimmed
                .chars()
                .enumerate()
                .find(|(index, character)| expected.chars().nth(*index) != Some(*character))
            {
                let wanted = expected.chars().nth(index).unwrap_or(' ');
                return format!(
                    "invalid character {actual:?} in literal null (expecting {wanted:?})"
                );
            }
        }
        error.to_string()
    })?;
    let serde_json::Value::Array(items) = value else {
        if value.is_null() {
            return Ok(Vec::new());
        }
        return Err(format!(
            "json: cannot unmarshal {} into Go value of type []string",
            json_type_name(&value)
        ));
    };
    items
        .into_iter()
        .map(|item| match item {
            serde_json::Value::String(command) => Ok(command),
            serde_json::Value::Null => Ok(String::new()),
            other => Err(format!(
                "json: cannot unmarshal {} into Go value of type string",
                json_type_name(&other)
            )),
        })
        .collect()
}

fn json_type_name(value: &serde_json::Value) -> &'static str {
    match value {
        serde_json::Value::Null => "null",
        serde_json::Value::Bool(_) => "bool",
        serde_json::Value::Number(_) => "number",
        serde_json::Value::String(_) => "string",
        serde_json::Value::Array(_) => "array",
        serde_json::Value::Object(_) => "object",
    }
}

fn execute_batch_item(argv: &[String]) -> ItemOutput {
    let args: Vec<OsString> = argv.iter().map(OsString::from).collect();
    match parse(&args) {
        Ok(Action::RootVersion) => ItemOutput {
            stdout: render_root_version(VERSION),
            error: None,
        },
        Ok(Action::Version { structured: true }) => match render_version_json(VERSION) {
            Ok(stdout) => ItemOutput {
                stdout,
                error: None,
            },
            Err(error) => ItemOutput {
                stdout: String::new(),
                error: Some(error.to_string()),
            },
        },
        Ok(Action::Version { structured: false }) => ItemOutput {
            stdout: render_version_text(VERSION),
            error: None,
        },
        Ok(Action::ConfigShow { format, flags }) => match render_config_show(format, flags) {
            Ok(stdout) => ItemOutput {
                stdout,
                error: None,
            },
            Err(error) => ItemOutput {
                stdout: String::new(),
                error: Some(error),
            },
        },
        Ok(Action::StateKeyInit { format }) => {
            let resolver = KeyResolver::new(SystemKeySources::default());
            match resolver.initialize() {
                Ok(result) => match render_state_key_init(format, &result) {
                    Ok(stdout) => ItemOutput {
                        stdout,
                        error: None,
                    },
                    Err(error) => ItemOutput {
                        stdout: String::new(),
                        error: Some(error.to_string()),
                    },
                },
                Err(error) => ItemOutput {
                    stdout: String::new(),
                    error: Some(error.to_string()),
                },
            }
        }
        Ok(_) => ItemOutput {
            stdout: String::new(),
            error: Some(format!("batch item {:?} is not available yet", argv[0])),
        },
        Err(error) => ItemOutput {
            stdout: String::new(),
            error: Some(error.message),
        },
    }
}

fn write_batch_error(format: Format, code: ErrorCode, message: &str, exit_code: u8) -> ExitCode {
    if format == Format::Text {
        let _ = writeln!(io::stderr(), "{message}");
    } else if let Ok(output) = Envelope::failure(code, message).render(format) {
        let _ = io::stdout().write_all(output.as_bytes());
    }
    ExitCode::from(exit_code)
}

fn run_config_show(format: Format, flags: FlagOverrides) -> ExitCode {
    match render_config_show(format, flags) {
        Ok(output) => write_stdout(&output),
        Err(error) => write_config_error(format, &error),
    }
}

fn render_config_show(format: Format, flags: FlagOverrides) -> Result<String, String> {
    let context = match LoadContext::from_process(flags) {
        Ok(context) => context,
        Err(error) => return Err(error.to_string()),
    };
    let result = match load(&context) {
        Ok(result) => result,
        Err(error) => return Err(error.to_string()),
    };
    if format == Format::Text {
        return Ok(render_show_text(&result));
    }
    if format == Format::Json {
        let payload = ConfigSuccess {
            success: true,
            data: ConfigData {
                fields: show_fields(&result),
            },
        };
        return match serde_json::to_string(&payload) {
            Ok(mut output) => {
                output.push('\n');
                Ok(output)
            }
            Err(error) => Err(error.to_string()),
        };
    }
    Ok(render_show_yaml(&result))
}

fn write_config_error(format: Format, message: &str) -> ExitCode {
    if format == Format::Text {
        let _ = writeln!(io::stderr(), "{message}");
    } else {
        let envelope_message = message.split_once(':').map_or(message, |(outer, _)| outer);
        let envelope = Envelope::failure(ErrorCode::Config, envelope_message);
        if let Ok(output) = envelope.render(format) {
            let _ = io::stdout().write_all(output.as_bytes());
        }
    }
    ExitCode::from(9)
}

fn parse(args: &[OsString]) -> Result<Action, ParseError> {
    let values: Vec<String> = args
        .iter()
        .map(|value| value.to_string_lossy().into_owned())
        .collect();
    if root_version_requested(&values)? {
        return Ok(Action::RootVersion);
    }
    let Some(command_index) = top_command_index(&values) else {
        if let Some(value) = values.iter().find(|value| value.starts_with('-')) {
            return Err(ParseError {
                message: format!("unknown flag: {value}"),
                exit_code: 2,
            });
        }
        return Err(ParseError {
            message: "a command is required".to_owned(),
            exit_code: 2,
        });
    };
    match values[command_index].as_str() {
        "version" => parse_version(&values, command_index),
        "config" => parse_config(&values, command_index),
        "batch" => parse_batch(&values, command_index),
        "state" => parse_state_lifecycle(&values, command_index),
        "daemon" => parse_daemon(&values, command_index),
        "mcp" => parse_mcp(&values, command_index),
        "flow" | "workflow" => parse_flow(&values, command_index),
        "profiles" => parse_profiles(&values, command_index),
        "tools" => parse_tools(&values, command_index),
        "fetch" | "read" | "open" | "goto" | "snapshot" | "click" | "fill" | "type" | "press"
        | "wait" | "back" | "forward" | "reload" | "get" | "is" | "find" => {
            parse_dispatch(&values, command_index)
        }
        command => Err(ParseError {
            message: format!("unknown command {command:?} for \"symbrowse\""),
            exit_code: 2,
        }),
    }
}

fn root_version_requested(values: &[String]) -> Result<bool, ParseError> {
    for value in values.iter().take_while(|value| value.as_str() != "--") {
        if matches!(value.as_str(), "-v" | "--version") {
            return Ok(true);
        }
        if let Some(raw) = value.strip_prefix("--version=") {
            return parse_bool("--version", raw);
        }
    }
    Ok(false)
}

fn top_command_index(values: &[String]) -> Option<usize> {
    let mut index = 0;
    while index < values.len() {
        let value = &values[index];
        if value == "--" {
            return (index + 1 < values.len()).then_some(index + 1);
        }
        if matches!(value.as_str(), "--json" | "-v" | "--version")
            || value.starts_with("--json=")
            || value.starts_with("--version=")
        {
            index += 1;
            continue;
        }
        if matches!(
            value.as_str(),
            "--output"
                | "--log-level"
                | "--log-format"
                | "--config-dir"
                | "--cache-dir"
                | "--state-dir"
                | "--executable-path"
        ) {
            index += 2;
            continue;
        }
        if value.starts_with("--output=")
            || value.starts_with("--log-level=")
            || value.starts_with("--log-format=")
            || value.starts_with("--config-dir=")
            || value.starts_with("--cache-dir=")
            || value.starts_with("--state-dir=")
            || value.starts_with("--executable-path=")
        {
            index += 1;
            continue;
        }
        if value.starts_with('-') {
            return None;
        }
        return Some(index);
    }
    None
}

fn parse_dispatch(values: &[String], command_index: usize) -> Result<Action, ParseError> {
    let name = values[command_index].as_str();
    let mut command = if name == "fetch" {
        "fetch.url".to_owned()
    } else {
        name.to_owned()
    };
    let mut session = "default".to_owned();
    let mut format = Format::Text;
    let mut positional = Vec::new();
    let mut args = serde_json::Map::new();
    let mut index = command_index + 1;
    while index < values.len() {
        let value = &values[index];
        match value.as_str() {
            "--json" => format = Format::Json,
            "--output" => {
                index += 1;
                format = parse_format(required_value(values, index, "--output")?)?;
            }
            "--session" => {
                index += 1;
                session = required_value(values, index, "--session")?.to_owned();
            }
            "--selector" => {
                index += 1;
                args.insert(
                    "selector".into(),
                    serde_json::Value::String(required_value(values, index, "--selector")?.into()),
                );
            }
            "--kind" => {
                index += 1;
                args.insert(
                    "kind".into(),
                    serde_json::Value::String(required_value(values, index, "--kind")?.into()),
                );
            }
            "--value" => {
                index += 1;
                args.insert(
                    "value".into(),
                    serde_json::Value::String(required_value(values, index, "--value")?.into()),
                );
            }
            "--key" => {
                index += 1;
                args.insert(
                    "key".into(),
                    serde_json::Value::String(required_value(values, index, "--key")?.into()),
                );
            }
            "--format" => {
                index += 1;
                args.insert(
                    "format".into(),
                    serde_json::Value::String(required_value(values, index, "--format")?.into()),
                );
            }
            "--action" | "--name" => {
                let key = value.trim_start_matches('-');
                index += 1;
                args.insert(
                    key.into(),
                    serde_json::Value::String(required_value(values, index, value)?.into()),
                );
            }
            "--exact" => {
                args.insert("exact".into(), serde_json::Value::Bool(true));
            }
            "--index" => {
                index += 1;
                let index_value = required_value(values, index, "--index")?
                    .parse::<u64>()
                    .map_err(|_| ParseError {
                        message: "invalid index".into(),
                        exit_code: 2,
                    })?;
                args.insert("index".into(), serde_json::Value::from(index_value));
            }
            "--attribute" => {
                index += 1;
                args.insert(
                    "attribute".into(),
                    serde_json::Value::String(required_value(values, index, "--attribute")?.into()),
                );
            }
            "--query" => {
                index += 1;
                args.insert(
                    "query".into(),
                    serde_json::Value::String(required_value(values, index, "--query")?.into()),
                );
            }
            "--ms" => {
                index += 1;
                let ms = required_value(values, index, "--ms")?
                    .parse::<u64>()
                    .map_err(|_| ParseError {
                        message: "invalid milliseconds".into(),
                        exit_code: 2,
                    })?;
                args.insert("ms".into(), serde_json::Value::from(ms));
            }
            "--depth" => {
                index += 1;
                let depth = required_value(values, index, "--depth")?
                    .parse::<u64>()
                    .map_err(|_| ParseError {
                        message: "invalid depth".into(),
                        exit_code: 2,
                    })?;
                args.insert("depth".into(), serde_json::Value::from(depth));
            }
            "--compact" | "--interactive" | "--urls" | "--diff" | "--engine-hint" => {
                let key = value.trim_start_matches('-').replace('-', "_");
                args.insert(key, serde_json::Value::Bool(true));
            }
            value if value.starts_with("--session=") => session = value[10..].to_owned(),
            value if value.starts_with("--output=") => format = parse_format(&value[9..])?,
            value if value.starts_with("--kind=") => {
                args.insert("kind".into(), serde_json::Value::String(value[7..].into()));
            }
            value if value.starts_with("--selector=") => {
                args.insert(
                    "selector".into(),
                    serde_json::Value::String(value[11..].into()),
                );
            }
            value if value.starts_with("--value=") => {
                args.insert("value".into(), serde_json::Value::String(value[8..].into()));
            }
            value if value.starts_with("--key=") => {
                args.insert("key".into(), serde_json::Value::String(value[6..].into()));
            }
            value if value.starts_with("--query=") => {
                args.insert("query".into(), serde_json::Value::String(value[8..].into()));
            }
            value if value.starts_with('-') => {
                return Err(ParseError {
                    message: format!("unknown flag: {value}"),
                    exit_code: 2,
                });
            }
            _ => positional.push(value.clone()),
        }
        index += 1;
    }
    match name {
        "fetch" | "read" | "open" | "goto" => {
            take_positional(&mut args, &mut positional, "url");
        }
        "click" | "fill" => take_positional(&mut args, &mut positional, "selector"),
        "press" => take_positional(&mut args, &mut positional, "key"),
        "get" | "is" => take_positional(&mut args, &mut positional, "kind"),
        _ => {}
    }
    if name == "fill" {
        take_positional(&mut args, &mut positional, "value");
    }
    if name == "type" {
        take_positional(&mut args, &mut positional, "value");
    }
    if name == "find" {
        if !args.contains_key("kind") && !args.contains_key("query") {
            match positional.len() {
                0 => {}
                1 => {
                    args.insert("kind".into(), serde_json::Value::String("text".into()));
                    args.insert(
                        "query".into(),
                        serde_json::Value::String(positional.remove(0)),
                    );
                }
                _ => {
                    take_positional(&mut args, &mut positional, "kind");
                    take_positional(&mut args, &mut positional, "query");
                }
            }
        } else {
            take_positional(&mut args, &mut positional, "kind");
            take_positional(&mut args, &mut positional, "query");
        }
    }
    if name == "get" || name == "is" {
        take_positional(&mut args, &mut positional, "selector");
    }
    if name == "wait"
        && !args.contains_key("kind")
        && let Some(value) = positional.first()
    {
        args.insert("kind".into(), serde_json::Value::String("selector".into()));
        args.insert("selector".into(), serde_json::Value::String(value.clone()));
        positional.remove(0);
    }
    if name == "get" || name == "is" {
        let kind = args
            .get("kind")
            .and_then(serde_json::Value::as_str)
            .unwrap_or("text");
        command = format!("{name}.{kind}");
    }
    if !positional.is_empty() {
        return Err(ParseError {
            message: format!("unknown argument {:?}", positional[0]),
            exit_code: 2,
        });
    }
    let required = match name {
        "fetch" | "open" | "goto" => &["url"][..],
        "click" => &["selector"][..],
        "fill" => &["selector", "value"][..],
        "type" => &["value"][..],
        "press" => &["key"][..],
        "get" | "is" => &["kind"][..],
        "find" => &["kind", "query"][..],
        "wait" => &["kind"][..],
        _ => &[][..],
    };
    for required in required {
        if args
            .get(*required)
            .and_then(serde_json::Value::as_str)
            .is_none_or(str::is_empty)
        {
            return Err(ParseError {
                message: format!("{name} requires {required}"),
                exit_code: 2,
            });
        }
    }
    if name == "fill"
        && args
            .get("value")
            .and_then(serde_json::Value::as_str)
            .is_none()
    {
        return Err(ParseError {
            message: "fill requires value".into(),
            exit_code: 2,
        });
    }
    Ok(Action::Dispatch {
        session,
        command,
        args: serde_json::Value::Object(args),
        format,
    })
}

fn take_positional(
    args: &mut serde_json::Map<String, serde_json::Value>,
    positional: &mut Vec<String>,
    key: &str,
) {
    if !args.contains_key(key)
        && let Some(value) = positional.first().cloned()
    {
        args.insert(key.to_owned(), serde_json::Value::String(value));
        positional.remove(0);
    }
}

fn parse_profiles(values: &[String], index: usize) -> Result<Action, ParseError> {
    if values.get(index + 1).map(String::as_str) != Some("list") {
        return Err(ParseError {
            message: "profiles requires list".into(),
            exit_code: 2,
        });
    }
    let mut format = Format::Text;
    let mut i = index + 2;
    while i < values.len() {
        match values[i].as_str() {
            "--json" => format = Format::Json,
            "--output" => {
                i += 1;
                format = parse_format(required_value(values, i, "--output")?)?;
            }
            value if value.starts_with("--output=") => format = parse_format(&value[9..])?,
            value => {
                return Err(ParseError {
                    message: format!("unknown argument {value:?}"),
                    exit_code: 2,
                });
            }
        }
        i += 1;
    }
    Ok(Action::ProfileList { format })
}

fn parse_tools(values: &[String], index: usize) -> Result<Action, ParseError> {
    if values.get(index + 1).map(String::as_str) != Some("list") {
        return Err(ParseError {
            message: "tools requires list".into(),
            exit_code: 2,
        });
    }
    let mut profiles = "core".to_owned();
    let mut format = Format::Text;
    let mut i = index + 2;
    while i < values.len() {
        match values[i].as_str() {
            "--json" => format = Format::Json,
            "--output" => {
                i += 1;
                format = parse_format(required_value(values, i, "--output")?)?;
            }
            "--profiles" | "--tools" => {
                i += 1;
                profiles = required_value(values, i, "--profiles")?.to_owned();
            }
            value if value.starts_with("--profiles=") => profiles = value[11..].to_owned(),
            value if value.starts_with("--tools=") => profiles = value[8..].to_owned(),
            value => {
                return Err(ParseError {
                    message: format!("unknown argument {value:?}"),
                    exit_code: 2,
                });
            }
        }
        i += 1;
    }
    Ok(Action::ToolList { profiles, format })
}

fn parse_flow(values: &[String], index: usize) -> Result<Action, ParseError> {
    let (subcommand, mut i) = match values.get(index + 1) {
        None => ("list", index + 1),
        Some(value) if value.starts_with('-') => ("list", index + 1),
        Some(value) => (value.as_str(), index + 2),
    };
    let mut format = Format::Text;
    let mut path = None;
    let mut session = "default".to_owned();
    let mut dry_run = false;
    let mut inputs = BTreeMap::new();
    while i < values.len() {
        match values[i].as_str() {
            "--json" => format = Format::Json,
            "--dry-run" => dry_run = true,
            "--output" => {
                i += 1;
                format = parse_format(required_value(values, i, "--output")?)?;
            }
            "--session" => {
                i += 1;
                session = required_value(values, i, "--session")?.to_owned();
            }
            "--input" => {
                i += 1;
                let pair = required_value(values, i, "--input")?;
                let (key, value) = pair.split_once('=').ok_or_else(|| ParseError {
                    message: "--input requires key=value".into(),
                    exit_code: 2,
                })?;
                inputs.insert(key.to_owned(), value.to_owned());
            }
            value if value.starts_with("--input=") => {
                let pair = &value[8..];
                let (key, val) = pair.split_once('=').ok_or_else(|| ParseError {
                    message: "--input requires key=value".into(),
                    exit_code: 2,
                })?;
                inputs.insert(key.to_owned(), val.to_owned());
            }
            value if value.starts_with('-') => {
                return Err(ParseError {
                    message: format!("unknown flag: {value}"),
                    exit_code: 2,
                });
            }
            value if path.is_none() => path = Some(PathBuf::from(value)),
            value => {
                return Err(ParseError {
                    message: format!("unknown argument {value:?}"),
                    exit_code: 2,
                });
            }
        }
        i += 1;
    }
    match subcommand {
        "list" if path.is_none() => Ok(Action::FlowList { format }),
        "validate" => path
            .map(|path| Action::FlowValidate { path, format })
            .ok_or_else(|| ParseError {
                message: "flow validate requires a path".into(),
                exit_code: 2,
            }),
        "run" => path
            .map(|path| Action::FlowRun {
                session,
                path,
                inputs,
                dry_run,
                format,
            })
            .ok_or_else(|| ParseError {
                message: "flow run requires a path".into(),
                exit_code: 2,
            }),
        other => Err(ParseError {
            message: format!("unknown command {other:?} for flow"),
            exit_code: 2,
        }),
    }
}
fn parse_daemon(values: &[String], daemon_index: usize) -> Result<Action, ParseError> {
    let mut session = "default".to_owned();
    let mut engine = String::new();
    let mut mode = String::new();
    let mut ssrf = None;
    let mut allow_private = None;
    let mut format = Format::Text;
    let subcommand = values
        .get(daemon_index + 1)
        .map(String::as_str)
        .unwrap_or("");
    let mut index = daemon_index + 1;
    while index < values.len() {
        match values[index].as_str() {
            "status" | "stop" => {}
            "--session" => {
                index += 1;
                session = required_value(values, index, "--session")?.to_owned();
            }
            "--engine" => {
                index += 1;
                engine = required_value(values, index, "--engine")?.to_owned();
            }
            "--mode" => {
                index += 1;
                mode = required_value(values, index, "--mode")?.to_owned();
            }
            "--ssrf" => {
                if let Some(value @ ("true" | "false")) = values.get(index + 1).map(String::as_str)
                {
                    index += 1;
                    ssrf = Some(parse_bool("--ssrf", value)?);
                } else {
                    ssrf = Some(true);
                }
            }
            "--mcp-mode" => {}
            "--allow-private" => allow_private = Some(true),
            "--json" => format = Format::Json,
            "--output" => {
                index += 1;
                format = parse_format(required_value(values, index, "--output")?)?;
            }
            value if value.starts_with("--session=") => session = value[10..].to_owned(),
            value if value.starts_with("--engine=") => engine = value[9..].to_owned(),
            value if value.starts_with("--mode=") => mode = value[7..].to_owned(),
            value if value.starts_with("--ssrf=") => {
                ssrf = Some(parse_bool("--ssrf", &value[7..])?);
            }
            "--allow-private=true" => allow_private = Some(true),
            "--allow-private=false" => allow_private = Some(false),
            value if value.starts_with("--output=") => format = parse_format(&value[9..])?,
            "--json=true" => format = Format::Json,
            value if value.starts_with('-') => {
                return Err(ParseError {
                    message: format!("unknown flag: {value}"),
                    exit_code: 2,
                });
            }
            "daemon" => {}
            value => {
                return Err(ParseError {
                    message: format!("unknown command {value:?} for daemon"),
                    exit_code: 2,
                });
            }
        }
        index += 1;
    }
    if subcommand == "status" || subcommand == "stop" {
        return Ok(Action::DaemonLifecycle {
            session,
            command: format!("daemon.{subcommand}"),
            format,
        });
    }
    Ok(Action::DaemonRun {
        session,
        mode,
        engine,
        ssrf,
        allow_private,
    })
}

fn parse_mcp(values: &[String], mcp_index: usize) -> Result<Action, ParseError> {
    let mut session = String::from("default");
    let mut profiles = String::from("core");
    let mut list_profiles = false;
    let mut allow_private = false;
    let mut engine = None;
    let mut index = 0;
    while index < values.len() {
        if index == mcp_index {
            index += 1;
            continue;
        }
        let value = &values[index];
        match value.as_str() {
            "--list-profiles" => list_profiles = true,
            "--allow-private" => allow_private = true,
            "--session" => {
                index += 1;
                session = required_value(values, index, "--session")?.to_owned();
            }
            "--tools" => {
                index += 1;
                profiles = required_value(values, index, "--tools")?.to_owned();
            }
            "--engine" => {
                index += 1;
                engine = Some(required_value(values, index, "--engine")?.to_owned());
            }
            _ if value.starts_with("--session=") => session = value[10..].to_owned(),
            _ if value.starts_with("--tools=") => profiles = value[8..].to_owned(),
            _ if value.starts_with("--engine=") => engine = Some(value[9..].to_owned()),
            _ if value == "--json" || value.starts_with("--json=") => {}
            _ if value.starts_with('-') => {
                return Err(ParseError {
                    message: format!("unknown flag: {value}"),
                    exit_code: 2,
                });
            }
            _ => {
                return Err(ParseError {
                    message: format!("unknown command {value:?} for \"symbrowse mcp\""),
                    exit_code: 2,
                });
            }
        }
        index += 1;
    }
    if let Some(value) = engine.as_deref()
        && !matches!(value, "chrome" | "static" | "safari-attach" | "safari-bidi")
    {
        return Err(ParseError {
            message: format!(
                "invalid engine {value:?}: use one of chrome, static, safari-attach, safari-bidi"
            ),
            exit_code: 1,
        });
    }
    if list_profiles {
        Ok(Action::McpListProfiles)
    } else {
        Ok(Action::Mcp {
            session,
            profiles,
            allow_private,
            engine,
        })
    }
}

fn parse_state_lifecycle(values: &[String], state_index: usize) -> Result<Action, ParseError> {
    let subcommand = values
        .get(state_index + 1)
        .map(String::as_str)
        .ok_or_else(|| ParseError {
            message: "state requires a subcommand".into(),
            exit_code: 2,
        })?;
    if subcommand == "key" {
        return parse_state(values, state_index);
    }
    let mut session = "default".to_owned();
    let mut output = "text".to_owned();
    let mut json = false;
    let mut name = None;
    let mut older_than = None;
    let mut index = state_index + 2;
    while index < values.len() {
        match values[index].as_str() {
            "--json" => json = true,
            "--output" => {
                index += 1;
                output = required_value(values, index, "--output")?.to_owned();
            }
            "--session" => {
                index += 1;
                session = required_value(values, index, "--session")?.to_owned();
            }
            "--older-than" => {
                index += 1;
                older_than = Some(
                    required_value(values, index, "--older-than")?
                        .parse()
                        .map_err(|_| ParseError {
                            message: "invalid day count".into(),
                            exit_code: 2,
                        })?,
                );
            }
            value if value.starts_with("--json=") => json = parse_bool("--json", &value[7..])?,
            value if value.starts_with("--output=") => output = value[9..].to_owned(),
            value if value.starts_with("--session=") => session = value[10..].to_owned(),
            value if value.starts_with('-') => {
                return Err(ParseError {
                    message: format!("unknown flag: {value}"),
                    exit_code: 2,
                });
            }
            value if name.is_none() => name = Some(value.to_owned()),
            value => {
                return Err(ParseError {
                    message: format!("unknown argument {value:?}"),
                    exit_code: 2,
                });
            }
        }
        index += 1;
    }
    let format = if json {
        Format::Json
    } else {
        parse_format(&output)?
    };
    let command = match subcommand {
        "save" | "load" | "show" | "clear" | "list" | "clean" => format!("state.{subcommand}"),
        _ => {
            return Err(ParseError {
                message: format!("unknown command {subcommand:?} for state"),
                exit_code: 2,
            });
        }
    };
    if matches!(subcommand, "save" | "load" | "show" | "clear") && name.is_none() {
        return Err(ParseError {
            message: format!("state {subcommand} requires a name"),
            exit_code: 2,
        });
    }
    Ok(Action::StateOperation {
        session,
        command,
        name,
        older_than,
        format,
    })
}

fn parse_state(values: &[String], state_index: usize) -> Result<Action, ParseError> {
    let key_index = values
        .iter()
        .enumerate()
        .skip(state_index + 1)
        .find_map(|(index, value)| (value == "key").then_some(index))
        .ok_or_else(|| ParseError {
            message: "state requires a subcommand".to_owned(),
            exit_code: 2,
        })?;
    let init_index = values
        .iter()
        .enumerate()
        .skip(key_index + 1)
        .find_map(|(index, value)| (value == "init").then_some(index))
        .ok_or_else(|| ParseError {
            message: "state key requires a subcommand".to_owned(),
            exit_code: 2,
        })?;
    let mut json = false;
    let mut output = String::from("text");
    let mut index = 0;
    while index < values.len() {
        if matches!(index, value if value == state_index || value == key_index || value == init_index)
        {
            index += 1;
            continue;
        }
        let value = &values[index];
        match value.as_str() {
            "--json" => json = true,
            "--output" => {
                index += 1;
                output = required_value(values, index, "--output")?.to_owned();
            }
            _ if value.starts_with("--json=") => json = parse_bool("--json", &value[7..])?,
            _ if value.starts_with("--output=") => output = value[9..].to_owned(),
            _ if value.starts_with('-') => {
                return Err(ParseError {
                    message: format!("unknown flag: {value}"),
                    exit_code: 2,
                });
            }
            _ => {
                return Err(ParseError {
                    message: format!("unknown command {value:?} for \"symbrowse state key init\""),
                    exit_code: 2,
                });
            }
        }
        index += 1;
    }
    Ok(Action::StateKeyInit {
        format: if json {
            Format::Json
        } else {
            parse_format(&output)?
        },
    })
}

fn parse_batch(values: &[String], batch_index: usize) -> Result<Action, ParseError> {
    let mut json = false;
    let mut output = String::from("text");
    let mut bail = false;
    let mut dry_run = false;
    let mut commands = Vec::new();
    let mut index = 0;
    let mut positional_only = false;
    while index < values.len() {
        if index == batch_index {
            index += 1;
            continue;
        }
        let value = &values[index];
        if positional_only {
            commands.push(value.clone());
            index += 1;
            continue;
        }
        match value.as_str() {
            "--" => positional_only = true,
            "--json" => json = true,
            "--bail" => bail = true,
            "--dry-run" => dry_run = true,
            "--output" => {
                index += 1;
                output = required_value(values, index, "--output")?.to_owned();
            }
            _ if value.starts_with("--json=") => json = parse_bool("--json", &value[7..])?,
            _ if value.starts_with("--bail=") => bail = parse_bool("--bail", &value[7..])?,
            _ if value.starts_with("--dry-run=") => {
                dry_run = parse_bool("--dry-run", &value[10..])?;
            }
            _ if value.starts_with("--output=") => output = value[9..].to_owned(),
            _ if value.starts_with('-') => {
                return Err(ParseError {
                    message: format!("unknown flag: {value}"),
                    exit_code: 2,
                });
            }
            _ => commands.push(value.clone()),
        }
        index += 1;
    }
    let format = if json {
        Format::Json
    } else {
        parse_format(&output)?
    };
    Ok(Action::Batch {
        format,
        commands,
        bail,
        dry_run,
    })
}

fn parse_bool(name: &str, value: &str) -> Result<bool, ParseError> {
    match value {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(false),
        _ => Err(ParseError {
            message: format!(
                "invalid argument {value:?} for {name:?} flag: strconv.ParseBool: parsing {value:?}: invalid syntax"
            ),
            exit_code: 2,
        }),
    }
}

fn parse_version(values: &[String], version_index: usize) -> Result<Action, ParseError> {
    let mut json = false;
    let mut output = String::from("text");
    let mut index = 0;
    let mut after_separator = false;
    while index < values.len() {
        let value = &values[index];
        if index == version_index {
            index += 1;
            continue;
        }
        if after_separator {
            return Err(unknown_version_argument(value));
        }
        match value.as_str() {
            "--" => after_separator = true,
            "--json" => json = true,
            "--output" => {
                index += 1;
                output = required_value(values, index, "--output")?.to_owned();
            }
            _ if value.starts_with("--output=") => output = value[9..].to_owned(),
            _ => return Err(unknown_version_argument(value)),
        }
        index += 1;
    }
    let format = parse_format(&output)?;
    Ok(Action::Version {
        structured: json || format != Format::Text,
    })
}

fn parse_config(values: &[String], config_index: usize) -> Result<Action, ParseError> {
    let Some(show_index) = values
        .iter()
        .enumerate()
        .skip(config_index + 1)
        .find_map(|(index, value)| (value == "show").then_some(index))
    else {
        return Err(ParseError {
            message: "config requires the show subcommand".to_owned(),
            exit_code: 2,
        });
    };
    let mut json = false;
    let mut output = String::from("text");
    let mut flags = FlagOverrides::default();
    let mut index = 0;
    while index < values.len() {
        if index == config_index || index == show_index {
            index += 1;
            continue;
        }
        let value = &values[index];
        match value.as_str() {
            "--json" => json = true,
            "--output" => {
                index += 1;
                output = required_value(values, index, "--output")?.to_owned();
            }
            "--log-level" => set_flag(values, &mut index, "--log-level", &mut flags.log_level)?,
            "--log-format" => set_flag(values, &mut index, "--log-format", &mut flags.log_format)?,
            "--config-dir" => set_flag(values, &mut index, "--config-dir", &mut flags.config_dir)?,
            "--cache-dir" => set_flag(values, &mut index, "--cache-dir", &mut flags.cache_dir)?,
            "--state-dir" => set_flag(values, &mut index, "--state-dir", &mut flags.state_dir)?,
            "--executable-path" => set_flag(
                values,
                &mut index,
                "--executable-path",
                &mut flags.executable_path,
            )?,
            "--mode" => set_flag(values, &mut index, "--mode", &mut flags.mode)?,
            "--engine" => set_flag(values, &mut index, "--engine", &mut flags.engine)?,
            _ if value.starts_with("--output=") => output = value[9..].to_owned(),
            _ => {
                return Err(ParseError {
                    message: if value.starts_with('-') {
                        format!("unknown flag: {value}")
                    } else {
                        format!("unknown command {value:?} for \"symbrowse config show\"")
                    },
                    exit_code: 2,
                });
            }
        }
        index += 1;
    }
    let format = if json {
        Format::Json
    } else {
        parse_format(&output)?
    };
    Ok(Action::ConfigShow { format, flags })
}

fn set_flag(
    values: &[String],
    index: &mut usize,
    name: &str,
    target: &mut Option<String>,
) -> Result<(), ParseError> {
    *index += 1;
    *target = Some(required_value(values, *index, name)?.to_owned());
    Ok(())
}

fn required_value<'a>(
    values: &'a [String],
    index: usize,
    name: &str,
) -> Result<&'a str, ParseError> {
    values
        .get(index)
        .map(String::as_str)
        .ok_or_else(|| ParseError {
            message: format!("flag needs an argument: {name}"),
            exit_code: 2,
        })
}

fn parse_format(value: &str) -> Result<Format, ParseError> {
    match value {
        "text" => Ok(Format::Text),
        "json" => Ok(Format::Json),
        "yaml" => Ok(Format::Yaml),
        _ => Err(ParseError {
            message: format!("invalid --output format {value:?}: want text, json or yaml"),
            exit_code: 2,
        }),
    }
}

fn unknown_version_argument(value: &str) -> ParseError {
    ParseError {
        message: format!("unknown command {value:?} for \"symbrowse version\""),
        exit_code: 2,
    }
}

fn write_stdout(value: &str) -> ExitCode {
    if io::stdout().write_all(value.as_bytes()).is_err() {
        return ExitCode::from(1);
    }
    ExitCode::SUCCESS
}

#[cfg(test)]
mod tests {
    use super::{Action, Format, KeyInitResult, ParseError, parse, render_state_key_init};
    use std::ffi::OsString;

    fn args(values: &[&str]) -> Vec<OsString> {
        values.iter().map(OsString::from).collect()
    }

    #[test]
    fn parses_version_flag_permutations() {
        for values in [
            &["--json", "version"][..],
            &["version", "--json"][..],
            &["--output=json", "version"][..],
            &["version", "--output", "yaml"][..],
        ] {
            assert_eq!(
                parse(&args(values)),
                Ok(Action::Version { structured: true })
            );
        }
        assert_eq!(
            parse(&args(&["version"])),
            Ok(Action::Version { structured: false })
        );
    }

    #[test]
    fn parses_config_show() {
        let parsed = parse(&args(&["config", "show", "--json", "--state-dir", "state"]))
            .expect("parse config show");
        let Action::ConfigShow { format, flags } = parsed else {
            panic!("wrong action");
        };
        assert_eq!(format, Format::Json);
        assert_eq!(flags.state_dir.as_deref(), Some("state"));
    }

    #[test]
    fn parses_batch_flags_and_commands() {
        assert_eq!(
            parse(&args(&[
                "--json",
                "batch",
                "--bail",
                "--dry-run",
                "version --json",
            ])),
            Ok(Action::Batch {
                format: Format::Json,
                commands: vec!["version --json".to_owned()],
                bail: true,
                dry_run: true,
            })
        );
    }

    #[test]
    fn state_key_init_parser_and_output_match_go() {
        assert_eq!(
            parse(&args(&["--json", "state", "key", "init"])),
            Ok(Action::StateKeyInit {
                format: Format::Json
            })
        );
        let result = KeyInitResult {
            action: "already_configured".to_owned(),
            configured: true,
            key_source: "symvault".to_owned(),
            instruction: String::new(),
        };
        assert_eq!(
            render_state_key_init(Format::Text, &result).unwrap(),
            "State encryption key already_configured via symvault.\n"
        );
        assert_eq!(
            render_state_key_init(Format::Json, &result).unwrap(),
            "{\"success\":true,\"data\":{\"action\":\"already_configured\",\"configured\":true,\"key_source\":\"symvault\"}}\n"
        );
        assert_eq!(
            render_state_key_init(Format::Yaml, &result).unwrap(),
            "success: true\ndata:\n    action: already_configured\n    configured: true\n    keysource: symvault\n    instruction: \"\"\nwarnings: []\nerror: null\n"
        );
    }

    #[test]
    fn parses_mcp_daemon_autostart_flags() {
        assert_eq!(
            parse(&args(&[
                "daemon",
                "--session",
                "auto",
                "--engine",
                "static",
                "--ssrf",
                "--mcp-mode",
            ])),
            Ok(Action::DaemonRun {
                session: "auto".to_owned(),
                mode: String::new(),
                engine: "static".to_owned(),
                ssrf: Some(true),
                allow_private: None,
            })
        );
    }

    #[test]
    fn mcp_rejects_unknown_engine_like_go() {
        let error = parse(&args(&["mcp", "--engine", "netscape"]))
            .expect_err("unknown engine must fail before stdio starts");
        assert_eq!(error.exit_code, 1);
        assert_eq!(
            error.message,
            "invalid engine \"netscape\": use one of chrome, static, safari-attach, safari-bidi"
        );
    }

    #[test]
    fn preserves_version_errors() {
        assert_eq!(
            parse(&args(&["version", "extra"])),
            Err(ParseError {
                message: "unknown command \"extra\" for \"symbrowse version\"".to_owned(),
                exit_code: 2,
            })
        );
        assert_eq!(
            parse(&args(&["version", "--output", "wat"])),
            Err(ParseError {
                message: "invalid --output format \"wat\": want text, json or yaml".to_owned(),
                exit_code: 2,
            })
        );
    }

    #[test]
    fn browser_fetch_and_flow_commands_are_typed_dispatches() {
        let Action::Dispatch {
            command,
            args: payload,
            format,
            ..
        } = parse(&args(&["fetch", "https://example.com", "--json"])).expect("fetch dispatch")
        else {
            panic!("wrong fetch action")
        };
        assert_eq!(command, "fetch.url");
        assert_eq!(format, Format::Json);
        assert_eq!(payload["url"], "https://example.com");

        let Action::Dispatch {
            command,
            args: payload,
            ..
        } = parse(&args(&["get", "title"])).expect("get dispatch")
        else {
            panic!("wrong get action")
        };
        assert_eq!(command, "get.title");
        assert_eq!(payload["kind"], "title");

        let Action::Dispatch {
            command,
            args: payload,
            ..
        } = parse(&args(&["find", "text", "hello"])).expect("find dispatch")
        else {
            panic!("wrong find action")
        };
        assert_eq!(command, "find");
        assert_eq!(payload["kind"], "text");
        assert_eq!(payload["query"], "hello");

        let Action::Dispatch { args: payload, .. } =
            parse(&args(&["find", "hello"])).expect("default text find")
        else {
            panic!("wrong default find action")
        };
        assert_eq!(payload["kind"], "text");
        assert_eq!(payload["query"], "hello");

        let Action::FlowRun { dry_run, path, .. } =
            parse(&args(&["workflow", "run", "demo.yaml", "--dry-run"])).expect("flow dispatch")
        else {
            panic!("wrong flow action")
        };
        assert!(dry_run);
        assert_eq!(path, std::path::PathBuf::from("demo.yaml"));
    }

    #[test]
    fn unknown_public_commands_fail_instead_of_succeeding() {
        for command in ["fetchx", "browser", "workflow-nope"] {
            let error = parse(&args(&[command])).expect_err("unknown command must fail");
            assert_eq!(error.exit_code, 2);
            assert!(error.message.contains("unknown command"));
        }
        assert!(parse(&args(&["flow", "nope"])).is_err());
    }
}
