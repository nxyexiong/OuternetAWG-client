package profile

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxSize = 128 * 1024

type Profile struct {
	Name       string
	Native     string
	Addresses  []netip.Prefix
	DNS        []netip.Addr
	MTU        int
	Host       string
	Port       uint16
	privateKey string
	publicKey  string
	preshared  string
	keepalive  int
	listenPort int
	awg        map[string]string
	ipv6       bool
}

type Summary struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Address  string `json:"address"`
}

func (p *Profile) Summary() Summary {
	a := make([]string, len(p.Addresses))
	for i, address := range p.Addresses {
		a[i] = address.String()
	}

	return Summary{p.Name, net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), strings.Join(a, ", ")}
}

func (p *Profile) PersistentKeepalive() int { return p.keepalive }

var awgFields = map[string]string{
	"jc": "jc", "jmin": "jmin", "jmax": "jmax",
	"s1": "s1", "s2": "s2", "s3": "s3", "s4": "s4",
	"h1": "h1", "h2": "h2", "h3": "h3", "h4": "h4",
	"i1": "i1", "i2": "i2", "i3": "i3", "i4": "i4", "i5": "i5",
	"headerprotectionkey": "header_protection_key", "contentpaddingaddition": "content_padding_addition",
	"rekeyaftertime": "rekey_after_time", "rekeytimeout": "rekey_timeout", "rejectaftertime": "reject_after_time",
	"keepalivetimeout": "keepalive_timeout", "maxhandshakeattempts": "max_handshake_attempts",
	"randomtrailers": "random_trailers", "disablecookies": "disable_cookies",
}

