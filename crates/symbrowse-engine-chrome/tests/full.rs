use std::{
    fs,
    io::{Read, Write},
    net::{TcpListener, TcpStream},
    path::{Path, PathBuf},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::Duration,
};

use symbrowse_engine_chrome::{
    BrowserMode, ChromeSession, ScreenshotOptions, UnsupportedOperation, capabilities,
};

fn e2e_enabled() -> bool {
    std::env::var_os("SYMBROWSE_E2E").as_deref() == Some(std::ffi::OsStr::new("1"))
}

fn chrome_executable() -> PathBuf {
    symbrowse_engine_chrome::discover_chrome_executable()
        .expect("Chrome/Chromium executable discovery")
}

fn cleanup_profile(path: &Path) {
    for _ in 0..40 {
        let _ = fs::remove_dir_all(path);
        thread::sleep(Duration::from_millis(50));
        if !path.exists() {
            return;
        }
    }
    panic!("Chrome profile did not clean up: {}", path.display());
}

struct ProfileCleanup(PathBuf);

impl Drop for ProfileCleanup {
    fn drop(&mut self) {
        if self.0.exists() {
            cleanup_profile(&self.0);
        }
    }
}

struct TestServer {
    base_url: String,
    stop: Arc<AtomicBool>,
    thread: Option<thread::JoinHandle<()>>,
}

impl TestServer {
    fn start() -> Self {
        let listener = TcpListener::bind(("127.0.0.1", 0)).expect("bind test server");
        let port = listener.local_addr().expect("test server address").port();
        listener
            .set_nonblocking(true)
            .expect("nonblocking listener");
        let stop = Arc::new(AtomicBool::new(false));
        let stop_for_thread = Arc::clone(&stop);
        let thread = thread::spawn(move || {
            while !stop_for_thread.load(Ordering::Relaxed) {
                match listener.accept() {
                    Ok((stream, _)) => serve(stream),
                    Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                        thread::sleep(Duration::from_millis(2));
                    }
                    Err(_) => break,
                }
            }
        });
        Self {
            base_url: format!("http://127.0.0.1:{port}"),
            stop,
            thread: Some(thread),
        }
    }
}

impl Drop for TestServer {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        if let Some(thread) = self.thread.take() {
            let _ = thread.join();
        }
    }
}

