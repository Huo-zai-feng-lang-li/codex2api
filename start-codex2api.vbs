Set ws = CreateObject("WScript.Shell")
ws.Run "cmd /c """ & Left(WScript.ScriptFullName, InStrRev(WScript.ScriptFullName, "\")) & "start-codex2api.bat""", 0, False
