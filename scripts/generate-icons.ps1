# Render the native Echo mark for PWA launchers. No downloaded assets required.
Add-Type -AssemblyName System.Drawing

function Paint-RoundedBar($graphics, $brush, $x, $y, $width, $height, $radius) {
    $shape = [System.Drawing.Drawing2D.GraphicsPath]::new()
    try {
        $diameter = [single]($radius * 2)
        $shape.AddArc([single]$x, [single]$y, $diameter, $diameter, 180, 90)
        $shape.AddArc([single]($x + $width - $diameter), [single]$y, $diameter, $diameter, 270, 90)
        $shape.AddArc([single]($x + $width - $diameter), [single]($y + $height - $diameter), $diameter, $diameter, 0, 90)
        $shape.AddArc([single]$x, [single]($y + $height - $diameter), $diameter, $diameter, 90, 90)
        $shape.CloseFigure()
        $graphics.FillPath($brush, $shape)
    } finally { $shape.Dispose() }
}

$assetRoot = Join-Path (Split-Path -Parent $PSScriptRoot) 'web'
foreach ($iconSize in @(192, 512)) {
    $bitmap = [System.Drawing.Bitmap]::new($iconSize, $iconSize)
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    $brush = [System.Drawing.SolidBrush]::new([System.Drawing.ColorTranslator]::FromHtml('#ffffff'))
    try {
        $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
        $graphics.Clear([System.Drawing.ColorTranslator]::FromHtml('#6252e8'))
        $scale = $iconSize / 64.0
        foreach ($bar in @(@(15,26,5,12), @(25,16,5,32), @(35,22,5,20), @(45,28,5,8))) {
            Paint-RoundedBar $graphics $brush ($bar[0]*$scale) ($bar[1]*$scale) ($bar[2]*$scale) ($bar[3]*$scale) (2.5*$scale)
        }
        $bitmap.Save((Join-Path $assetRoot "icon-$iconSize.png"), [System.Drawing.Imaging.ImageFormat]::Png)
    } finally {
        $brush.Dispose()
        $graphics.Dispose()
        $bitmap.Dispose()
    }
}
