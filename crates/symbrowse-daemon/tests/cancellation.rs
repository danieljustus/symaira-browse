#![deny(unsafe_code)]

#[cfg(unix)]
#[allow(clippy::result_large_err)]
mod unix {
    use std::{
        fs,
        io::{BufRead, BufReader, Write},
        net::TcpListener,
        os::unix::fs::PermissionsExt,
        path::{Path, PathBuf},
        sync::{
            Arc,
            atomic::{AtomicU64, Ordering},
            mpsc,
        },
        thread,
        time::{Duration, Instant},
    };

    use symbrowse_daemon::{
        Client, ClientError, ClientOptions, Frame, Server, ServerOptions, SessionSpec,
        StartOptions, codes,
    };

    static NEXT: AtomicU64 = AtomicU64::new(1);

    #[test]
    fn timed_out_client_request_is_not_retried_as_autostart() {
        let root = root("client-timeout");
        let socket = root.join("default.sock");
        let marker = root.join("autostarted");
        let log = root.join("daemon.log");
        let server = Arc::new(
            Server::new(ServerOptions {
                socket_path: socket.clone(),
                session: "default".into(),
                idle_timeout: None,
                operation_timeout: Duration::from_millis(500),
                handler: Some(Arc::new(|frame, _| {
                    if frame.cmd == "slow" {
                        thread::sleep(Duration::from_millis(100));
                    }
                    Ok((Some(serde_json::json!({"pong": true})), Vec::new()))
                })),
                ..Default::default()
            })
            .unwrap(),
        );
        let running = server.clone();
        let thread = thread::spawn(move || running.listen_and_serve());
        wait_for_socket(&socket);

        let client = Client::new(ClientOptions {
            socket_path: socket.clone(),
            session: "default".into(),
            read_timeout: Duration::from_millis(10),
            startup_timeout: Duration::from_millis(30),
            autostart: true,
            start: Some(StartOptions {
                executable: PathBuf::from("/bin/sh"),
                log_path: log,
                args: vec!["-c".into(), format!("touch {}; sleep 1", marker.display())],
            }),
            ..Default::default()
        });

        let error = client
            .request(Frame {
                cmd: "slow".into(),
                ..Default::default()
            })
            .expect_err("a client deadline must fail the request");
        match error {
            ClientError::Transport(error) => assert_eq!(error.code, codes::OPERATION_TIMEOUT),
            other => panic!("client timeout = {other:?}"),
        }
        thread::sleep(Duration::from_millis(50));
        assert!(
            !marker.exists(),
            "a timed-out live daemon request must not start a second daemon"
        );

        server.stop();
        assert!(thread.join().unwrap().is_ok());
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn status_failure_is_preserved_without_stopping_daemon() {
        let root = root("status-failure");
        fs::create_dir_all(&root).unwrap();
        let socket = root.join("default.sock");
        let listener = std::os::unix::net::UnixListener::bind(&socket).unwrap();
        let (commands, seen) = mpsc::channel();
        let (release, released) = mpsc::channel();
        let thread = thread::spawn(move || {
            let mut status_seen = false;
            loop {
                let (stream, _) = listener.accept().expect("accept daemon fixture");
                let mut reader = BufReader::new(stream);
                let mut line = String::new();
                if reader.read_line(&mut line).unwrap_or(0) == 0 {
                    continue;
                }
                let frame: Frame = serde_json::from_str(&line).unwrap();
                let _ = commands.send(frame.cmd.clone());
                let response = if frame.cmd == "daemon.status" {
                    status_seen = true;
                    symbrowse_daemon::error_response(
                        codes::OPERATION_FAILED,
                        "status fixture failed",
                    )
                } else {
                    symbrowse_daemon::success_response(
                        Some(serde_json::json!({"ok": true})),
                        Vec::new(),
                    )
                };
                let mut writer = reader.into_inner();
                writer
                    .write_all(
                        format!("{}\n", serde_json::to_string(&response).unwrap()).as_bytes(),
                    )
                    .expect("write daemon fixture response");
                writer.flush().expect("flush daemon fixture response");
                if status_seen {
                    released
                        .recv_timeout(Duration::from_secs(2))
                        .expect("wait for cancellation fixture release");
                    break;
                }
            }
        });

        let client = Client::new(ClientOptions {
            socket_path: socket,
            session: "default".into(),
            read_timeout: Duration::from_millis(100),
            autostart: false,
            ..Default::default()
        });
        let error = client
            .request(Frame {
                cmd: "daemon.ping".into(),
                ..Default::default()
            })
            .expect_err("failed status must be returned to the caller");
        match error {
            ClientError::Transport(error) => {
                assert_eq!(error.code, codes::OPERATION_FAILED);
                assert_eq!(error.message, "status fixture failed");
            }
            other => panic!("status failure = {other:?}"),
        }
        release.send(()).unwrap();
        thread.join().unwrap();
        let seen: Vec<_> = seen.try_iter().collect();
        assert_eq!(seen, ["daemon.ping", "daemon.status"]);
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn late_handler_completion_does_not_emit_a_second_response() {
        let root = root("late-response");
        let socket = root.join("default.sock");
        let server = Arc::new(
            Server::new(ServerOptions {
                socket_path: socket.clone(),
                session: "default".into(),
                idle_timeout: None,
                operation_timeout: Duration::from_millis(10),
                handler: Some(Arc::new(|frame, _| {
                    if frame.cmd == "slow" {
                        thread::sleep(Duration::from_millis(50));
                    }
                    Ok((Some(serde_json::json!({"pong": true})), Vec::new()))
                })),
                ..Default::default()
            })
            .unwrap(),
        );
        let running = server.clone();
        let thread = thread::spawn(move || running.listen_and_serve());
        wait_for_socket(&socket);

        let mut stream = std::os::unix::net::UnixStream::connect(&socket).unwrap();
        stream
            .set_read_timeout(Some(Duration::from_millis(250)))
            .unwrap();
        stream
            .write_all(b"{\"cmd\":\"slow\"}\n{\"cmd\":\"daemon.ping\"}\n")
            .unwrap();
        let mut reader = BufReader::new(stream);
        let mut first = String::new();
        let mut second = String::new();
        reader.read_line(&mut first).unwrap();
        reader.read_line(&mut second).unwrap();
        let first: serde_json::Value = serde_json::from_str(&first).unwrap();
        let second: serde_json::Value = serde_json::from_str(&second).unwrap();
        assert_eq!(first["error"]["code"], "operation_timeout");
        assert_eq!(second["data"]["pong"], true);

        thread::sleep(Duration::from_millis(75));
        let mut late = String::new();
        let read = reader.read_line(&mut late);
        assert!(
            read.is_err() || read.unwrap() == 0,
            "late handler completion emitted an unexpected frame: {late:?}"
        );

        server.stop();
        assert!(thread.join().unwrap().is_ok());
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn production_runtime_cancellation_allows_same_process_restart() {
        let root = std::path::PathBuf::from(format!(
            "/tmp/symbrowse-pc-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        let socket = root.join("default.sock");
        let mut spec = SessionSpec::for_session("default");
        spec.socket_path = socket.clone();
        spec.state_dir = root.join("state");
        spec.cache_dir = root.join("cache");
        spec.engine = "static".into();
        spec.mode = "static".into();
        spec.allow_private = true;
        spec.operation_timeout = Duration::from_secs(5);
        spec.read_timeout = Duration::from_millis(200);
        spec.idle_timeout = None;
        let upstream = TcpListener::bind(("127.0.0.1", 0)).unwrap();
        let upstream_url = format!("http://{}", upstream.local_addr().unwrap());
        thread::spawn(move || {
            let Ok((mut stream, _)) = upstream.accept() else {
                return;
            };
            let mut request = [0_u8; 1024];
            let _ = std::io::Read::read(&mut stream, &mut request);
            thread::sleep(Duration::from_secs(5));
        });
        let server = Arc::new(
            Server::new(ServerOptions {
                session_spec: Some(spec.clone()),
                ..Default::default()
            })
            .unwrap(),
        );
        let running = server.clone();
        let thread = thread::spawn(move || running.listen_and_serve());
        thread::sleep(Duration::from_millis(100));
        if thread.is_finished() {
            panic!(
                "production server exited during startup: {:?}",
                thread.join().unwrap()
            );
        }
        wait_for_socket(&socket);
        let request_socket = socket.clone();
        let request = thread::spawn(move || {
            Client::new(ClientOptions {
                socket_path: request_socket,
                session: "default".into(),
                read_timeout: Duration::from_secs(2),
                autostart: false,
                ..Default::default()
            })
            .request_without_autostart(Frame {
                cmd: "open".into(),
                args: Some(serde_json::json!({"url": upstream_url})),
                ..Default::default()
            })
        });
        thread::sleep(Duration::from_millis(50));
        server.stop();
        assert!(thread.join().unwrap().is_ok());
        let request_result = request.join().unwrap();
        match request_result {
            Ok(response) => assert_eq!(response.error.unwrap().code, codes::OPERATION_TIMEOUT),
            Err(ClientError::Transport(error)) => assert_eq!(error.code, "daemon_unavailable"),
            Err(error) => panic!("production cancellation request = {error:?}"),
        }

        let restarted = Arc::new(
            Server::new(ServerOptions {
                session_spec: Some(spec),
                ..Default::default()
            })
            .unwrap(),
        );
        let running = restarted.clone();
        let restart_thread = thread::spawn(move || running.listen_and_serve());
        wait_for_socket(&socket);
        let response = Client::new(ClientOptions {
            socket_path: socket.clone(),
            session: "default".into(),
            autostart: false,
            ..Default::default()
        })
        .request_without_autostart(Frame {
            cmd: "daemon.ping".into(),
            ..Default::default()
        })
        .unwrap();
        assert!(response.success);
        restarted.stop();
        assert!(restart_thread.join().unwrap().is_ok());
        fs::remove_dir_all(root).unwrap();
    }

    fn wait_for_socket(path: &Path) {
        let deadline = Instant::now() + Duration::from_secs(2);
        while Instant::now() < deadline {
            if fs::symlink_metadata(path)
                .map(|metadata| metadata.permissions().mode() & 0o777 == 0o600)
                .unwrap_or(false)
            {
                return;
            }
            thread::sleep(Duration::from_millis(5));
        }
        panic!("secured socket was not created: {}", path.display());
    }

    fn root(name: &str) -> PathBuf {
        std::env::temp_dir().join(format!(
            "symbrowse-daemon-{name}-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ))
    }
}
