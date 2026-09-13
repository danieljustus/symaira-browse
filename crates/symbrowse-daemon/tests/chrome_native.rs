#![cfg(target_os = "macos")]

use std::{
    fs,
    io::{BufRead, BufReader, Read, Write},
    net::{SocketAddr, TcpListener},
    os::unix::net::UnixStream,
    path::PathBuf,
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::Duration,
};

use serde_json::{Value, json};
use symbrowse_daemon::{Frame, Server, ServerOptions, SessionSpec};

// The initial page and six additional data: payloads are unchanged; only their
// transport becomes loopback HTTP to exercise production navigation admission.
struct FixtureServer {
    address: SocketAddr,
    stop: Arc<AtomicBool>,
    worker: Option<thread::JoinHandle<()>>,
}

impl FixtureServer {
    fn start() -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind HTTP fixture");
        let address = listener.local_addr().expect("HTTP fixture address");
        listener.set_nonblocking(true).expect("nonblocking fixture");
        let stop = Arc::new(AtomicBool::new(false));
        let stopped = Arc::clone(&stop);
        let worker = thread::spawn(move || {
            while !stopped.load(Ordering::SeqCst) {
                let mut stream = match listener.accept() {
                    Ok((stream, _)) => stream,
                    Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                        thread::sleep(Duration::from_millis(10));
                        continue;
                    }
                    Err(error) => panic!("accept HTTP fixture: {error}"),
                };
                stream
                    .set_read_timeout(Some(Duration::from_secs(2)))
                    .unwrap();
                stream
                    .set_write_timeout(Some(Duration::from_secs(2)))
                    .unwrap();
                let mut reader = BufReader::new(&stream);
                let mut line = String::new();
                // Chrome may close a speculative connection without a request.
                match reader.read_line(&mut line) {
                    Ok(0) => continue,
                    Ok(_) => {}
                    Err(error)
                        if matches!(
                            error.kind(),
                            std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut
                        ) =>
                    {
                        continue;
                    }
                    Err(error) => panic!("read fixture request: {error}"),
                }
                let path = line.split_whitespace().nth(1).unwrap_or("").to_owned();
                loop {
                    line.clear();
                    if reader.read_line(&mut line).expect("read fixture headers") == 0
                        || line == "\r\n"
                    {
                        break;
                    }
                }
                let (status, body) = match path.as_str() {
                    "/native" => ("200 OK", r##"<title>daemon</title><h1>native</h1>"##),
                    "/second" => ("200 OK", r##"<h1>second</h1>"##),
                    "/frames" => (
                        "200 OK",
                        r##"<iframe id='outer' srcdoc="<iframe id='inner' srcdoc='nested'></iframe>"></iframe>"##,
                    ),
                    "/interactions" => (
                        "200 OK",
                        r##"<input id='text' onfocus="this.dataset.focused='yes'"><select id='choice'><option value='one'>One</option><option value='two'>Two</option></select><input id='check' type='checkbox'><div id='dbl' ondblclick="this.dataset.doubled='yes'">Double</div><div id='hover' onmouseenter="this.dataset.hovered='yes'">Hover</div>"##,
                    ),
                    "/prompt" => (
                        "200 OK",
                        r##"<script>setTimeout(()=>prompt('native prompt','seed'),100)</script>"##,
                    ),
                    "/alert" => (
                        "200 OK",
                        r##"<script>setTimeout(()=>alert('native alert'),100)</script>"##,
                    ),
                    "/auto-alert" => (
                        "200 OK",
                        r##"<script>setTimeout(()=>alert('auto dismiss'),100)</script>"##,
                    ),
                    _ => ("404 Not Found", "unknown fixture"),
                };
                write!(stream, "HTTP/1.1 {status}\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len())
                    .expect("write HTTP fixture");
            }
        });
        Self {
            address,
            stop,
            worker: Some(worker),
        }
    }

    fn url(&self, path: &str) -> String {
        format!("http://{}{path}", self.address)
    }
}

impl Drop for FixtureServer {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::SeqCst);
        self.worker
            .take()
            .expect("fixture worker")
            .join()
            .expect("HTTP fixture thread");
    }
}

fn enabled() -> bool {
    std::env::var_os("SYMBROWSE_E2E").as_deref() == Some(std::ffi::OsStr::new("1"))
}

