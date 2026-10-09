using System.Runtime.InteropServices;
using System.Text.Json;
using Avalonia;
using Avalonia.Controls;
using Avalonia.Media.Imaging;
using Avalonia.Media;
using Avalonia.Styling;
using Avalonia.Threading;
using Avalonia.VisualTree;
using OuternetAWG;
using OuternetAWG.Services;
using OuternetAWG.ViewModels;

internal static class NativeSmoke
{
    [STAThread]
    public static int Main(string[] args)
    {
        if (args.Length != 1 || args[0] != "--native-smoke" || !OperatingSystem.IsWindows())
        {
            Console.Error.WriteLine("Use --native-smoke on Windows. The harness uses a fake service and never changes networking.");
            return 2;
        }
        AppBuilder.Configure<App>().UseWin32().UseSkia().SetupWithoutStarting();
        var result = 1;
        Dispatcher.UIThread.Post(async () =>
        {
            try { await VerifyAsync(); result = 0; }
            catch (Exception ex) { Console.Error.WriteLine(ex); }
            finally { Dispatcher.UIThread.InvokeShutdown(); }
        });
        Dispatcher.UIThread.MainLoop(CancellationToken.None);
        return result;
    }

    private static async Task VerifyAsync()
    {
        var service = new FakeService();
        var preferences = TestApp.Preferences(theme => Application.Current!.RequestedThemeVariant = theme);
        var window = new MainWindow(service, preferences) { ShowActivated = false };
        var exited = false;
        using var tray = new TrayController(window, () => exited = true);
        try
        {
            window.Show();
            await Task.Delay(500);
            var handle = window.TryGetPlatformHandle() ?? throw new InvalidOperationException("No native HWND.");
            Require(handle.HandleDescriptor == "HWND", "The smoke test must use a real Win32 window.");
            var style = GetWindowLong(handle.Handle, -16);
            var extendedStyle = GetWindowLong(handle.Handle, -20);
            Require((style & 0x00040000) != 0, "DWM needs the native resizable frame for the built-in window shadow.");
            Require((style & 0x00C00000) == 0x00C00000, "The underlying native frame must remain available to DWM.");
            Require((extendedStyle & 0x00080000) == 0, "The window must not use the layered-window shadow workaround.");
            Marshal.ThrowExceptionForHR(DwmIsCompositionEnabled(out var composition));
            Require(composition, "DWM composition is disabled on this host.");
            Marshal.ThrowExceptionForHR(DwmGetWindowAttribute(handle.Handle, 1, out var nonClientRendering, sizeof(int)));
            Require(nonClientRendering != 0, "Native non-client rendering must be enabled.");
            var corners = 0;
            if (OperatingSystem.IsWindowsVersionAtLeast(10, 0, 22000))
            {
                Marshal.ThrowExceptionForHR(DwmGetWindowAttribute(handle.Handle, 33, out corners, sizeof(int)));
                Require(corners == 2, "DWM must own the rounded corners.");
            }
            VerifyLayout(window);
            SaveFrame(window, "native-connection.png");
            window.Width = 760;
            window.Height = 580;
            window.Model.ShowError("Synthetic service error used to check minimum-size layout.");
            await Task.Delay(150);
            VerifyLayout(window);
            SaveFrame(window, "native-connection-minimum.png");
            foreach (var theme in new[] { "light", "dark" })
            {
                window.Model.Navigate(ClientPage.Settings);
                window.FindControl<ComboBox>("ThemeSelector")!.SelectedItem = preferences.Themes.Single(x => x.Id == theme);
                window.FindControl<ComboBox>("LanguageSelector")!.SelectedItem = preferences.Languages.Single(x => x.Id == "zh-Hans");
                await Task.Delay(150);
                Require(window.ActualThemeVariant == (theme == "light" ? ThemeVariant.Light : ThemeVariant.Dark), "Native theme did not switch.");
                var color = ((ISolidColorBrush)window.FindControl<Border>("WindowSurface")!.Background!).Color;
                Require(color == Color.Parse(theme == "light" ? "#F8F7FA" : "#1B1921"), "Native colors did not switch.");
                Require(((NativeMenuItem)tray.Menu.Items[3]).Header == preferences.Text["Tray.Quit"], "Native tray was not translated.");
                ClientTests.AssertContained(window.FindControl<Border>("SettingsPage")!, window.FindControl<Grid>("ContentPanel")!);
                SaveFrame(window, $"native-settings-{theme}.png");
            }
            window.FindControl<ComboBox>("ThemeSelector")!.SelectedItem = preferences.Themes[0];
            Require(Application.Current!.RequestedThemeVariant == ThemeVariant.Default, "System theme must remain OS-controlled.");
            preferences.SelectedLanguage = preferences.Languages.Single(x => x.Id == "en");
            window.Model.Navigate(ClientPage.Connection);
            window.WindowState = WindowState.Maximized;
            await Task.Delay(100);
            Require(!window.FindControl<Border>("WindowOutline")!.IsVisible, "Maximized windows must not retain a rounded inset border.");
            window.WindowState = WindowState.Normal;
            await Task.Delay(100);
            window.Close();
            Require(!window.IsVisible, "Close must hide the window.");
            Require(!service.Commands.Contains("stop-service"), "Close must not stop the service.");
            window.ShowActivated = false;
            window.Show();
            Require(service.Commands.Count(x => x == "start-service") == 1, "Showing the UI must not create another polling/start loop.");
            Require(await tray.QuitAsync() && exited, "Tray Quit must complete.");
            Require(service.Commands.Last() == "service-stopped", "The service must be stopped before the UI exits.");
            Console.WriteLine(JsonSerializer.Serialize(new
            {
                NativeFrame = true, DwmComposition = composition, DwmRoundedCorners = corners,
                WindowScale = window.RenderScaling, FitsWithoutScroller = true,
                CloseHides = true, QuitStopsService = true, LiveThemeAndLanguage = true, RealServiceTouched = false
            }));
        }
        finally { window.CloseForShutdown(); }
    }

    private static void VerifyLayout(MainWindow window)
    {
        window.UpdateLayout();
        var parent = window.FindControl<Grid>("ContentPanel")!;
        ClientTests.AssertConnectionCentered(window);
        foreach (var name in new[] { "ConnectButton", "ProfileCard" })
        {
            var control = window.FindControl<Control>(name)!;
            var end = control.TranslatePoint(new Point(control.Bounds.Width, control.Bounds.Height), parent)!.Value;
            Require(end.X <= parent.Bounds.Width + 1 && end.Y <= parent.Bounds.Height + 1, $"{name} overflows the native content area.");
        }
    }

    private static void SaveFrame(MainWindow window, string name)
    {
        using var bitmap = new RenderTargetBitmap(new PixelSize((int)window.Bounds.Width, (int)window.Bounds.Height));
        bitmap.Render(window);
        bitmap.Save(Path.Combine(AppContext.BaseDirectory, name));
    }

    private static void Require(bool condition, string message)
    {
        if (!condition) throw new InvalidOperationException(message);
    }

    [DllImport("user32.dll", EntryPoint = "GetWindowLongW", SetLastError = true)]
    private static extern int GetWindowLong(IntPtr window, int index);
    [DllImport("dwmapi.dll")]
    private static extern int DwmIsCompositionEnabled([MarshalAs(UnmanagedType.Bool)] out bool enabled);
    [DllImport("dwmapi.dll")]
    private static extern int DwmGetWindowAttribute(IntPtr window, int attribute, out int value, int size);
}
