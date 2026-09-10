#![cfg(target_os = "macos")]

use std::{
    fs,
    io::{BufRead, BufReader, Write},
    os::unix::net::UnixStream,
    path::Path,
    thread,
    time::Duration,
};

use serde_json::{Value, json};
use symbrowse_daemon::{Frame, Server, ServerOptions, SessionSpec};

fn request(socket: &Path, command: &str, args: Value) -> Value {
    let mut stream = UnixStream::connect(socket).expect("connect daemon");
    let frame = Frame {
        cmd: command.into(),
        args: Some(args),
        session: "native-safari".into(),
        ..Default::default()
    };
    writeln!(
        stream,
        "{}",
        serde_json::to_string(&frame).expect("encode frame")
    )
    .expect("write frame");
    let mut response = String::new();
    BufReader::new(stream)
        .read_line(&mut response)
        .expect("read response");
    serde_json::from_str(&response).expect("decode response")
}

#[test]
fn production_daemon_path_runs_safari_bidi_and_reaps_owned_driver() {
    if std::env::var_os("SYMBROWSE_E2E").as_deref() != Some(std::ffi::OsStr::new("1")) {
        return;
    }
    let root = std::env::temp_dir().join(format!("symbrowse-daemon-safari-{}", std::process::id()));
    let _ = fs::remove_dir_all(&root);
    fs::create_dir_all(&root).expect("create isolated root");
    let mut spec = SessionSpec::for_session("native-safari");
    spec.engine = "safari-bidi".into();
    spec.state_dir = root.join("state");
    spec.cache_dir = root.join("cache");
    spec.socket_path = root.join("daemon.sock");
    spec.operation_timeout = Duration::from_secs(60);
    spec.idle_timeout = Some(Duration::from_secs(90));
    let profile = spec.user_data_dir();
    let server = std::sync::Arc::new(
        Server::new(ServerOptions {
            session_spec: Some(spec.clone()),
            operation_timeout: Duration::from_secs(60),
            idle_timeout: Some(Duration::from_secs(90)),
            ..Default::default()
        })
        .expect("create daemon"),
    );
    let socket = spec.socket_path.clone();
    let serving = std::sync::Arc::clone(&server);
    let thread = thread::spawn(move || serving.listen_and_serve().expect("serve daemon"));
    for _ in 0..150 {
        if socket.exists() {
            break;
        }
        thread::sleep(Duration::from_millis(20));
    }
    assert!(socket.exists(), "daemon socket did not appear");

    let fixture = std::net::TcpListener::bind(("127.0.0.1", 0)).expect("bind fixture");
    let fixture_url = format!("http://{}", fixture.local_addr().expect("fixture address"));
    let fixture_thread = thread::spawn(move || {
        if let Ok((mut stream, _)) = fixture.accept() {
            let mut request = [0_u8; 4096];
            let _ = std::io::Read::read(&mut stream, &mut request);
            let body = b"<title>safari-native</title><input id='name'><button id='go'>go</button>";
            let header = format!(
                "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nContent-Type: text/html\r\nConnection: close\r\n\r\n",
                body.len()
            );
            let _ = stream.write_all(header.as_bytes());
            let _ = stream.write_all(body);
        }
    });
    let opened = request(&socket, "open", json!({"url": fixture_url}));
    assert_eq!(opened["success"], true, "open: {opened}");
    let title = request(&socket, "get.title", json!({"selector":"body"}));
    assert_eq!(title["success"], true, "title: {title}");
    assert_eq!(title["data"], "safari-native");
    let filled = request(
        &socket,
        "fill",
        json!({"selector":"#name","value":"native"}),
    );
    assert_eq!(filled["success"], true, "fill: {filled}");
    let value = request(&socket, "get.value", json!({"selector":"#name"}));
    assert_eq!(value["success"], true, "value: {value}");
    assert_eq!(value["data"], "native");
    let clicked = request(&socket, "click", json!({"selector":"#go"}));
    assert_eq!(clicked["success"], true, "click: {clicked}");

    let _ = fixture_thread.join();
    server.stop();
    let _ = thread.join();
    assert!(profile.exists(), "configured session profile disappeared");
    let _ = fs::remove_dir_all(root);
}
