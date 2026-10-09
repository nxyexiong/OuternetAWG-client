package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nxyexiong/OuternetAWG-client/shared/profile"
)

const (
	filterTable   = "outernetawg_gateway"
	natTable      = "outernetawg_gateway_nat"
	ownerComment  = "OuternetAWG gateway v1"
	routeTable    = 21110
	routeProtocol = 196
	rulePriority  = 11000
	guardLifetime = 15
)

type Runner interface {
	Run(context.Context, string, []string, string) ([]byte, error)
}

type ExecRunner struct{}

type limitedBuffer struct{ data bytes.Buffer }

func (b *limitedBuffer) Len() int       { return b.data.Len() }
func (b *limitedBuffer) Bytes() []byte  { return b.data.Bytes() }
func (b *limitedBuffer) String() string { return b.data.String() }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4*1024*1024 {
		return 0, errors.New("system command output exceeded 4 MiB")
	}
	return b.data.Write(p)
}

func (ExecRunner) Run(ctx context.Context, name string, args []string, input string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if len(message) > 2048 {
			message = message[:2048]
		}
		return nil, fmt.Errorf("%s failed: %w: %s", name, err, message)
	}
	return stdout.Bytes(), nil
}

type Network interface {
	Protect(context.Context) error
	Check(context.Context, *profile.Profile) error
	Configure(context.Context, *profile.Profile) error
	Permit(context.Context) error
	Deny(context.Context) error
}

type LinuxNetwork struct {
	Config Config
	Runner Runner
}

func (n *LinuxNetwork) command(ctx context.Context, name string, args ...string) error {
	_, err := n.Runner.Run(ctx, name, args, "")
	return err
}

func (n *LinuxNetwork) nft(ctx context.Context, script string) error {
	_, err := n.Runner.Run(ctx, "nft", []string{"-f", "-"}, script)
	return err
}

type nftTable struct {
	Family  string `json:"family"`
	Name    string `json:"name"`
	Comment string `json:"comment"`
}

