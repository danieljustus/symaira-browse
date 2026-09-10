#![deny(unsafe_code)]

#[cfg(unix)]
mod unix {
    use std::{
        fs,
        io::{BufRead, BufReader, Write},
        os::unix::fs::{FileTypeExt, PermissionsExt},
        path::{Path, PathBuf},
        sync::{
            Arc,
            atomic::{AtomicBool, AtomicU64, Ordering},
        },
        thread,
        time::{Duration, Instant},
    };

    use symbrowse_daemon::{
        Client, ClientOptions, Frame, Response, Server, ServerError, ServerOptions, codes,
        connect_unix,
    };

    static NEXT: AtomicU64 = AtomicU64::new(1);

    #[test]
    fn socket_lifecycle_permissions_status_and_stop() {
        let root = root("lifecycle");
        let socket = root.join("run/default.sock");
        let server = Arc::new(
            Server::new(ServerOptions {
                socket_path: socket.clone(),
                session: "default".to_owned(),
                idle_timeout: None,
                ..Default::default()
            })
            .unwrap(),
        );
        let running = server.clone();
        let thread = thread::spawn(move || running.listen_and_serve());
        wait_for_socket(&socket);
        let metadata = fs::symlink_metadata(&socket).unwrap();
        assert!(metadata.file_type().is_socket());
        assert_eq!(metadata.permissions().mode() & 0o777, 0o600);
        assert_eq!(
            fs::metadata(socket.parent().unwrap())
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o700
        );

        let client = Client::new(ClientOptions {
            socket_path: socket.clone(),
            session: "default".to_owned(),
            autostart: false,
            ..Default::default()
        });
        let status = client
            .request_without_autostart(Frame {
                cmd: "daemon.status".to_owned(),
                ..Default::default()
            })
            .unwrap();
        assert!(status.success);
        assert_eq!(status.data.as_ref().unwrap()["running"], true);

        let mut stream = std::os::unix::net::UnixStream::connect(&socket).unwrap();
        stream
            .write_all(b"{\"cmd\":\"daemon.ping\"}\n{\"cmd\":\"daemon.ping\"}\n")
            .unwrap();
        let mut reader = BufReader::new(stream);
        for _ in 0..2 {
            let mut line = String::new();
            reader.read_line(&mut line).unwrap();
            let response: Response = serde_json::from_str(&line).unwrap();
            assert!(response.success);
            assert_eq!(response.data.unwrap()["pong"], true);
        }

        let stop = client
            .request_without_autostart(Frame {
                cmd: "daemon.stop".to_owned(),
                ..Default::default()
            })
            .unwrap();
        assert!(stop.success);
        assert!(thread.join().unwrap().is_ok());
        assert!(!socket.exists());
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn shutdown_does_not_unlink_replacement_socket() {
        let root = root("replacement");
        let socket = root.join("default.sock");
        let server = Arc::new(
            Server::new(ServerOptions {
                socket_path: socket.clone(),
                session: "default".to_owned(),
                idle_timeout: None,
                ..Default::default()
            })
            .unwrap(),
        );
        let running = server.clone();
        let thread = thread::spawn(move || running.listen_and_serve());
        wait_for_socket(&socket);

        // Unlinking the old directory entry does not stop the first listener;
        // a different live listener can publish the same configured path.
        fs::remove_file(&socket).unwrap();
        let replacement = std::os::unix::net::UnixListener::bind(&socket).unwrap();
        let replacement_connection = connect_unix(&socket, Duration::from_secs(1)).unwrap();

        server.stop();
        assert!(thread.join().unwrap().is_ok());
        assert!(socket.exists(), "the replacement socket was unlinked");
        drop(replacement_connection);
        drop(replacement);
        fs::remove_file(&socket).unwrap();
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn stale_socket_is_recovered_but_live_socket_is_not_unlinked() {
        let root = root("stale");
        let socket = root.join("default.sock");
        fs::create_dir_all(&root).unwrap();
        let stale = std::os::unix::net::UnixListener::bind(&socket).unwrap();
        drop(stale);

        let server = Arc::new(
            Server::new(ServerOptions {
                socket_path: socket.clone(),
                session: "default".to_owned(),
                idle_timeout: None,
                ..Default::default()
            })
            .unwrap(),
        );
        let running = server.clone();
        let started = Instant::now();
        let thread = thread::spawn(move || running.listen_and_serve());
        wait_for_listener(&socket);
        let elapsed = started.elapsed();
        assert!(
            elapsed < Duration::from_secs(1),
            "stale socket startup probe blocked for {elapsed:?}"
        );

        let competitor = Server::new(ServerOptions {
            socket_path: socket.clone(),
            session: "default".to_owned(),
            idle_timeout: None,
            ..Default::default()
        })
        .unwrap();
        assert!(matches!(
            competitor.listen_and_serve(),
            Err(ServerError::AlreadyRunning)
        ));
        assert!(socket.exists());
        server.stop();
        assert!(thread.join().unwrap().is_ok());
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    #[allow(clippy::result_large_err)]
    fn operation_timeout_is_a_typed_response() {
        let root = root("timeout");
        let socket = root.join("default.sock");
        let cancellation_seen = Arc::new(AtomicBool::new(false));
        let seen = cancellation_seen.clone();
        let server = Arc::new(
            Server::new(ServerOptions {
                socket_path: socket.clone(),
                session: "default".to_owned(),
                idle_timeout: None,
                operation_timeout: Duration::from_millis(10),
                handler: Some(Arc::new(move |_, operation| {
                    while !operation.is_cancelled() {
                        thread::sleep(Duration::from_millis(1));
                    }
                    seen.store(true, Ordering::Release);
                    Ok((None, Vec::new()))
                })),
                ..Default::default()
            })
            .unwrap(),
        );
        let running = server.clone();
        let thread = thread::spawn(move || running.listen_and_serve());
        wait_for_socket(&socket);
        let client = Client::new(ClientOptions {
            socket_path: socket,
            session: "default".to_owned(),
            autostart: false,
            ..Default::default()
        });
        let response = client
            .request_without_autostart(Frame {
                cmd: "slow".to_owned(),
                ..Default::default()
            })
            .unwrap();
        assert!(!response.success);
        assert_eq!(response.error.unwrap().code, codes::OPERATION_TIMEOUT);
        let deadline = Instant::now() + Duration::from_secs(1);
        while !cancellation_seen.load(Ordering::Acquire) && Instant::now() < deadline {
            thread::sleep(Duration::from_millis(1));
        }
        assert!(cancellation_seen.load(Ordering::Acquire));
        server.stop();
        assert!(thread.join().unwrap().is_ok());
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

    fn wait_for_listener(path: &Path) {
        let deadline = Instant::now() + Duration::from_secs(2);
        while Instant::now() < deadline {
            if connect_unix(path, Duration::from_millis(100)).is_ok() {
                return;
            }
            thread::sleep(Duration::from_millis(5));
        }
        panic!("socket did not accept connections: {}", path.display());
    }

    fn root(name: &str) -> PathBuf {
        std::env::temp_dir().join(format!(
            "symbrowse-daemon-{name}-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ))
    }
}
