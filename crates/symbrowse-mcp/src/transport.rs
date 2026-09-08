#![deny(unsafe_code)]

use std::io::{self, BufRead, BufReader, Read, Write};

use serde_json::Value;

use crate::{
    error,
    proxy::{DaemonProxy, DaemonProxyOptions, ToolError, ToolProxy},
    registry,
};

const PROTOCOL_VERSION: &str = "2024-11-05";
const INSTRUCTIONS: &str = "symbrowse drives a real Chrome browser via the local symbrowse daemon.\n\nEvery tool accepts an optional \"session\" argument (default: the server's\ndefault session). Use one session per task; sessions are isolated from each\nother.\n\nSecurity defaults in MCP mode: the domain allowlist and the SSRF guard are\nenforced by the daemon. Private/loopback targets are denied unless the server\nwas started with --allow-private. When a request is blocked, the tool result\ncarries a warnings[] array describing the denied URLs.\n\nStart at Tier 0 with fetch_url(url) for plain static content. Use fetch_batch\nfor independent URLs and wayback_snapshots for archive discovery. If the\nfetch result carries an \"escalate\" hint, or the page needs JavaScript, browser\nstate, or interaction, escalate to read(url) or open(url), then inspect with\nsnapshot() and act with click/fill/type/press. The output uses the symfetch\nschema.";

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ServeOptions {
    pub version: String,
    pub session: String,
    pub profiles: String,
    pub executable: String,
    pub allow_private: bool,
    pub engine: Option<String>,
    pub daemon_log_path: Option<String>,
}

impl Default for ServeOptions {
    fn default() -> Self {
        Self {
            version: "dev".to_owned(),
            session: "default".to_owned(),
            profiles: "core".to_owned(),
            executable: String::new(),
            allow_private: false,
            engine: None,
            daemon_log_path: None,
        }
    }
}

pub fn serve_stdio<R: Read, W: Write>(
    reader: R,
    writer: W,
    options: ServeOptions,
) -> io::Result<()> {
    let proxy_options = DaemonProxyOptions {
        session: options.session.clone(),
        executable: options.executable.clone(),
        allow_private: options.allow_private,
        engine: options.engine.clone(),
        daemon_log_path: options.daemon_log_path.clone(),
        ..DaemonProxyOptions::default()
    };
    let mut proxy = DaemonProxy::new(proxy_options);
    serve_with_proxy(reader, writer, options, &mut proxy)
}

pub fn serve_with_proxy<R: Read, W: Write, P: ToolProxy>(
    reader: R,
    mut writer: W,
    options: ServeOptions,
    proxy: &mut P,
) -> io::Result<()> {
    let mut reader = BufReader::new(reader);
    loop {
        let (body, mode) = match read_request(&mut reader)? {
            Some(request) => request,
            None => return Ok(()),
        };
        let line = String::from_utf8_lossy(&body);
        if let Some(response) = handle_line(&line, &options, proxy) {
            write_response(&mut writer, &response, mode)?;
        }
    }
}

#[derive(Debug)]
enum ResponseMode {
    Line,
    Framed,
}

fn read_request<R: BufRead>(reader: &mut R) -> io::Result<Option<(Vec<u8>, ResponseMode)>> {
    let mut first = String::new();
    loop {
        first.clear();
        if read_bounded_line(reader, &mut first, MAX_FRAME_BYTES, "MCP request line")? == 0 {
            return Ok(None);
        }
        if !first.trim().is_empty() {
            break;
        }
    }
    let trimmed = first.trim_start();
    if trimmed.starts_with('{') || !trimmed.contains(':') {
        return Ok(Some((first.into_bytes(), ResponseMode::Line)));
    }

    let mut content_length = None;
    parse_content_length(&first, &mut content_length)?;
    let mut header = String::new();
    let mut header_bytes = first.len();
    loop {
        header.clear();
        if read_bounded_line(
            reader,
            &mut header,
            MAX_HEADER_LINE_BYTES,
            "MCP header line",
        )? == 0
        {
            return Err(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                "EOF while reading MCP headers",
            ));
        }
        header_bytes = header_bytes.checked_add(header.len()).ok_or_else(|| {
            io::Error::new(io::ErrorKind::InvalidData, "MCP headers exceed size limit")
        })?;
        if header_bytes > MAX_HEADER_BYTES {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "MCP headers exceed size limit",
            ));
        }
        if header == "\n" || header == "\r\n" {
            break;
        }
        parse_content_length(&header, &mut content_length)?;
    }
    let length = content_length.ok_or_else(|| {
        io::Error::new(io::ErrorKind::InvalidData, "missing Content-Length header")
    })?;
    if length == 0 || length > MAX_FRAME_BYTES {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("invalid Content-Length: {length}"),
        ));
    }
    let mut body = vec![0; length];
    reader.read_exact(&mut body)?;
    Ok(Some((body, ResponseMode::Framed)))
}

