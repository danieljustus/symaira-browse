#![cfg(target_os = "macos")]

use std::{
    fs,
    io::{BufRead, BufReader, Read, Write},
    net::TcpListener,
    os::unix::net::UnixStream,
    path::PathBuf,
    thread,
    time::Duration,
};

use serde_json::{Value, json};
use symbrowse_daemon::{Frame, Server, ServerOptions, SessionSpec};

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

    let capabilities = request(&socket, "capabilities", json!({}));
    assert_eq!(capabilities["success"], true);
    let opened = request(
        &socket,
        "open",
        json!({"url": "data:text/html,<title>daemon</title><h1>native</h1>"}),
    );
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
        json!({"label":"second","url":"data:text/html,<h1>second</h1>"}),
    );
    assert_eq!(created["success"], true, "tab.new response: {created}");
    assert_eq!(created["data"]["tab"], "t2");
    assert_eq!(created["data"]["label"], "second");
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
    let fixture = thread::spawn(move || {
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
    fixture.join().expect("fixture thread");
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
    let framed = request(
        &socket,
        "open",
        json!({"url": "data:text/html,<iframe id='outer' srcdoc=\"<iframe id='inner' srcdoc='nested'></iframe>\"></iframe>"}),
    );
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
        json!({"url": "data:text/html,%3Cinput%20id%3D%27text%27%20onfocus%3D%22this.dataset.focused%3D%27yes%27%22%3E%3Cselect%20id%3D%27choice%27%3E%3Coption%20value%3D%27one%27%3EOne%3C%2Foption%3E%3Coption%20value%3D%27two%27%3ETwo%3C%2Foption%3E%3C%2Fselect%3E%3Cinput%20id%3D%27check%27%20type%3D%27checkbox%27%3E%3Cdiv%20id%3D%27dbl%27%20ondblclick%3D%22this.dataset.doubled%3D%27yes%27%22%3EDouble%3C%2Fdiv%3E%3Cdiv%20id%3D%27hover%27%20onmouseenter%3D%22this.dataset.hovered%3D%27yes%27%22%3EHover%3C%2Fdiv%3E"}),
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
    let prompt_page = request(
        &socket,
        "open",
        json!({"url":"data:text/html,%3Cscript%3EsetTimeout(()%3D%3Eprompt('native%20prompt'%2C'seed')%2C100)%3C%2Fscript%3E"}),
    );
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

    let alert_page = request(
        &socket,
        "open",
        json!({"url":"data:text/html,%3Cscript%3EsetTimeout(()%3D%3Ealert('native%20alert')%2C100)%3C%2Fscript%3E"}),
    );
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
