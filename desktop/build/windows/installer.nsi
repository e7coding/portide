; Portide 桌面端的 Windows 安装程序（M3b 设计 4）。由 scripts/build-desktop.sh 调用：
;   makensis -DVERSION=<版本> -DVERSION_NUM=<数字版本，例如 0.5.0> -DSRC=<含 portide-desktop.exe 的目录> -DOUT=<输出文件> installer.nsi
; 装到"程序文件\Portide"，开始菜单快捷方式，可选桌面快捷方式；卸载时保留数据目录（%APPDATA%\portide）。
; 界面有英文和简体中文：系统是中文时用中文，其余用英文（NSIS 按系统界面语言选，找不到时用第一个插入的英文）。
; 提示文字都放在文件末尾的 LangString 里，两种语言要一一对应。
Unicode true
!include "MUI2.nsh"
!include "x64.nsh"

Name "Portide"
OutFile "${OUT}"
InstallDir "$PROGRAMFILES64\Portide"
RequestExecutionLevel admin
SetCompressor /SOLID lzma

!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\Portide"
; WebView2 运行时（Evergreen）的注册表位置和下载地址。
!define WEBVIEW2_KEY "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
!define WEBVIEW2_URL "https://go.microsoft.com/fwlink/p/?LinkId=2124703"

!define MUI_ICON "icon.ico"
!define MUI_UNICON "icon.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\portide-desktop.exe"
!define MUI_FINISHPAGE_RUN_TEXT "$(RunPortide)"

!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "SimpChinese"

VIProductVersion "${VERSION_NUM}.0"
VIAddVersionKey /LANG=${LANG_ENGLISH} "ProductName" "Portide"
VIAddVersionKey /LANG=${LANG_ENGLISH} "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=${LANG_ENGLISH} "FileDescription" "Portide Setup"
VIAddVersionKey /LANG=${LANG_ENGLISH} "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=${LANG_ENGLISH} "LegalCopyright" "Portide"

Function .onInit
  ${IfNot} ${RunningX64}
    MessageBox MB_ICONSTOP "$(Only64Bit)"
    Abort
  ${EndIf}
  SetRegView 64
  ; InstallDirRegKey 是在 32 位注册表视图下读的，读不到装在 64 位视图里的键；这里自己在
  ; 64 位视图下读一遍，记住上一次安装选的目录（重装 / 升级时默认还是原来的位置）。
  ReadRegStr $0 HKLM "Software\Portide" "InstallDir"
  ${If} $0 != ""
    StrCpy $INSTDIR $0
  ${EndIf}
  ; 没有 WebView2 运行时的话界面打不开：提示下载地址，用户可以选择先装上 Portide。
  ReadRegStr $0 HKLM "${WEBVIEW2_KEY}" "pv"
  ${If} $0 == ""
    ReadRegStr $0 HKCU "Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  ${EndIf}
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    MessageBox MB_YESNO|MB_ICONEXCLAMATION "$(NoWebView2)" IDYES +2
    Abort
  ${EndIf}
FunctionEnd

Section "$(SecMainName)" SecMain
  SectionIn RO
  SetOutPath "$INSTDIR"
  SetOverwrite on

  ; Portide 退出到托盘后仍在运行，exe 文件被占用；覆盖安装 / 升级时 File 会失败并报一个
  ; 含糊的写入错误。这里检查错误，提示用户先从托盘退出，点"重试"即可继续，不强行结束进程。
  retry_exe:
  ClearErrors
  File "${SRC}\portide-desktop.exe"
  ${If} ${Errors}
    MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "$(RunningRetry)" IDRETRY retry_exe
    Abort
  ${EndIf}

  File "${SRC}\README.md"
  File "${SRC}\桌面端.md"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  ; 改名前（Portide）装过的快捷方式换成新名字的。
  Delete "$SMPROGRAMS\Portide.lnk"
  CreateShortcut "$SMPROGRAMS\Portide.lnk" "$INSTDIR\portide-desktop.exe"
  WriteRegStr HKLM "Software\Portide" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayName" "Portide"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\portide-desktop.exe"
  WriteRegStr HKLM "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKLM "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoRepair" 1
