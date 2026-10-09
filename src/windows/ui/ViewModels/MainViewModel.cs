using System.ComponentModel;
using System.Runtime.CompilerServices;
using OuternetAWG.Services;

namespace OuternetAWG.ViewModels;

public sealed class MainViewModel(IServiceClient client) : INotifyPropertyChanged
{
    private TunnelStatus? _status;
    private bool _available, _busy, _configuration, _stopped;
    private bool _availabilityError, _shutdownFailed;
    private long _revision;
    private string _error = "", _pastedConfig = "";
    public event PropertyChangedEventHandler? PropertyChanged;

    public bool ServiceAvailable => _available;
    public bool Busy => _busy;
    public bool HasActiveTunnel => _status?.Active == true;
    public bool CanImport => _available && !_busy && !_shutdownFailed && !(_status?.Active ?? false);
    public bool CanToggle => _available && !_busy && !_shutdownFailed && _status?.Profile is not null;
    public bool IsConnected => _available && _status?.State == "connected";
    public bool IsConnectionPage => !_configuration;
    public bool IsConfigurationPage => _configuration;
    public bool HasError => !string.IsNullOrEmpty(_error);
    public string Error => _error;
    public string ProfileName => _status?.Profile?.Name ?? "No configuration";
    public string Endpoint => _status?.Profile?.Endpoint ?? "Import a config to choose your server";
    public string Address => _status?.Profile?.Address ?? "Not configured";
    public string ButtonText => _busy ? "Please wait..." : _status?.Active == true ? "Disconnect" : "Connect";
    public string ServiceLabel => _available ? "Service ready" : "Service unavailable";
    public string StateLabel => _shutdownFailed ? "Shutdown incomplete" : !_available ? "Not ready" : _status?.State switch
    {
        "connected" => "Connected",
        "connecting" => "Connecting",
        "waiting" => "Waiting for server",
        "error" => "Connection error",
        _ => "Disconnected"
    };
    public string Description => _shutdownFailed
        ? "Quit did not finish. Resolve the error above, then retry Quit."
        : !_available
        ? "Install the Windows service to connect. No account or login is needed."
        : _status?.Message ?? "Import an OuternetAWG configuration to get started.";
    public string PasteConfig
    {
        get => _pastedConfig;
        set { _pastedConfig = value; Notify(); }
    }

    public void Navigate(bool configuration) { _configuration = configuration; NotifyAll(); }
    public void ShowError(string error) { _error = error; NotifyAll(); }

    public Task<bool> InitializeAsync(CancellationToken token = default) => RunAsync(async () =>
    {
        await client.StartAsync(token);
        return await client.SendAsync("status", cancellationToken: token);
    });

    public async Task RefreshAsync(CancellationToken token = default)
    {
        if (_busy || _stopped || _shutdownFailed) return;
        var revision = _revision;
        try
        {
            var status = await client.SendAsync("status", cancellationToken: token);
            if (revision != _revision || _busy) return;
            _status = status;
            _available = true;
            if (_availabilityError) _error = "";
            _availabilityError = false;
        }
        catch (OperationCanceledException) when (token.IsCancellationRequested) { return; }
        catch (Exception ex) when (IsExpected(ex))
        {
            if (revision != _revision || _busy) return;
            _available = false;
            _error = ex.Message;
            _availabilityError = true;
        }
        NotifyAll();
    }

    public Task ToggleAsync() => RunAsync(() => client.SendAsync(_status?.Active == true ? "disconnect" : "connect"));

    public async Task ImportAsync(string name, string config)
    {
        await RunAsync(async () =>
        {
            var status = await client.SendAsync("import", name, config);
            PasteConfig = "";
            _configuration = false;
            return status;
        });
    }

    public async Task<bool> StopForExitAsync()
    {
        if (_busy) { ShowError("Wait for the current operation before quitting."); return false; }
        var success = await RunAsync(async () =>
        {
            await client.StopAsync();
            _stopped = true;
            return new TunnelStatus("disconnected", false, "Disconnected. The service has stopped.", _status?.Profile, null);
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
        _error = "";
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
            _error = ex.Message;
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
}
