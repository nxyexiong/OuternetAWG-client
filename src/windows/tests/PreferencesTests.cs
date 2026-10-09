using System.Globalization;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;
using Avalonia;
using Avalonia.Controls;
using Avalonia.Headless;
using Avalonia.Headless.XUnit;
using Avalonia.Interactivity;
using Avalonia.Media;
using Avalonia.Styling;
using Avalonia.VisualTree;
using OuternetAWG;
using OuternetAWG.Services;
using OuternetAWG.ViewModels;
using Xunit;

public sealed class PreferencesTests
{
    [Fact]
    public void CatalogsHaveMatchingKeysAndFormatArguments()
    {
        Assert.Equal(8, LocalizationService.Languages.Count);
        var english = LocalizationService.Catalog("en");
        foreach (var language in LocalizationService.Languages)
        {
            var catalog = LocalizationService.Catalog(language);
            Assert.Equal(english.Keys.Order(), catalog.Keys.Order());
            foreach (var (key, value) in catalog)
            {
                Assert.False(string.IsNullOrWhiteSpace(value), $"{language}: {key}");
                Assert.Equal(Placeholders(english[key]), Placeholders(value));
                Assert.Equal(CompositeFormat.Parse(english[key]).MinimumArgumentCount,
                    CompositeFormat.Parse(value).MinimumArgumentCount);
                _ = string.Format(CultureInfo.GetCultureInfo(language), value, 7, 9);
            }
        }
    }

    private static string[] Placeholders(string value) =>
        Regex.Matches(value, @"\{\d+\}").Select(x => x.Value).Order().ToArray();

    [Theory]
    [InlineData("en-GB", "en")]
    [InlineData("fr-CA", "fr")]
    [InlineData("de-AT", "de")]
    [InlineData("ru-RU", "ru")]
    [InlineData("zh-CN", "zh-Hans")]
    [InlineData("zh-SG", "zh-Hans")]
    [InlineData("zh-Hans-HK", "zh-Hans")]
    [InlineData("zh-TW", "zh-Hant")]
    [InlineData("zh-HK", "zh-Hant")]
    [InlineData("zh-MO", "zh-Hant")]
    [InlineData("zh-Hant", "zh-Hant")]
    [InlineData("ja-JP", "ja")]
    [InlineData("ko-KR", "ko")]
    [InlineData("es-ES", "en")]
    public void SystemLanguageUsesSupportedRegionalFallback(string system, string expected) =>
        Assert.Equal(expected, new LocalizationService(() => system).EffectiveLanguage);

    [Fact]
    public void SystemLanguageRefreshesButExplicitLanguageStaysFixed()
    {
        var system = "fr-FR";
        using var preferences = new PreferencesViewModel(systemLanguage: () => system);
        Assert.Equal("fr", preferences.Text.EffectiveLanguage);
        var changes = 0;
        preferences.Text.PropertyChanged += (_, _) => changes++;
        system = "de-DE";
        preferences.Text.RefreshSystemLanguage();
        Assert.Equal("de", preferences.Text.EffectiveLanguage);
        Assert.Equal(preferences.Text["Theme.system"], preferences.Themes[0].Label);
        preferences.SelectedLanguage = preferences.Languages.Single(x => x.Id == "ja");
        system = "ko-KR";
        preferences.Text.RefreshSystemLanguage();
        Assert.Equal("ja", preferences.Text.EffectiveLanguage);
        preferences.SelectedLanguage = preferences.Languages[0];
        Assert.Equal("ko", preferences.Text.EffectiveLanguage);
        Assert.Equal(3, changes);
        Assert.Equal("system", preferences.SelectedLanguage.Id);
    }

    [Fact]
    public void PreferencesPersistWithoutVpnDataAndSurviveRestart()
    {
        var directory = Path.Combine(AppContext.BaseDirectory, "preferences-" + Guid.NewGuid().ToString("N"));
        var path = Path.Combine(directory, "settings.json");
        try
        {
            var store = new PreferencesStore(path);
            Assert.Equal(new UserPreferences(), store.Load());
            using (var preferences = new PreferencesViewModel(store, systemLanguage: () => "en"))
            {
                preferences.SelectedTheme = preferences.Themes.Single(x => x.Id == "dark");
                preferences.SelectedLanguage = preferences.Languages.Single(x => x.Id == "zh-Hant");
                Assert.False(preferences.HasError);
            }
            var applied = ThemeVariant.Default;
            using (var restarted = new PreferencesViewModel(store, theme => applied = theme, () => "en"))
            {
                Assert.Equal("dark", restarted.SelectedTheme.Id);
                Assert.Equal(ThemeVariant.Dark, applied);
                Assert.Equal("zh-Hant", restarted.Text.EffectiveLanguage);
                restarted.SelectedTheme = restarted.Themes[0];
                restarted.SelectedLanguage = restarted.Languages[0];
            }
            Assert.Equal(new UserPreferences(), store.Load());
            using var json = JsonDocument.Parse(File.ReadAllText(path));
            Assert.Equal(new[] { "language", "theme" }, json.RootElement.EnumerateObject().Select(x => x.Name).Order());
            Assert.Equal(new[] { path }, Directory.GetFiles(directory));
        }
        finally
        {
            if (File.Exists(path)) File.Delete(path);
            if (Directory.Exists(directory)) Directory.Delete(directory);
        }
    }

