using Avalonia.Controls;
using Avalonia.Platform;

namespace OuternetAWG.Services;

internal static class AppIcon
{
    public static WindowIcon Create()
    {
        using var stream = AssetLoader.Open(new Uri("avares://OuternetAWG/Assets/OuternetAWG.ico"));
        return new WindowIcon(stream);
    }
}
