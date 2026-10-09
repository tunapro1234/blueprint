//! Port of internal/config/color.go.

use crate::gojson::time::go_quote;
use crate::text::{go_to_lower, go_trim_space};

/// Go `ColorIndex`: normalizes a named accent or xterm palette index.
pub fn color_index(value: &str) -> Result<String, String> {
    let value = go_to_lower(go_trim_space(value));
    let named = match value.as_str() {
        "red" => "160",
        "orange" => "208",
        "yellow" => "178",
        "green" => "70",
        "cyan" => "44",
        "blue" => "33",
        "purple" => "135",
        "pink" => "205",
        "gray" => "245",
        "white" => "255",
        _ => "",
    };
    if !named.is_empty() {
        return Ok(named.to_string());
    }
    match super::atoi(&value) {
        Some(n) if (0..=255).contains(&n) => Ok(n.to_string()),
        _ => Err(format!("invalid color {}: use a named color or 0–255", go_quote(&value))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn color_index_cases() {
        for (input, want) in [
            ("Purple", Ok("135")),
            (" 33 ", Ok("33")),
            ("+7", Ok("7")),
            ("007", Ok("7")),
            ("255", Ok("255")),
            ("256", Err(())),
            ("-1", Err(())),
            ("", Err(())),
            ("blurple", Err(())),
        ] {
            assert_eq!(color_index(input).map_err(|_| ()), want.map(String::from), "{input}");
        }
        assert_eq!(
            color_index("Nope").unwrap_err(),
            "invalid color \"nope\": use a named color or 0–255"
        );
    }
}
