using Avalonia;
using Avalonia.Controls;
using Avalonia.Headless;
using Avalonia.Headless.XUnit;
using Avalonia.Interactivity;
using Avalonia.Styling;
using Avalonia.Threading;
using Avalonia.VisualTree;
using OuternetAWG;
using OuternetAWG.Services;
using OuternetAWG.ViewModels;
using Xunit;

public sealed class RoutingTests
{
    [Fact]
    public void RulesNormalizeAddressesAndKeepExplicitActions()
    {
        var rules = RoutingRules.Parse("\r\n 192.168.0.0/16 Direct\n10.110.0.2 VPN\nfd00::/8 direct\n\n");
        Assert.Equal(new[] { new RouteRule("192.168.0.0/16", "direct"), new RouteRule("10.110.0.2/32", "vpn"),
            new RouteRule("fd00::/8", "direct") }, rules);
        Assert.Equal(rules, RoutingRules.Parse(RoutingRules.Format(rules)));
    }

    [Theory]
    [InlineData("192.168.0.1/16 direct")]
    [InlineData("example.com vpn")]
    [InlineData("1.1.1.1 proxy")]
    [InlineData("1.1.1.1")]
    [InlineData("1.1.1.1 vpn\n1.1.1.1/32 direct")]
    [InlineData("fe80::1%0 vpn")]
    [InlineData("::ffff:192.168.0.1 vpn")]
    [InlineData("127.1 direct")]
    [InlineData("1.1.1.1/33 vpn")]
    [InlineData("fd00::1/8 vpn")]
    public void InvalidRulesHaveActionableErrors(string text) =>
        Assert.Throws<ClientException>(() => RoutingRules.Parse(text));

    [Fact]
    public async Task ProfilesCreateEditSelectDeleteAndSurviveNewUI()
    {
        var service = new FakeService();
        using var model = new MainViewModel(service, TestApp.Preferences());
        await model.InitializeAsync();
        Assert.Equal(new[] { "vpn", "direct" }, model.Routing.Profiles.Select(x => x.Id));
        Assert.False(model.Routing.CanEdit);
        Assert.False(model.Routing.HasNotice);
        Assert.Empty(model.Routing.Notice);
        using (var editor = new RouteEditorViewModel(model, true))
        {
            editor.Name = "Home LAN";
            editor.Rules = "192.168.0.0/16 direct";
            Assert.True(await editor.SaveAsync());
        }
        Assert.True(model.Routing.CanEdit);
        var id = model.Routing.Selected!.Id;
        using (var editor = new RouteEditorViewModel(model, false))
        {
            Assert.Equal("Home LAN", editor.Name);
            editor.Base = editor.Bases.Single(x => x.Id == "direct");
            editor.Rules = "10.110.0.0/16 vpn";
            Assert.True(await editor.SaveAsync());
        }
        using (var reopened = new MainViewModel(service, TestApp.Preferences()))
        {
            await reopened.InitializeAsync();
            Assert.Equal(id, reopened.Routing.Selected!.Id);
            Assert.Equal("direct", reopened.Routing.Current.Base);
            Assert.Equal("10.110.0.0/16", reopened.Routing.Current.Rules.Single().Destination);
        }
        await model.Routing.SelectAsync("vpn");
        Assert.False(model.Routing.CanEdit);
        await model.Routing.SelectAsync(id);
        await model.Routing.DeleteAsync();
        Assert.Equal("vpn", model.Routing.Selected!.Id);
        Assert.Equal(2, model.Routing.Profiles.Count);
        Assert.DoesNotContain("connect", service.Commands);
    }

    [Fact]
    public async Task FailedPersistenceKeepsSelectionAndEditorDraft()
    {
        var service = new FakeService();
        using var model = new MainViewModel(service, TestApp.Preferences());
        await model.InitializeAsync();
        service.Error = new ServiceException("Disk full");
        await model.Routing.SelectAsync("direct");
        Assert.Equal("vpn", model.Routing.Selected!.Id);
        Assert.Contains("Disk full", model.Error);
        using var editor = new RouteEditorViewModel(model, true) { Name = "Keep me", Rules = "192.168.0.0/16 direct" };
        Assert.False(await editor.SaveAsync());
        Assert.Equal("Keep me", editor.Name);
        Assert.True(editor.HasError);
        service.Error = null;
        Assert.True(await editor.SaveAsync());
    }