func (n *LinuxNetwork) tables(ctx context.Context) (map[string]bool, error) {
	data, err := n.Runner.Run(ctx, "nft", []string{"-j", "list", "ruleset"}, "")
	if err != nil {
		return nil, err
	}
	var result struct {
		Objects []struct {
			Table     *nftTable       `json:"table"`
			Flowtable json.RawMessage `json:"flowtable"`
			Rule      *struct {
				Family  string `json:"family"`
				Table   string `json:"table"`
				Chain   string `json:"chain"`
				Comment string `json:"comment"`
			} `json:"rule"`
		} `json:"nftables"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode nftables ruleset: %w", err)
	}
	found := map[string]bool{}
	markers := map[string]bool{}
	for _, object := range result.Objects {
		if r := object.Rule; r != nil && r.Chain == "ownership" && r.Comment == ownerComment {
			markers[r.Family+"/"+r.Table] = true
		}
	}
	for _, object := range result.Objects {
		if len(object.Flowtable) != 0 {
			return nil, errors.New("nftables flow offload is not supported: remove flowtables before enabling the gateway")
		}
		if t := object.Table; t != nil && (t.Name == filterTable || t.Name == natTable) {
			family := "inet"
			if t.Name == natTable {
				family = "ip"
			}
			if t.Family != family || t.Comment != ownerComment && !markers[t.Family+"/"+t.Name] {
				return nil, fmt.Errorf("firewall table %s is not owned by this gateway", t.Name)
			}
			found[t.Name] = true
		}
	}
	return found, nil
}

func guardScript(configs ...Config) string {
	var b strings.Builder
	// nft 1.0.6 omits table comments from JSON, but retains rule comments.
	fmt.Fprintf(&b, "table inet %s {\n comment \"%s\"\n chain ownership { counter comment \"%s\"; }\n set active { type ifname; flags timeout; }\n", filterTable, ownerComment, ownerComment)
	for i, c := range configs {
		fmt.Fprintf(&b, ` set sources%d { type ipv4_addr; flags interval; elements = { %s }; }
 chain input%d { type filter hook input priority -50; policy accept; iifname "%s" drop; }
 chain output%d {
  type filter hook output priority -50; policy accept;
  oifname "%s" icmp type redirect drop
  oifname "%s" icmpv6 type nd-redirect drop
 }
 chain forward%d {
  type filter hook forward priority -50; policy accept;
  iifname "%s" meta nfproto ipv6 drop
  oifname "%s" meta nfproto ipv6 drop
  iifname "%s" ip saddr @sources%d oifname "%s" oifname @active ct state { new, established, related } accept
  iifname "%s" ip saddr @sources%d drop
  iifname "%s" oifname "%s" ip daddr @sources%d iifname @active ct state { established, related } accept
  iifname "%s" drop
  oifname "%s" drop
 }
`, i, strings.Join(c.ProtectedSources, ", "), i, c.TunnelInterface, i, c.IngressInterface, c.IngressInterface, i,
			c.IngressInterface, c.TunnelInterface, c.IngressInterface, i, c.TunnelInterface,
			c.IngressInterface, i, c.TunnelInterface, c.IngressInterface, i, c.TunnelInterface, c.TunnelInterface)
	}
	b.WriteString("}\n")
	return b.String()
}

func natScript(c Config, address string, mtu int) string {
	rule, mss := "", ""
	if address != "" {
		rule = fmt.Sprintf(`iifname "%s" ip saddr { %s } oifname "%s" snat to %s;`,
			c.IngressInterface, strings.Join(c.ProtectedSources, ", "), c.TunnelInterface, address)
		// Clamp both SYN directions; conntrack/NAT translate replies after prerouting.
		mss = fmt.Sprintf(`iifname "%s" ip saddr { %s } oifname "%s" tcp flags & (syn | rst) == syn tcp option maxseg size > %d tcp option maxseg size set %d
  iifname "%s" oifname "%s" ip daddr { %s } tcp flags & (syn | rst) == syn tcp option maxseg size > %d tcp option maxseg size set %d`,
			c.IngressInterface, strings.Join(c.ProtectedSources, ", "), c.TunnelInterface, mtu-40, mtu-40,
			c.TunnelInterface, c.IngressInterface, strings.Join(c.ProtectedSources, ", "), mtu-40, mtu-40)
	}
	return fmt.Sprintf(`table ip %s {
 comment "%s"
 chain ownership { counter comment "%s"; }
 chain postrouting { type nat hook postrouting priority 100; policy accept; %s }
 chain mss { type filter hook forward priority -150; policy accept;
  %s
 }
}
`, natTable, ownerComment, ownerComment, rule, mss)
}

func (n *LinuxNetwork) Protect(ctx context.Context) error {
	return n.ProtectConfigs(ctx, n.Config)
}

func (n *LinuxNetwork) ProtectConfigs(ctx context.Context, configs ...Config) error {
	for _, c := range configs {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	tables, err := n.tables(ctx)
	if err != nil {
		return err
	}
	var script strings.Builder
	if tables[filterTable] {
		fmt.Fprintf(&script, "delete table inet %s\n", filterTable)
	}
	if tables[natTable] {
		fmt.Fprintf(&script, "delete table ip %s\n", natTable)
	}
	script.WriteString(guardScript(configs...))
	script.WriteString(natScript(n.Config, "", 0))
	return n.nft(ctx, script.String())
}

type linkInfo struct {
	Name      string `json:"ifname"`
	Addresses []struct {
		Family string `json:"family"`
		Local  string `json:"local"`
		Bits   int    `json:"prefixlen"`
	} `json:"addr_info"`
}

func (n *LinuxNetwork) Check(ctx context.Context, p *profile.Profile) error {
	if err := n.Config.ValidateProfile(p); err != nil {
		return err
	}
	data, err := n.Runner.Run(ctx, "ip", []string{"-j", "address", "show"}, "")
	if err != nil {
		return err
	}
	var links []linkInfo
	if err = json.Unmarshal(data, &links); err != nil {
		return fmt.Errorf("decode interfaces: %w", err)
	}
	var attached []netip.Prefix
	for _, link := range links {
		if link.Name == n.Config.TunnelInterface {
			return errors.New("the configured tunnel interface already exists; it will not be taken over")
		}
		for _, address := range link.Addresses {
			if address.Family != "inet" {
				continue
			}
			prefix, err := netip.ParsePrefix(fmt.Sprintf("%s/%d", address.Local, address.Bits))
			if err != nil {
				return errors.New("invalid IPv4 interface information")
			}
			if prefix.Contains(p.Addresses[0].Addr()) {
				return errors.New("the VPN address overlaps an existing host network")
			}
			if link.Name == n.Config.IngressInterface {
				attached = append(attached, prefix.Masked())
			}
		}
	}
	for _, source := range n.Config.ProtectedSources {
		selected := netip.MustParsePrefix(source)
		onLink := false
		for _, prefix := range attached {
			onLink = onLink || prefix.Bits() <= selected.Bits() && prefix.Contains(selected.Addr())
		}
		if !onLink {
			return fmt.Errorf("%s is not on the ingress interface's IPv4 network; routed downstream subnets without router NAT are not supported", source)
		}
	}
	_, _, err = n.ownedRouting(ctx)
	return err
}

type routeInfo struct {
	Table    json.RawMessage `json:"table"`
	Protocol json.RawMessage `json:"protocol"`
}

type ruleInfo struct {
	Priority int             `json:"priority"`
	Protocol json.RawMessage `json:"protocol"`
}

func numeric(value json.RawMessage, expected int) bool {
	return strings.Trim(string(value), `"`) == strconv.Itoa(expected)
}

func (n *LinuxNetwork) ownedRouting(ctx context.Context) ([]ruleInfo, bool, error) {
	data, err := n.Runner.Run(ctx, "ip", []string{"-j", "-4", "rule", "show"}, "")
	if err != nil {
		return nil, false, err
	}
	var allRules, owned []ruleInfo
	if err = json.Unmarshal(data, &allRules); err != nil {
		return nil, false, err
	}
	for _, rule := range allRules {
		if rule.Priority >= rulePriority && rule.Priority < rulePriority+128 {
			if !numeric(rule.Protocol, routeProtocol) {
				return nil, false, fmt.Errorf("policy rule priority %d is already in use", rule.Priority)
			}
			owned = append(owned, rule)
		}
	}
	data, err = n.Runner.Run(ctx, "ip", []string{"-j", "-4", "route", "show", "table", "all"}, "")
	if err != nil {
		return nil, false, err
	}
	var routes []routeInfo
	if err = json.Unmarshal(data, &routes); err != nil {
		return nil, false, err
	}
	exists := false
	for _, route := range routes {
		if numeric(route.Table, routeTable) {
			exists = true
			if !numeric(route.Protocol, routeProtocol) {
				return nil, false, errors.New("routing table 21110 is already in use")
			}
		}
	}
	return owned, exists, nil
}

func (n *LinuxNetwork) Configure(ctx context.Context, p *profile.Profile) error {
	if err := n.Deny(ctx); err != nil {
		return err
	}
	rules, _, err := n.ownedRouting(ctx)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if err = n.command(ctx, "ip", "-4", "rule", "del", "pref", strconv.Itoa(rule.Priority), "protocol", strconv.Itoa(routeProtocol)); err != nil {
			return err
		}
	}
	table, protocol := strconv.Itoa(routeTable), strconv.Itoa(routeProtocol)
	commands := [][]string{
		{"-4", "route", "replace", "unreachable", "default", "table", table, "proto", protocol, "metric", "32760"},
		{"-4", "address", "add", p.Addresses[0].String(), "dev", n.Config.TunnelInterface},
		{"link", "set", "dev", n.Config.TunnelInterface, "mtu", strconv.Itoa(p.MTU), "up"},
		{"-4", "route", "replace", "default", "dev", n.Config.TunnelInterface, "table", table, "proto", protocol, "metric", "10"},
	}
	for _, args := range commands {
		if err = n.command(ctx, "ip", args...); err != nil {
			return err
		}
	}
	for i, source := range n.Config.ProtectedSources {
		for _, terminal := range []bool{true, false} {
			priority, action := rulePriority+i, []string{"lookup", table}
			if terminal {
				priority, action = rulePriority+64+i, []string{"prohibit"}
			}
			args := []string{"-4", "rule", "add", "pref", strconv.Itoa(priority), "from", source, "iif", n.Config.IngressInterface, "protocol", protocol}
			if err = n.command(ctx, "ip", append(args, action...)...); err != nil {
				return err
			}
		}
	}
	if err = n.nft(ctx, "delete table ip "+natTable+"\n"+natScript(n.Config, p.Addresses[0].Addr().String(), p.MTU)); err != nil {
		return err
	}
	// ip_forward resets other IPv4 defaults, so set the interface controls afterward.
	for _, setting := range []string{
		"net.ipv4.ip_forward=1",
		"net.ipv4.conf.all.send_redirects=0",
		"net.ipv4.conf." + n.Config.IngressInterface + ".send_redirects=0",
		"net.ipv4.conf." + n.Config.IngressInterface + ".accept_redirects=0",
		"net.ipv4.conf." + n.Config.IngressInterface + ".rp_filter=2",
		"net.ipv4.conf." + n.Config.TunnelInterface + ".rp_filter=2",
	} {
		if err = n.command(ctx, "sysctl", "-q", "-w", setting); err != nil {
			return err
		}
	}
	return nil
}

