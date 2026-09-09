#![deny(unsafe_code)]

use serde_json::Value;

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct JsonRpcError {
    pub code: i32,
    pub message: String,
}

impl JsonRpcError {
    pub fn render(&self, id: &Value) -> String {
        let id = serde_json::to_string(id).expect("JSON-RPC id is serializable");
        let message =
            serde_json::to_string(&self.message).expect("JSON-RPC message is serializable");
        format!(
            "{{\"jsonrpc\":\"2.0\",\"id\":{id},\"error\":{{\"code\":{},\"message\":{message}}}}}",
            self.code
        )
    }
}

pub fn parse_error(message: impl Into<String>) -> JsonRpcError {
    JsonRpcError {
        code: -32700,
        message: format!("Parse error: {}", message.into()),
    }
}

pub fn method_not_found(method: &str) -> JsonRpcError {
    JsonRpcError {
        code: -32601,
        message: format!("Method not found: {method}"),
    }
}

pub fn unknown_tool(name: &str) -> JsonRpcError {
    JsonRpcError {
        code: -32601,
        message: format!("Unknown tool: {name}"),
    }
}
