# Windows service

The agent is a single `dsui-agent.exe`.
Run it as a Windows service with the built-in `sc.exe`, or with NSSM if you want automatic log files.

## With NSSM

```powershell
# 1. Download dsui-agent_<version>_windows_amd64.zip from the GitHub release and unzip it to C:\dsui-agent
# 2. Save the token (from DSUI) to a file only administrators can read:
Set-Content -Path C:\dsui-agent\token -Value "<token>" -NoNewline
icacls C:\dsui-agent\token /inheritance:r /grant:r "Administrators:R" "SYSTEM:R"

# 3. Verify the configuration:
C:\dsui-agent\dsui-agent.exe check --url https://dsui.example.com --token-file C:\dsui-agent\token --targets "airflow.corp.local:443"

# 4. Install and start the service:
nssm install dsui-agent C:\dsui-agent\dsui-agent.exe run --url https://dsui.example.com --token-file C:\dsui-agent\token --targets "airflow.corp.local:443" --log-format json
nssm set dsui-agent AppStdout C:\dsui-agent\agent.log
nssm set dsui-agent AppStderr C:\dsui-agent\agent.log
nssm set dsui-agent AppEnvironmentExtra HTTPS_PROXY=http://proxy.corp.local:3128
nssm start dsui-agent
```

Use `AppEnvironmentExtra` for `HTTPS_PROXY` and `NO_PROXY` when the machine only reaches the internet through a corporate proxy.
If the proxy re-signs TLS, export its root certificate as PEM and pass `--ca-file C:\dsui-agent\corp-ca.pem`.
