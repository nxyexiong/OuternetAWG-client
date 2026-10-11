using System.Text;
using Avalonia;
using Avalonia.Controls;
using Avalonia.Input;
using Avalonia.Interactivity;
using Avalonia.Platform.Storage;
using Avalonia.VisualTree;
using Avalonia.Threading;
using OuternetAWG.Services;
using OuternetAWG.ViewModels;

namespace OuternetAWG;

public sealed partial class MainWindow : Window
{
    private readonly MainViewModel _model;
    private readonly CancellationTokenSource _lifetime = new();
    private bool _allowClose;
    private Task? _polling;
    public MainViewModel Model => _model;

    public MainWindow() : this(new ServiceClient(), new PreferencesViewModel(new PreferencesStore(),
        theme => Application.Current!.RequestedThemeVariant = theme)) { }

    public MainWindow(IServiceClient client, PreferencesViewModel? preferences = null)
    {
        InitializeComponent();
        DataContext = _model = new MainViewModel(client, preferences);
        Opened += (_, _) => _polling ??= PollAsync();
        Closing += OnClosing;
        Closed += (_, _) => { _lifetime.Cancel(); _model.Dispose(); };
        AddHandler(PointerPressedEvent, ResizePressed, RoutingStrategies.Tunnel);
        UpdateWindowOutline();
    }

    private async Task PollAsync()
    {
        using var timer = new PeriodicTimer(TimeSpan.FromSeconds(2));
        try
        {
            await _model.InitializeAsync(_lifetime.Token);
            while (await timer.WaitForNextTickAsync(_lifetime.Token))
            {
                _model.Text.RefreshSystemLanguage();
                await _model.RefreshAsync(_lifetime.Token);
            }
        }
        catch (OperationCanceledException) when (_lifetime.IsCancellationRequested) { }
    }

    private void ConnectionClicked(object? sender, RoutedEventArgs e) => _model.Navigate(ClientPage.Connection);
    private void ConfigurationClicked(object? sender, RoutedEventArgs e) => _model.Navigate(ClientPage.Configuration);
    private void SettingsClicked(object? sender, RoutedEventArgs e) => _model.Navigate(ClientPage.Settings);
    private async void ConnectClicked(object? sender, RoutedEventArgs e) => await _model.ToggleAsync();
    private async void UseConfigClicked(object? sender, RoutedEventArgs e) =>
        await _model.ImportAsync(_model.Text["Configuration.ImportedName"], _model.PasteConfig);

    private async void RouteSelectionChanged(object? sender, SelectionChangedEventArgs e)
    {
        if (DataContext is MainViewModel model && sender is ComboBox { SelectedItem: PreferenceChoice choice } selector)
        {
            await model.Routing.SelectAsync(choice.Id);
            // Restore rejected selections after ComboBox finishes its selection event.
            if (selector.SelectedItem != model.Routing.Selected)
                Dispatcher.UIThread.Post(() => selector.SetCurrentValue(ComboBox.SelectedItemProperty, model.Routing.Selected));
        }
    }

    private async void NewRouteClicked(object? sender, RoutedEventArgs e)
    {
        if (_model.Routing.CanChange) await new RouteProfileWindow(_model, true).ShowDialog<bool>(this);
    }

    private async void EditRouteClicked(object? sender, RoutedEventArgs e)
    {
        if (_model.Routing.CanEdit) await new RouteProfileWindow(_model, false).ShowDialog<bool>(this);
    }

    private async void DeleteRouteClicked(object? sender, RoutedEventArgs e)
    {
        if (!_model.Routing.CanEdit) return;
        var selected = _model.Routing.Current.Id;
        var dialog = new Window
        {
            Title = _model.Text["Routing.Delete"], Width = 440, SizeToContent = SizeToContent.Height,
            CanResize = false, ShowInTaskbar = false, WindowStartupLocation = WindowStartupLocation.CenterOwner,
            Background = Background
        };
        var cancel = new Button { Content = _model.Text["Routing.Cancel"] };
        var delete = new Button { Content = _model.Text["Routing.Delete"], Classes = { "primary" } };
        cancel.Click += (_, _) => dialog.Close(false);
        delete.Click += (_, _) => dialog.Close(true);
        dialog.Content = new StackPanel
        {
            Margin = new Thickness(24), Spacing = 20,
            Children =
            {
                new TextBlock { Text = _model.Text.Format("Routing.DeleteConfirm", _model.Routing.Current.Name), TextWrapping = Avalonia.Media.TextWrapping.Wrap },
                new StackPanel { Orientation = Avalonia.Layout.Orientation.Horizontal, HorizontalAlignment = Avalonia.Layout.HorizontalAlignment.Right,
                    Spacing = 10, Children = { cancel, delete } }
            }
        };
        if (await dialog.ShowDialog<bool>(this) && _model.Routing.Current.Id == selected)
            await _model.Routing.DeleteAsync();
    }

