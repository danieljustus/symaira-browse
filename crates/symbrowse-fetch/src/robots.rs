use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

use futures_util::StreamExt;
use symbrowse_core::policy::{SsrfError, SsrfGuard};
use url::Url;

use crate::honest::PinnedResolver;

/// Parsed robots.txt rules following RFC 9309's longest-match behavior.
#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct Robots {
    groups: Vec<Group>,
}

#[derive(Clone, Debug, Default, Eq, PartialEq)]
struct Group {
    agents: Vec<String>,
    rules: Vec<Rule>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct Rule {
    path: String,
    allowed: bool,
}

impl Robots {
    #[must_use]
    pub fn parse(input: &str) -> Self {
        let mut groups: Vec<Group> = Vec::new();
        let mut current: Option<usize> = None;
        for line in input.lines() {
            let line = line.split('#').next().unwrap_or("").trim();
            let Some((key, value)) = line.split_once(':') else {
                continue;
            };
            let key = key.trim().to_ascii_lowercase();
            let value = value.trim();
            if value.is_empty() && key != "allow" && key != "disallow" {
                continue;
            }
            match key.as_str() {
                "user-agent" => {
                    if current.is_none() || !groups[current.unwrap()].rules.is_empty() {
                        groups.push(Group::default());
                        current = Some(groups.len() - 1);
                    }
                    groups[current.unwrap()]
                        .agents
                        .push(value.to_ascii_lowercase());
                }
                "allow" | "disallow" => {
                    if let Some(index) = current
                        && !value.is_empty()
                    {
                        groups[index].rules.push(Rule {
                            path: value.to_owned(),
                            allowed: key == "allow",
                        });
                    }
                }
                _ => {}
            }
        }
        Self { groups }
    }

    /// Checks a path, choosing the most specific matching user-agent group.
    #[must_use]
    pub fn allows(&self, user_agent: &str, path_and_query: &str) -> bool {
        let user_agent = user_agent.to_ascii_lowercase();
        let mut selected: Option<(&Group, usize)> = None;
        for candidate in &self.groups {
            let Some(specificity) = candidate
                .agents
                .iter()
                .filter_map(|agent| {
                    if agent == "*" || user_agent.contains(agent) {
                        Some(if agent == "*" { 0 } else { agent.len() })
                    } else {
                        None
                    }
                })
                .max()
            else {
                continue;
            };
            if selected.is_none_or(|(_, best)| specificity > best) {
                selected = Some((candidate, specificity));
            }
        }
        let Some((group, _)) = selected else {
            return true;
        };
        let mut best: Option<(&Rule, usize)> = None;
        for rule in &group.rules {
            let matches = if rule.path.ends_with('*') {
                path_and_query.starts_with(&rule.path[..rule.path.len() - 1])
            } else {
                path_and_query.starts_with(&rule.path)
            };
            if matches
                && best.is_none_or(|(_, length)| {
                    rule.path.len() > length || (rule.path.len() == length && rule.allowed)
                })
            {
                best = Some((rule, rule.path.len()));
            }
        }
        best.is_none_or(|(rule, _)| rule.allowed)
    }

    #[must_use]
    pub fn is_empty(&self) -> bool {
        self.groups.is_empty()
    }
}

struct CachedRules {
    rules: Robots,
    expires: Instant,
}

/// A conservative robots checker. Fetch failures are fail-open for polite
/// crawling, while SSRF policy failures deny the page.
pub struct RobotsChecker {
    client: reqwest::Client,
    private_client: reqwest::Client,
    cache: Arc<Mutex<HashMap<String, CachedRules>>>,
    ttl: Duration,
    private: bool,
}

impl std::fmt::Debug for RobotsChecker {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("RobotsChecker")
            .field("ttl", &self.ttl)
            .finish_non_exhaustive()
    }
}

