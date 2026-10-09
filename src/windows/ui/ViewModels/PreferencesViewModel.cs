using System.ComponentModel;
using System.Text.Json;
using Avalonia.Styling;
using OuternetAWG.Services;

namespace OuternetAWG.ViewModels;

public sealed class PreferenceChoice(string id, string label) : INotifyPropertyChanged
{
    public string Id { get; } = id;
    public string Label { get; private set; } = label;
    public event PropertyChangedEventHandler? PropertyChanged;
    internal void Relabel(string label)
    {
        Label = label;
        PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(nameof(Label)));
    }
}

public sealed class PreferencesViewModel : INotifyPropertyChanged, IDisposable
{
    private readonly IPreferencesStore? _store;
    private readonly Action<ThemeVariant>? _applyTheme;
    private UserPreferences _preferences = new();
    private Exception? _error;
    private string _errorKey = "Error.SettingsLoad";
    public LocalizationService Text { get; }
    public IReadOnlyList<PreferenceChoice> Themes { get; }
    public IReadOnlyList<PreferenceChoice> Languages { get; }
    public bool HasError => _error is not null;
    public string Error => _error is null ? "" : Text.Format(_errorKey, _error);
    public event PropertyChangedEventHandler? PropertyChanged;

    public PreferencesViewModel(IPreferencesStore? store = null, Action<ThemeVariant>? applyTheme = null,
        Func<string>? systemLanguage = null)
    {
        _store = store;
        _applyTheme = applyTheme;
        Text = new LocalizationService(systemLanguage);
        try { _preferences = store?.Load() ?? new(); }
        catch (Exception ex) when (IsStorageError(ex)) { _error = ex; }
        Text.SetLanguage(_preferences.Language);
        Themes = new[] { "system", "light", "dark" }
            .Select(id => new PreferenceChoice(id, Text["Theme." + id])).ToArray();
        Languages = new[] { new PreferenceChoice("system", Text["Language.System"]) }
            .Concat(LocalizationService.Languages.Select(id => new PreferenceChoice(id, LocalizationService.NativeName(id)))).ToArray();
        Text.PropertyChanged += LanguageChanged;
        ApplyTheme();
    }

    public PreferenceChoice SelectedTheme
    {
        get => Themes.Single(x => x.Id == _preferences.Theme);
        set
        {
            if (value is null || !Themes.Contains(value) || value.Id == _preferences.Theme) return;
            _preferences = _preferences with { Theme = value.Id };
            ApplyTheme();
            Save();
        }
    }

    public PreferenceChoice SelectedLanguage
    {
        get => Languages.Single(x => x.Id == _preferences.Language);
        set
        {
            if (value is null || !Languages.Contains(value) || value.Id == _preferences.Language) return;
            _preferences = _preferences with { Language = value.Id };
            Text.SetLanguage(value.Id);
            Save();
        }
    }

    private void ApplyTheme() => _applyTheme?.Invoke(_preferences.Theme switch
    {
        "light" => ThemeVariant.Light,
        "dark" => ThemeVariant.Dark,
        _ => ThemeVariant.Default
    });

    private void LanguageChanged(object? sender, PropertyChangedEventArgs e)
    {
        foreach (var theme in Themes) theme.Relabel(Text["Theme." + theme.Id]);
        Languages[0].Relabel(Text["Language.System"]);
        NotifyAll();
    }

    private void Save()
    {
        try { _store?.Save(_preferences); _error = null; }
        catch (Exception ex) when (IsStorageError(ex)) { _errorKey = "Error.SettingsSave"; _error = ex; }
        NotifyAll();
    }

    private static bool IsStorageError(Exception ex) =>
        ex is IOException or InvalidDataException or UnauthorizedAccessException or JsonException;
    private void NotifyAll() => PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(string.Empty));
    public void Dispose() => Text.PropertyChanged -= LanguageChanged;
}
