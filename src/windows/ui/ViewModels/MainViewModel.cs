using System.ComponentModel;
using System.Runtime.CompilerServices;
using OuternetAWG.Services;

namespace OuternetAWG.ViewModels;

public enum ClientPage { Connection, Configuration, Settings }

public sealed class MainViewModel : INotifyPropertyChanged, IDisposable
{
    private readonly IServiceClient _client;
    private TunnelStatus? _status;
    private bool _available, _busy, _stopped;
    private ClientPage _page;
    private bool _availabilityError, _shutdownFailed;
    private long _revision;
    private Exception? _error;
    private string _pastedConfig = "";
    public event PropertyChangedEventHandler? PropertyChanged;
    public PreferencesViewModel Preferences { get; }
    public LocalizationService Text => Preferences.Text;

    public MainViewModel(IServiceClient client, PreferencesViewModel? preferences = null)
    {
        _client = client;
        Preferences = preferences ?? new();
        Text.PropertyChanged += LanguageChanged;
    }

    private void LanguageChanged(object? sender, PropertyChangedEventArgs e) => NotifyAll();

    public bool ServiceAvailable => _available;
    public bool Busy => _busy;
    public bool HasActiveTunnel => _status?.Active == true;
    public bool CanImport => _available && !_busy && !_shutdownFailed && !(_status?.Active ?? false);
    public bool CanToggle => _available && !_busy && !_shutdownFailed && _status?.Profile is not null;
    public bool IsConnected => _available && _status?.State == "connected";
    public bool IsConnectionPage => _page == ClientPage.Connection;
    public bool IsConfigurationPage => _page == ClientPage.Configuration;
    public bool IsSettingsPage => _page == ClientPage.Settings;
    public bool HasError => _error is not null;
    public string Error => _error is null ? "" : Text.Describe(_error);
    public string ProfileName => _status?.Profile?.Name ?? Text["Connection.NoProfile"];
    public string Endpoint => _status?.Profile?.Endpoint ?? Text["Connection.NoEndpoint"];
    public string Address => _status?.Profile?.Address ?? Text["Connection.NoAddress"];
    public string ButtonText => Text[_busy ? "Connection.Wait" : _status?.Active == true ? "Connection.Disconnect" : "Connection.Connect"];
    public string ServiceLabel => Text[_available ? "Service.Ready" : "Service.Unavailable"];
    public string StateLabel => Text[_shutdownFailed ? "State.ShutdownIncomplete" : !_available ? "State.NotReady" : _status?.State switch
    {
        "connected" => "State.Connected",
        "connecting" => "State.Connecting",
        "waiting" => "State.Waiting",
        "error" => "State.Error",
        _ => "State.Disconnected"
    }];
    public string Description => _shutdownFailed
        ? Text["Description.ShutdownIncomplete"]
        : !_available
        ? Text["Description.Unavailable"]
        : _status?.State == "error"
        ? Text.Format("Error.ServiceDetails", _status.Message)
        : Text[_status?.State switch
        {
            "connecting" => "Description.Connecting",
            "waiting" => _status.Handshake is null ? "Description.Waiting" : "Description.Stale",
            "connected" => "Description.Connected",
            _ when _stopped => "Description.Stopped",
            _ => _status?.Profile is null ? "Description.NoProfile" : "Description.Ready"
        }];
    public string PasteConfig
    {
        get => _pastedConfig;
        set { _pastedConfig = value; Notify(); }
    }

    public void Navigate(ClientPage page) { _page = page; NotifyAll(); }
    public void ShowError(string error) => ShowError(new IOException(error));
    public void ShowError(Exception error) { _error = error; NotifyAll(); }

    public Task<bool> InitializeAsync(CancellationToken token = default) => RunAsync(async () =>
    {
        await _client.StartAsync(token);
        return await _client.SendAsync("status", cancellationToken: token);
    });

    public async Task RefreshAsync(CancellationToken token = default)
    {
        if (_busy || _stopped || _shutdownFailed) return;
        var revision = _revision;
        try
        {
            var status = await _client.SendAsync("status", cancellationToken: token);
            if (revision != _revision || _busy) return;
            _status = status;
            _available = true;
            if (_availabilityError) _error = null;
            _availabilityError = false;
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested) { return; }
        catch (Exception ex) when (IsExpected(ex))
        {
            if (revision != _revision || _busy) return;
            _available = false;
            _error = ex;
            _availabilityError = true;
        }
        NotifyAll();
    }

    public Task ToggleAsync() => RunAsync(() => _client.SendAsync(_status?.Active == true ? "disconnect" : "connect"));

    public async Task ImportAsync(string name, string config)
    {
        await RunAsync(async () =>
        {
            var status = await _client.SendAsync("import", name, config);
            PasteConfig = "";
            _page = ClientPage.Connection;
            return status;
        });
    }

    public async Task<bool> StopForExitAsync()
    {
        if (_busy) { ShowError(new ClientException("Error.BusyQuit")); return false; }
        var success = await RunAsync(async () =>
        {
            await _client.StopAsync();
            _stopped = true;
            return new TunnelStatus("disconnected", false, "", _status?.Profile, null);
        });
        _shutdownFailed = !success;
        NotifyAll();
        return success;
    }

    private async Task<bool> RunAsync(Func<Task<TunnelStatus>> operation)
    {
        if (_busy) return false;
        _busy = true;
        _revision++;
        _error = null;
        _availabilityError = false;
        _shutdownFailed = false;
        NotifyAll();
        try
        {
            _status = await operation();
            _available = true;
            return true;
        }
        catch (Exception ex) when (IsExpected(ex))
        {
            _error = ex;
            if (ex is not ServiceException)
            {
                _available = false;
                _availabilityError = true;
            }
            return false;
        }
        finally { _busy = false; NotifyAll(); }
    }

    private static bool IsExpected(Exception ex) =>
        ex is IOException or ServiceException or Win32Exception or UnauthorizedAccessException
            or System.Text.Json.JsonException or PlatformNotSupportedException;

    private void Notify([CallerMemberName] string? property = null) =>
        PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(property));
    private void NotifyAll() => Notify(string.Empty);
    public void Dispose()
    {
        Text.PropertyChanged -= LanguageChanged;
        Preferences.Dispose();
    }
}
