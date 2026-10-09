#Requires -Version 5.1
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing
$destination = Join-Path (Split-Path -Parent $PSScriptRoot) 'ui\Assets\OuternetAWG.ico'
New-Item -ItemType Directory -Path (Split-Path -Parent $destination) -Force | Out-Null
$frames = [Collections.Generic.List[object]]::new()

foreach ($size in @(16, 24, 32, 48, 64, 128, 256)) {
    $large = [Drawing.Bitmap]::new($size * 4, $size * 4, [Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $canvas = [Drawing.Graphics]::FromImage($large)
    $outline = [Drawing.Drawing2D.GraphicsPath]::new()
    $purple = [Drawing.SolidBrush]::new([Drawing.ColorTranslator]::FromHtml('#7455DA'))
    $white = [Drawing.Pen]::new([Drawing.Color]::White, 3)
    $frame = [Drawing.Bitmap]::new($size, $size, [Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $scaled = [Drawing.Graphics]::FromImage($frame)
    $png = [IO.MemoryStream]::new()
    try {
        $canvas.Clear([Drawing.Color]::Transparent)
        $canvas.SmoothingMode = [Drawing.Drawing2D.SmoothingMode]::AntiAlias
        $canvas.ScaleTransform([single]($size * 4 / 64), [single]($size * 4 / 64))
        $outline.AddArc(2, 2, 32, 32, 180, 90)
        $outline.AddArc(30, 2, 32, 32, 270, 90)
        $outline.AddArc(30, 30, 32, 32, 0, 90)
        $outline.AddArc(2, 30, 32, 32, 90, 90)
        $outline.CloseFigure()
        $canvas.FillPath($purple, $outline)
        $canvas.DrawEllipse($white, 12, 12, 40, 40)
        $canvas.DrawLine($white, 12, 32, 52, 32)
        $canvas.DrawBezier($white, 32, 12, 18, 22, 18, 42, 32, 52)
        $canvas.DrawBezier($white, 32, 12, 46, 22, 46, 42, 32, 52)
        $scaled.InterpolationMode = [Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
        $scaled.PixelOffsetMode = [Drawing.Drawing2D.PixelOffsetMode]::HighQuality
        $scaled.DrawImage($large, [Drawing.Rectangle]::new(0, 0, $size, $size))
        $frame.Save($png, [Drawing.Imaging.ImageFormat]::Png)
        $frames.Add([pscustomobject]@{ Size = $size; Bytes = $png.ToArray() })
    }
    finally {
        $png.Dispose(); $scaled.Dispose(); $frame.Dispose(); $white.Dispose()
        $purple.Dispose(); $outline.Dispose(); $canvas.Dispose(); $large.Dispose()
    }
}
$file = [IO.File]::Create($destination)
$writer = [IO.BinaryWriter]::new($file)
try {
    $writer.Write([uint16]0); $writer.Write([uint16]1); $writer.Write([uint16]$frames.Count)
    $offset = 6 + 16 * $frames.Count
    foreach ($frame in $frames) {
        $dimension = if ($frame.Size -eq 256) { 0 } else { $frame.Size }
        $writer.Write([byte]$dimension); $writer.Write([byte]$dimension)
        $writer.Write([byte]0); $writer.Write([byte]0)
        $writer.Write([uint16]1); $writer.Write([uint16]32)
        $writer.Write([uint32]$frame.Bytes.Length); $writer.Write([uint32]$offset)
        $offset += $frame.Bytes.Length
    }
    foreach ($frame in $frames) { $writer.Write([byte[]]$frame.Bytes) }
}
finally { $writer.Dispose(); $file.Dispose() }
Write-Host "Generated shared icon: $destination"
