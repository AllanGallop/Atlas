pub fn normalize_host(value: &str) -> String {
    value
        .trim()
        .trim_start_matches("https://")
        .trim_start_matches("http://")
        .split('/')
        .next()
        .unwrap_or("")
        .split(':')
        .next()
        .unwrap_or("")
        .trim_end_matches('.')
        .to_lowercase()
}

pub fn is_multi_part_public_suffix(suffix: &str) -> bool {
    matches!(
        suffix,
        "co.uk" | "org.uk" | "ac.uk" | "gov.uk" | "net.uk" | "sch.uk"
            | "com.au" | "net.au" | "org.au" | "co.nz" | "co.jp" | "com.br" | "com.mx" | "co.za"
    )
}

pub fn registrable_domain(host: &str) -> String {
    let host = normalize_host(host);
    let host = host.trim_start_matches("*.");
    let parts: Vec<&str> = host.split('.').collect();
    if parts.len() >= 3 {
        let suffix = format!("{}.{}", parts[parts.len() - 2], parts[parts.len() - 1]);
        if is_multi_part_public_suffix(&suffix) {
            return format!("{}.{}", parts[parts.len() - 3], suffix);
        }
    }
    if parts.len() >= 2 {
        return format!("{}.{}", parts[parts.len() - 2], parts[parts.len() - 1]);
    }
    host.to_string()
}

pub fn classify_host(host: &str) -> &'static str {
    let host = normalize_host(host);
    if host.parse::<std::net::IpAddr>().is_ok() {
        return "ip";
    }
    if host == registrable_domain(&host) {
        "domain"
    } else {
        "subdomain"
    }
}

pub fn is_crawlable_host(host: &str) -> bool {
    let host = normalize_host(host);
    !host.is_empty() && !host.starts_with('*') && !host.contains('*')
}

pub fn is_subdomain_of(host: &str, apex: &str) -> bool {
    let host = normalize_host(host);
    let apex = normalize_host(apex);
    host != apex && (host.ends_with(&format!(".{apex}")) || host == apex)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn normalize_host_strips_scheme_and_port() {
        assert_eq!(normalize_host("https://Api.Example.com:443/path"), "api.example.com");
        assert_eq!(normalize_host("http://example.com."), "example.com");
    }

    #[test]
    fn registrable_domain_handles_multi_part_suffix() {
        assert_eq!(registrable_domain("api.example.com"), "example.com");
        assert_eq!(registrable_domain("shop.example.co.uk"), "example.co.uk");
        assert_eq!(registrable_domain("*.cdn.example.com"), "example.com");
    }

    #[test]
    fn classify_host_distinguishes_domain_and_subdomain() {
        assert_eq!(classify_host("example.com"), "domain");
        assert_eq!(classify_host("api.example.com"), "subdomain");
        assert_eq!(classify_host("203.0.113.1"), "ip");
    }

    #[test]
    fn is_crawlable_host_rejects_wildcards() {
        assert!(is_crawlable_host("api.example.com"));
        assert!(!is_crawlable_host("*.example.com"));
        assert!(!is_crawlable_host(""));
    }

    #[test]
    fn is_subdomain_of_checks_apex_boundary() {
        assert!(is_subdomain_of("api.example.com", "example.com"));
        assert!(!is_subdomain_of("example.com", "example.com"));
        assert!(!is_subdomain_of("notexample.com", "example.com"));
    }
}
