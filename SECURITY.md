# 安全策略 / Security Policy

如果你发现了安全漏洞，请**不要**公开提交 Issue，而是通过 GitHub 的
[私密漏洞报告](https://github.com/DeepFal/PortRelay/security/advisories/new) 提交，
我们会尽快确认并修复。

If you find a security vulnerability, please do **not** open a public issue.
Report it privately via [GitHub Security Advisories](https://github.com/DeepFal/PortRelay/security/advisories/new) instead.

已知限制（不视为漏洞）/ Known limitations (not considered vulnerabilities):

- Agent 与网关之间的链路未加密，请运行在可信网络或加密组网之上。
  The agent ↔ gateway link is not encrypted; run it over a trusted network or an encrypted overlay.
- 授权以来源 IP 为准，同一 NAT 出口后的设备共享授权。
  Grants are tied to the source IP; devices behind the same NAT share a grant.
