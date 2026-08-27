$cred = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes("opsuser:OpsPass123!"))
$client = New-Object Net.Sockets.TcpClient([Net.Sockets.AddressFamily]::InterNetworkV6)
$client.Connect("::1", 20001)
$stream = $client.GetStream()

$req = "CONNECT 127.0.0.1:9998 HTTP/1.1`r`nProxy-Authorization: Basic $cred`r`n`r`n"
$bytes = [Text.Encoding]::UTF8.GetBytes($req)
$stream.Write($bytes, 0, $bytes.Length)
$stream.Flush()

$buf = New-Object byte[] 8192
$n1 = $stream.Read($buf, 0, 8192)
Write-Host "=== CONNECT response ==="
[Text.Encoding]::UTF8.GetString($buf, 0, $n1)

$get = "GET /health HTTP/1.1`r`nHost: 127.0.0.1:9998`r`nConnection: close`r`n`r`n"
$bytes = [Text.Encoding]::UTF8.GetBytes($get)
$stream.Write($bytes, 0, $bytes.Length)
$stream.Flush()

Start-Sleep -Milliseconds 500
$n2 = $stream.Read($buf, 0, 8192)
Write-Host "=== GET /health inside tunnel ==="
[Text.Encoding]::UTF8.GetString($buf, 0, $n2)
$client.Close()