const MAX_FRAME_BYTES: usize = 1 << 20;
const MAX_HEADER_LINE_BYTES: usize = 8 << 10;
const MAX_HEADER_BYTES: usize = 64 << 10;

fn read_bounded_line<R: BufRead>(
    reader: &mut R,
    output: &mut String,
    limit: usize,
    label: &str,
) -> io::Result<usize> {
    output.clear();
    let mut bytes = Vec::new();
    loop {
        let available = reader.fill_buf()?;
        if available.is_empty() {
            break;
        }
        let take = available
            .iter()
            .position(|byte| *byte == b'\n')
            .map_or(available.len(), |position| position + 1);
        if bytes.len().saturating_add(take) > limit {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("{label} exceeds size limit"),
            ));
        }
        bytes.extend_from_slice(&available[..take]);
        reader.consume(take);
        if bytes.last() == Some(&b'\n') {
            break;
        }
    }
    if bytes.is_empty() {
        return Ok(0);
    }
    *output = String::from_utf8(bytes).map_err(|_| {
        io::Error::new(
            io::ErrorKind::InvalidData,
            format!("{label} is not valid UTF-8"),
        )
    })?;
    Ok(output.len())
}

fn parse_content_length(line: &str, result: &mut Option<usize>) -> io::Result<()> {
    let line = line.trim_end_matches(['\r', '\n']);
    if let Some(value) = line.strip_prefix("Content-Length:") {
        *result = Some(value.trim().parse().map_err(|_| {
            io::Error::new(
                io::ErrorKind::InvalidData,
                format!("invalid Content-Length: {:?}", value.trim()),
            )
        })?);
    }
    Ok(())
}

fn write_response<W: Write>(writer: &mut W, response: &str, mode: ResponseMode) -> io::Result<()> {
    match mode {
        ResponseMode::Line => {
            writer.write_all(response.as_bytes())?;
            writer.write_all(b"\n")?;
        }
        ResponseMode::Framed => {
            write!(
                writer,
                "Content-Length: {}\r\n\r\n{}",
                response.len(),
                response
            )?;
        }
    }
    writer.flush()
}

fn handle_line<P: ToolProxy>(line: &str, options: &ServeOptions, proxy: &mut P) -> Option<String> {
    let value: Value = match serde_json::from_str(line) {
        Ok(value) => value,
        Err(parse) => {
            return Some(error::parse_error(go_parse_message(line, &parse)).render(&Value::Null));
        }
    };
    if value.is_null() {
        return None;
    }
    let Some(object) = value.as_object() else {
        return Some(
            error::parse_error(format!(
                "json: cannot unmarshal {} into Go value of type mcpserver.requestAlias",
                json_type(&value)
            ))
            .render(&Value::Null),
        );
    };
    let id = object.get("id").cloned().unwrap_or(Value::Null);
    let request_is_notification = !object.contains_key("id");
    let method = object
        .get("method")
        .and_then(Value::as_str)
        .unwrap_or_default();
    match method {
        "initialize" => {
            if request_is_notification {
                None
            } else {
                Some(initialize_response(&id, options))
            }
        }
        "notifications/initialized" | "notifications/cancelled" => None,
        "ping" => {
            if request_is_notification {
                None
            } else {
                Some(format!(
                    "{{\"jsonrpc\":\"2.0\",\"id\":{id},\"result\":{{}}}}"
                ))
            }
        }
        "tools/list" => {
            if request_is_notification {
                None
            } else {
                Some(
                    match registry::tools_list(&options.profiles, &options.session, &id) {
                        Ok(response) => response,
                        Err(message) => error::parse_error(message).render(&id),
                    },
                )
            }
        }
        "tools/call" => {
            if request_is_notification {
                None
            } else {
                Some(call_response(&id, object.get("params"), proxy))
            }
        }
        _ => {
            if request_is_notification {
                None
            } else {
                Some(error::method_not_found(method).render(&id))
            }
        }
    }
}