    [Theory]
    [InlineData("{")]
    [InlineData("null")]
    [InlineData("{\"theme\":\"invalid\"}")]
    [InlineData("{\"language\":\"es\"}")]
    [InlineData("{\"language\":null}")]
    public void InvalidPreferencesReportAnErrorWithoutOverwritingTheFile(string content)
    {
        var path = Path.Combine(AppContext.BaseDirectory, $"invalid-settings-{Guid.NewGuid():N}.json");
        try
        {
            File.WriteAllText(path, content);
            using var preferences = new PreferencesViewModel(new PreferencesStore(path), systemLanguage: () => "en");
            Assert.True(preferences.HasError);
            Assert.Contains("Could not load preferences", preferences.Error);
            Assert.Equal("system", preferences.SelectedLanguage.Id);
            Assert.Equal(content, File.ReadAllText(path));
            preferences.SelectedTheme = preferences.Themes.Single(x => x.Id == "dark");
            Assert.False(preferences.HasError);
            Assert.Equal("dark", new PreferencesStore(path).Load().Theme);
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void SaveFailureIsVisibleAndSelectionsStillApply()
    {
        var store = new MemoryPreferencesStore { SaveError = new UnauthorizedAccessException("test access denied") };
        var applied = ThemeVariant.Default;
        using var preferences = new PreferencesViewModel(store, theme => applied = theme, () => "en");
        preferences.SelectedTheme = preferences.Themes.Single(x => x.Id == "dark");
        Assert.Equal(ThemeVariant.Dark, applied);
        Assert.True(preferences.HasError);
        Assert.Contains("could not be saved", preferences.Error);
        preferences.SelectedLanguage = preferences.Languages.Single(x => x.Id == "fr");
        Assert.Contains("test access denied", preferences.Error);
        Assert.StartsWith("Votre", preferences.Error);
        store.SaveError = null;
        preferences.SelectedTheme = preferences.Themes[0];
        Assert.False(preferences.HasError);
        Assert.Equal(new UserPreferences("system", "fr"), store.Saved);
    }

    [Fact]
    public async Task ChangingLanguageRetranslatesExistingErrorsAndPreservesDiagnostics()
    {
        using var preferences = TestApp.Preferences();
        var service = new FakeService { Error = new ClientException("Error.Cleanup", null, 1066, 4) };
        using var model = new MainViewModel(service, preferences);
        await model.RefreshAsync();
        Assert.Contains("(1066/4)", model.Error);
        preferences.SelectedLanguage = preferences.Languages.Single(x => x.Id == "fr");
        Assert.StartsWith("Le service", model.Error);
        Assert.Contains("(1066/4)", model.Error);
        model.ShowError(new ServiceException("WFP diagnostic 123"));
        Assert.Contains("WFP diagnostic 123", model.Error);
        Assert.StartsWith("Diagnostic du service", model.Error);
    }

    [AvaloniaTheory]
    [InlineData("en")]
    [InlineData("fr")]
    [InlineData("de")]
    [InlineData("ru")]
    [InlineData("zh-Hans")]
    [InlineData("zh-Hant")]
    [InlineData("ja")]
    [InlineData("ko")]
    public async Task AllLanguagesSwitchLiveInBothThemesWithoutTouchingTheTunnel(string language)
    {
        var app = Application.Current!;
        var originalTheme = app.RequestedThemeVariant;
        var service = new FakeService();
        var preferences = TestApp.Preferences(theme => app.RequestedThemeVariant = theme);
        var window = new MainWindow(service, preferences) { Width = 760, Height = 580 };
        using var tray = new TrayController(window, () => { });
        try
        {
            window.Show();
            window.FindControl<Button>("SettingsNavButton")!.RaiseEvent(new RoutedEventArgs(Button.ClickEvent));
            Assert.True(window.Model.IsSettingsPage);
            Assert.False(window.Model.IsConnectionPage);
            Assert.False(window.Model.IsConfigurationPage);
            var languageSelector = window.FindControl<ComboBox>("LanguageSelector")!;
            languageSelector.SelectedItem = preferences.Languages.Single(x => x.Id == language);
            Assert.Equal(language, window.Model.Text.EffectiveLanguage);
            Assert.Equal(new[] { "English", "Français", "Deutsch", "Русский", "简体中文", "繁體中文", "日本語", "한국어" },
                preferences.Languages.Skip(1).Select(x => x.Label));
            Assert.Equal(preferences.Text["Nav.Connection"], window.FindControl<TextBlock>("ConnectionNavLabel")!.Text);
            Assert.Equal(preferences.Text["Window.Hide"], ToolTip.GetTip(window.FindControl<Button>("CloseButton")!));
            Assert.Equal(preferences.Text["Tray.Open"], ((NativeMenuItem)tray.Menu.Items[0]).Header);
            Assert.Equal(preferences.Text["Tray.Quit"], ((NativeMenuItem)tray.Menu.Items[3]).Header);
            window.UpdateLayout();
            foreach (var button in window.GetVisualDescendants().OfType<Button>().Where(x => x.Classes.Contains("nav")))
                foreach (var label in button.GetVisualDescendants().OfType<TextBlock>())
                    ClientTests.AssertContained(label, button);
            foreach (var theme in new[] { "light", "dark" })
            {
                window.FindControl<ComboBox>("ThemeSelector")!.SelectedItem = preferences.Themes.Single(x => x.Id == theme);
                Assert.Equal(theme == "light" ? ThemeVariant.Light : ThemeVariant.Dark, window.ActualThemeVariant);
                var expectedBackground = Color.Parse(theme == "light" ? "#F8F7FA" : "#1B1921");
                Assert.Equal(expectedBackground, ((ISolidColorBrush)window.FindControl<Border>("WindowSurface")!.Background!).Color);
                window.Model.Navigate(ClientPage.Settings);
                window.UpdateLayout();
                var settings = window.FindControl<Border>("SettingsPage")!;
                ClientTests.AssertContained(settings, window.FindControl<Grid>("ContentPanel")!);
                foreach (var child in settings.GetVisualDescendants().OfType<Control>().Where(x => x.IsEffectivelyVisible))
                    ClientTests.AssertContained(child, settings);
                using (var frame = window.CaptureRenderedFrame())
                    frame!.Save(Path.Combine(AppContext.BaseDirectory, $"settings-{language}-{theme}.png"));
                foreach (var state in new[] { "connected", "connecting", "waiting", "error" })
                {
                    service.Status = service.Status with { State = state, Active = true, Message = "Synthetic diagnostic", Handshake = null };
                    await window.Model.RefreshAsync();
                    window.Model.Navigate(ClientPage.Connection);
                    window.Model.ShowError(new ClientException("Error.ResponseTimeout"));
                    window.UpdateLayout();
                    var card = window.FindControl<Border>("ConnectionCard")!;
                    foreach (var child in card.GetVisualDescendants().OfType<Control>().Where(x => x.IsEffectivelyVisible))
                        ClientTests.AssertContained(child, card);
                    ClientTests.AssertContained(card, window.FindControl<Grid>("ContentPanel")!);
                    ClientTests.AssertConnectionCentered(window);
                    Assert.Equal(preferences.Text["Connection.Disconnect"], window.FindControl<Button>("ConnectButton")!.Content);
                    Assert.Equal(preferences.Text["Connection.Disconnect"], ((NativeMenuItem)tray.Menu.Items[1]).Header);
                }
                window.Model.Navigate(ClientPage.Configuration);
                window.UpdateLayout();
                Assert.True(window.FindControl<ScrollViewer>("ConfigurationScroller")!.IsEffectivelyVisible);
                Assert.Equal(preferences.Text["Configuration.Placeholder"], window.FindControl<TextBox>("ConfigInput")!.Watermark);
            }
            window.FindControl<ComboBox>("ThemeSelector")!.SelectedItem = preferences.Themes[0];
            Assert.Equal(ThemeVariant.Default, app.RequestedThemeVariant);
            Assert.True(service.Status.Active);
            Assert.DoesNotContain("connect", service.Commands);
            Assert.DoesNotContain("disconnect", service.Commands);
            Assert.DoesNotContain("stop-service", service.Commands);
            Assert.Equal(1, service.Commands.Count(x => x == "start-service"));
        }
        finally { window.CloseForShutdown(); app.RequestedThemeVariant = originalTheme; }
    }

    private sealed class MemoryPreferencesStore : IPreferencesStore
    {
        public UserPreferences Saved = new();
        public Exception? SaveError;
        public UserPreferences Load() => Saved;
        public void Save(UserPreferences preferences)
        {
            if (SaveError is not null) throw SaveError;
            Saved = preferences;
        }
    }
}
