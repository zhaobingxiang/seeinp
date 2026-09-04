; seeinpc 安装脚本（Inno Setup 6）
; 编译：ISCC.exe seeinpc\installer\seeinpc.iss
; 说明：
;   - 需要管理员权限（虚拟网卡与路由操作）
;   - wintun.dll 已内嵌于 exe，首次运行时自动释放到 resource/ 并做哈希校验；
;     安装包同时随包携带一份以便离线校验对照
;   - 卸载时移除 SeeinpcVpn 虚拟网卡并清理路由

#define MyAppName "seeinpc"
#define MyAppVersion "1.0.26.0904.1"
#define MyAppPublisher "seeinp"
#define MyAppExeName "seeinpc.exe"
; 构建产物目录（相对本脚本）：..\..\release\seeinpc
#define SrcDir "..\..\release\seeinpc"

[Setup]
AppId={{479852F4-6330-4BFE-89D6-B7F3A6D33A9F}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
DefaultDirName={autopf}\seeinpc
DefaultGroupName=seeinpc
UninstallDisplayIcon={app}\{#MyAppExeName}
OutputDir=.\output
OutputBaseFilename=seeinpc-setup-{#MyAppVersion}
SetupIconFile=..\..\ico\seeinpc.ico
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
PrivilegesRequired=admin
ArchitecturesInstallIn64BitMode=x64
DisableProgramGroupPage=yes
; 安装前若程序在运行则先关闭
CloseApplications=yes
RestartApplications=no

[Languages]
Name: "chinesesimplified"; MessagesFile: "compiler:Languages\ChineseSimplified.isl"

[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加任务："

[Files]
Source: "{#SrcDir}\seeinpc.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SrcDir}\wintun.dll"; DestDir: "{app}\resource"; Flags: ignoreversion

[Icons]
Name: "{autodesktop}\seeinpc"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon
Name: "{group}\seeinpc"; Filename: "{app}\{#MyAppExeName}"

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "立即运行 seeinpc"; Flags: nowait postinstall skipifsilent

[UninstallRun]
; 移除 SeeinpcVpn 虚拟网卡（pnputil 按设备名匹配；不存在时静默跳过）
Filename: "powershell.exe"; Parameters: "-NoProfile -Command ""Get-PnpDevice -FriendlyName 'SeeinpcVpn*' -ErrorAction SilentlyContinue | ForEach-Object { pnputil /remove-device $_.InstanceId }"""; Flags: runhidden waituntilterminated

[UninstallDelete]
; 清理运行时目录（配置与日志；如需保留用户配置可注释本段）
Type: filesandordirs; Name: "{app}\conf"
Type: filesandordirs; Name: "{app}\logs"
Type: filesandordirs; Name: "{app}\resource"
Type: files; Name: "{app}\wintun.dll"
Type: files; Name: "{app}\seeinpc.lock"

[Code]
// 安装前结束正在运行的 seeinpc（确保文件可替换）
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ResultCode: Integer;
begin
  Exec('taskkill', '/F /IM seeinpc.exe', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Result := '';
end;
