use crate::providers::Report;
use std::io::Write;

pub fn render_json<W: Write>(w: &mut W, report: &Report) -> std::io::Result<()> {
    let s = serde_json::to_string_pretty(report)
        .map_err(|e| std::io::Error::new(std::io::ErrorKind::Other, e))?;
    // serde_json::to_string_pretty uses 2-space indent and no HTML escaping by default
    writeln!(w, "{}", s)
}
