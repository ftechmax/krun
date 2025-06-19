## Build krunhelper.exe

```powershell
go build -C helper -o ../krunhelper.exe ; `
& "C:\Program Files (x86)\Windows Kits\10\bin\10.0.26100.0\x64\mt.exe" -manifest helper\helper.manifest -outputresource:krunhelper.exe;1
```