fn initialize_response(id: &Value, options: &ServeOptions) -> String {
    let id = serde_json::to_string(id).expect("JSON-RPC id is serializable");
    let instructions = serde_json::to_string(INSTRUCTIONS).expect("instructions are serializable");
    let version = serde_json::to_string(&options.version).expect("version is serializable");
    let mut response = String::from("{\"jsonrpc\":\"2.0\",\"id\":");
    response.push_str(&id);
    response.push_str(",\"result\":{\"capabilities\":{\"tools\":{}},\"instructions\":");
    response.push_str(&instructions);
    response.push_str(",\"protocolVersion\":\"");
    response.push_str(PROTOCOL_VERSION);
    response.push_str("\",\"serverInfo\":{\"name\":\"symbrowse\",\"version\":");
    response.push_str(&version);
    response.push_str("}}}");
    response
}

fn call_response<P: ToolProxy>(id: &Value, params: Option<&Value>, proxy: &mut P) -> String {
    let Some(params) = params.and_then(Value::as_object) else {
        return tool_failure(id, "invalid tools/call params", None);
    };
    let Some(name) = params.get("name").and_then(Value::as_str) else {
        return tool_failure(id, "missing required argument \"name\"", None);
    };
    let Some(spec) = registry::lookup(name) else {
        return error::unknown_tool(name).render(id);
    };
    let args = params
        .get("arguments")
        .cloned()
        .unwrap_or_else(|| Value::Object(Default::default()));
    let args = if args.is_null() {
        Value::Object(Default::default())
    } else {
        args
    };
    if let Err(message) = registry::validate_arguments(spec, &args) {
        return tool_failure(id, &message, None);
    }
    call_proxy(id, spec, &args, proxy)
}

fn call_proxy<P: ToolProxy>(
    id: &Value,
    spec: &registry::ToolSpec,
    args: &Value,
    proxy: &mut P,
) -> String {
    match proxy.call(spec, args) {
        Ok(data) => tool_success(id, data),
        Err(error) => tool_failure(id, &error.display_message(), Some(*error)),
    }
}

fn tool_success(id: &Value, data: Value) -> String {
    let text = match data {
        Value::String(value) => value,
        value => serde_json::to_string(&value).expect("tool data is serializable"),
    };
    let id = serde_json::to_string(id).expect("JSON-RPC id is serializable");
    let text = serde_json::to_string(&text).expect("tool text is serializable");
    format!(
        "{{\"jsonrpc\":\"2.0\",\"id\":{id},\"result\":{{\"content\":[{{\"text\":{text},\"type\":\"text\"}}],\"isError\":false}}}}"
    )
}

fn tool_failure(id: &Value, message: &str, structured: Option<ToolError>) -> String {
    let id = serde_json::to_string(id).expect("JSON-RPC id is serializable");
    let message = serde_json::to_string(message).expect("tool message is serializable");
    let mut response = String::from("{\"jsonrpc\":\"2.0\",\"id\":");
    response.push_str(&id);
    response.push_str(",\"result\":{");
    if let Some(error) = structured {
        let metadata =
            serde_json::to_string(&error.metadata()).expect("tool metadata is serializable");
        response.push_str("\"_meta\":{\"symaira.dev/tool_error\":");
        response.push_str(&metadata);
        response.push_str("},");
    }
    response.push_str("\"content\":[{\"text\":");
    response.push_str(&message);
    response.push_str(",\"type\":\"text\"}],\"isError\":true}}");
    response
}

fn go_parse_message(line: &str, parse: &serde_json::Error) -> String {
    let trimmed = line.trim();
    if let Some(message) = incomplete_literal_message(trimmed) {
        return message;
    }
    if parse.is_eof() {
        return "unexpected end of JSON input".to_owned();
    }
    let serde_message = parse.to_string();
    let character = parse_character(line, parse.column());
    if (serde_message.starts_with("expected value") || serde_message.starts_with("trailing comma"))
        && let Some(character) = character
    {
        return format!("invalid character '{character}' looking for beginning of value");
    }
    if serde_message.starts_with("expected `:`")
        && let Some(character) = character
    {
        return format!("invalid character '{character}' after object key");
    }
    if serde_message.starts_with("invalid escape")
        && let Some(character) = character
    {
        return format!("invalid character '{character}' in string escape code");
    }
    match trimmed
        .strip_prefix('{')
        .and_then(|rest| rest.chars().next())
    {
        Some(character) if character != '"' && character != '}' => {
            return format!(
                "invalid character '{character}' looking for beginning of object key string"
            );
        }
        _ => {}
    }
    serde_message
}

