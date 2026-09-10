use std::{
    io::{Read, Write},
    net::SocketAddr,
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
};

use flate2::{Compression, write::GzEncoder};
use symbrowse_core::policy::Allowlist;
use symbrowse_fetch::{
    BackoffConfig, BodyTooLarge, ClientOptions, FetchClient, FetchError, Profile, Request,
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt, copy_bidirectional},
    net::{TcpListener, TcpStream},
};

struct TestServer {
    address: SocketAddr,
    requests: Arc<AtomicUsize>,
}

impl TestServer {
    async fn start() -> Self {
        let listener = TcpListener::bind(("127.0.0.1", 0)).await.unwrap();
        let address = listener.local_addr().unwrap();
        let requests = Arc::new(AtomicUsize::new(0));
        let count = Arc::clone(&requests);
        tokio::spawn(async move {
            loop {
                let Ok((mut stream, _)) = listener.accept().await else {
                    break;
                };
                let count = Arc::clone(&count);
                tokio::spawn(async move {
                    let mut input = vec![0_u8; 16 * 1024];
                    let length = stream.read(&mut input).await.unwrap_or(0);
                    let request = String::from_utf8_lossy(&input[..length]);
                    let path = request.split_whitespace().nth(1).unwrap_or("/");
                    let attempt = count.fetch_add(1, Ordering::SeqCst) + 1;
                    let (status, headers, body) = match path {
                        "/redirect" => ("302 Found", "Location: /final\r\n", b"redirect".to_vec()),
                        "/loop" => ("302 Found", "Location: /loop\r\n", b"loop".to_vec()),
                        "/cross-host" => (
                            "302 Found",
                            "Location: http://other.invalid/blocked\r\n",
                            b"cross-host".to_vec(),
                        ),
                        "/final" => ("200 OK", "Content-Type: text/plain\r\n", b"final".to_vec()),
                        "/gzip" => {
                            let mut encoder = GzEncoder::new(Vec::new(), Compression::default());
                            encoder.write_all(b"decoded gzip body").unwrap();
                            (
                                "200 OK",
                                "Content-Encoding: gzip\r\nContent-Type: text/plain\r\n",
                                encoder.finish().unwrap(),
                            )
                        }
                        "/br" => (
                            "200 OK",
                            "Content-Encoding: br\r\nContent-Type: text/plain\r\n",
                            vec![
                                15, 9, 128, 100, 101, 99, 111, 100, 101, 100, 32, 98, 114, 111,
                                116, 108, 105, 32, 98, 111, 100, 121, 3,
                            ],
                        ),
                        "/zstd" => (
                            "200 OK",
                            "Content-Encoding: zstd\r\nContent-Type: text/plain\r\n",
                            vec![
                                40, 181, 47, 253, 4, 88, 137, 0, 0, 100, 101, 99, 111, 100, 101,
                                100, 32, 122, 115, 116, 100, 32, 98, 111, 100, 121, 248, 161, 42,
                                74,
                            ],
                        ),
                        "/latin1" => (
                            "200 OK",
                            "Content-Type: text/plain; charset=iso-8859-1\r\n",
                            vec![99, 97, 102, 233],
                        ),
                        "/meta-latin1" => (
                            "200 OK",
                            "Content-Type: text/html\r\n",
                            b"<meta charset=iso-8859-1><p>caf\xe9</p>".to_vec(),
                        ),
                        "/retry" if attempt == 1 => (
                            "503 Service Unavailable",
                            "Retry-After: 0\r\n",
                            b"try again".to_vec(),
                        ),
                        "/retry" => (
                            "200 OK",
                            "Content-Type: text/plain\r\n",
                            b"recovered".to_vec(),
                        ),
                        "/set-cookie" => (
                            "200 OK",
                            "Set-Cookie: sid=abc123; Path=/\r\n",
                            b"set".to_vec(),
                        ),
                        "/cookie"
                            if request.to_ascii_lowercase().contains("cookie: sid=abc123") =>
                        {
                            ("200 OK", "", b"cookie ok".to_vec())
                        }
                        "/cookie" => ("200 OK", "", b"no cookie".to_vec()),
                        _ if path.starts_with("http://") => (
                            "200 OK",
                            "Content-Type: text/plain\r\n",
                            b"proxied".to_vec(),
                        ),
                        _ => ("200 OK", "Content-Type: text/plain\r\n", b"ok".to_vec()),
                    };
                    let response = format!(
                        "HTTP/1.1 {status}\r\n{headers}Content-Length: {}\r\nConnection: close\r\n\r\n",
                        body.len()
                    );
                    let _ = stream.write_all(response.as_bytes()).await;
                    let _ = stream.write_all(&body).await;
                });
            }
        });
        Self { address, requests }
    }

    fn url(&self, path: &str) -> String {
        format!("http://{}{}", self.address, path)
    }
}

struct SocksServer {
    address: SocketAddr,
}

