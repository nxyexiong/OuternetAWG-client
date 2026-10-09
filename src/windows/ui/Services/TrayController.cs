using System.ComponentModel;
using Avalonia.Controls;

namespace OuternetAWG.Services;

public sealed class TrayController : IDisposable
{
    private readonly MainWindow _window;
    private readonly Action _shutdown;
    private readonly TrayIcon _icon;
    private readonly NativeMenuItem _toggle;
    private readonly NativeMenuItem _quit;
    private bool _quitting, _disposed;

    public NativeMenu Menu { get; }

    public TrayController(MainWindow window, Action shutdown)
    {
        _window = window;
        _shutdown = shutdown;
        var open = new NativeMenuItem("Open UI");
        open.Click += (_, _) => _window.OpenUI();
        _toggle = new NativeMenuItem("Connect");
        _toggle.Click += ToggleClicked;
        _quit = new NativeMenuItem("Quit");
        _quit.Click += QuitClicked;
        Menu = new NativeMenu { Items = { open, _toggle, new NativeMenuItemSeparator(), _quit } };
        _window.Icon = AppIcon.Create();
        _icon = new TrayIcon { Icon = _window.Icon, Menu = Menu, ToolTipText = "OuternetAWG", IsVisible = true };
        _icon.Clicked += OpenClicked;
        _window.Model.PropertyChanged += ModelChanged;
        Refresh();
    }

    private void OpenClicked(object? sender, EventArgs e) => _window.OpenUI();
    private void ModelChanged(object? sender, PropertyChangedEventArgs e) => Refresh();
    private async void ToggleClicked(object? sender, EventArgs e)
    {
        await _window.Model.ToggleAsync();
        if (_window.Model.HasError) _window.OpenUI();
    }
    private async void QuitClicked(object? sender, EventArgs e) => await QuitAsync();

    private void Refresh()
    {
        _toggle.Header = _window.Model.HasActiveTunnel ? "Disconnect" : "Connect";
        _toggle.IsEnabled = !_quitting && _window.Model.CanToggle;
        _quit.Header = _quitting ? "Quitting..." : "Quit";
        _quit.IsEnabled = !_quitting && !_window.Model.Busy;
        _icon.ToolTipText = "OuternetAWG - " + _window.Model.StateLabel;
    }

    public async Task<bool> QuitAsync()
    {
        if (_quitting || _disposed) return false;
        _quitting = true;
        Refresh();
        try
        {
            if (!await _window.Model.StopForExitAsync())
            {
                _window.OpenUI();
                return false;
            }
            _window.CloseForShutdown();
            Dispose();
            _shutdown();
            return true;
        }
        finally
        {
            _quitting = false;
            if (!_disposed) Refresh();
        }
    }

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        _window.Model.PropertyChanged -= ModelChanged;
        _icon.Clicked -= OpenClicked;
        _toggle.Click -= ToggleClicked;
        _quit.Click -= QuitClicked;
        _icon.IsVisible = false;
        _icon.Dispose();
    }
}