    private async void ImportClicked(object? sender, RoutedEventArgs e)
    {
        try
        {
            var files = await StorageProvider.OpenFilePickerAsync(new FilePickerOpenOptions
            {
                Title = _model.Text["Configuration.PickerTitle"],
                AllowMultiple = false,
                FileTypeFilter = [new FilePickerFileType(_model.Text["Configuration.FileType"]) { Patterns = ["*.conf", "*.vpn"] }]
            });
            if (files.Count == 0) return;
            using var file = files[0];
            await using var stream = await file.OpenReadAsync();
            var bytes = new byte[128 * 1024 + 1];
            var size = 0;
            while (size < bytes.Length)
            {
                var read = await stream.ReadAsync(bytes.AsMemory(size));
                if (read == 0) break;
                size += read;
            }
            try
            {
                if (size > 128 * 1024) throw new ClientException("Error.FileSize");
                var content = new UTF8Encoding(false, true).GetString(bytes, 0, size);
                await _model.ImportAsync(Path.GetFileNameWithoutExtension(file.Name), content);
            }
            finally { Array.Clear(bytes); }
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or DecoderFallbackException)
        {
            _model.ShowError(new ClientException("Error.Import", ex, ex));
        }
    }

    private void TitlePressed(object? sender, PointerPressedEventArgs e)
    {
        if (e.Handled || !e.GetCurrentPoint(this).Properties.IsLeftButtonPressed ||
            e.Source is Visual source && (source is Button || source.FindAncestorOfType<Button>() is not null)) return;
        if (e.ClickCount == 2) ToggleMaximize();
        else BeginMoveDrag(e);
    }

    private void MinimizeClicked(object? sender, RoutedEventArgs e) => WindowState = WindowState.Minimized;
    private void MaximizeClicked(object? sender, RoutedEventArgs e) => ToggleMaximize();
    private void ToggleMaximize()
    {
        WindowState = WindowState == WindowState.Maximized ? WindowState.Normal : WindowState.Maximized;
    }
    private void CloseClicked(object? sender, RoutedEventArgs e) => Close();

    public void OpenUI()
    {
        Show();
        if (WindowState == WindowState.Minimized) WindowState = WindowState.Normal;
        Activate();
    }

    public void CloseForShutdown()
    {
        _allowClose = true;
        Close();
    }

    private void OnClosing(object? sender, WindowClosingEventArgs e)
    {
        if (_allowClose || e.CloseReason is WindowCloseReason.OSShutdown or WindowCloseReason.ApplicationShutdown) return;
        e.Cancel = true;
        Hide();
    }

    protected override void OnPropertyChanged(AvaloniaPropertyChangedEventArgs change)
    {
        base.OnPropertyChanged(change);
        if (change.Property == WindowStateProperty && WindowOutline is not null) UpdateWindowOutline();
    }

    private void UpdateWindowOutline()
    {
        var radius = WindowState == WindowState.Normal && OperatingSystem.IsWindowsVersionAtLeast(10, 0, 22000) ? 8 : 0;
        WindowSurface.CornerRadius = WindowOutline.CornerRadius = new CornerRadius(radius);
        WindowOutline.IsVisible = WindowState != WindowState.Maximized;
    }

    private void ResizePressed(object? sender, PointerPressedEventArgs e)
    {
        if (WindowState != WindowState.Normal || !e.GetCurrentPoint(this).Properties.IsLeftButtonPressed) return;
        var p = e.GetPosition(this);
        var left = p.X < 6;
        var right = p.X >= Bounds.Width - 6;
        var top = p.Y < 6;
        var bottom = p.Y >= Bounds.Height - 6;
        WindowEdge? edge = (left, right, top, bottom) switch
        {
            (true, _, true, _) => WindowEdge.NorthWest,
            (_, true, true, _) => WindowEdge.NorthEast,
            (true, _, _, true) => WindowEdge.SouthWest,
            (_, true, _, true) => WindowEdge.SouthEast,
            (true, _, _, _) => WindowEdge.West,
            (_, true, _, _) => WindowEdge.East,
            (_, _, true, _) => WindowEdge.North,
            (_, _, _, true) => WindowEdge.South,
            _ => null
        };
        if (edge is { } direction) { BeginResizeDrag(direction, e); e.Handled = true; }
    }
}
