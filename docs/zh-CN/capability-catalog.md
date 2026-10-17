# 能力目录（生成物，请勿手工编辑）

来源：`internal/capability` 策略表 + `tools/*.yaml` 的 `capability:` 清单。
重新生成：`make generate`。不一致时 `TestGeneratedCatalogIsUpToDate` 会失败。

| 身份 ID | 工具名 | 级别 | 运行时 | 权限 | 审批 | 声明能力（上限） |
|---|---|---|---|---|---|---|
| `core.amass` | `amass` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(amass) |
| `core.analyze_image` | `analyze_image` | readonly | go-builtin | agent:execute | inherited | fs.read(workspace) |
| `core.angr` | `angr` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(python3) |
| `core.api-schema-analyzer` | `api-schema-analyzer` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(spectral) |
| `core.arjun` | `arjun` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(arjun) |
| `core.arp-scan` | `arp-scan` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(arp-scan) |
| `core.batch_task_add_task` | `batch_task_add_task` | mutating | go-builtin | tasks:write | inherited | — |
| `core.batch_task_create` | `batch_task_create` | mutating | go-builtin | tasks:write | inherited | — |
| `core.batch_task_delete` | `batch_task_delete` | destructive | go-builtin | tasks:delete | always | — |
| `core.batch_task_get` | `batch_task_get` | readonly | go-builtin | tasks:read | never | — |
| `core.batch_task_list` | `batch_task_list` | readonly | go-builtin | tasks:read | never | — |
| `core.batch_task_pause` | `batch_task_pause` | mutating | go-builtin | tasks:write | inherited | — |
| `core.batch_task_remove_task` | `batch_task_remove_task` | destructive | go-builtin | tasks:delete | always | — |
| `core.batch_task_rerun` | `batch_task_rerun` | mutating | go-builtin | tasks:write | always | — |
| `core.batch_task_schedule_enabled` | `batch_task_schedule_enabled` | mutating | go-builtin | tasks:write | inherited | — |
| `core.batch_task_start` | `batch_task_start` | mutating | go-builtin | tasks:write | always | — |
| `core.batch_task_update_metadata` | `batch_task_update_metadata` | mutating | go-builtin | tasks:write | inherited | — |
| `core.batch_task_update_schedule` | `batch_task_update_schedule` | mutating | go-builtin | tasks:write | inherited | — |
| `core.batch_task_update_task` | `batch_task_update_task` | mutating | go-builtin | tasks:write | inherited | — |
| `core.binwalk` | `binwalk` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(binwalk) |
| `core.bloodhound` | `bloodhound` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(bloodhound-python) |
| `core.c2_event` | `c2_event` | readonly | go-builtin | c2:read | inherited | — |
| `core.c2_file` | `c2_file` | readonly | go-builtin | c2:read | inherited | — |
| `core.c2_listener` | `c2_listener` | destructive | go-builtin | c2:write | always | net.bind(listener) |
| `core.c2_payload` | `c2_payload` | destructive | go-builtin | c2:write | always | process.exec(go build) |
| `core.c2_profile` | `c2_profile` | mutating | go-builtin | c2:write | inherited | — |
| `core.c2_session` | `c2_session` | mutating | go-builtin | c2:write | inherited | — |
| `core.c2_task` | `c2_task` | destructive | go-builtin | c2:write | always | c2.exec() |
| `core.c2_task_manage` | `c2_task_manage` | mutating | go-builtin | c2:write | inherited | — |
| `core.cancel_tool_execution` | `cancel_tool_execution` | mutating | go-builtin | monitor:write | inherited | — |
| `core.checkov` | `checkov` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(checkov) |
| `core.checksec` | `checksec` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(checksec) |
| `core.clair` | `clair` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(clair) |
| `core.cloudmapper` | `cloudmapper` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(cloudmapper) |
| `core.complete_asset_scan` | `complete_asset_scan` | mutating | go-builtin | asset:write | inherited | — |
| `core.create_asset` | `create_asset` | mutating | go-builtin | asset:write | inherited | — |
| `core.dalfox` | `dalfox` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(dalfox) |
| `core.delete_asset` | `delete_asset` | destructive | go-builtin | asset:delete | always | — |
| `core.deprecate_project_fact` | `deprecate_project_fact` | mutating | go-builtin | project:write | inherited | — |
| `core.dirsearch` | `dirsearch` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(dirsearch) |
| `core.dnsenum` | `dnsenum` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(dnsenum) |
| `core.dnslog` | `dnslog` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(python3) |
| `core.dotdotpwn` | `dotdotpwn` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(dotdotpwn) |
| `core.edit_file` | `edit_file` | mutating | go-builtin | agent:local-execute | inherited | fs.write(workspace) |
| `core.enum4linux-ng` | `enum4linux-ng` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(enum4linux-ng) |
| `core.exec` | `exec` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(sh) |
| `core.execute` | `execute` | destructive | go-builtin | agent:local-execute | inherited | process.exec(*) |
| `core.execute-python-script` | `execute-python-script` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(/bin/bash) |
| `core.exiftool` | `exiftool` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(exiftool) |
| `core.falco` | `falco` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(falco) |
| `core.feroxbuster` | `feroxbuster` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(feroxbuster) |
| `core.ffuf` | `ffuf` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(ffuf) |
| `core.fierce` | `fierce` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(fierce) |
| `core.fofa_search` | `fofa_search` | readonly | recipe:exec | agent:execute | never | net.connect(target), process.exec(python3) |
| `core.foremost` | `foremost` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(foremost) |
| `core.fscan` | `fscan` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(fscan) |
| `core.gau` | `gau` | readonly | recipe:exec | agent:execute | never | net.connect(target), process.exec(gau) |
| `core.gdb` | `gdb` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(gdb) |
| `core.get_asset` | `get_asset` | readonly | go-builtin | asset:read | never | — |
| `core.get_project_fact` | `get_project_fact` | readonly | go-builtin | project:read | never | — |
| `core.get_tool_execution` | `get_tool_execution` | readonly | go-builtin | monitor:read | never | — |
| `core.get_vulnerability` | `get_vulnerability` | readonly | go-builtin | vulnerability:read | never | — |
| `core.ghidra` | `ghidra` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(analyzeHeadless) |
| `core.glob` | `glob` | readonly | go-builtin | agent:local-execute | never | fs.read(workspace) |
| `core.gobuster` | `gobuster` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(gobuster) |
| `core.graphql-scanner` | `graphql-scanner` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(graphqlmap) |
| `core.grep` | `grep` | readonly | go-builtin | agent:local-execute | never | fs.read(workspace) |
| `core.hashcat` | `hashcat` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(hashcat) |
| `core.hashpump` | `hashpump` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(hashpump) |
| `core.http-framework-test` | `http-framework-test` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(python3) |
| `core.hydra` | `hydra` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(hydra) |
| `core.impacket` | `impacket` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(python3) |
| `core.install-python-package` | `install-python-package` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(/bin/bash) |
| `core.jaeles` | `jaeles` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(jaeles) |
| `core.john` | `john` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(john) |
| `core.jwt-analyzer` | `jwt-analyzer` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(jwt_tool) |
| `core.katana` | `katana` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(katana) |
| `core.kube-bench` | `kube-bench` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(kube-bench) |
| `core.kube-hunter` | `kube-hunter` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(kube-hunter) |
| `core.libc-database` | `libc-database` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(python3) |
| `core.lightx` | `lightx` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(lightx) |
| `core.linpeas` | `linpeas` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(linpeas.sh) |
| `core.list_knowledge_risk_types` | `list_knowledge_risk_types` | readonly | go-builtin | knowledge:read | never | — |
| `core.list_project_facts` | `list_project_facts` | readonly | go-builtin | project:read | never | — |
| `core.list_vulnerabilities` | `list_vulnerabilities` | readonly | go-builtin | vulnerability:read | never | — |
| `core.ls` | `ls` | readonly | go-builtin | agent:local-execute | never | fs.read(workspace) |
| `core.manage_webshell_add` | `manage_webshell_add` | mutating | go-builtin | webshell:write | inherited | — |
| `core.manage_webshell_delete` | `manage_webshell_delete` | destructive | go-builtin | webshell:delete | always | — |
| `core.manage_webshell_list` | `manage_webshell_list` | readonly | go-builtin | webshell:read | never | — |
| `core.manage_webshell_test` | `manage_webshell_test` | mutating | go-builtin | webshell:write | inherited | net.connect(target) |
| `core.manage_webshell_update` | `manage_webshell_update` | mutating | go-builtin | webshell:write | inherited | — |
| `core.masscan` | `masscan` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(masscan) |
| `core.mcp_external_execute` | `mcp:external:execute` | mutating | mcp:remote | mcp:external:execute | inherited | — |
| `core.metasploit` | `metasploit` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(python3) |
| `core.msfvenom` | `msfvenom` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(msfvenom) |
| `core.nbtscan` | `nbtscan` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(nbtscan) |
| `core.netexec` | `netexec` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(netexec) |
| `core.nikto` | `nikto` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(nikto) |
| `core.nmap` | `nmap` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(nmap) |
| `core.nuclei` | `nuclei` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(nuclei) |
| `core.objdump` | `objdump` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(objdump) |
| `core.one-gadget` | `one-gadget` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(one_gadget) |
| `core.pacu` | `pacu` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(pacu) |
| `core.paramspider` | `paramspider` | readonly | recipe:exec | agent:execute | never | net.connect(target), process.exec(paramspider) |
| `core.prowler` | `prowler` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(prowler) |
| `core.pwninit` | `pwninit` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(pwninit) |
| `core.pwntools` | `pwntools` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(python3) |
| `core.quake_search` | `quake_search` | readonly | recipe:exec | agent:execute | never | net.connect(target), process.exec(python3) |
| `core.query_assets` | `query_assets` | readonly | go-builtin | asset:read | never | — |
| `core.query_execution_result` | `query_execution_result` | readonly | go-internal | agent:execute | never | db.query(executions) |
| `core.radare2` | `radare2` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(r2) |
| `core.read_file` | `read_file` | readonly | go-builtin | agent:local-execute | never | fs.read(workspace) |
| `core.record_vulnerability` | `record_vulnerability` | mutating | go-builtin | vulnerability:write | inherited | — |
| `core.responder` | `responder` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(python3) |
| `core.restore_project_fact` | `restore_project_fact` | mutating | go-builtin | project:write | inherited | — |
| `core.ropgadget` | `ropgadget` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(ROPgadget) |
| `core.ropper` | `ropper` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(ropper) |
| `core.rpcclient` | `rpcclient` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(python3) |
| `core.rustscan` | `rustscan` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(rustscan) |
| `core.scout-suite` | `scout-suite` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(scout) |
| `core.search_knowledge_base` | `search_knowledge_base` | readonly | go-builtin | knowledge:read | never | — |
| `core.search_project_facts` | `search_project_facts` | readonly | go-builtin | project:read | never | — |
| `core.shodan_search` | `shodan_search` | readonly | recipe:exec | agent:execute | never | net.connect(target), process.exec(python3) |
| `core.smbmap` | `smbmap` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(smbmap) |
| `core.sqlmap` | `sqlmap` | destructive | recipe:exec | agent:destructive-execute | always | fs.read(*), process.exec(sqlmap) |
| `core.steghide` | `steghide` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(steghide) |
| `core.strings` | `strings` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(strings) |
| `core.subfinder` | `subfinder` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(subfinder) |
| `core.terrascan` | `terrascan` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(terrascan) |
| `core.trivy` | `trivy` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(trivy) |
| `core.update_asset` | `update_asset` | mutating | go-builtin | asset:write | inherited | — |
| `core.update_vulnerability_status` | `update_vulnerability_status` | mutating | go-builtin | vulnerability:write | inherited | — |
| `core.upsert_project_fact` | `upsert_project_fact` | mutating | go-builtin | project:write | inherited | — |
| `core.virustotal_search` | `virustotal_search` | readonly | recipe:exec | agent:execute | never | net.connect(target), process.exec(python3) |
| `core.volatility3` | `volatility3` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(volatility3) |
| `core.wafw00f` | `wafw00f` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(wafw00f) |
| `core.wait_tool_execution` | `wait_tool_execution` | readonly | go-builtin | monitor:read | inherited | — |
| `core.waybackurls` | `waybackurls` | readonly | recipe:exec | agent:execute | never | net.connect(target), process.exec(waybackurls) |
| `core.webshell_exec` | `webshell_exec` | destructive | go-builtin | webshell:write | always | c2.exec() |
| `core.webshell_file_list` | `webshell_file_list` | readonly | go-builtin | webshell:read | inherited | — |
| `core.webshell_file_read` | `webshell_file_read` | readonly | go-builtin | webshell:read | inherited | — |
| `core.webshell_file_write` | `webshell_file_write` | destructive | go-builtin | webshell:write | always | c2.exec() |
| `core.wpscan` | `wpscan` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(wpscan) |
| `core.write_file` | `write_file` | mutating | go-builtin | agent:local-execute | inherited | fs.write(workspace) |
| `core.x8` | `x8` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(x8) |
| `core.xsser` | `xsser` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(xsser) |
| `core.xxd` | `xxd` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(xxd) |
| `core.zap` | `zap` | mutating | recipe:exec | agent:local-execute | inherited | net.connect(target), process.exec(zap-cli) |
| `core.zoomeye_search` | `zoomeye_search` | readonly | recipe:exec | agent:execute | never | net.connect(target), process.exec(python3) |
| `core.zsteg` | `zsteg` | readonly | recipe:exec | agent:execute | never | fs.read(workspace), process.exec(zsteg) |
