use crate::providers::{provider_title, AccountResult, Limit, Report};
use std::collections::BTreeMap;
use std::io::Write;

struct LabelEntry {
    key: &'static str,
    label: &'static str,
}

fn text_labels(provider_id: &str) -> &'static [LabelEntry] {
    match provider_id {
        "claude" => &[
            LabelEntry { key: "five_hour", label: "5-hour" },
            LabelEntry { key: "seven_day", label: "7-day" },
            LabelEntry { key: "seven_day_sonnet", label: "7-day sonnet" },
        ],
        "codex" => &[
            LabelEntry { key: "five_hour", label: "5-hour" },
            LabelEntry { key: "seven_day", label: "7-day" },
            LabelEntry { key: "code_review_seven_day", label: "Code review 7-day" },
        ],
        "copilot" => &[LabelEntry { key: "month", label: "month" }],
        _ => &[],
    }
}

fn rate_limit_tier_label(tier: &str) -> &str {
    match tier {
        "default_claude_max_5x" => "Max 5x",
        "default_claude_max_20x" => "Max 20x",
        "default_claude_pro" => "Pro",
        "default_claude_free" => "Free",
        other => other,
    }
}

fn format_reset_duration(seconds: i64) -> String {
    if seconds <= 0 {
        return "0m".into();
    }
    let days = seconds / 86400;
    let hours = (seconds % 86400) / 3600;
    let minutes = (seconds % 3600) / 60;
    if days > 0 {
        format!("{}d {}h", days, hours)
    } else if hours > 0 {
        format!("{}h {}m", hours, minutes)
    } else {
        format!("{}m", minutes)
    }
}

fn format_limit_line(label: &str, l: &Limit) -> String {
    format!(
        "- {}: {:.1}% (resets in {})",
        label,
        l.used_percent,
        format_reset_duration(l.reset_after_seconds)
    )
}

fn render_limits_section(
    lines: &mut Vec<String>,
    provider_id: &str,
    limits: &BTreeMap<String, Limit>,
) {
    let known = text_labels(provider_id);
    let mut seen = std::collections::HashSet::new();
    for entry in known {
        if let Some(limit) = limits.get(entry.key) {
            seen.insert(entry.key);
            lines.push(format_limit_line(entry.label, limit));
        }
    }
    let mut unknown: Vec<&String> = limits.keys().filter(|k| !seen.contains(k.as_str())).collect();
    unknown.sort();
    for k in unknown {
        lines.push(format_limit_line(k, &limits[k]));
    }
}

fn render_accounts_section(
    title: &str,
    provider_id: &str,
    accts: &[AccountResult],
) -> String {
    let mut lines = vec![title.to_string()];
    for ar in accts {
        let mut header = format!("- {}", ar.email);
        if ar.active {
            header.push_str(" (active)");
        }
        let plan_label = rate_limit_tier_label(&ar.plan);
        if !plan_label.is_empty() {
            header.push_str(&format!(" [{}]", plan_label));
        }
        if let Some(ref err) = ar.error {
            if !err.is_empty() {
                lines.push(format!("{}: {}", header, err));
                continue;
            }
        }
        lines.push(header);
        if let Some(ref lims) = ar.limits {
            let known = text_labels(provider_id);
            let mut seen = std::collections::HashSet::new();
            for entry in known {
                if let Some(limit) = lims.get(entry.key) {
                    seen.insert(entry.key);
                    lines.push(format!("  {}", format_limit_line(entry.label, limit)));
                }
            }
            let mut unknown: Vec<&String> = lims.keys().filter(|k| !seen.contains(k.as_str())).collect();
            unknown.sort();
            for k in unknown {
                lines.push(format!("  {}", format_limit_line(k, &lims[k])));
            }
        }
    }
    lines.join("\n")
}

pub fn render_text<W: Write>(
    w: &mut W,
    report: &Report,
    requested: &[&str],
) -> std::io::Result<()> {
    let mut sections: Vec<String> = vec![];
    for &id in requested {
        let result = match report.providers.get(id) {
            Some(r) => r,
            None => continue,
        };
        let title = format!("{} usage", provider_title(id));

        if !result.accounts.is_empty() {
            sections.push(render_accounts_section(&title, id, &result.accounts));
            continue;
        }

        if let Some(ref err) = result.error {
            if !err.is_empty() {
                sections.push(format!("{}: {}", title, err));
                continue;
            }
        }

        let limits = match result.limits.as_ref() {
            Some(l) if !l.is_empty() => l,
            _ => continue,
        };

        let mut lines = vec![title.clone()];
        render_limits_section(&mut lines, id, limits);
        sections.push(lines.join("\n"));
    }

    if sections.is_empty() {
        return Ok(());
    }
    write!(w, "{}\n", sections.join("\n\n"))
}
