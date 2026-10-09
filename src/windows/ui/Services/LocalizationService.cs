using System.ComponentModel;
using System.Globalization;
using System.Runtime.InteropServices;
using System.Text.Json;

namespace OuternetAWG.Services;

public sealed class LocalizationService : INotifyPropertyChanged
{
    public static IReadOnlyList<string> Languages { get; } = Array.AsReadOnly(
        new[] { "en", "fr", "de", "ru", "zh-Hans", "zh-Hant", "ja", "ko" });
    private static readonly IReadOnlyDictionary<string, IReadOnlyDictionary<string, string>> Catalogs = LoadCatalogs();
    private readonly Func<string> _systemLanguage;
    public event PropertyChangedEventHandler? PropertyChanged;
    public string Language { get; private set; } = "system";
    public string EffectiveLanguage { get; private set; } = "en";
    public string this[string key] => Catalogs[EffectiveLanguage][key];

    public LocalizationService(Func<string>? systemLanguage = null)
    {
        _systemLanguage = systemLanguage ?? ReadSystemLanguage;
        SetLanguage("system");
    }

    public void SetLanguage(string language)
    {
        if (language != "system" && !Languages.Contains(language))
            throw new ArgumentException("Unsupported language.", nameof(language));
        Language = language;
        RefreshSystemLanguage();
    }

    public void RefreshSystemLanguage()
    {
        var effective = Language == "system" ? ResolveLanguage(_systemLanguage()) : Language;
        if (effective == EffectiveLanguage) return;
        EffectiveLanguage = effective;
        PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(string.Empty));
    }

    public string Format(string key, params object[] arguments) =>
        string.Format(CultureInfo.GetCultureInfo(EffectiveLanguage), this[key],
            arguments.Select(x => x is Exception error ? Describe(error) : x).ToArray());

    public string Describe(Exception error) => error switch
    {
        ClientException localized => Format(localized.Key, localized.Arguments),
        ServiceException service => service.HasDetails ? Format("Error.ServiceDetails", error.Message) : this["Error.Rejected"],
        _ => error.Message
    };

    public static string NativeName(string language) => Catalogs[language]["Language.NativeName"];
    internal static IReadOnlyDictionary<string, string> Catalog(string language) => Catalogs[language];
    internal static string EnglishFormat(string key, object[] arguments) =>
        string.Format(CultureInfo.InvariantCulture, Catalogs["en"][key],
            arguments.Select(x => x is Exception error ? error.Message : x).ToArray());

    internal static string ResolveLanguage(string culture)
    {
        var parts = culture.ToLowerInvariant().Split('-');
        if (parts[0] == "zh")
        {
            if (parts.Contains("hans")) return "zh-Hans";
            return parts.Any(x => x is "hant" or "tw" or "hk" or "mo") ? "zh-Hant" : "zh-Hans";
        }
        return Languages.Contains(parts[0]) ? parts[0] : "en";
    }

    private static string ReadSystemLanguage() => OperatingSystem.IsWindows()
        ? CultureInfo.GetCultureInfo(GetUserDefaultUILanguage()).Name
        : CultureInfo.CurrentUICulture.Name;

    [DllImport("kernel32.dll")]
    private static extern ushort GetUserDefaultUILanguage();

    private static IReadOnlyDictionary<string, IReadOnlyDictionary<string, string>> LoadCatalogs()
    {
        var catalogs = new Dictionary<string, IReadOnlyDictionary<string, string>>();
        foreach (var language in Languages)
        {
            using var stream = typeof(LocalizationService).Assembly.GetManifestResourceStream(
                $"OuternetAWG.Locales.{language}.json")
                ?? throw new InvalidDataException($"Missing language catalog: {language}");
            var strings = JsonSerializer.Deserialize<Dictionary<string, string>>(stream)
                ?? throw new InvalidDataException($"Empty language catalog: {language}");
            if (strings.Values.Any(string.IsNullOrWhiteSpace) ||
                catalogs.TryGetValue("en", out var english) && !strings.Keys.ToHashSet().SetEquals(english.Keys))
                throw new InvalidDataException($"Incomplete language catalog: {language}");
            catalogs.Add(language, strings);
        }
        return catalogs;
    }
}

public sealed class ClientException(string key, Exception? innerException = null, params object[] arguments)
    : IOException(LocalizationService.EnglishFormat(key, arguments), innerException)
{
    public string Key { get; } = key;
    public object[] Arguments { get; } = arguments;
}
