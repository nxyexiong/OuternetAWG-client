using System.Text.Json;

namespace OuternetAWG.Services;

public sealed record UserPreferences(string Theme = "system", string Language = "system")
{
    public void Validate()
    {
        if (Theme is not ("system" or "light" or "dark") ||
            Language != "system" && !LocalizationService.Languages.Contains(Language))
            throw new InvalidDataException("Unsupported theme or language in settings.json.");
    }
}

public interface IPreferencesStore
{
    UserPreferences Load();
    void Save(UserPreferences preferences);
}

public sealed class PreferencesStore(string path) : IPreferencesStore
{
    private static readonly JsonSerializerOptions Json = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        WriteIndented = true
    };

    public PreferencesStore() : this(Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "OuternetAWG", "settings.json")) { }

    public UserPreferences Load()
    {
        try
        {
            using var file = File.OpenRead(path);
            if (file.Length > 16 * 1024) throw new InvalidDataException("settings.json exceeds 16 KiB.");
            var preferences = JsonSerializer.Deserialize<UserPreferences>(file, Json)
                ?? throw new InvalidDataException("settings.json is empty.");
            preferences.Validate();
            return preferences;
        }
        catch (FileNotFoundException) { return new(); }
        catch (DirectoryNotFoundException) { return new(); }
    }

    public void Save(UserPreferences preferences)
    {
        preferences.Validate();
        var directory = Path.GetDirectoryName(Path.GetFullPath(path))!;
        Directory.CreateDirectory(directory);
        var temporary = Path.Combine(directory, $".settings-{Guid.NewGuid():N}.tmp");
        try
        {
            using (var file = new FileStream(temporary, FileMode.CreateNew, FileAccess.Write, FileShare.None))
            {
                JsonSerializer.Serialize(file, preferences, Json);
                file.Flush(flushToDisk: true);
            }
            File.Move(temporary, path, overwrite: true);
        }
        finally { if (File.Exists(temporary)) File.Delete(temporary); }
    }
}
