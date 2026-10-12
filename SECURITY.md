# Security Policy

## Supported versions

| Version | |
|---------|---|
| 15.x | supported: fixes are released as updates of 15 |
| older | not supported: update to 15 |

## Reporting a vulnerability

**Do not report security vulnerabilities in public issues.**

Report privately through GitHub Security Advisories:
`https://github.com/4nonX/DPlaneOS/security/advisories/new`

Or write to the maintainer (see the GitHub profile). Please include:

1. What the vulnerability is
2. The affected part (daemon, web interface, system image, installer)
3. Steps to reproduce, as small as possible
4. What an attacker could achieve
5. A suggested fix, if you have one

### What to expect

DPlaneOS is maintained by one person. You will get an acknowledgement as soon as the report has been read, normally within a few days, and an assessment after it has been reproduced. Critical issues are fixed first and released as an update with a note in the changelog; you are credited there if you want to be.

### Safe harbour

No legal action will be pursued against researchers who:

- report privately before disclosing publicly,
- do not access, change or delete data beyond what is needed to show the issue,
- do not disrupt systems that are not their own,
- allow a reasonable time for a fix (30 to 90 days) before disclosure.

## What is defended, and what is not

DPlaneOS is an appliance for a network you control; its web interface is not meant to face the internet. The [Threat Model](docs/reference/THREAT-MODEL.md) describes the security design, and names what it does not defend against. The points most worth knowing:

- The daemon runs as root, and an administrator of the web interface is root on the machine.
- The system disk is not encrypted: the database, with the (encrypted) secrets of integrations and the file holding their key, is readable by whoever has the disk. Session tokens and the CHAP secrets of iSCSI initiators are stored unencrypted.
- Containers are confined only as far as their compose files confine them, and their published ports bypass the firewall page.
- NFS, NVMe over TCP, iSCSI without CHAP and plain FTP trust the network they are offered on.