fn serve(mut stream: TcpStream) {
    stream
        .set_read_timeout(Some(Duration::from_secs(2)))
        .expect("set read timeout");
    let mut request = [0_u8; 4096];
    let size = stream.read(&mut request).unwrap_or(0);
    let request = String::from_utf8_lossy(&request[..size]);
    let path = request
        .lines()
        .next()
        .and_then(|line| line.split_whitespace().nth(1))
        .unwrap_or("/");
    let (status, content_type, body) = match path {
        "/asset.js" => (
            "200 OK",
            "application/javascript",
            "window.assetLoaded = true;",
        ),
        "/download.txt" => ("200 OK", "text/plain", "downloaded-by-rust012"),
        _ => (
            "200 OK",
            "text/html; charset=utf-8",
            r#"<!doctype html>
<title>rust012</title>
<style>#scroll { margin-top: 1800px; width: 20px; height: 20px; background: green }</style>
<input id="text" value="initial"><select id="choice"><option value="one">One</option><option value="two">Two</option></select>
<input id="check" type="checkbox"><button id="button" onclick="document.querySelector('#status').textContent='clicked'">Click</button>
<div id="dbl" ondblclick="this.dataset.doubled='yes'">Double</div><div id="hover" onmouseenter="this.dataset.hovered='yes'">Hover</div>
<div id="status"></div><div id="scroll"></div>
<input id="file" type="file"><a id="download" download="rust012.txt" href="/download.txt">download</a>
<iframe id="frame" srcdoc="<!doctype html><title>child</title><p>frame</p>"></iframe>
<script src="/asset.js"></script>"#,
        ),
    };
    let response = format!(
        "HTTP/1.1 {status}\r\nContent-Type: {content_type}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
    let _ = stream.write_all(response.as_bytes());
}

#[tokio::test]
async fn full_chrome_surface_is_real_and_opt_in() {
    if !e2e_enabled() {
        return;
    }

    let server = TestServer::start();
    let profile = std::env::temp_dir().join(format!(
        "symbrowse-rust012-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("system clock")
            .as_nanos()
    ));
    let _profile_cleanup = ProfileCleanup(profile.clone());
    let download_dir = profile.join("downloads");
    fs::create_dir_all(&download_dir).expect("create download directory");
    let upload = profile.join("upload.txt");
    fs::write(&upload, "uploaded-by-rust012").expect("write upload fixture");

    let session = ChromeSession::connect(
        BrowserMode::Launch {
            executable: chrome_executable(),
            user_data_dir: profile.clone(),
            headless: true,
        },
        Duration::from_secs(20),
    )
    .await
    .expect("launch Chrome");
    let page = session
        .new_page(format!("{}/", server.base_url))
        .await
        .expect("create page");
    assert!(!session.pages().await.expect("list tabs").is_empty());

    page.wait_for_selector("#text", true, Duration::from_secs(5))
        .await
        .expect("wait for initial page");
    assert_eq!(page.inspect("#text", "count").await.expect("count"), 1);
    assert!(page.inspect("#text", "find").await.expect("find").as_bool() == Some(true));
    assert_eq!(
        page.inspect("#text", "get").await.expect("get")["value"],
        "initial"
    );
    assert!(page.inspect("#text", "is").await.expect("is").as_bool() == Some(true));

    page.click("#button").await.expect("click");
    page.double_click("#dbl").await.expect("double click");
    page.focus("#text").await.expect("focus");
    page.hover("#hover").await.expect("hover");
    page.scroll_into_view("#scroll").await.expect("scroll");
    page.type_text("#text", " typed").await.expect("type");
    page.fill("#text", "filled").await.expect("fill");
    page.press("#text", "End").await.expect("press");
    page.select("#choice", "two").await.expect("select");
    page.check("#check").await.expect("check");
    page.uncheck("#check").await.expect("uncheck");
    page.wait_for_selector("#status", true, Duration::from_secs(1))
        .await
        .expect("wait for status");
    assert_eq!(
        page.inspect("#status", "get").await.expect("status")["text"],
        "clicked"
    );
    assert_eq!(
        page.inspect("#dbl", "get").await.expect("double state")["attributes"]["data-doubled"],
        "yes"
    );
    assert_eq!(
        page.inspect("#hover", "get").await.expect("hover state")["attributes"]["data-hovered"],
        "yes"
    );
    assert_eq!(
        page.inspect("#choice", "get").await.expect("select state")["value"],
        "two"
    );
    assert_eq!(
        page.inspect("#check", "get").await.expect("check state")["checked"],
        false
    );

    let frames = page.frames().await.expect("frame tree");
    assert!(
        frames
            .iter()
            .any(|frame| frame.url.starts_with("about:srcdoc"))
    );
    assert!(
        !page
            .accessibility_tree()
            .await
            .expect("accessibility tree")
            .is_empty()
    );

    page.upload_files(
        "#file",
        &[upload.to_string_lossy().into_owned()],
        &[profile.to_string_lossy().into_owned()],
    )
    .await
    .expect("upload file");
    assert!(
        page.inspect("#file", "get").await.expect("file state")["value"]
            .as_str()
            .is_some_and(|value| value.ends_with("upload.txt"))
    );

    page.set_download_behavior(Some(&download_dir))
        .await
        .expect("allow downloads");
    page.click("#download").await.expect("download");
    tokio::time::sleep(Duration::from_millis(250)).await;
    assert!(
        fs::read_dir(&download_dir)
            .expect("read downloads")
            .next()
            .is_some()
    );

    let screenshot = page
        .screenshot(ScreenshotOptions::default())
        .await
        .expect("PNG screenshot");
    assert_eq!(screenshot.mime_type, "image/png");
    assert!(screenshot.bytes.starts_with(&[0x89, b'P', b'N', b'G']));
    let jpeg = page
        .screenshot(ScreenshotOptions {
            format: "jpeg".into(),
            ..Default::default()
        })
        .await
        .expect("JPEG screenshot");
    assert_eq!(jpeg.mime_type, "image/jpeg");
    assert!(jpeg.bytes.starts_with(&[0xff, 0xd8]));
    let selected = page
        .screenshot(ScreenshotOptions {
            selector: "#status".into(),
            ..Default::default()
        })
        .await
        .expect("selector screenshot");
    assert!(!selected.bytes.is_empty());
    let pdf = page.pdf().await.expect("PDF");
    assert_eq!(pdf.mime_type, "application/pdf");
    assert!(pdf.bytes.starts_with(b"%PDF"));

    let wait_page = page.clone();
    let navigation =
        tokio::spawn(async move { wait_page.wait_for_navigation(Duration::from_secs(2)).await });
    tokio::task::yield_now().await;
    page.raw()
        .goto(format!("{}/", server.base_url))
        .await
        .expect("navigation");
    navigation
        .await
        .expect("navigation wait task")
        .expect("wait for navigation");

    let capture = page.start_network_capture().await.expect("network capture");
    page.raw()
        .goto(format!("{}/", server.base_url))
        .await
        .expect("reload page");
    let events = capture.collect(Duration::from_secs(2)).await;
    assert!(
        events
            .iter()
            .any(|event| event.kind == "request" && event.url.ends_with('/'))
    );
    assert!(
        events
            .iter()
            .any(|event| event.kind == "response" && event.status == 200)
    );

    page.set_offline(true).await.expect("offline on");
    page.set_offline(false).await.expect("offline off");
    page.block_urls(vec![format!("{}/blocked.js", server.base_url)])
        .await
        .expect("block URL patterns");

    let trigger = {
        let raw = page.raw().clone();
        tokio::spawn(async move { raw.evaluate("alert('manual')").await })
    };
    let dialog = page
        .dialog(true, None, Duration::from_secs(2))
        .await
        .expect("manual dialog");
    assert_eq!(dialog.kind, "alert");
    assert_eq!(dialog.message, "manual");
    trigger
        .await
        .expect("manual dialog task")
        .expect("manual evaluate");

    let handler = page
        .start_auto_dialog_handler(true, Duration::from_millis(300))
        .await
        .expect("auto dialog handler");
    page.raw()
        .evaluate("alert('automatic')")
        .await
        .expect("automatic dialog");
    assert_eq!(
        handler
            .await
            .expect("auto handler task")
            .expect("auto handler"),
        1
    );

    assert!(
        matches!(page.har().await, Err(error) if error.downcast_ref::<UnsupportedOperation>().is_some())
    );
    assert!(
        matches!(page.axe_audit().await, Err(error) if error.downcast_ref::<UnsupportedOperation>().is_some())
    );
    assert_eq!(
        capabilities().unsupported,
        vec!["har-export", "axe-core-audit"]
    );

    session.close().await.expect("close Chrome");
    cleanup_profile(&profile);
}
