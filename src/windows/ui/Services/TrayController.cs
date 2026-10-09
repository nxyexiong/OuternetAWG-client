using System.ComponentModel;
using Avalonia.Controls;

namespace OuternetAWG.Services;

public sealed class TrayController : IDisposable
{
    private readonly MainWindow _window;
    private readonly Action _shutdown;
    private readonly TrayIcon _icon;
    private readonly NativeMenuItem _open;
    private readonly NativeMenuItem _toggle;
    private readonly NativeMenuItem _quit;
    private bool _quitting, _disposed;

    public NativeMenu Menu { get; }

    public TrayController(MainWindow window, Action shutdown)
    {
        _window = window;
        _shutdown = shutdown;
        _open = new NativeMenuItem();
        _open.Click += OpenClicked;
        _toggle = new NativeMenuItem();
        _toggle.Click += ToggleClicked;
        _quit = new NativeMenuItem();
        _quit.Click += QuitClicked;
        Menu = new NativeMenu { Items = { _open, _toggle, new NativeMenuItemSeparator(), _quit } };
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
        var text = _window.Model.Text;
        _open.Header = text["Tray.Open"];
        _toggle.Header = text[_window.Model.HasActiveTunnel ? "Connection.Disconnect" : "Connection.Connect"];
        _toggle.IsEnabled = !_quitting && _window.Model.CanToggle;
        _quit.Header = text[_quitting ? "Tray.Quitting" : "Tray.Quit"];
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
        _open.Click -= OpenClicked;
        _toggle.Click -= ToggleClicked;
        _quit.Click -= QuitClicked;
        _icon.IsVisible = false;
        _icon.Dispose();
    }
}
