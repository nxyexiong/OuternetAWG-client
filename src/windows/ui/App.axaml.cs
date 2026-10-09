using Avalonia;
using Avalonia.Controls.ApplicationLifetimes;
using Avalonia.Controls;
using Avalonia.Markup.Xaml;
using OuternetAWG.Services;

namespace OuternetAWG;

public sealed partial class App : Application
{
    private TrayController? _tray;
    public override void Initialize() => AvaloniaXamlLoader.Load(this);

    public override void OnFrameworkInitializationCompleted()
    {
        if (ApplicationLifetime is IClassicDesktopStyleApplicationLifetime desktop)
        {
            desktop.ShutdownMode = ShutdownMode.OnExplicitShutdown;
            var window = new MainWindow();
            desktop.MainWindow = window;
            _tray = new TrayController(window, () => desktop.Shutdown());
            desktop.Exit += (_, _) => _tray.Dispose();
        }
        base.OnFrameworkInitializationCompleted();
    }
}
