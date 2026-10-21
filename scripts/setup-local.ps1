$path = Join-Path (Split-Path $PSScriptRoot -Parent) '.env'
if (Test-Path -LiteralPath $path) {
    Write-Output '.env already exists; no changes made'
    exit 0
}
$bytes = New-Object byte[] 48
$generator = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$generator.GetBytes($bytes)
$generator.Dispose()
$secret = [Convert]::ToBase64String($bytes)
[System.IO.File]::WriteAllText($path, "JWT_SECRET=$secret`n", [System.Text.UTF8Encoding]::new($false))
Write-Output 'Created local .env with a random JWT secret'
