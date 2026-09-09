use symbrowse_engine_chrome::ConnectionMode;

#[test]
fn connection_modes_have_stable_wire_names() {
    assert_eq!(
        serde_json::to_string(&ConnectionMode::Launch).unwrap(),
        "\"launch\""
    );
    assert_eq!(
        serde_json::to_string(&ConnectionMode::Attach).unwrap(),
        "\"attach\""
    );
}

#[tokio::test]
async fn real_launch_probe_is_opt_in() {
    if std::env::var_os("SYMBROWSE_E2E").as_deref() != Some(std::ffi::OsStr::new("1")) {
        return;
    }
    let executable = std::env::var_os("SYMBROWSE_CHROME_EXECUTABLE")
        .map(std::path::PathBuf::from)
        .expect("SYMBROWSE_CHROME_EXECUTABLE is required for the opt-in E2E test");
    let profile = std::env::temp_dir().join(format!("symbrowse-cdp-test-{}", std::process::id()));
    let report = symbrowse_engine_chrome::run_probe(symbrowse_engine_chrome::ProbeConfig {
        mode: symbrowse_engine_chrome::BrowserMode::Launch {
            executable,
            user_data_dir: profile.clone(),
            headless: true,
        },
        url: "data:text/html,<title>test</title><h1>test</h1>".to_owned(),
        timeout: std::time::Duration::from_secs(10),
    })
    .await
    .expect("real Chrome launch probe");
    assert_eq!(report.mode, ConnectionMode::Launch);
    assert_eq!(report.title, "test");
    assert_eq!(report.evaluated_value["heading"], "test");
    assert!(report.ax_node_count > 0);
    assert!(report.screenshot_bytes > 0);
    assert!(report.event_count > 0);
    for _ in 0..20 {
        match std::fs::remove_dir_all(&profile) {
            Ok(()) => break,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => break,
            Err(_) => std::thread::sleep(std::time::Duration::from_millis(25)),
        }
    }
    assert!(!profile.exists(), "Chrome profile did not clean up");
}