fn request(socket: &PathBuf, command: &str, args: Value) -> Value {
    let mut stream = UnixStream::connect(socket).expect("connect daemon");
    let frame = Frame {
        cmd: command.to_owned(),
        args: Some(args),
        session: "native-chrome".into(),
        ..Frame::default()
    };
    let line = serde_json::to_string(&frame).expect("encode frame");
    writeln!(stream, "{line}").expect("write frame");
    let mut response = String::new();
    BufReader::new(stream)
        .read_line(&mut response)
        .expect("read response");
    serde_json::from_str(&response).expect("decode response")
}

#[test]
fn production_daemon_path_runs_chrome_and_reaps_owned_profile() {
    if !enabled() {
        eprintln!("native Chrome test not enabled; run with SYMBROWSE_E2E=1");
        return;
    }
    let root = std::env::temp_dir().join(format!("symbrowse-daemon-chrome-{}", std::process::id()));
    let _ = fs::remove_dir_all(&root);
    fs::create_dir_all(&root).expect("create isolated root");
    let mut spec = SessionSpec::for_session("native-chrome");
    spec.state_dir = root.join("state");
    spec.cache_dir = root.join("cache");
    spec.socket_path = root.join("daemon.sock");
    spec.operation_timeout = Duration::from_secs(10);
    spec.idle_timeout = Some(Duration::from_secs(30));
    let profile = spec.user_data_dir();

    let server = std::sync::Arc::new(
        Server::new(ServerOptions {
            session_spec: Some(spec.clone()),
            operation_timeout: Duration::from_secs(10),
            idle_timeout: Some(Duration::from_secs(30)),
            ..ServerOptions::default()
        })
        .expect("create daemon"),
    );
    let socket = spec.socket_path.clone();
    let server_for_thread = std::sync::Arc::clone(&server);
    let thread = thread::spawn(move || server_for_thread.listen_and_serve().expect("serve daemon"));
    for _ in 0..100 {
        if socket.exists() {
            break;
        }
        thread::sleep(Duration::from_millis(20));
    }
    assert!(socket.exists(), "daemon socket did not appear");

    let fixture = FixtureServer::start();
    let capabilities = request(&socket, "capabilities", json!({}));
    assert_eq!(capabilities["success"], true);
    for command in ["open", "goto", "tab.new"] {
        let denied = request(
            &socket,
            command,
            json!({"url":"data:text/html,<h1>denied</h1>"}),
        );
        assert_eq!(denied["success"], false, "{command}: {denied}");
        assert_eq!(denied["error"]["code"], "operation_failed");
        assert_eq!(
            denied["error"]["message"],
            "navigation URL policy: unsupported target \"data:text/html,<h1>denied</h1>\" (http/https URL required)"
        );
    }
    let opened = request(&socket, "open", json!({"url": fixture.url("/native")}));
    assert_eq!(opened["success"], true, "open response: {opened}");
    let script = request(&socket, "read", json!({}));
    assert!(
        script["data"]
            .as_str()
            .is_some_and(|text| text.contains("native")),
        "read response: {script}"
    );
    let tabs = request(&socket, "tabs.list", json!({}));
    assert_eq!(tabs["success"], true, "tabs response: {tabs}");
    let listed = tabs["data"]["tabs"].as_array().expect("tab list");
    assert!(!listed.is_empty(), "tabs response: {tabs}");
    let active = listed
        .iter()
        .filter(|tab| tab["active"] == true)
        .collect::<Vec<_>>();
    assert_eq!(active.len(), 1, "tabs response: {tabs}");
    assert_eq!(tabs["data"]["active"], active[0]["id"]);
    let created = request(
        &socket,
        "tab.new",
        json!({"label":"second","url":fixture.url("/second")}),
    );
    assert_eq!(created["success"], true, "tab.new response: {created}");
    assert_eq!(created["data"]["tab"], "t2");
    assert_eq!(created["data"]["label"], "second");
    let second = request(&socket, "read", json!({}));
    assert!(
        second["data"]
            .as_str()
            .is_some_and(|text| text.contains("second")),
        "HTTP second-tab read: {second}"
    );
    let listed_tabs = request(&socket, "tab.list", json!({}));
    assert_eq!(
        listed_tabs["success"], true,
        "tab.list response: {listed_tabs}"
    );
    assert_eq!(
        listed_tabs["data"]["tabs"].as_array().map(Vec::len),
        Some(2)
    );
    assert_eq!(listed_tabs["data"]["active"], "t2");
    let switched = request(&socket, "tab.switch", json!({"tab":"t1"}));
    assert_eq!(switched["success"], true, "tab.switch response: {switched}");
    let original = request(&socket, "read", json!({}));
    assert!(
        original["data"]
            .as_str()
            .is_some_and(|text| text.contains("native")),
        "switched tab read: {original}"
    );
    let closed = request(&socket, "tab.close", json!({"tab":"second"}));
    assert_eq!(closed["success"], true, "tab.close response: {closed}");
    assert_eq!(closed["data"]["closed"], "t2");
    assert_eq!(closed["data"]["active"], "t1");
    let remaining = request(&socket, "tab.list", json!({}));
    assert_eq!(remaining["data"]["tabs"].as_array().map(Vec::len), Some(1));
    let window = request(&socket, "window.new", json!({}));
    assert_eq!(window["success"], true, "window.new response: {window}");
    assert_eq!(window["data"]["tab"], "t2");
    let closed_window = request(&socket, "tab.close", json!({}));
    assert_eq!(
        closed_window["success"], true,
        "close active tab: {closed_window}"
    );
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind fixture server");
    let address = listener.local_addr().expect("fixture address");
    let network_fixture = thread::spawn(move || {
        let (mut stream, _) = listener.accept().expect("accept fixture request");
        let mut request = [0_u8; 1024];
        let _ = stream.read(&mut request).expect("read fixture request");
        let body = b"<h1>network</h1>";
        let header = format!(
            "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
            body.len()
        );
        stream
            .write_all(header.as_bytes())
            .expect("write fixture header");
        stream.write_all(body).expect("write fixture body");
    });
    let started = request(&socket, "network.capture", json!({}));
    assert_eq!(started["success"], true, "network capture start: {started}");
    assert_eq!(started["data"]["started"], true);
    let url = format!("http://{address}/");
    let network_page = request(&socket, "open", json!({"url": url}));
    assert_eq!(
        network_page["success"], true,
        "network page: {network_page}"
    );
    network_fixture.join().expect("fixture thread");
    let captured = request(&socket, "network.requests", json!({}));
    assert_eq!(captured["success"], true, "network capture: {captured}");
    assert!(
        captured["data"]["count"]
            .as_u64()
            .is_some_and(|count| count > 0)
    );
    assert!(
        captured["data"]["requests"]
            .as_array()
            .is_some_and(|events| events.iter().any(|event| event["status"] == 200)),
        "network capture: {captured}"
    );
    let framed = request(&socket, "open", json!({"url": fixture.url("/frames")}));
    assert_eq!(framed["success"], true, "frame page: {framed}");
    let frames = request(&socket, "frame.tree", json!({}));
    assert_eq!(frames["success"], true, "frame response: {frames}");
    let listed = frames["data"]["frames"].as_array().expect("frame tree");
    assert!(listed.len() >= 2, "frame response: {frames}");
    assert!(
        listed.iter().any(|frame| frame["parent_id"].is_string()),
        "frame response: {frames}"
    );
    let ax = request(&socket, "a11y", json!({}));
    assert_eq!(ax["success"], true, "a11y response: {ax}");

    let interactions = request(
        &socket,
        "open",
        json!({"url": fixture.url("/interactions")}),
    );
    assert_eq!(
        interactions["success"], true,
        "interaction page: {interactions}"
    );
    for (command, args) in [
        ("dblclick", json!({"selector":"#dbl"})),
        ("focus", json!({"selector":"#text"})),
        ("hover", json!({"selector":"#hover"})),
        ("select", json!({"selector":"#choice","value":"two"})),
        ("check", json!({"selector":"#check"})),
        ("uncheck", json!({"selector":"#check"})),
    ] {
        let response = request(&socket, command, args);
        assert_eq!(response["success"], true, "{command} response: {response}");
        assert_eq!(response["data"]["action"], command);
        if command == "focus" {
            let focused = request(
                &socket,
                "get.attr",
                json!({"selector":"#text","attribute":"data-focused"}),
            );
            assert_eq!(focused["data"], "yes", "focus state: {focused}");
        }
        if command == "check" {
            let checked = request(&socket, "is.checked", json!({"selector":"#check"}));
            assert_eq!(checked["data"], true, "check state: {checked}");
        }
    }
    let doubled = request(
        &socket,
        "get.attr",
        json!({"selector":"#dbl","attribute":"data-doubled"}),
    );
    assert_eq!(doubled["data"], "yes", "double-click state: {doubled}");
    let hovered = request(
        &socket,
        "get.attr",
        json!({"selector":"#hover","attribute":"data-hovered"}),
    );
    assert_eq!(hovered["data"], "yes", "hover state: {hovered}");
    let selected = request(&socket, "get.value", json!({"selector":"#choice"}));
    assert_eq!(selected["data"], "two", "select state: {selected}");
    let checked = request(&socket, "is.checked", json!({"selector":"#check"}));
    assert_eq!(checked["data"], false, "uncheck state: {checked}");

    let no_dialog = request(&socket, "dialog.status", json!({}));
    assert_eq!(
        no_dialog["success"], true,
        "empty dialog status: {no_dialog}"
    );
    assert_eq!(no_dialog["data"]["handled"], true);
    let prompt_page = request(&socket, "open", json!({"url":fixture.url("/prompt")}));
    assert_eq!(prompt_page["success"], true, "prompt page: {prompt_page}");
    thread::sleep(Duration::from_millis(250));
    let prompt_status = request(&socket, "dialog.status", json!({}));
    assert_eq!(
        prompt_status["success"], true,
        "prompt status: {prompt_status}"
    );
    assert_eq!(prompt_status["data"]["type"], "prompt");
    assert_eq!(prompt_status["data"]["message"], "native prompt");
    assert_eq!(prompt_status["data"]["default"], "seed");
    assert_eq!(prompt_status["data"]["handled"], false);
    let accepted = request(&socket, "dialog.accept", json!({"text":"answer"}));
    assert_eq!(accepted["success"], true, "dialog accept: {accepted}");
    assert_eq!(accepted["data"], json!({"handled":true,"action":"accept"}));
    let handled = request(&socket, "dialog.status", json!({}));
    assert_eq!(
        handled["data"]["handled"], true,
        "handled status: {handled}"
    );

    let alert_page = request(&socket, "open", json!({"url":fixture.url("/alert")}));
    assert_eq!(alert_page["success"], true, "alert page: {alert_page}");
    thread::sleep(Duration::from_millis(250));
    let dismissed = request(&socket, "dialog.dismiss", json!({}));
    assert_eq!(dismissed["success"], true, "dialog dismiss: {dismissed}");
    assert_eq!(
        dismissed["data"],
        json!({"handled":true,"action":"dismiss"})
    );
    let no_pending = request(&socket, "dialog.dismiss", json!({}));
    assert_eq!(
        no_pending["success"], false,
        "no-pending dismiss: {no_pending}"
    );

    let auto = request(&socket, "dialog.auto", json!({"mode":"dismiss"}));
    assert_eq!(auto["success"], true, "dialog auto: {auto}");
    assert_eq!(auto["data"], json!({"auto_mode":"dismiss"}));
    let auto_alert = request(&socket, "open", json!({"url":fixture.url("/auto-alert")}));
    assert_eq!(auto_alert["success"], true, "auto alert page: {auto_alert}");
    thread::sleep(Duration::from_millis(250));
    let auto_status = request(&socket, "dialog.status", json!({}));
    assert_eq!(auto_status["success"], true, "auto status: {auto_status}");
    assert_eq!(auto_status["data"]["handled"], true);
    assert_eq!(auto_status["data"]["auto_mode"], "dismiss");
    let auto_off = request(&socket, "dialog.auto", json!({"mode":"off"}));
    assert_eq!(auto_off["success"], true, "dialog auto off: {auto_off}");
    assert_eq!(auto_off["data"], json!({"auto_mode":"off"}));

    let unsupported = request(&socket, "network.har", json!({}));
    assert_eq!(unsupported["success"], false);
    assert_eq!(unsupported["error"]["code"], "unsupported");

    // The one-second idle budget lets the production accept loop stop and run
    // its owned socket/profile cleanup without touching any user browser.
    server.stop();
    let _ = thread.join();
    // A configured session profile is persistent by contract. The daemon must
    // not delete it; only the engine's internally-created temporary profile is
    // removable on close.
    assert!(profile.exists(), "configured session profile disappeared");
    let _ = fs::remove_dir_all(root);
}