impl SocksServer {
    async fn start() -> Self {
        let listener = TcpListener::bind(("127.0.0.1", 0)).await.unwrap();
        let address = listener.local_addr().unwrap();
        tokio::spawn(async move {
            loop {
                let Ok((mut client, _)) = listener.accept().await else {
                    break;
                };
                tokio::spawn(async move {
                    let mut greeting = [0_u8; 2];
                    if client.read_exact(&mut greeting).await.is_err() {
                        return;
                    }
                    let mut methods = vec![0_u8; greeting[1] as usize];
                    if client.read_exact(&mut methods).await.is_err() {
                        return;
                    }
                    if client.write_all(&[5, 0]).await.is_err() {
                        return;
                    }
                    let mut header = [0_u8; 4];
                    if client.read_exact(&mut header).await.is_err() || header[1] != 1 {
                        return;
                    }
                    let target = match header[3] {
                        1 => {
                            let mut bytes = [0_u8; 4];
                            if client.read_exact(&mut bytes).await.is_err() {
                                return;
                            }
                            std::net::Ipv4Addr::from(bytes).to_string()
                        }
                        _ => return,
                    };
                    let mut port = [0_u8; 2];
                    if client.read_exact(&mut port).await.is_err() {
                        return;
                    }
                    let port = u16::from_be_bytes(port);
                    let Ok(mut upstream) = TcpStream::connect((target.as_str(), port)).await else {
                        return;
                    };
                    if client
                        .write_all(&[5, 0, 0, 1, 0, 0, 0, 0, 0, 0])
                        .await
                        .is_err()
                    {
                        return;
                    }
                    let _ = copy_bidirectional(&mut client, &mut upstream).await;
                });
            }
        });
        Self { address }
    }

    fn url(&self) -> String {
        format!("socks5h://{}", self.address)
    }
}

#[tokio::test]
async fn socks5_proxy_tunnels_http_request() {
    let server = TestServer::start().await;
    let socks = SocksServer::start().await;
    let client = FetchClient::honest().unwrap();
    let mut request = Request::get(server.url("/"));
    request.allow_private = true;
    request.proxy = Some(socks.url());
    let response = client.fetch(request).await.unwrap();
    assert_eq!(response.body, b"ok");
}