SectionEnd

Section /o "$(SecDesktopName)" SecDesktop
  Delete "$DESKTOP\Portide.lnk"
  CreateShortcut "$DESKTOP\Portide.lnk" "$INSTDIR\portide-desktop.exe"
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecMain} "$(SecMainDesc)"
  !insertmacro MUI_DESCRIPTION_TEXT ${SecDesktop} "$(SecDesktopDesc)"
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Function un.onInit
  SetRegView 64
FunctionEnd

; 卸载只删除程序和快捷方式，不碰数据目录：设备身份、设备圈和历史都还在，重新安装后接着用。
Section "Uninstall"
  ; Portide 退出到托盘后仍在运行时，exe 文件被占用删不掉：不强行结束进程，提示用户先从托盘
  ; 退出再重新卸载，并在删快捷方式和注册表项之前中止，避免留下一个删不掉的孤儿 exe。
  ClearErrors
  Delete "$INSTDIR\portide-desktop.exe"
  ${If} ${Errors}
    MessageBox MB_ICONSTOP "$(RunningUninstall)"
    Abort
  ${EndIf}
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\桌面端.md"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\Portide.lnk"
  Delete "$DESKTOP\Portide.lnk"
  Delete "$SMPROGRAMS\Portide.lnk"
  Delete "$DESKTOP\Portide.lnk"
  ; 设置里打开过"开机时启动"的话，去掉登记（值名见 desktop/platform.go 的 autostartID）。
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "portide-desktop"
  DeleteRegKey HKLM "${UNINST_KEY}"
  DeleteRegKey HKLM "Software\Portide"
SectionEnd

; 界面文字（英文、简体中文）。
LangString RunPortide ${LANG_ENGLISH} "Start Portide"
LangString RunPortide ${LANG_SIMPCHINESE} "启动 Portide"
LangString Only64Bit ${LANG_ENGLISH} "Portide requires 64-bit Windows."
LangString Only64Bit ${LANG_SIMPCHINESE} "Portide 只支持 64 位 Windows。"
LangString NoWebView2 ${LANG_ENGLISH} "This computer doesn't have the Microsoft Edge WebView2 Runtime, which Portide needs to show its window.$\n$\nDownload it from ${WEBVIEW2_URL}$\n$\nContinue installing Portide now?"
LangString NoWebView2 ${LANG_SIMPCHINESE} "这台电脑没有 Microsoft Edge WebView2 运行时，Portide 的界面需要它。$\n$\n请从 ${WEBVIEW2_URL} 下载安装。$\n$\n现在继续安装 Portide 吗？"
LangString SecMainName ${LANG_ENGLISH} "Portide (required)"
LangString SecMainName ${LANG_SIMPCHINESE} "Portide（必需）"
LangString SecMainDesc ${LANG_ENGLISH} "The Portide desktop app."
LangString SecMainDesc ${LANG_SIMPCHINESE} "Portide 桌面端程序。"
LangString SecDesktopName ${LANG_ENGLISH} "Desktop shortcut"
LangString SecDesktopName ${LANG_SIMPCHINESE} "桌面快捷方式"
LangString SecDesktopDesc ${LANG_ENGLISH} "Put a Portide shortcut on the desktop."
LangString SecDesktopDesc ${LANG_SIMPCHINESE} "在桌面上放一个 Portide 的快捷方式。"
LangString RunningRetry ${LANG_ENGLISH} "Portide is running. Choose Quit from its tray menu, then click Retry."
LangString RunningRetry ${LANG_SIMPCHINESE} "Portide 正在运行，请先从托盘菜单选择“退出”，然后点“重试”。"
LangString RunningUninstall ${LANG_ENGLISH} "Portide is running. Choose Quit from its tray menu, then uninstall again."
LangString RunningUninstall ${LANG_SIMPCHINESE} "Portide 正在运行，请先从托盘菜单选择“退出”，然后重新卸载。"
