using System.Net;
using System.Net.Sockets;

namespace OuternetAWG.Services;

public sealed record RouteRule(string Destination, string Action);
public sealed record RoutingProfile(string Id, string Name, string Base, RouteRule[] Rules);
public sealed record RoutingState(int Version, string Selected, RoutingProfile[] Profiles)
{
    public static RoutingState Default() => new(1, "vpn", []);
}

public static class RoutingRules
{
    public static RouteRule[] Parse(string text)
    {
        var rules = new List<RouteRule>();
        var destinations = new HashSet<string>(StringComparer.Ordinal);
        var lines = text.Replace("\r", "").Split('\n');
        for (var i = 0; i < lines.Length; i++)
        {
            if (string.IsNullOrWhiteSpace(lines[i])) continue;
            var parts = lines[i].Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries);
            if (parts.Length != 2 || parts[1].ToLowerInvariant() is not ("vpn" or "direct"))
                throw new ClientException("Routing.InvalidRule", null, i + 1);
            var cidr = parts[0].Split('/');
            if (cidr.Length > 2 || cidr[0].Contains('%') || !IPAddress.TryParse(cidr[0], out var ip) || ip.IsIPv4MappedToIPv6 ||
                ip.AddressFamily == AddressFamily.InterNetworkV6 && ip.ScopeId != 0 ||
                ip.AddressFamily == AddressFamily.InterNetwork && cidr[0] != ip.ToString())
                throw new ClientException("Routing.InvalidRule", null, i + 1);
            var bytes = ip.GetAddressBytes();
            var bits = bytes.Length * 8;
            if (cidr.Length == 2 && (!int.TryParse(cidr[1], System.Globalization.NumberStyles.None,
                    System.Globalization.CultureInfo.InvariantCulture, out bits) || bits < 0 || bits > bytes.Length * 8))
                throw new ClientException("Routing.InvalidRule", null, i + 1);
            for (var bit = bits; bit < bytes.Length * 8; bit++)
                if ((bytes[bit / 8] & (1 << (7 - bit % 8))) != 0)
                    throw new ClientException("Routing.InvalidRule", null, i + 1);
            var destination = $"{ip}/{bits}";
            if (!destinations.Add(destination)) throw new ClientException("Routing.DuplicateRule", null, i + 1);
            rules.Add(new(destination, parts[1].ToLowerInvariant()));
        }
        if (rules.Count > 64) throw new ClientException("Routing.RuleLimit");
        return rules.ToArray();
    }

    public static string Format(IEnumerable<RouteRule> rules) =>
        string.Join(Environment.NewLine, rules.Select(x => $"{x.Destination} {x.Action}"));
}
