using Avalonia.Controls;
using Avalonia.Interactivity;
using OuternetAWG.ViewModels;

namespace OuternetAWG;

public sealed partial class RouteProfileWindow : Window
{
    public RouteProfileWindow()
    {
        InitializeComponent();
    }

    public RouteProfileWindow(MainViewModel owner, bool create) : this()
    {
        var model = new RouteEditorViewModel(owner, create);
        DataContext = model;
        Closed += (_, _) => model.Dispose();
    }

    private void CancelClicked(object? sender, RoutedEventArgs e) => Close(false);
    private async void SaveClicked(object? sender, RoutedEventArgs e)
    {
        if (DataContext is RouteEditorViewModel model && await model.SaveAsync()) Close(true);
    }
}
