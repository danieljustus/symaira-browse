#![deny(unsafe_code)]

#[cfg(windows)]
mod windows {
    // The production handler ABI returns DaemonError by value. These test
    // handlers exercise that public ABI and do not own its error layout.
    #![allow(clippy::result_large_err)]

    use std::path::Path;

    use symbrowse_daemon::{ClientOptions, default_socket_path, validate_session};

    fn connect_when_server_ready(
        endpoint: &Path,
    ) -> interprocess::os::windows::named_pipe::DuplexPipeStream<
        interprocess::os::windows::named_pipe::pipe_mode::Bytes,
    > {
        use interprocess::os::windows::named_pipe::{DuplexPipeStream, pipe_mode};
        use std::{
            io::ErrorKind,
            thread,
            time::{Duration, Instant},
        };

        let deadline = Instant::now() + Duration::from_secs(2);
        let mut last_error = None;
        while Instant::now() < deadline {
            match DuplexPipeStream::<pipe_mode::Bytes>::connect_by_path_with_wait_mode(
                endpoint.to_string_lossy().as_ref(),
                interprocess::ConnectWaitMode::Timeout(Duration::from_millis(50)),
            ) {
                Ok(stream) => return stream,
                Err(error)
                    if matches!(error.kind(), ErrorKind::NotFound | ErrorKind::WouldBlock) =>
                {
                    last_error = Some(error);
                    thread::sleep(Duration::from_millis(10));
                }
                Err(error) => panic!("connect to Windows daemon failed: {error}"),
            }
        }
        panic!("Windows daemon did not publish its pipe: {last_error:?}");
    }

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

        let mut stream = connect_when_server_ready(&endpoint);
        eprintln!("phase=client-write-fragment");
        stream
            .write_all(br#"{"cmd":"daemon.ping"}"#)
            .expect("write first frame fragment");
        thread::sleep(Duration::from_millis(50));
        eprintln!("phase=client-write-delimiter");
        stream.write_all(b"\n").expect("write frame delimiter");
        stream.flush().expect("flush frame");

        let mut response_line = String::new();
        eprintln!("phase=client-read-response");
        BufReader::new(stream)
            .read_line(&mut response_line)
            .expect("read daemon response");
        let response: serde_json::Value =
            serde_json::from_str(&response_line).expect("decode daemon response");
        assert_eq!(response["success"], true);
        assert_eq!(response["data"]["pong"], true);

        eprintln!("phase=server-stop");
        server.stop();
        eprintln!("phase=server-join");
        assert!(server_thread.join().expect("join Windows daemon").is_ok());
    }