#[tokio::test]
async fn honest_http_methods_headers_and_body_are_preserved() {
    let server = TestServer::start().await;
    let client = FetchClient::honest().unwrap();
    let mut request = Request::post(server.url("/"), br#"{"key":"value"}"#.to_vec());
    request.allow_private = true;
    request
        .headers
        .insert("X-Test".to_owned(), "yes".to_owned());
    let response = client.fetch(request).await.unwrap();
    assert_eq!(response.status_code, 200);
    assert_eq!(response.body, b"ok");
}

#[tokio::test]
async fn redirects_are_followed_and_final_url_is_reported() {
    let server = TestServer::start().await;
    let client = FetchClient::honest().unwrap();
    let mut request = Request::get(server.url("/redirect"));
    request.allow_private = true;
    let response = client.fetch(request).await.unwrap();
    assert_eq!(response.final_url, server.url("/final"));
    assert_eq!(response.body, b"final");
}

#[tokio::test]
async fn redirect_loop_is_bounded() {
    let server = TestServer::start().await;
    let client = FetchClient::honest().unwrap();
    let mut request = Request::get(server.url("/loop"));
    request.allow_private = true;
    let result = client.fetch(request).await;
    assert!(matches!(result, Err(FetchError::Request(_))));
    assert!(server.requests.load(Ordering::SeqCst) <= 11);
}

#[tokio::test]
async fn cross_host_redirect_is_rejected_by_allowlist() {
    let server = TestServer::start().await;
    let client = FetchClient::honest().unwrap();
    let mut request = Request::get(server.url("/cross-host"));
    request.allow_private = true;
    request.allowlist = Some(Allowlist::parse(&[server.address.ip().to_string()]).unwrap());
    let result = client.fetch(request).await;
    match result {
        Err(FetchError::Request(error)) => assert!(
            !error.to_string().is_empty(),
            "redirect rejection error was empty"
        ),
        other => panic!("unexpected result: {other:?}"),
    }
}

#[tokio::test]
async fn http_proxy_is_used_for_a_request() {
    let proxy = TestServer::start().await;
    let client = FetchClient::honest().unwrap();
    let mut request = Request::get("http://target.invalid/resource");
    request.allow_private = true;
    request.proxy = Some(proxy.url("/"));
    let response = client.fetch(request).await.unwrap();
    assert_eq!(response.body, b"proxied");
}
#[tokio::test]
async fn invalid_proxy_is_rejected_before_request() {
    let client = FetchClient::honest().unwrap();
    let mut request = Request::get("http://target.invalid/resource");
    request.allow_private = true;
    request.proxy = Some("://invalid".to_owned());
    assert!(matches!(
        client.fetch(request).await,
        Err(FetchError::InvalidProxy(_))
    ));
}
#[tokio::test]
async fn compressed_and_decompressed_limits_are_bounded() {
    let server = TestServer::start().await;
    let client = FetchClient::new(Profile::Honest, ClientOptions::default()).unwrap();
    let mut request = Request::get(server.url("/gzip"));
    request.allow_private = true;
    let response = client.fetch(request.clone()).await.unwrap();
    assert_eq!(response.body, b"decoded gzip body");

    for (path, expected) in [
        ("/br", b"decoded brotli body".as_slice()),
        ("/zstd", b"decoded zstd body".as_slice()),
    ] {
        let mut compressed = Request::get(server.url(path));
        compressed.allow_private = true;
        let response = client.fetch(compressed).await.unwrap();
        assert_eq!(response.body, expected);
    }

    let mut latin1 = Request::get(server.url("/latin1"));
    latin1.allow_private = true;
    assert_eq!(client.fetch(latin1).await.unwrap().body, "café".as_bytes());

    let mut meta_latin1 = Request::get(server.url("/meta-latin1"));
    meta_latin1.allow_private = true;
    assert_eq!(
        client.fetch(meta_latin1).await.unwrap().body,
        b"<meta charset=iso-8859-1><p>caf\xc3\xa9</p>"
    );

    request.max_body = Some(4);
    assert!(matches!(
        client.fetch(request).await,
        Err(FetchError::BodyTooLarge(BodyTooLarge {
            compressed: false,
            ..
        }))
    ));

    let mut compressed = Request::get(server.url("/gzip"));
    compressed.allow_private = true;
    compressed.max_compressed_body = Some(1);
    assert!(matches!(
        client.fetch(compressed).await,
        Err(FetchError::BodyTooLarge(BodyTooLarge {
            compressed: true,
            ..
        }))
    ));
}

#[tokio::test]
async fn retry_retries_503_and_returns_recovered_body() {
    let server = TestServer::start().await;
    let options = ClientOptions::default().retry(true).backoff(BackoffConfig {
        initial_delay: std::time::Duration::from_millis(1),
        max_delay: std::time::Duration::from_millis(1),
        multiplier: 1.0,
        max_retries: 1,
    });
    let client = FetchClient::new(Profile::Honest, options).unwrap();
    let mut request = Request::get(server.url("/retry"));
    request.allow_private = true;
    let response = client.fetch(request).await.unwrap();
    assert_eq!(response.body, b"recovered");
    assert_eq!(server.requests.load(Ordering::SeqCst), 2);
}

#[tokio::test]
async fn named_sessions_persist_cookies_but_ephemeral_sessions_do_not() {
    let server = TestServer::start().await;
    let client = FetchClient::honest().unwrap();
    let mut set = Request::get(server.url("/set-cookie"));
    set.allow_private = true;
    set.session = Some("named".to_owned());
    client.fetch(set).await.unwrap();
    let mut named = Request::get(server.url("/cookie"));
    named.allow_private = true;
    named.session = Some("named".to_owned());
    assert_eq!(client.fetch(named).await.unwrap().body, b"cookie ok");
    let mut ephemeral = Request::get(server.url("/cookie"));
    ephemeral.allow_private = true;
    assert_eq!(client.fetch(ephemeral).await.unwrap().body, b"no cookie");
}

#[tokio::test]
async fn resolver_pins_one_public_address_set_per_host() {
    use std::sync::atomic::AtomicUsize;
    use symbrowse_fetch::PinnedResolver;

    let lookups = Arc::new(AtomicUsize::new(0));
    let observed = Arc::clone(&lookups);
    let resolver = PinnedResolver::with_lookup(move |host| {
        assert_eq!(host, "example.test");
        observed.fetch_add(1, Ordering::SeqCst);
        Ok(vec!["93.184.216.34:0".parse().unwrap()])
    });
    let first: reqwest::dns::Name = "example.test".parse().unwrap();
    let _ = reqwest::dns::Resolve::resolve(&resolver, first)
        .await
        .unwrap()
        .collect::<Vec<_>>();
    let second: reqwest::dns::Name = "example.test".parse().unwrap();
    let _ = reqwest::dns::Resolve::resolve(&resolver, second)
        .await
        .unwrap()
        .collect::<Vec<_>>();
    assert_eq!(lookups.load(Ordering::SeqCst), 1);
}

#[tokio::test]
async fn resolver_rejects_private_addresses_before_connecting() {
    use symbrowse_fetch::PinnedResolver;

    let resolver = PinnedResolver::with_lookup(|_| Ok(vec!["127.0.0.1:0".parse().unwrap()]));
    let name: reqwest::dns::Name = "public.example".parse().unwrap();
    let result = reqwest::dns::Resolve::resolve(&resolver, name).await;
    match result {
        Ok(_) => panic!("private address was accepted"),
        Err(error) => assert!(error.to_string().contains("private resolved address")),
    }
}

#[allow(dead_code)]
fn read_all(mut reader: impl Read) -> Vec<u8> {
    let mut body = Vec::new();
    reader.read_to_end(&mut body).unwrap();
    body
}