fn parse_character(line: &str, column: usize) -> Option<char> {
    line.chars().nth(column.saturating_sub(1))
}

fn incomplete_literal_message(trimmed: &str) -> Option<String> {
    for literal in ["true", "false", "null"] {
        if literal.starts_with(trimmed) && trimmed != literal {
            let expected = literal.chars().nth(trimmed.chars().count())?;
            return Some(format!(
                "invalid character ' ' in literal {literal} (expecting '{expected}')"
            ));
        }
    }
    None
}

fn json_type(value: &Value) -> &'static str {
    match value {
        Value::Null => "null",
        Value::Bool(_) => "bool",
        Value::Number(_) => "number",
        Value::String(_) => "string",
        Value::Array(_) => "array",
        Value::Object(_) => "object",
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proxy::ToolProxy;
    use std::io::Cursor;

    #[derive(Default)]
    struct Fake;

    impl ToolProxy for Fake {
        fn call(
            &mut self,
            _tool: &registry::ToolSpec,
            _args: &Value,
        ) -> Result<Value, Box<ToolError>> {
            Ok(serde_json::json!({"ok": true}))
        }
    }

    #[test]
    fn eof_and_notifications_are_silent() {
        let mut out = Vec::new();
        serve_with_proxy(
            Cursor::new(b""),
            &mut out,
            ServeOptions::default(),
            &mut Fake,
        )
        .expect("serve eof");
        assert!(out.is_empty());
        out.clear();
        serve_with_proxy(
            Cursor::new(b"{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n"),
            &mut out,
            ServeOptions::default(),
            &mut Fake,
        )
        .expect("serve notification");
        assert!(out.is_empty());
    }

    #[test]
    fn oversized_request_line_is_rejected_without_allocating_the_full_input() {
        let input = vec![b'x'; MAX_FRAME_BYTES + 1];
        let error = read_request(&mut Cursor::new(input)).expect_err("oversized line must fail");
        assert_eq!(error.kind(), io::ErrorKind::InvalidData);
        assert_eq!(error.to_string(), "MCP request line exceeds size limit");
    }

    #[test]
    fn oversized_header_line_is_rejected() {
        let mut input = b"Content-Length: 2\r\nX-Test: ".to_vec();
        input.extend(std::iter::repeat_n(b'x', MAX_HEADER_LINE_BYTES));
        input.push(b'\n');
        let error = read_request(&mut Cursor::new(input)).expect_err("oversized header must fail");
        assert_eq!(error.kind(), io::ErrorKind::InvalidData);
        assert_eq!(error.to_string(), "MCP header line exceeds size limit");
    }

    #[test]
    fn cumulative_header_size_is_bounded() {
        let mut input = b"Content-Length: 2\r\n".to_vec();
        while input.len() <= MAX_HEADER_BYTES {
            input.extend_from_slice(b"X: a\r\n");
        }
        input.extend_from_slice(b"\r\n{}");
        let error = read_request(&mut Cursor::new(input)).expect_err("oversized headers must fail");
        assert_eq!(error.kind(), io::ErrorKind::InvalidData);
        assert_eq!(error.to_string(), "MCP headers exceed size limit");
    }

    #[test]
    fn ping_and_go_parse_errors_are_compatible() {
        let options = ServeOptions::default();
        let mut proxy = Fake;
        assert_eq!(
            handle_line(
                r#"{"jsonrpc":"2.0","id":9,"method":"ping"}"#,
                &options,
                &mut proxy,
            ),
            Some(r#"{"jsonrpc":"2.0","id":9,"result":{}}"#.to_owned())
        );
        for (input, message) in [
            (
                r#"{"a":}"#,
                "invalid character '}' looking for beginning of value",
            ),
            (r#"{"a" 1}"#, "invalid character '1' after object key"),
            (r#"{"a":1"#, "unexpected end of JSON input"),
            (
                r#"{"a": [}"#,
                "invalid character '}' looking for beginning of value",
            ),
            (
                "tru",
                "invalid character ' ' in literal true (expecting 'e')",
            ),
            ("{", "unexpected end of JSON input"),
            (
                "[1,]",
                "invalid character ']' looking for beginning of value",
            ),
            (
                r#"{"a":"\x"}"#,
                "invalid character 'x' in string escape code",
            ),
        ] {
            assert_eq!(
                handle_line(input, &options, &mut proxy),
                Some(error::parse_error(message).render(&Value::Null)),
                "{input}"
            );
        }
    }
}