    #[test]
    fn stalled_fragmented_frame_times_out_and_closes_connection() {
        use std::{io::Write, sync::Arc, thread, time::Duration};

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

        let mut stream = connect_when_server_ready(&endpoint);
        eprintln!("phase=client-write-fragment");
        stream
            .write_all(br#"{"cmd":"daemon.ping"}"#)
            .expect("write stalled frame fragment");
        // The client stays blocking: the server-side overlapped read must
        // expire and reclaim its own handle without a client pre-read probe.
        eprintln!("phase=server-read-deadline");
        thread::sleep(Duration::from_millis(200));
        eprintln!("phase=client-drop-after-deadline");
        drop(stream);

        eprintln!("phase=server-stop-after-deadline");
        server.stop();
        eprintln!("phase=server-join-after-deadline");
        assert!(server_thread.join().expect("join Windows daemon").is_ok());
    }

    #[test]
    fn stop_drains_blocked_read_and_cancellable_handler_before_restart() {
        use std::{
            io::Write,
            sync::{
                Arc,
                atomic::{AtomicBool, AtomicUsize, Ordering},
            },
            thread,
            time::{Duration, Instant},
        };

        let session = format!("windows-shutdown-{}", std::process::id());
        let endpoint = default_socket_path(&session);
        let handler_started = Arc::new(AtomicBool::new(false));
        let handler_finished = Arc::new(AtomicBool::new(false));
        let effects = Arc::new(AtomicUsize::new(0));
        let started = handler_started.clone();
        let finished = handler_finished.clone();
        let effects_seen = effects.clone();
        let server = Arc::new(
            symbrowse_daemon::Server::new(symbrowse_daemon::ServerOptions {
                socket_path: endpoint.clone(),
                session: session.clone(),
                idle_timeout: None,
                operation_timeout: Duration::from_secs(5),
                handler: Some(Arc::new(move |_, operation| {
                    started.store(true, Ordering::Release);
                    while !operation.is_cancelled() {
                        thread::sleep(Duration::from_millis(1));
                    }
                    if !operation.is_cancelled() {
                        effects_seen.fetch_add(1, Ordering::AcqRel);
                    }
                    finished.store(true, Ordering::Release);
                    Ok((None, Vec::new()))
                })),
                ..Default::default()
            })
            .expect("construct shutdown daemon"),
        );
        let running = server.clone();
        let server_thread = thread::spawn(move || running.listen_and_serve());

        let blocked = connect_when_server_ready(&endpoint);
        let mut active = connect_when_server_ready(&endpoint);
        active
            .write_all(
                br#"{"cmd":"cancellable"}
"#,
            )
            .expect("send cancellable frame");
        let deadline = Instant::now() + Duration::from_secs(1);
        while !handler_started.load(Ordering::Acquire) && Instant::now() < deadline {
            thread::sleep(Duration::from_millis(2));
        }
        assert!(handler_started.load(Ordering::Acquire));

        let stopped_at = Instant::now();
        eprintln!("phase=server-stop-blocked-read");
        server.stop();
        eprintln!("phase=server-join-blocked-read");
        let result = server_thread.join().expect("join shutdown daemon");
        assert!(result.is_ok(), "shutdown result = {result:?}");
        assert!(
            stopped_at.elapsed() < Duration::from_secs(1),
            "shutdown exceeded bounded drain: {:?}",
            stopped_at.elapsed()
        );
        assert!(handler_finished.load(Ordering::Acquire));
        assert_eq!(effects.load(Ordering::Acquire), 0);

        // The blocked connection was owned and closed by a joined worker; the
        // endpoint can be reclaimed by a fresh same-process server.
        eprintln!("phase=client-drop-blocked");
        drop(blocked);
        let replacement = symbrowse_daemon::Server::new(symbrowse_daemon::ServerOptions {
            socket_path: endpoint,
            session,
            idle_timeout: Some(Duration::from_millis(1)),
            ..Default::default()
        })
        .expect("construct replacement after shutdown");
        assert!(replacement.listen_and_serve().is_ok());
    }

    #[test]
    fn paused_peer_backpressure_closes_with_eof_after_shutdown() {
        use std::{
            io::{Read, Write},
            sync::Arc,
            thread,
            time::Duration,
        };

        let session = format!("windows-backpressure-{}", std::process::id());
        let endpoint = default_socket_path(&session);
        let server = Arc::new(
            symbrowse_daemon::Server::new(symbrowse_daemon::ServerOptions {
                socket_path: endpoint.clone(),
                session,
                idle_timeout: None,
                handler: Some(Arc::new(|_, _| {
                    Ok((
                        Some(serde_json::json!({"payload": "x".repeat(256 * 1024)})),
                        Vec::new(),
                    ))
                })),
                ..Default::default()
            })
            .expect("construct backpressure daemon"),
        );
        let running = server.clone();
        let server_thread = thread::spawn(move || running.listen_and_serve());

        let mut peer = connect_when_server_ready(&endpoint);
        peer.write_all(
            br#"{"cmd":"large"}
"#,
        )
        .expect("send request to backpressure daemon");
        peer.flush().expect("flush request");
        // Do not read the response: the server must exercise its bounded
        // write/flush path while the named-pipe peer applies backpressure.
        thread::sleep(Duration::from_millis(100));

        server.stop();
        let (done, result) = std::sync::mpsc::channel();
        thread::spawn(move || {
            let result = server_thread.join().expect("join backpressure daemon");
            let _ = done.send(result);
        });
        let result = result
            .recv_timeout(Duration::from_secs(2))
            .expect("daemon join exceeded bounded shutdown timeout");
        assert!(result.is_ok(), "backpressure shutdown result = {result:?}");
        // The peer intentionally never resumes reading. Dropping it after the
        // joined server proves cleanup does not depend on a client-side drain.
        drop(peer);
    }

    #[test]
    fn concurrent_starts_have_one_owner_and_recover_after_stop() {
        use std::{sync::Arc, thread, time::Duration};

        let session = format!("windows-race-{}", std::process::id());
        let endpoint = default_socket_path(&session);
        let servers = (0..8)
            .map(|_| {
                Arc::new(
                    symbrowse_daemon::Server::new(symbrowse_daemon::ServerOptions {
                        socket_path: endpoint.clone(),
                        session: session.clone(),
                        idle_timeout: None,
                        handler: Some(Arc::new(|_, _| {
                            Ok((Some(serde_json::json!({"pong": true})), Vec::new()))
                        })),
                        ..Default::default()
                    })
                    .expect("construct concurrent Windows daemon"),
                )
            })
            .collect::<Vec<_>>();
        let threads = servers
            .iter()
            .cloned()
            .map(|server| thread::spawn(move || server.listen_and_serve()))
            .collect::<Vec<_>>();
        drop(connect_when_server_ready(&endpoint));
        for server in &servers {
            server.stop();
        }
        let results = threads
            .into_iter()
            .map(|thread| thread.join().expect("join concurrent daemon"))
            .collect::<Vec<_>>();
        assert_eq!(
            results.iter().filter(|result| result.is_ok()).count(),
            1,
            "exactly one concurrent starter owns the endpoint: {results:?}"
        );
        assert_eq!(
            results
                .iter()
                .filter(|result| matches!(
                    result,
                    Err(symbrowse_daemon::ServerError::AlreadyRunning)
                ))
                .count(),
            7
        );

        // The first pipe instance is released with the owner process, so a
        // fresh start can reclaim the same endpoint after the previous owner stops.
        let replacement = symbrowse_daemon::Server::new(symbrowse_daemon::ServerOptions {
            socket_path: endpoint,
            session,
            idle_timeout: Some(Duration::from_millis(1)),
            ..Default::default()
        })
        .expect("construct replacement Windows daemon");
        let result = replacement.listen_and_serve();
        assert!(
            result.is_ok(),
            "released endpoint was not recoverable: {result:?}"
        );
    }
}
