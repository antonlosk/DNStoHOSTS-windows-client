@echo off
REM Build script for DNStoHOSTS
REM Optimization flags used:
REM -H=windowsgui : Hide console window
REM -s : Disable symbol table
REM -w : Disable DWARF generation
REM -trimpath : Remove all file system paths from the resulting executable

echo Building optimized executable...

REM Initialize go.mod if it does not exist
if not exist go.mod (
    go mod init DNStoHOSTS
)
go mod tidy

REM Generate resource file for the icon
go run github.com/akavel/rsrc@latest -arch amd64 -ico icon.ico -o rsrc.syso

REM Build the executable. IMPORTANT: use a dot (.) to include rsrc.syso in the build!
go build -ldflags="-H=windowsgui -s -w" -trimpath -o DNStoHOSTS.exe .

if %errorlevel% equ 0 (
    echo Build successful: DNStoHOSTS.exe has been created.
) else (
    echo Build failed!
)
pause