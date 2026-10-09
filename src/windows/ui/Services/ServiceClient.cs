using System.Buffers.Binary;
using System.ComponentModel;
using System.Diagnostics;
using System.IO.Pipes;
using System.Runtime.InteropServices;
using System.Security.Principal;
using System.Text.Json;
using Microsoft.Win32.SafeHandles;

namespace OuternetAWG.Services;

public sealed record ProfileSummary(string Name, string Endpoint, string Address);
public sealed record TunnelStatus(string State, bool Active, string Message, ProfileSummary? Profile, DateTimeOffset? Handshake);
public sealed record ServiceResponse(int Version, bool Ok, string? Error, TunnelStatus Status);
public sealed record ServiceRequest(int Version, string Command, string? Name = null, string? Config = null);

public interface IServiceClient
{
    Task StartAsync(CancellationToken cancellationToken = default);
    Task StopAsync(CancellationToken cancellationToken = default);
    Task<TunnelStatus> SendAsync(string command, string? name = null, string? config = null,
        CancellationToken cancellationToken = default);
}

public sealed class ServiceClient : IServiceClient
{
    private const int MaxFrame = 1024 * 1024;
    private static readonly JsonSerializerOptions Json = new() { PropertyNamingPolicy = JsonNamingPolicy.CamelCase };

    public Task StartAsync(CancellationToken cancellationToken = default) =>
        ChangeServiceStateAsync(start: true, cancellationToken);

    public async Task StopAsync(CancellationToken cancellationToken = default)
    {
        if (!OperatingSystem.IsWindows()) throw new PlatformNotSupportedException("OuternetAWG requires Windows.");
        await DisconnectAndStopAsync(
            () => ReadServiceStatus()?.CurrentState == 4,
            () => SendAsync("disconnect", cancellationToken: cancellationToken),
            () => ChangeServiceStateAsync(start: false, cancellationToken));
    }

    internal static async Task DisconnectAndStopAsync(Func<bool> isRunning, Func<Task> disconnect, Func<Task> stop)
    {
        if (isRunning())
        {
            try
            {
                await disconnect();
            }
            catch (Exception disconnectError) when (disconnectError is IOException or UnauthorizedAccessException)
            {
                // SCM Stop performs the same cleanup if the local control pipe is unavailable.
                Trace.TraceWarning("Disconnect response unavailable; requesting graceful service stop: {0}", disconnectError.Message);
                try { await stop(); }
                catch (Exception stopError) when (stopError is IOException or Win32Exception or UnauthorizedAccessException)
                {
                    throw new IOException("Disconnect could not be confirmed and the service could not stop: " + stopError.Message,
                        new AggregateException(disconnectError, stopError));
                }
                return;
            }
        }
        await stop();
    }

    private static Native.ServiceStatus? ReadServiceStatus()
    {
        using var manager = Native.OpenSCManager(null, null, 1);
        if (manager.IsInvalid) throw new Win32Exception(Marshal.GetLastWin32Error());
        using var service = Native.OpenService(manager, "OuternetAWG", 4);
        if (service.IsInvalid)
        {
            var error = Marshal.GetLastWin32Error();
            if (error == 1060) return null;
            throw ServiceAccessError(error);
        }
        return QueryStatus(service);
    }

    private static Task ChangeServiceStateAsync(bool start, CancellationToken cancellationToken) =>
        Task.Run(async () =>
        {
            if (!OperatingSystem.IsWindows()) throw new PlatformNotSupportedException("OuternetAWG requires Windows.");
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
            timeout.CancelAfter(TimeSpan.FromSeconds(start ? 40 : 75));
            using var manager = Native.OpenSCManager(null, null, 1);
            if (manager.IsInvalid) throw new Win32Exception(Marshal.GetLastWin32Error());
            using var service = Native.OpenService(manager, "OuternetAWG", 4u | (start ? 0x10u : 0x20u));
            if (service.IsInvalid)
            {
                var error = Marshal.GetLastWin32Error();
                if (!start && error == 1060) return;
                throw ServiceAccessError(error);
            }
            var requested = false;
            try
            {
                while (true)
                {
                    timeout.Token.ThrowIfCancellationRequested();
                    var status = QueryStatus(service);
                    if (start && status.CurrentState == 4) return;
                    if (!start && status.CurrentState == 1)
                    {
                        if (requested && (status.Win32ExitCode != 0 || status.ServiceSpecificExitCode != 0))
                            throw new IOException($"The service stopped with a cleanup error ({status.Win32ExitCode}/{status.ServiceSpecificExitCode}). Check the service log.");
                        return;
                    }
                    if (start && status.CurrentState == 1)
                    {
                        if (requested) throw new IOException("The service exited during startup. Check the service log or repair the installation.");
                        if (!Native.StartService(service, 0, IntPtr.Zero))
                        {
                            var error = Marshal.GetLastWin32Error();
                            if (error != 1056) throw ServiceAccessError(error);
                        }
                        requested = true;
                    }
                    else if (!start && status.CurrentState == 4 && !requested)
                    {
                        // The service's Stop handler disconnects and releases Wintun/routes/WFP before reporting Stopped.
                        if (!Native.ControlService(service, 1, out _))
                        {
                            var error = Marshal.GetLastWin32Error();
                            if (error is not (1061 or 1062)) throw ServiceAccessError(error);
                        }
                        else requested = true;
                    }
                    else if (status.CurrentState is not (1 or 2 or 3 or 4))
                        throw new IOException("The OuternetAWG service is in an unsupported state. Restart it from Windows Services.");
                    await Task.Delay(200, timeout.Token);
                }
            }
            catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
            {
                throw new IOException(start
                    ? "The service did not start in time. Check the service log."
                    : "The service has not finished disconnecting and stopping. The UI will remain open; retry Quit after it finishes.");
            }
        }, cancellationToken);