    [Fact]
    public async Task ActiveTunnelLocksAllRoutingChanges()
    {
        var service = new FakeService();
        using var model = new MainViewModel(service, TestApp.Preferences());
        await model.InitializeAsync();
        using var editor = new RouteEditorViewModel(model, true) { Name = "Draft" };
        await model.ToggleAsync();
        Assert.False(model.Routing.CanChange);
        Assert.False(editor.CanSave);
        Assert.True(model.Routing.HasNotice);
        Assert.Equal(model.Text["Routing.DisconnectFirst"], model.Routing.Notice);
        await model.Routing.SelectAsync("direct");
        Assert.False(await editor.SaveAsync());
        Assert.DoesNotContain("save-routing", service.Commands);
        Assert.Equal("vpn", model.Routing.Selected!.Id);
    }

    [Fact]
    public async Task OlderServiceDoesNotOfferNonfunctionalRoutingControls()
    {
        var service = new FakeService();
        service.Status = service.Status with { Routing = null };
        using var model = new MainViewModel(service, TestApp.Preferences());
        await model.InitializeAsync();
        Assert.False(model.Routing.CanChange);
        Assert.True(model.Routing.HasNotice);
        Assert.Contains("matching Windows service", model.Routing.Notice);
    }

    [AvaloniaFact]
    public async Task DropdownSavesExactlyOnceAndRestoresAfterRejection()
    {
        var service = new FakeService();
        var window = new MainWindow(service, TestApp.Preferences());
        window.Show();
        try
        {
            window.Model.Navigate(ClientPage.Settings);
            var selector = window.FindControl<ComboBox>("RoutingProfileSelector")!;
            selector.SelectedItem = window.Model.Routing.Profiles.Single(x => x.Id == "direct");
            Assert.Equal("direct", window.Model.Routing.Selected!.Id);
            Assert.Equal(1, service.Commands.Count(x => x == "save-routing"));
            await window.Model.RefreshAsync();
            Assert.Equal(1, service.Commands.Count(x => x == "save-routing"));
            service.Error = new ServiceException("Synthetic rejection");
            selector.SelectedItem = window.Model.Routing.Profiles.Single(x => x.Id == "vpn");
            Dispatcher.UIThread.RunJobs();
            Assert.Equal("direct", Assert.IsType<PreferenceChoice>(selector.SelectedItem).Id);
            Assert.True(window.Model.HasError);
        }
        finally { window.CloseForShutdown(); }
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
    public async Task EditorFitsInBothThemesAndValidatesWithoutNetworking(string language)
    {
        var service = new FakeService();
        var original = Application.Current!.RequestedThemeVariant;
        var preferences = TestApp.Preferences(theme => Application.Current!.RequestedThemeVariant = theme);
        var owner = new MainWindow(service, preferences) { Width = 760, Height = 580 };
        owner.Show();
        preferences.SelectedLanguage = preferences.Languages.Single(x => x.Id == language);
        var editor = new RouteProfileWindow(owner.Model, true) { Width = 460, Height = 510 };
        editor.Show(owner);
        try
        {
            var vm = Assert.IsType<RouteEditorViewModel>(editor.DataContext);
            vm.Name = "Home LAN";
            vm.Rules = "192.168.0.1/16 direct";
            Assert.False(await vm.SaveAsync());
            Assert.True(vm.HasError);
            Assert.DoesNotContain("save-routing", service.Commands);
            foreach (var theme in new[] { "light", "dark" })
            {
                preferences.SelectedTheme = preferences.Themes.Single(x => x.Id == theme);
                editor.UpdateLayout();
                var root = Assert.IsType<Grid>(editor.Content);
                foreach (var name in new[] { "RouteSaveButton", "RouteCancelButton" })
                    ClientTests.AssertContained(editor.FindControl<Button>(name)!, root);
                ClientTests.AssertContained(editor.GetVisualDescendants().OfType<ScrollViewer>().First(), root);
                var save = editor.FindControl<Button>("RouteSaveButton")!;
                foreach (var text in save.GetVisualDescendants().OfType<TextBlock>()) ClientTests.AssertContained(text, save);
                using var frame = editor.CaptureRenderedFrame();
                frame!.Save(Path.Combine(AppContext.BaseDirectory, $"route-editor-{language}-{theme}.png"));
            }
            vm.Rules = "192.168.0.0/16 direct";
            Assert.True(await vm.SaveAsync());
            Assert.False(vm.HasError);
            Assert.Equal("Home LAN", owner.Model.Routing.Selected!.Label);
        }
        finally { editor.Close(); owner.CloseForShutdown(); Application.Current!.RequestedThemeVariant = original; }
    }
}