func Parse(name, content string) (*Profile, error) {
	if len(content) == 0 || len(content) > MaxSize || !utf8.ValidString(content) {
		return nil, errors.New("config must be UTF-8 text, at most 128 KiB")
	}
	content = strings.TrimSpace(strings.TrimPrefix(content, "\ufeff"))
	if strings.HasPrefix(content, "vpn://") {
		var err error
		content, err = unpackGuest(content)
		if err != nil {
			return nil, err
		}
	}
	if name == "" {
		name = "Outernet"
	}
	if utf8.RuneCountInString(name) > 80 || strings.ContainsFunc(name, unicode.IsControl) {
		return nil, errors.New("config name must be at most 80 characters without control characters")
	}
	p := &Profile{Name: name, Native: content, MTU: 1280, keepalive: 25, awg: map[string]string{}}
	section := ""
	seen := map[string]bool{}
	sections := map[string]bool{}
	var allowed []netip.Prefix
	for index, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		bad := func(reason string) (*Profile, error) {
			return nil, fmt.Errorf("config line %d: %s", index+1, reason)
		}
		if strings.HasPrefix(line, "[") {
			section = strings.ToLower(line)
			if section != "[interface]" && section != "[peer]" || sections[section] {
				return bad("exactly one [Interface] and one [Peer] are supported")
			}
			sections[section] = true
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		if !ok || section == "" || value == "" || len(value) > 8192 || strings.ContainsFunc(value, unicode.IsControl) {
			return bad("invalid setting")
		}
		id := section + key
		if seen[id] {
			return bad("duplicate setting")
		}
		seen[id] = true
		var err error
		switch {
		case section == "[interface]" && key == "privatekey":
			p.privateKey, err = keyHex(value)
		case section == "[interface]" && key == "address":
			for _, item := range strings.Split(value, ",") {
				a, e := netip.ParsePrefix(strings.TrimSpace(item))
				if e != nil || !a.Addr().IsGlobalUnicast() || a.Addr().Is4In6() || a.Bits() == 0 {
					err = errors.New("invalid tunnel address")
					break
				}
				p.Addresses = append(p.Addresses, a)
				p.ipv6 = p.ipv6 || a.Addr().Is6()
			}
		case section == "[interface]" && key == "dns":
			for _, item := range strings.Split(value, ",") {
				a, e := netip.ParseAddr(strings.TrimSpace(item))
				if e != nil || !a.IsGlobalUnicast() || a.Is4In6() {
					err = errors.New("DNS must contain IP addresses, not search domains")
					break
				}
				p.DNS = append(p.DNS, a)
			}
		case section == "[interface]" && key == "mtu":
			p.MTU, err = boundedInt(value, 1280, 1420)
		case section == "[interface]" && key == "listenport":
			p.listenPort, err = boundedInt(value, 0, 65535)
		case section == "[interface]" && awgFields[key] != "":
			p.awg[awgFields[key]], err = validateAWG(key, value)
		case section == "[peer]" && key == "publickey":
			p.publicKey, err = keyHex(value)
		case section == "[peer]" && key == "presharedkey":
			p.preshared, err = keyHex(value)
		case section == "[peer]" && key == "endpoint":
			var port string
			p.Host, port, err = net.SplitHostPort(value)
			if err == nil {
				var number int
				number, err = boundedInt(port, 1, 65535)
				p.Port = uint16(number)
			}
			if p.Host == "" || strings.ContainsAny(p.Host, " /\\%") {
				err = errors.New("invalid endpoint host")
			}
		case section == "[peer]" && key == "persistentkeepalive":
			p.keepalive, err = boundedInt(value, 0, 65535)
		case section == "[peer]" && key == "allowedips":
			for _, item := range strings.Split(value, ",") {
				a, e := netip.ParsePrefix(strings.TrimSpace(item))
				if e != nil || a.Addr().Is4In6() {
					err = errors.New("invalid AllowedIPs")
					break
				}
				allowed = append(allowed, a.Masked())
			}
		default:
			return bad("unsupported setting (scripts, multiple peers and custom routing are not supported)")
		}
		if err != nil {
			// Never echo a setting's value: it may contain private key material.
			return bad("invalid value for " + key)
		}
	}
	if p.privateKey == "" || p.publicKey == "" || p.Host == "" || len(p.Addresses) == 0 || len(p.DNS) == 0 {
		return nil, errors.New("config requires private/public keys, Address, DNS and Endpoint")
	}
	full4, low4, high4, full6, low6, high6 := false, false, false, false, false, false
	for _, a := range allowed {
		switch a.String() {
		case "0.0.0.0/0":
			full4 = true
		case "0.0.0.0/1":
			low4 = true
		case "128.0.0.0/1":
			high4 = true
		case "::/0":
			full6 = true
		case "::/1":
			low6 = true
		case "8000::/1":
			high6 = true
		}
	}
	if !full4 && !(low4 && high4) {
		return nil, errors.New("this client requires full-tunnel IPv4 AllowedIPs (0.0.0.0/0)")
	}
	has4 := false
	for _, a := range p.Addresses {
		has4 = has4 || a.Addr().Is4()
	}
	if !has4 || p.ipv6 && !full6 && !(low6 && high6) {
		return nil, errors.New("a full-tunnel address and AllowedIPs are required for each configured family")
	}
	for _, a := range p.DNS {
		if a.Is6() && !p.ipv6 {
			return nil, errors.New("IPv6 DNS requires an IPv6 tunnel address")
		}
	}
	if min, ok := p.awg["jmin"]; ok {
		a, _ := strconv.Atoi(min)
		b, _ := strconv.Atoi(p.awg["jmax"])
		if a > b {
			return nil, errors.New("Jmin must not exceed Jmax")
		}
	}
	if p.awg["header_protection_key"] != "" {
		for _, name := range []string{"s1", "s2", "s3", "s4"} {
			padding, _ := strconv.Atoi(p.awg[name])
			if padding < 12 {
				return nil, errors.New("HeaderProtectionKey requires S1 through S4 to be at least 12")
			}
		}
	}
	return p, nil
}

func keyHex(value string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(b) != 32 || bytes.Equal(b, make([]byte, 32)) {
		return "", errors.New("expected nonzero 32-byte base64 key")
	}
	return hex.EncodeToString(b), nil
}

func boundedInt(s string, min, max int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < min || n > max {
		return 0, errors.New("number outside supported range")
	}
	return n, nil
}

func validateAWG(key, value string) (string, error) {
	switch {
	case key == "headerprotectionkey":
		return keyHex(value)
	case key == "randomtrailers" || key == "disablecookies":
		v, err := strconv.ParseBool(value)
		return strconv.FormatBool(v), err
	case strings.HasPrefix(key, "i") && len(key) == 2:
		return value, validatePacketTemplate(value)
	case key == "jc" || key == "jmin" || key == "jmax" || strings.HasPrefix(key, "s") && len(key) == 2:
		max := 128
		if key == "jmin" || key == "jmax" {
			max = 1280
		} else if key != "jc" {
			max = 256
		}
		n, err := boundedInt(value, 0, max)
		return strconv.Itoa(n), err
	default:
		parts := strings.Split(value, "-")
		if len(parts) > 2 {
			return "", errors.New("invalid numeric range")
		}
		var previous uint64
		for i, part := range parts {
			n, err := strconv.ParseUint(part, 10, 32)
			if err != nil || i > 0 && n < previous {
				return "", errors.New("invalid numeric range")
			}
			switch key {
			case "contentpaddingaddition":
				if n > 256 {
					return "", errors.New("padding is too large")
				}
			case "rekeyaftertime", "rekeytimeout", "rejectaftertime", "keepalivetimeout":
				if n < 1 || n > 86400 {
					return "", errors.New("timing is outside supported range")
				}
			case "maxhandshakeattempts":
				if n < 1 || n > 128 {
					return "", errors.New("handshake count is outside supported range")
				}
			}
			previous = n
		}

		return value, nil
	}
}

