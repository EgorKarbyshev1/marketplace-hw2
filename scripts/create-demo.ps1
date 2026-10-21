$ErrorActionPreference = 'Stop'
$securePassword = Read-Host 'Password for local demo accounts (8-72 UTF-8 bytes)' -AsSecureString
$env:DEMO_USER_PASSWORD = [System.Net.NetworkCredential]::new('', $securePassword).Password
try {
    foreach ($role in @('USER','SELLER','ADMIN')) {
        $email = $role.ToLower() + '@local.test'
        docker compose exec -T -e ALLOW_DEMO_USERS=true -e DEMO_USER_PASSWORD api ./create-user -email $email -role $role
        if ($LASTEXITCODE -ne 0) { throw 'Could not create account; existing accounts are not overwritten' }
    }
} finally {
    Remove-Item Env:DEMO_USER_PASSWORD -ErrorAction SilentlyContinue
}
