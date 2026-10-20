package application

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/mrhollen/ravn/internal/domain"
)

// ValidateOpenCodeServerURL checks that rawURL has an http or https scheme,
// a non-empty host, and does not target cloud metadata or prohibited private/loopback
// IP ranges unless explicitly allowed via OPENCODE_ALLOWED_HOSTS or OPENCODE_ALLOW_PRIVATE_IPS.
func ValidateOpenCodeServerURL(rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("%w: invalid opencode server URL: %v", domain.ErrInvalid, err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: opencode server URL scheme must be http or https, got %q", domain.ErrInvalid, u.Scheme)
	}

	hostname := strings.ToLower(u.Hostname())
	if hostname == "" {
		return fmt.Errorf("%w: opencode server URL host must not be empty", domain.ErrInvalid)
	}

	// Always block cloud metadata endpoints unless explicitly allowlisted
	if isCloudMetadataHost(hostname) && !isHostExplicitlyAllowed(u.Host, hostname) {
		return fmt.Errorf("%w: opencode server URL targets prohibited metadata endpoint %q", domain.ErrInvalid, hostname)
	}

	// If host is explicitly in OPENCODE_ALLOWED_HOSTS, allow it
	if isHostExplicitlyAllowed(u.Host, hostname) {
		return nil
	}

	// Parse direct IP if provided
	if ip := net.ParseIP(hostname); ip != nil {
		return checkIPProhibited(ip)
	}

	// Check loopback name "localhost"
	if hostname == "localhost" {
		if allowPrivateIPs() {
			return nil
		}
		return fmt.Errorf("%w: opencode server URL targets prohibited loopback address %q", domain.ErrInvalid, hostname)
	}

	// If hostname is not an IP, attempt DNS lookup to block rebinding / alias attacks
	ips, err := net.LookupIP(hostname)
	if err == nil && len(ips) > 0 {
		for _, ip := range ips {
			if err := checkIPProhibited(ip); err != nil {
				return err
			}
		}
	}

	return nil
}

// ValidateOpenCodeDirectory validates that an OpenCode working directory
// is an absolute path, does not contain path traversal ('..'), and complies with
// OPENCODE_ALLOWED_DIRECTORIES if configured.
func ValidateOpenCodeDirectory(dir string) error {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return nil
	}

	if strings.Contains(trimmed, "..") {
		return fmt.Errorf("%w: opencode directory must not contain path traversal ('..')", domain.ErrInvalid)
	}

	clean := filepath.Clean(trimmed)
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("%w: opencode directory must be an absolute path", domain.ErrInvalid)
	}

	if allowedDirs := os.Getenv("OPENCODE_ALLOWED_DIRECTORIES"); allowedDirs != "" {
		matched := false
		for _, allowed := range strings.Split(allowedDirs, ",") {
			allowed = filepath.Clean(strings.TrimSpace(allowed))
			if allowed != "" && (clean == allowed || strings.HasPrefix(clean, allowed+string(filepath.Separator))) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%w: opencode directory %q is outside allowed directories", domain.ErrInvalid, clean)
		}
	}

	return nil
}

func isCloudMetadataHost(host string) bool {
	h := strings.ToLower(host)
	switch h {
	case "169.254.169.254", "metadata.google.internal", "instance-data", "metadata":
		return true
	}
	return strings.HasSuffix(h, ".metadata.google.internal")
}

func isHostExplicitlyAllowed(hostPort, hostname string) bool {
	allowed := os.Getenv("OPENCODE_ALLOWED_HOSTS")
	if allowed == "" {
		return false
	}
	hostPort = strings.ToLower(hostPort)
	hostname = strings.ToLower(hostname)
	ip := net.ParseIP(hostname)

	for _, token := range strings.Split(allowed, ",") {
		token = strings.TrimSpace(strings.ToLower(token))
		if token == "" {
			continue
		}
		if token == "*" {
			return true
		}
		if token == hostPort || token == hostname {
			return true
		}
		if ip != nil && strings.Contains(token, "/") {
			if _, cidr, err := net.ParseCIDR(token); err == nil && cidr.Contains(ip) {
				return true
			}
		}
	}
	return false
}

func allowPrivateIPs() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("OPENCODE_ALLOW_PRIVATE_IPS")))
	return v == "true" || v == "1" || v == "yes"
}

func checkIPProhibited(ip net.IP) error {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}

	// Cloud metadata and link-local (169.254.0.0/16 and fe80::/10)
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || isIPv4LinkLocal(ip) {
		return fmt.Errorf("%w: opencode server URL targets prohibited metadata or link-local address %s", domain.ErrInvalid, ip)
	}

	if allowPrivateIPs() {
		return nil
	}

	if ip.IsLoopback() {
		return fmt.Errorf("%w: opencode server URL targets prohibited loopback address %s", domain.ErrInvalid, ip)
	}
	if ip.IsPrivate() {
		return fmt.Errorf("%w: opencode server URL targets prohibited private network address %s", domain.ErrInvalid, ip)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("%w: opencode server URL targets prohibited unspecified address %s", domain.ErrInvalid, ip)
	}

	return nil
}

func isIPv4LinkLocal(ip net.IP) bool {
	if len(ip) == net.IPv4len {
		return ip[0] == 169 && ip[1] == 254
	}
	return false
}