fn robots_redirect(
    attempt: reqwest::redirect::Attempt<'_>,
    allow_private: bool,
) -> reqwest::redirect::Action {
    if attempt.previous().len() >= 10 {
        return attempt.error("too many redirects");
    }
    if !allow_private && let Err(error) = SsrfGuard::new(false).allows_url(attempt.url().as_str()) {
        return attempt.error(error.to_string());
    }
    attempt.follow()
}

fn path_and_query(url: &Url) -> String {
    let path = if url.path().is_empty() {
        "/"
    } else {
        url.path()
    };
    url.query()
        .map_or_else(|| path.to_owned(), |query| format!("{path}?{query}"))
}

impl RobotsChecker {
    pub fn new() -> Result<Self, reqwest::Error> {
        let client = reqwest::Client::builder()
            .timeout(Duration::from_secs(10))
            .dns_resolver(PinnedResolver::system())
            .redirect(reqwest::redirect::Policy::custom(|attempt| {
                robots_redirect(attempt, false)
            }))
            .build()?;
        let private_client = reqwest::Client::builder()
            .timeout(Duration::from_secs(10))
            .redirect(reqwest::redirect::Policy::custom(|attempt| {
                robots_redirect(attempt, true)
            }))
            .build()?;
        Ok(Self {
            client,
            private_client,
            cache: Arc::new(Mutex::new(HashMap::new())),
            ttl: Duration::from_secs(3600),
            private: false,
        })
    }

    #[must_use]
    pub fn with_ttl(mut self, ttl: Duration) -> Self {
        self.ttl = ttl;
        self
    }

    #[must_use]
    pub fn with_private(mut self, private: bool) -> Self {
        self.private = private;
        self
    }

    pub async fn check(
        &self,
        user_agent: &str,
        raw_url: &str,
        allow_private: bool,
    ) -> Result<bool, String> {
        let url = Url::parse(raw_url).map_err(|error| format!("robots: parse url: {error}"))?;
        if !matches!(url.scheme(), "http" | "https") {
            return Ok(true);
        }
        let allow_private = allow_private || self.private;
        let origin = format!("{}://{}", url.scheme(), url.authority());
        let path = path_and_query(&url);
        if let Some(entry) = self
            .cache
            .lock()
            .expect("robots mutex poisoned")
            .get(&origin)
            && entry.expires > Instant::now()
        {
            return Ok(entry.rules.allows(user_agent, &path));
        }
        let robots_url = format!("{origin}/robots.txt");
        if !allow_private {
            match SsrfGuard::new(false).allows_url(&robots_url) {
                Ok(()) => {}
                Err(SsrfError::InvalidUrl { .. }) => return Ok(false),
                Err(SsrfError::BlockedPrivate { .. }) => return Ok(false),
                Err(SsrfError::ResolutionFailed { .. }) => return Ok(true),
            }
        }
        let http_client = if allow_private {
            &self.private_client
        } else {
            &self.client
        };
        let mut request = http_client.get(&robots_url);
        if !user_agent.is_empty() {
            request = request.header("User-Agent", user_agent);
        }
        let response = match request.send().await {
            Ok(response) => response,
            Err(error) => return Ok(!error.to_string().contains("blocked_private")),
        };
        if response.status().as_u16() == 404 {
            self.store(origin, Robots::default());
            return Ok(true);
        }
        if !response.status().is_success() {
            return Ok(true);
        }
        let mut stream = response.bytes_stream();
        let mut body = Vec::new();
        while let Some(chunk) = stream.next().await {
            let chunk = match chunk {
                Ok(chunk) => chunk,
                Err(_) => return Ok(true),
            };
            if body.len().saturating_add(chunk.len()) > 1 << 20 {
                return Ok(true);
            }
            body.extend_from_slice(&chunk);
        }
        let rules = Robots::parse(&String::from_utf8_lossy(&body));
        let allowed = rules.allows(user_agent, &path);
        self.store(origin, rules);
        Ok(allowed)
    }

    fn store(&self, origin: String, rules: Robots) {
        self.cache.lock().expect("robots mutex poisoned").insert(
            origin,
            CachedRules {
                rules,
                expires: Instant::now() + self.ttl,
            },
        );
    }
}
