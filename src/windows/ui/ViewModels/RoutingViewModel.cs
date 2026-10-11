using System.ComponentModel;
using System.Text.Json;
using System.Text;
using OuternetAWG.Services;

namespace OuternetAWG.ViewModels;

public sealed class RoutingViewModel : INotifyPropertyChanged, IDisposable
{
    private readonly MainViewModel _owner;
    private RoutingState? _state;
    private string? _snapshot;
    public LocalizationService Text => _owner.Text;
    public event PropertyChangedEventHandler? PropertyChanged;
    public IReadOnlyList<PreferenceChoice> Profiles { get; private set; } = [];
    public PreferenceChoice? Selected => Profiles.FirstOrDefault(x => x.Id == _state?.Selected);
    public bool CanChange => _state is not null && _owner.CanImport;
    public bool CanEdit => CanChange && _state!.Profiles.Any(x => x.Id == _state.Selected);
    public bool HasNotice => _state is null || _owner.HasActiveTunnel;
    public string Notice => _state is null ? Text["Routing.Unavailable"] :
        _owner.HasActiveTunnel ? Text["Routing.DisconnectFirst"] : "";
    public RoutingProfile Current => _state?.Profiles.FirstOrDefault(x => x.Id == _state.Selected)
        ?? new RoutingProfile(_state?.Selected ?? "vpn", "", _state?.Selected ?? "vpn", []);

    public RoutingViewModel(MainViewModel owner)
    {
        _owner = owner;
        owner.PropertyChanged += OwnerChanged;
        Text.PropertyChanged += LanguageChanged;
    }

    internal void Synchronize(RoutingState? state)
    {
        if (state is not null && (state.Version != 1 || state.Profiles is null ||
            state.Selected is not ("vpn" or "direct") && !state.Profiles.Any(x => x.Id == state.Selected)))
            throw new ClientException("Error.Version");
        var snapshot = JsonSerializer.Serialize(state);
        if (snapshot == _snapshot) return;
        _snapshot = snapshot;
        _state = state;
        Relabel();
    }

    private void Relabel()
    {
        Profiles = _state is null ? [] :
            new[] { new PreferenceChoice("vpn", Text["Routing.vpn"]), new PreferenceChoice("direct", Text["Routing.direct"]) }
                .Concat(_state.Profiles.Select(x => new PreferenceChoice(x.Id, x.Name))).ToArray();
        Notify();
    }

    private void OwnerChanged(object? sender, PropertyChangedEventArgs e) => Notify();
    private void LanguageChanged(object? sender, PropertyChangedEventArgs e) => Relabel();
    private void Notify() => PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(string.Empty));

    public async Task SelectAsync(string id)
    {
        if (_state is null || id == _state.Selected) return;
        if (CanChange && Profiles.Any(x => x.Id == id))
            await _owner.SaveRoutingAsync(_state with { Selected = id });
        Notify();
    }

    public async Task<bool> SaveAsync(RoutingProfile profile)
    {
        if (!CanChange || _state is null) return false;
        var profiles = _state.Profiles.Where(x => x.Id != profile.Id).Append(profile).ToArray();
        return await _owner.SaveRoutingAsync(_state with { Selected = profile.Id, Profiles = profiles });
    }

    public async Task DeleteAsync()
    {
        if (!CanEdit || _state is null) return;
        await _owner.SaveRoutingAsync(_state with
        {
            Selected = "vpn",
            Profiles = _state.Profiles.Where(x => x.Id != _state.Selected).ToArray()
        });
    }

    public void Dispose()
    {
        _owner.PropertyChanged -= OwnerChanged;
        Text.PropertyChanged -= LanguageChanged;
    }
}

public sealed class RouteEditorViewModel : INotifyPropertyChanged, IDisposable
{
    private readonly MainViewModel _owner;
    private readonly string _id;
    private Exception? _error;
    private string _name, _rules;
    private PreferenceChoice _base;
    public LocalizationService Text => _owner.Text;
    public event PropertyChangedEventHandler? PropertyChanged;
    public IReadOnlyList<PreferenceChoice> Bases { get; }
    public string Title => Text[IsNew ? "Routing.NewTitle" : "Routing.EditTitle"];
    public bool IsNew { get; }
    public bool CanSave => _owner.Routing.CanChange;
    public string Error => _error is null ? _owner.Error : Text.Describe(_error);
    public bool HasError => _error is not null || _owner.HasError;
    public string Name { get => _name; set { _name = value; Notify(); } }
    public string Rules { get => _rules; set { _rules = value; Notify(); } }
    public PreferenceChoice Base { get => _base; set { if (value is not null) { _base = value; Notify(); } } }
    public string BaseHint => Text[Base.Id == "vpn" ? "Routing.VpnHint" : "Routing.DirectHint"];

    public RouteEditorViewModel(MainViewModel owner, bool create)
    {
        _owner = owner;
        IsNew = create;
        var profile = owner.Routing.Current;
        _id = create ? Guid.NewGuid().ToString("N") : profile.Id;
        _name = create ? "" : profile.Name;
        _rules = RoutingRules.Format(profile.Rules);
        Bases = new[] { "vpn", "direct" }.Select(x => new PreferenceChoice(x, Text["Routing." + x])).ToArray();
        _base = Bases.Single(x => x.Id == profile.Base);
        owner.PropertyChanged += Changed;
        Text.PropertyChanged += LanguageChanged;
    }

    public async Task<bool> SaveAsync()
    {
        _error = null;
        try
        {
            var name = Name.Trim();
            if (name.EnumerateRunes().Count() is < 1 or > 64 || name.Any(char.IsControl))
                throw new ClientException("Routing.InvalidName");
            return await _owner.Routing.SaveAsync(new(_id, name, Base.Id, RoutingRules.Parse(Rules)));
        }
        catch (ClientException ex) { _error = ex; return false; }
        finally { Notify(); }
    }

    private void Changed(object? sender, PropertyChangedEventArgs e) => Notify();
    private void LanguageChanged(object? sender, PropertyChangedEventArgs e)
    {
        foreach (var choice in Bases) choice.Relabel(Text["Routing." + choice.Id]);
        Notify();
    }
    private void Notify() => PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(string.Empty));
    public void Dispose()
    {
        _owner.PropertyChanged -= Changed;
        Text.PropertyChanged -= LanguageChanged;
    }
}
