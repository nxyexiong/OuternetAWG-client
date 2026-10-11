using Avalonia;
using System.Reflection;
using Avalonia.Controls;
using Avalonia.Headless;
using Avalonia.Headless.XUnit;
using Avalonia.Platform;
using Avalonia.Styling;
using Avalonia.VisualTree;
using OuternetAWG;
using OuternetAWG.Services;
using OuternetAWG.ViewModels;
using Xunit;

[assembly: AvaloniaTestApplication(typeof(TestApp))]

public static class TestApp
{
    public static PreferencesViewModel Preferences(Action<ThemeVariant>? applyTheme = null) =>
        new(applyTheme: applyTheme, systemLanguage: () => "en-US");

    public static AppBuilder BuildAvaloniaApp() =>
        AppBuilder.Configure<App>().UseSkia()
            .UseHeadless(new AvaloniaHeadlessPlatformOptions { UseHeadlessDrawing = false });
}

public sealed class FakeService : IServiceClient
{
    public TunnelStatus Status = new("disconnected", false, "Ready to connect.",
        new ProfileSummary("Japan East", "node.example.com:51820", "10.110.0.2/32"), null, RoutingState.Default());
    public Exception? Error;
    public Exception? StopError;
    public List<string> Commands = [];
    public Task<TunnelStatus>? PendingPoll;
    public Task? PendingStop;
    public Action<RoutingState>? PersistRouting;

    public Task<TunnelStatus> SaveRoutingAsync(RoutingState routing, CancellationToken cancellationToken = default)
    {
        Commands.Add("save-routing");
        if (Error is not null) throw Error;
        if (Status.Active) throw new ServiceException("Disconnect before changing routing profiles.");
        PersistRouting?.Invoke(routing);
        Status = Status with { Routing = routing };
        return Task.FromResult(Status);
    }

    public Task StartAsync(CancellationToken cancellationToken = default)
    {
        Commands.Add("start-service");
        return Task.CompletedTask;
    }

    public Task StopAsync(CancellationToken cancellationToken = default) =>
        ServiceClient.DisconnectAndStopAsync(
            () => true,
            () => SendAsync("disconnect", cancellationToken: cancellationToken),
            async () =>
            {
                Commands.Add("stop-service");
                if (StopError is not null) throw StopError;
                if (PendingStop is not null) await PendingStop;
                Status = Status with { Active = false, State = "disconnected" };
                Commands.Add("service-stopped");
            });

    public Task<TunnelStatus> SendAsync(string command, string? name = null, string? config = null,
        CancellationToken cancellationToken = default)
    {
        Commands.Add(command);
        if (Error is not null) throw Error;
        if (command == "status" && PendingPoll is not null) return PendingPoll;
        Status = command switch
        {
            "connect" => Status with { Active = true, State = "connecting" },
            "disconnect" => Status with { Active = false, State = "disconnected" },
            "import" => Status with { Profile = new ProfileSummary(name!, "node.example.com:51820", "10.110.0.2/32") },
            _ => Status
        };
        return Task.FromResult(Status);
    }
}

public sealed class ClientTests
{
    [AvaloniaTheory]
    [InlineData(920, 680)]
    [InlineData(760, 580)]
    public void AppVersionUsesBuildMetadataAndAppearsBelowBrand(double width, double height)
    {
        var window = new MainWindow(new FakeService(), TestApp.Preferences()) { Width = width, Height = height };
        window.Show();
        try
        {
            window.UpdateLayout();
            var metadata = typeof(MainViewModel).Assembly.GetCustomAttribute<AssemblyInformationalVersionAttribute>();
            Assert.NotNull(metadata);
            var version = window.FindControl<TextBlock>("AppVersionLabel")!;
            Assert.Equal("v" + metadata.InformationalVersion.Split('+')[0], version.Text);
            Assert.DoesNotContain("+", version.Text!);
            var brand = window.FindControl<TextBlock>("BrandName")!;
            var parent = window.FindControl<StackPanel>("SidebarBrand")!;
            Assert.True(version.TranslatePoint(default, parent)!.Value.Y >=
                brand.TranslatePoint(new Point(0, brand.Bounds.Height), parent)!.Value.Y);
            Assert.Equal(brand.TranslatePoint(default, parent)!.Value.X, version.TranslatePoint(default, parent)!.Value.X);
            AssertContained(version, parent);
        }
        finally { window.CloseForShutdown(); }
    }

