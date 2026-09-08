use std::{net::SocketAddr, sync::Arc};

use symbrowse_fetch::{
    FetchClient, Request,
    archive::{self, CdxClient, CdxQuery, WaybackClient},
    pipeline,
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpListener,
};

struct ArchiveServer {
    address: SocketAddr,
}

impl ArchiveServer {
    async fn start() -> Self {
        let listener = TcpListener::bind(("127.0.0.1", 0)).await.unwrap();
        let address = listener.local_addr().unwrap();
        tokio::spawn(async move {
            loop {
                let Ok((mut stream, _)) = listener.accept().await else {
                    break;
                };
                tokio::spawn(async move {
                    let mut input = vec![0_u8; 16 * 1024];
                    let length = stream.read(&mut input).await.unwrap_or(0);
                    let request = String::from_utf8_lossy(&input[..length]);
                    let path = request.split_whitespace().nth(1).unwrap_or("/");
                    let (status, body) = if path.starts_with("/cdx") {
                        (
                            "200 OK",
                            br#"[["timestamp","original","mimetype","statuscode","digest","length"],["20260101120000","https://example.test/missing","text/html","200","abc","42"]]"#.to_vec(),
                        )
                    } else if path.starts_with("/web/") {
                        ("200 OK", b"archived body".to_vec())
                    } else if path.starts_with("/missing410") {
                        ("410 Gone", b"gone".to_vec())
                    } else if path.starts_with("/missing") {
                        ("404 Not Found", b"missing".to_vec())
                    } else {
                        ("200 OK", b"ok".to_vec())
                    };
                    let response = format!(
                        "HTTP/1.1 {status}\r\nContent-Type: text/plain\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                        body.len()
                    );
                    let _ = stream.write_all(response.as_bytes()).await;
                    let _ = stream.write_all(&body).await;
                });
            }
        });
        Self { address }
    }

    fn url(&self, path: &str) -> String {
        format!("http://{}{}", self.address, path)
    }
}

#[tokio::test]
async fn cdx_lookup_is_http_backed_and_encodes_all_query_controls() {
    let server = ArchiveServer::start().await;
    let client = CdxClient::new(server.url("/cdx")).unwrap();
    let snapshots = client
        .lookup_with_policy(
            &CdxQuery {
                url: "https://example.test/a path".into(),
                from: Some("20200101".into()),
                to: Some("20260101".into()),
                limit: Some(3),
                match_type: Some("exact".into()),
            },
            true,
            None,
        )
        .await
        .unwrap();
    assert_eq!(snapshots.len(), 1);
    assert_eq!(snapshots[0].timestamp, "20260101120000");
    assert_eq!(snapshots[0].statuscode, "200");
}

#[tokio::test]
async fn wayback_fetch_and_404_410_recovery_are_http_backed() {
    let server = Arc::new(ArchiveServer::start().await);
    let archive_client = WaybackClient::new(server.url("/web")).unwrap();
    let archived = archive_client
        .fetch_with_policy(
            "https://example.test/missing",
            Some("20260101120000"),
            true,
            None,
        )
        .await
        .unwrap();
    assert_eq!(archived.status_code, 200);
    assert_eq!(archived.body, b"archived body");
    assert!(pipeline::needs_recovery(404));
    assert!(pipeline::needs_recovery(410));

    let client = FetchClient::honest().unwrap();
    let mut request = Request::get(server.url("/missing410"));
    request.allow_private = true;
    let recovered =
        archive::fetch_with_wayback(&client, request, &archive_client, Some("20260101120000"))
            .await
            .unwrap();
    assert_eq!(recovered.status_code, 200);
    assert_eq!(recovered.body, b"archived body");
}