    private static Exception ServiceAccessError(int error) => error switch
    {
        1060 => new IOException("The OuternetAWG service is not installed. Run the installer first."),
        5 => new IOException("Service control permission was denied. Reinstall OuternetAWG for this Windows user to enable on-demand start and stop."),
        1058 => new IOException("The OuternetAWG service is disabled. Set it to Manual in Windows Services or repair the installation."),
        _ => new Win32Exception(error)
    };

    private static Native.ServiceStatus QueryStatus(Native.ServiceHandle service)
    {
        if (!Native.QueryServiceStatusEx(service, 0, out var status, Marshal.SizeOf<Native.ServiceStatus>(), out _))
            throw new Win32Exception(Marshal.GetLastWin32Error());
        return status;
    }

    public async Task<TunnelStatus> SendAsync(string command, string? name = null, string? config = null,
        CancellationToken cancellationToken = default)
    {
        if (!OperatingSystem.IsWindows()) throw new PlatformNotSupportedException("OuternetAWG requires Windows.");
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        timeout.CancelAfter(command == "status" ? TimeSpan.FromSeconds(3) : TimeSpan.FromSeconds(40));
        await using var pipe = new NamedPipeClientStream(".", "OuternetAWG.Control.v1",
            PipeDirection.InOut, PipeOptions.Asynchronous, TokenImpersonationLevel.Anonymous);
        try
        {
            await pipe.ConnectAsync(timeout.Token);
            VerifyService(pipe.SafePipeHandle);
            var payload = JsonSerializer.SerializeToUtf8Bytes(new ServiceRequest(1, command, name, config), Json);
            if (payload.Length > MaxFrame) throw new IOException("Config is too large.");
            try
            {
                var prefix = new byte[4];
                BinaryPrimitives.WriteInt32LittleEndian(prefix, payload.Length);
                await pipe.WriteAsync(prefix, timeout.Token);
                await pipe.WriteAsync(payload, timeout.Token);
                await pipe.FlushAsync(timeout.Token);
                await pipe.ReadExactlyAsync(prefix, timeout.Token);
                var length = BinaryPrimitives.ReadInt32LittleEndian(prefix);
                if (length is <= 0 or > MaxFrame) throw new IOException("Invalid response from the service.");
                var body = new byte[length];
                await pipe.ReadExactlyAsync(body, timeout.Token);
                var response = JsonSerializer.Deserialize<ServiceResponse>(body, Json)
                    ?? throw new IOException("Empty response from the service.");
                if (response.Version != 1 || response.Status is null)
                    throw new IOException("UI and service versions do not match. Reinstall them together.");
                if (!response.Ok) throw new ServiceException(response.Error ?? "The service rejected the request.");
                return response.Status;
            }
            finally { Array.Clear(payload); }
        }
        catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
        {
            throw new IOException("The service did not respond. Install or restart the OuternetAWG service.");
        }
    }

    private static void VerifyService(SafePipeHandle pipe)
    {
        if (!Native.GetNamedPipeServerProcessId(pipe, out var serverPid))
            throw new Win32Exception(Marshal.GetLastWin32Error(), "Cannot verify the control pipe.");
        using var manager = Native.OpenSCManager(null, null, 1);
        if (manager.IsInvalid) throw new Win32Exception(Marshal.GetLastWin32Error());
        using var service = Native.OpenService(manager, "OuternetAWG", 4);
        if (service.IsInvalid) throw new IOException("The OuternetAWG service is not installed.");
        var status = QueryStatus(service);
        // SCM is the trusted authority. Never send a private key to a pipe-name impostor.
        if (status.ProcessId == 0 || status.ProcessId != serverPid || status.CurrentState != 4)
            throw new IOException("The control pipe does not belong to the running OuternetAWG service.");
    }

    private static class Native
    {
        [StructLayout(LayoutKind.Sequential)]
        internal struct ServiceStatus
        {
            public uint ServiceType, CurrentState, ControlsAccepted, Win32ExitCode,
                ServiceSpecificExitCode, CheckPoint, WaitHint, ProcessId, ServiceFlags;
        }

        [StructLayout(LayoutKind.Sequential)]
        internal struct ControlStatus
        {
            public uint ServiceType, CurrentState, ControlsAccepted, Win32ExitCode,
                ServiceSpecificExitCode, CheckPoint, WaitHint;
        }

        internal sealed class ServiceHandle : SafeHandleZeroOrMinusOneIsInvalid
        {
            public ServiceHandle() : base(true) { }
            protected override bool ReleaseHandle() => CloseServiceHandle(handle);
        }

        [DllImport("kernel32.dll", SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        internal static extern bool GetNamedPipeServerProcessId(SafePipeHandle pipe, out uint pid);
        [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        internal static extern ServiceHandle OpenSCManager(string? machine, string? database, uint access);
        [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        internal static extern ServiceHandle OpenService(ServiceHandle manager, string name, uint access);
        [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        internal static extern bool StartService(ServiceHandle service, uint count, IntPtr arguments);
        [DllImport("advapi32.dll", SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        internal static extern bool ControlService(ServiceHandle service, uint control, out ControlStatus status);
        [DllImport("advapi32.dll", SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        internal static extern bool QueryServiceStatusEx(ServiceHandle service, int level,
            out ServiceStatus status, int size, out int needed);
        [DllImport("advapi32.dll")]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool CloseServiceHandle(IntPtr handle);
    }
}

public sealed class ServiceException(string message) : Exception(message);