    [Fact]
    public async Task ShutdownDisconnectsBeforeStoppingAndWaitsForBoth()
    {
        var disconnected = new TaskCompletionSource();
        var stopped = new TaskCompletionSource();
        var calls = new List<string>();
        var shutdown = ServiceClient.DisconnectAndStopAsync(
            () => true,
            async () => { calls.Add("disconnect"); await disconnected.Task; calls.Add("disconnected"); },
            async () => { calls.Add("stop"); await stopped.Task; calls.Add("stopped"); });
        Assert.Equal(new[] { "disconnect" }, calls);
        Assert.False(shutdown.IsCompleted);
        disconnected.SetResult();
        Assert.Equal(new[] { "disconnect", "disconnected", "stop" }, calls);
        Assert.False(shutdown.IsCompleted);
        stopped.SetResult();
        await shutdown;
        Assert.Equal("stopped", calls.Last());
    }

    [Fact]
    public async Task ShutdownDoesNotHideAnExplicitCleanupRejection()
    {
        var stopCalled = false;
        await Assert.ThrowsAsync<ServiceException>(() => ServiceClient.DisconnectAndStopAsync(
            () => true,
            () => throw new ServiceException("remove endpoint failed"),
            () => { stopCalled = true; return Task.CompletedTask; }));
        Assert.False(stopCalled);
    }

    [Fact]
    public async Task AlreadyStoppedServiceDoesNotRequireAControlPipe()
    {
        var stopCalled = false;
        await ServiceClient.DisconnectAndStopAsync(
            () => false,
            () => throw new InvalidOperationException("must not send IPC to a stopped service"),
            () => { stopCalled = true; return Task.CompletedTask; });
        Assert.True(stopCalled);
    }

    [Fact]
    public async Task QuitFailureIsNotReplacedByBackgroundPolling()
    {
        var service = new FakeService { StopError = new IOException("service cleanup failed") };
        using var vm = new MainViewModel(service, TestApp.Preferences());
        await vm.RefreshAsync();
        Assert.False(await vm.StopForExitAsync());
        var count = service.Commands.Count;
        service.Error = new IOException("service did not respond");
        await vm.RefreshAsync();
        Assert.Equal(count, service.Commands.Count);
        Assert.Equal("service cleanup failed", vm.Error);
        Assert.Equal("Shutdown incomplete", vm.StateLabel);
        Assert.False(vm.CanToggle);
    }

    [AvaloniaFact]
    public void TrayAssetMatchesTheExecutableIconAsset()
    {
        using var asset = AssetLoader.Open(new Uri("avares://OuternetAWG/Assets/OuternetAWG.ico"));
        using var data = new MemoryStream();
        asset.CopyTo(data);
        Assert.Equal(File.ReadAllBytes(Path.Combine(AppContext.BaseDirectory, "OuternetAWG.ico")), data.ToArray());
    }

    [Fact]
    public async Task LatePollCannotOverwriteACompletedConnect()
    {
        var service = new FakeService();
        using var vm = new MainViewModel(service, TestApp.Preferences());
        await vm.RefreshAsync();
        var staleStatus = service.Status;
        var pending = new TaskCompletionSource<TunnelStatus>();
        service.PendingPoll = pending.Task;
        var poll = vm.RefreshAsync();
        await vm.ToggleAsync();
        pending.SetResult(staleStatus);
        await poll;
        Assert.Equal("Disconnect", vm.ButtonText);
        Assert.Equal("Connecting", vm.StateLabel);
    }

    [Fact]
    public async Task AvailabilityErrorClearsWhenServiceRecovers()
    {
        var service = new FakeService { Error = new IOException("service stopped") };
        using var vm = new MainViewModel(service, TestApp.Preferences());
        await vm.RefreshAsync();
        service.Error = null;
        await vm.RefreshAsync();
        Assert.True(vm.ServiceAvailable);
        Assert.False(vm.HasError);
    }

    [Fact]
    public async Task ImportConnectDisconnectHasNoLogin()
    {
        var service = new FakeService();
        using var vm = new MainViewModel(service, TestApp.Preferences());
        await vm.RefreshAsync();
        Assert.True(vm.CanImport);
        await vm.ImportAsync("Imported", "synthetic");
        Assert.Equal("Imported", vm.ProfileName);
        await vm.ToggleAsync();
        Assert.False(vm.IsConnected);
        Assert.Equal("Connecting", vm.StateLabel);
        Assert.False(vm.CanImport);
        Assert.Equal("Disconnect", vm.ButtonText);
        await vm.ToggleAsync();
        Assert.True(vm.CanImport);
        Assert.Equal(["status", "import", "connect", "disconnect"], service.Commands);
    }

