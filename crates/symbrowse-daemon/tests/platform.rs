#![deny(unsafe_code)]

#[cfg(windows)]
mod windows {
    use std::path::Path;

    use symbrowse_daemon::{ClientOptions, default_socket_path, validate_session};

    #[test]
    fn default_endpoint_is_a_private_named_pipe_path() {
        let path = default_socket_path("portable");
        assert_eq!(path, Path::new(r"\\.\pipe\symbrowse-portable"));
        assert!(validate_session("portable"));
    }

    #[test]
    fn client_defaults_keep_named_pipe_transport_bounded() {
        let options = ClientOptions::default();
        assert!(!options.read_timeout.is_zero());
        assert!(!options.startup_timeout.is_zero());
    }

    #[test]
    fn delayed_fragmented_frame_is_served_after_accept() {
        use std::{
            io::{BufRead, BufReader, Write},
            sync::Arc,
            thread,
            time::Duration,
        };

        use interprocess::os::windows::named_pipe::{DuplexPipeStream, pipe_mode};

        let session = format!("windows-frame-{}", std::process::id());
        let endpoint = default_socket_path(&session);
        let server = Arc::new(
            symbrowse_daemon::Server::new(symbrowse_daemon::ServerOptions {
                socket_path: endpoint.clone(),
                session: session.clone(),
                idle_timeout: None,
                handler: Some(Arc::new(|_, _| {
                    Ok((Some(serde_json::json!({"pong": true})), Vec::new()))
                })),
                ..Default::default()
            })
            .expect("construct Windows daemon"),
        );
        let running = server.clone();
        let server_thread = thread::spawn(move || running.listen_and_serve());

        let mut stream = DuplexPipeStream::<pipe_mode::Bytes>::connect_by_path_with_wait_mode(
            endpoint.to_string_lossy().as_ref(),
            interprocess::ConnectWaitMode::Timeout(Duration::from_secs(2)),
        )
        .expect("connect to Windows daemon");
        stream
            .write_all(br#"{"cmd":"daemon.ping"}"#)
            .expect("write first frame fragment");
        thread::sleep(Duration::from_millis(50));
        stream.write_all(b"\n").expect("write frame delimiter");
        stream.flush().expect("flush frame");

        let mut response_line = String::new();
        BufReader::new(stream)
            .read_line(&mut response_line)
            .expect("read daemon response");
        let response: serde_json::Value =
            serde_json::from_str(&response_line).expect("decode daemon response");
        assert_eq!(response["success"], true);
        assert_eq!(response["data"]["pong"], true);

        server.stop();
        assert!(server_thread.join().expect("join Windows daemon").is_ok());
    }

    #[test]
    fn stalled_fragmented_frame_times_out_and_closes_connection() {
        use std::{
            io::{ErrorKind, Read, Write},
            sync::Arc,
            thread,
            time::{Duration, Instant},
        };

        use interprocess::os::windows::named_pipe::{DuplexPipeStream, pipe_mode};

        let session = format!("windows-stalled-{}", std::process::id());
        let endpoint = default_socket_path(&session);
        let server = Arc::new(
            symbrowse_daemon::Server::new(symbrowse_daemon::ServerOptions {
                socket_path: endpoint.clone(),
                session,
                idle_timeout: None,
                read_timeout: Duration::from_millis(50),
                ..Default::default()
            })
            .expect("construct Windows daemon"),
        );
        let running = server.clone();
        let server_thread = thread::spawn(move || running.listen_and_serve());

        let mut stream = DuplexPipeStream::<pipe_mode::Bytes>::connect_by_path_with_wait_mode(
            endpoint.to_string_lossy().as_ref(),
            interprocess::ConnectWaitMode::Timeout(Duration::from_secs(2)),
        )
        .expect("connect to Windows daemon");
        stream
            .write_all(br#"{"cmd":"daemon.ping"}"#)
            .expect("write stalled frame fragment");
        stream
            .set_nonblocking(true)
            .expect("make client nonblocking");

        let deadline = Instant::now() + Duration::from_secs(1);
        let mut closed = false;
        let mut byte = [0_u8; 1];
        while Instant::now() < deadline {
            match stream.read(&mut byte) {
                Ok(0) => {
                    closed = true;
                    break;
                }
                Ok(_) => {}
                Err(error) if error.kind() == ErrorKind::WouldBlock => {
                    thread::sleep(Duration::from_millis(2));
                }
                Err(_) => {
                    closed = true;
                    break;
                }
            }
        }
        assert!(
            closed,
            "stalled named-pipe frame was not cleaned up by its deadline"
        );

        server.stop();
        assert!(server_thread.join().expect("join Windows daemon").is_ok());
    }
}