func validatePacketTemplate(value string) error {
	total := 0
	for remaining := strings.TrimSpace(value); remaining != ""; {
		if remaining[0] != '<' {
			return errors.New("invalid packet template")
		}
		tag, rest, ok := strings.Cut(remaining[1:], ">")
		if !ok {
			return errors.New("unterminated packet template")
		}
		parts := strings.Fields(tag)
		if len(parts) == 1 && parts[0] == "t" {
			total += 4
		} else if len(parts) == 2 {
			switch parts[0] {
			case "b":
				data, err := hex.DecodeString(strings.TrimPrefix(parts[1], "0x"))
				if err != nil || len(data) == 0 {
					return errors.New("invalid packet bytes")
				}
				total += len(data)
			case "r", "rc", "rd":
				n, err := boundedInt(parts[1], 0, 1280)
				if err != nil {
					return err
				}
				total += n
			default:
				return errors.New("unsupported packet template tag")
			}
		} else {
			return errors.New("invalid packet template tag")
		}
		if total > 1280 {
			return errors.New("packet template is too large")
		}
		remaining = strings.TrimSpace(rest)
	}
	if total == 0 {
		return errors.New("empty packet template")
	}
	return nil
}

func unpackGuest(content string) (string, error) {
	encoded := strings.TrimSpace(strings.TrimPrefix(content, "vpn://"))
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encoded, "="))
	if err != nil || len(raw) < 5 {
		return "", errors.New("invalid vpn:// encoding")
	}
	size := binary.BigEndian.Uint32(raw)
	if size == 0 || size > MaxSize {
		return "", errors.New("guest config is too large")
	}
	r, err := zlib.NewReader(bytes.NewReader(raw[4:]))
	if err != nil {
		return "", errors.New("invalid compressed guest config")
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil || len(data) != int(size) {
		return "", errors.New("invalid guest config size")
	}
	var guest struct {
		Containers []struct {
			Container string `json:"container"`
			AWG       struct {
				LastConfig string `json:"last_config"`
			} `json:"awg"`
		} `json:"containers"`
	}
	if json.Unmarshal(data, &guest) != nil || len(guest.Containers) != 1 {
		return "", errors.New("guest config must contain one AmneziaWG connection")
	}
	c := guest.Containers[0]
	if c.Container != "amnezia-awg2" && c.Container != "amnezia-awg" {
		return "", errors.New("only AmneziaWG guest configs are supported")
	}
	var last struct {
		Config string `json:"config"`
	}
	if json.Unmarshal([]byte(c.AWG.LastConfig), &last) != nil || last.Config == "" {
		return "", errors.New("guest config has no native AmneziaWG config")
	}
	return last.Config, nil
}

func (p *Profile) UAPI(endpoint netip.Addr) string {
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\nlisten_port=%d\nreplace_peers=true\n", p.privateKey, p.listenPort)
	keys := make([]string, 0, len(p.awg))
	for k := range p.awg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, p.awg[k])
	}
	fmt.Fprintf(&b, "public_key=%s\n", p.publicKey)
	if p.preshared != "" {
		fmt.Fprintf(&b, "preshared_key=%s\n", p.preshared)
	}
	fmt.Fprintf(&b, "endpoint=%s\npersistent_keepalive_interval=%d\nreplace_allowed_ips=true\nallowed_ip=0.0.0.0/0\n",
		netip.AddrPortFrom(endpoint, p.Port), p.keepalive)
	if p.ipv6 {
		b.WriteString("allowed_ip=::/0\n")
	}
	return b.String()
}

func (p *Profile) Routes() []netip.Prefix {
	routes := []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1")}
	if p.ipv6 {
		routes = append(routes, netip.MustParsePrefix("::/1"), netip.MustParsePrefix("8000::/1"))
	}
	return routes
}
