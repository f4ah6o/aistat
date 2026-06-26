#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AccountProvider {
    Claude,
    Codex,
}

impl AccountProvider {
    pub fn as_str(&self) -> &'static str {
        match self {
            AccountProvider::Claude => "claude",
            AccountProvider::Codex => "codex",
        }
    }
}

impl std::fmt::Display for AccountProvider {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
