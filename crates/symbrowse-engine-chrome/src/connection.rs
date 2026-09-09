use chromiumoxide::{Browser, Handler};

#[derive(Clone, Copy, Debug, Eq, PartialEq, serde::Serialize)]
#[serde(rename_all = "lowercase")]
pub enum ConnectionMode {
    Launch,
    Attach,
}

pub(crate) struct Connected {
    pub browser: Browser,
    pub handler: Handler,
    pub mode: ConnectionMode,
}