func (n *LinuxNetwork) Permit(ctx context.Context) error {
	return n.nft(ctx, fmt.Sprintf("flush set inet %s active\nadd element inet %s active { \"%s\" timeout %ds }\n",
		filterTable, filterTable, n.Config.TunnelInterface, guardLifetime))
}

func (n *LinuxNetwork) Deny(ctx context.Context) error {
	return n.nft(ctx, "flush set inet "+filterTable+" active\n")
}

func (n *LinuxNetwork) Unprotect(ctx context.Context) error {
	tables, err := n.tables(ctx)
	if err != nil {
		return err
	}
	if err = n.ClearRouting(ctx); err != nil {
		return err
	}
	var script strings.Builder
	if tables[natTable] {
		fmt.Fprintf(&script, "delete table ip %s\n", natTable)
	}
	if tables[filterTable] {
		fmt.Fprintf(&script, "delete table inet %s\n", filterTable)
	}
	if script.Len() == 0 {
		return nil
	}
	return n.nft(ctx, script.String())
}

func (n *LinuxNetwork) ClearRouting(ctx context.Context) error {
	rules, exists, err := n.ownedRouting(ctx)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if err = n.command(ctx, "ip", "-4", "rule", "del", "pref", strconv.Itoa(rule.Priority), "protocol", strconv.Itoa(routeProtocol)); err != nil {
			return err
		}
	}
	if exists {
		if err = n.command(ctx, "ip", "-4", "route", "flush", "table", strconv.Itoa(routeTable), "proto", strconv.Itoa(routeProtocol)); err != nil {
			return err
		}
	}
	return nil
}

func (n *LinuxNetwork) EmergencyGuard(ctx context.Context) error {
	tables, err := n.tables(ctx)
	if err != nil {
		return err
	}
	script := ""
	if tables[filterTable] {
		script = "delete table inet " + filterTable + "\n"
	}
	return n.nft(ctx, script+fmt.Sprintf(`table inet %s {
 comment "%s"
 chain ownership { counter comment "%s"; }
 chain forward { type filter hook forward priority -50; policy drop; }
}
`, filterTable, ownerComment, ownerComment))
}