    [Fact]
    public async Task ServiceFailureIsNotShownAsDisconnected()
    {
        var service = new FakeService { Error = new IOException("service unavailable") };
        using var vm = new MainViewModel(service, TestApp.Preferences());
        await vm.RefreshAsync();
        Assert.False(vm.ServiceAvailable);
        Assert.False(vm.CanToggle);
        Assert.Equal("Not ready", vm.StateLabel);
        Assert.True(vm.HasError);
        Assert.True(await vm.StopForExitAsync());
    }

    [Fact]
    public async Task ExitStopsTheServiceEvenIfLastPollWasIdle()
    {
        var service = new FakeService();
        using var vm = new MainViewModel(service, TestApp.Preferences());
        await vm.RefreshAsync();
        Assert.True(await vm.StopForExitAsync());
        Assert.Equal("service-stopped", service.Commands.Last());
        var commands = service.Commands.Count;
        await vm.RefreshAsync();
        Assert.Equal(commands, service.Commands.Count);
    }

    [Fact]
    public async Task RejectedImportKeepsThePreviousProfileAndInput()
    {
        var service = new FakeService();
        using var vm = new MainViewModel(service, TestApp.Preferences());
        await vm.RefreshAsync();
        service.Error = new ServiceException("invalid config");
        vm.PasteConfig = "keep this until corrected";
        await vm.ImportAsync("Bad", "invalid");
        Assert.Equal("Japan East", vm.ProfileName);
        Assert.NotEmpty(vm.PasteConfig);
        Assert.True(vm.HasError);
    }

    [AvaloniaTheory]
    [InlineData(1200, 900, false)]
    [InlineData(920, 680, false)]
    [InlineData(920, 680, true)]
    [InlineData(760, 580, false)]
    [InlineData(760, 580, true)]
    public void ConnectionFitsAndCentersWithoutScrolling(double width, double height, bool showError)
    {
        var service = new FakeService();
        var window = new MainWindow(service, TestApp.Preferences()) { Width = width, Height = height };
        window.Show();
        try
        {
            if (showError) window.Model.ShowError("The Windows service could not stop. " + new string('x', 160));
            window.UpdateLayout();
            Assert.Equal(SystemDecorations.Full, window.SystemDecorations);
            Assert.True(window.ExtendClientAreaToDecorationsHint);
            Assert.Equal(ExtendClientAreaChromeHints.NoChrome, window.ExtendClientAreaChromeHints);
            Assert.True(window.FindControl<Button>("ConnectButton")!.IsEnabled);
            Assert.False(window.FindControl<ScrollViewer>("ConfigurationScroller")!.IsEffectivelyVisible);
            var page = window.FindControl<Grid>("ConnectionPage")!;
            Assert.Empty(page.GetVisualDescendants().OfType<ScrollViewer>());
            foreach (var name in new[] { "CloseButton", "MinimizeButton", "MaximizeButton" })
                Assert.Null(window.FindControl<Button>(name)!.Content);
            foreach (var name in new[] { "ConnectionCard", "ProfileCard", "ConnectButton" })
                AssertContained(window.FindControl<Control>(name)!, window.FindControl<Grid>("ContentPanel")!);
            var card = window.FindControl<Border>("ConnectionCard")!;
            foreach (var child in card.GetVisualDescendants().OfType<Control>().Where(x => x.IsEffectivelyVisible))
                AssertContained(child, card);
            AssertConnectionCentered(window);
            using var frame = window.CaptureRenderedFrame();
            Assert.NotNull(frame);
            frame!.Save(Path.Combine(AppContext.BaseDirectory, $"connection-{width}-{height}-{showError}.png"));
        }
        finally { window.CloseForShutdown(); }
    }

