use std::io::{Read, Write};
use std::net::TcpListener;

use symbrowse_fetch::robots::{Robots, RobotsChecker};

#[test]
fn robots_uses_specific_agent_and_longest_rule() {
    let rules = Robots::parse(
        "User-agent: *\nDisallow: /private\nAllow: /private/public\n\nUser-agent: symfetch\nDisallow: /all\nAllow: /all/public\n",
    );
    assert!(rules.allows("other-client", "/private/public/page"));
    assert!(!rules.allows("other-client", "/private/secret"));
    assert!(rules.allows("symfetch/1", "/all/public/page"));
    assert!(!rules.allows("symfetch/1", "/all/secret"));
}

#[test]
fn robots_without_matching_group_is_allow() {
    let rules = Robots::parse("User-agent: crawler\nDisallow: /hidden\n");
    assert!(rules.allows("other", "/hidden"));
    assert!(rules.allows("crawler", "/visible"));
    assert!(!rules.allows("crawler", "/hidden"));
}

#[test]
fn robots_preserves_query_and_allow_wins_equal_length_ties() {
    let rules = Robots::parse(
        "User-agent: *\nDisallow: /search?blocked=true\nAllow: /search?blocked=true\nDisallow: /equal\nAllow: /equal\n",
    );
    assert!(rules.allows("crawler", "/search?blocked=true"));
    assert!(rules.allows("crawler", "/equal/page"));
    assert!(rules.allows("crawler", "/search?other=true"));
}

#[test]
fn robots_empty_url_path_matches_root_rules() {
    let rules = Robots::parse("User-agent: *\nDisallow: /\n");
    assert!(!rules.allows("crawler", "/"));
}

#[tokio::test]
async fn oversized_robots_body_is_rejected_while_streaming() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let server = std::thread::spawn(move || {
        let (mut stream, _) = listener.accept().unwrap();
        let mut request = [0_u8; 4096];
        let _ = stream.read(&mut request);
        let body = vec![b'x'; (1 << 20) + 1];
        write!(
            stream,
            "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
            body.len()
        )
        .unwrap();
        stream.write_all(&body).unwrap();
    });
    let checker = RobotsChecker::new().unwrap().with_private(true);
    assert!(
        checker
            .check("symbrowse", &format!("http://{address}/page"), true)
            .await
            .unwrap()
    );
    server.join().unwrap();
}