    internal static void AssertConnectionCentered(MainWindow window)
    {
        var card = window.FindControl<Border>("ConnectionCard")!;
        var icon = window.FindControl<Viewbox>("ConnectionIcon")!;
        var button = window.FindControl<Button>("ConnectButton")!;
        var top = icon.TranslatePoint(default, card)!.Value.Y;
        var bottom = button.TranslatePoint(new Point(0, button.Bounds.Height), card)!.Value.Y;
        Assert.InRange(Math.Abs((top + bottom) / 2 - card.Bounds.Height / 2), 0, 1);
        foreach (var name in new[] { "ConnectionIcon", "ConnectionState", "ConnectionDescription", "ConnectButton" })
        {
            var child = window.FindControl<Control>(name)!;
            var center = child.TranslatePoint(new Point(child.Bounds.Width / 2, 0), card)!.Value.X;
            Assert.InRange(Math.Abs(center - card.Bounds.Width / 2), 0, 1);
        }
        var iconContent = Assert.IsType<Grid>(icon.Child);
        var glyph = window.FindControl<Avalonia.Controls.Shapes.Path>("ConnectionPowerGlyph")!;
        var bounds = glyph.Data!.Bounds;
        // The open circle includes its full diameter, so bottom minus radius gives its center.
        var circleCenter = new Point(bounds.Center.X, bounds.Bottom - bounds.Width / 2);
        Assert.InRange(Math.Abs(circleCenter.X - iconContent.Bounds.Width / 2), 0, 0.05);
        Assert.InRange(Math.Abs(circleCenter.Y - iconContent.Bounds.Height / 2), 0, 0.05);
    }

    internal static void AssertContained(Visual child, Visual parent)
    {
        var first = child.TranslatePoint(default, parent)!.Value;
        var last = child.TranslatePoint(new Point(child.Bounds.Width, child.Bounds.Height), parent)!.Value;
        Assert.True(first.X >= -1 && first.Y >= -1 && last.X <= parent.Bounds.Width + 1 && last.Y <= parent.Bounds.Height + 1,
            $"{child.GetType().Name} {child.Bounds} escapes {parent.GetType().Name} {parent.Bounds}: {first} to {last}");
    }

    [AvaloniaFact]
    public void CloseHidesAndOpenRestoresWithoutRestartingService()
    {
        var service = new FakeService();
        var window = new MainWindow(service, TestApp.Preferences());
        window.Show();
        try
        {
            window.Close();
            Assert.False(window.IsVisible);
            Assert.DoesNotContain("stop-service", service.Commands);
            window.OpenUI();
            Assert.True(window.IsVisible);
            Assert.Equal(1, service.Commands.Count(x => x == "start-service"));
        }
        finally { window.CloseForShutdown(); }
    }

    [AvaloniaFact]
    public async Task TrayQuitWaitsForDisconnectAndServiceStopBeforeExitingUI()
    {
        var stopped = new TaskCompletionSource();
        var service = new FakeService { PendingStop = stopped.Task };
        service.Status = service.Status with { Active = true, State = "connected" };
        var window = new MainWindow(service, TestApp.Preferences());
        using var tray = new TrayController(window, () => service.Commands.Add("ui-exit"));
        window.Show();
        window.Close();
        Assert.False(window.IsVisible);
        Assert.True(service.Status.Active);
        Assert.Equal(new[] { "Open UI", "Disconnect", "Quit" },
            tray.Menu.Items.OfType<NativeMenuItem>().Where(x => x is not NativeMenuItemSeparator).Select(x => x.Header).ToArray());
        var quit = tray.QuitAsync();
        Assert.False(quit.IsCompleted);
        Assert.False(service.Status.Active);
        Assert.True(service.Commands.IndexOf("disconnect") < service.Commands.IndexOf("stop-service"));
        Assert.DoesNotContain("ui-exit", service.Commands);
        stopped.SetResult();
        Assert.True(await quit);
        Assert.False(service.Status.Active);
        Assert.True(service.Commands.IndexOf("service-stopped") < service.Commands.IndexOf("ui-exit"));
    }

    [AvaloniaFact]
    public async Task FailedServiceStopKeepsTheUIAndRestoresTheWindow()
    {
        var service = new FakeService { StopError = new IOException("Stop permission denied") };
        var window = new MainWindow(service, TestApp.Preferences());
        var exited = false;
        using var tray = new TrayController(window, () => exited = true);
        window.Show();
        window.Close();
        try
        {
            Assert.False(await tray.QuitAsync());
            Assert.False(exited);
            Assert.True(window.IsVisible);
            Assert.True(window.Model.HasError);
        }
        finally { window.CloseForShutdown(); }
    }
}
