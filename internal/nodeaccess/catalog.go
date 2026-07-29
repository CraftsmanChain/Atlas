package nodeaccess

func skillCatalog() []SkillDefinition {
	return []SkillDefinition{
		{
			ID: "atlas-node-evidence", Version: "v0.4.3", Class: "evidence", Status: "open_readonly_collection",
			Purpose: Text{ZH: "按注册命令、固定参数和资源预算采集节点只读证据", EN: "Collect read-only node evidence through registered commands, fixed parameters, and resource budgets"},
		},
		{
			ID: "atlas-fault-analysis", Version: "v0.1.0", Class: "analysis", Status: "contract_ready",
			Purpose: Text{ZH: "基于证据引用生成结构化故障分析报告与待验证假设", EN: "Generate structured fault reports and testable hypotheses with evidence references"},
		},
		{
			ID: "atlas-case-learning", Version: "v0.1.0", Class: "learning", Status: "contract_ready",
			Purpose: Text{ZH: "将人工确认案例整理为脱敏、可审计的训练与评估数据", EN: "Curate confirmed cases into redacted, auditable training and evaluation data"},
		},
	}
}

func commandCatalog() []CommandDefinition {
	return []CommandDefinition{
		command("node.identity", "node", "read_only", "读取主机名、内核和操作系统版本", "Read hostname, kernel, and operating-system version", "hostname · uname · /etc/os-release"),
		command("node.cpu", "node", "read_only", "读取 CPU 拓扑与能力", "Read CPU topology and capabilities", "lscpu · /proc/cpuinfo (registered fields)"),
		command("node.uptime", "node", "read_only", "读取运行时间和系统负载", "Read uptime and system load", "uptime · /proc/loadavg"),
		command("node.memory", "node", "read_only", "读取内存统计", "Read memory statistics", "/proc/meminfo"),
		command("node.storage", "node", "read_only", "读取块设备与文件系统容量状态", "Read block-device and filesystem capacity state", "lsblk (registered fields) · df -P"),
		command("node.network", "node", "read_only", "读取网卡、地址和链路状态", "Read network interfaces, addresses, and link state", "ip -brief link · ip -brief address"),
		command("node.kernel_parameters", "node", "read_only", "读取注册的内核参数", "Read registered kernel parameters", "/proc/sys/<registered keys>"),
		command("gpu.inventory", "gpu", "read_only", "读取 GPU 身份清单", "Read GPU identity inventory", "nvidia-smi -L"),
		command("gpu.driver", "gpu", "read_only", "读取 NVIDIA 驱动和 CUDA 兼容信息", "Read NVIDIA driver and CUDA compatibility information", "nvidia-smi (registered driver fields)"),
		command("gpu.snapshot", "gpu", "read_only", "读取固定字段 GPU 状态快照", "Read a fixed-field GPU status snapshot", "nvidia-smi --query-gpu=<registered fields>"),
		command("gpu.topology", "gpu", "read_only", "读取 GPU/PCIe/NVLink 拓扑", "Read GPU, PCIe, and NVLink topology", "nvidia-smi topo -m"),
		command("pcie.inventory", "pcie", "read_only", "读取 PCI 设备身份", "Read PCI device identity", "lspci -Dnn"),
		command("service.state", "service", "read_only", "读取注册服务状态", "Read registered service state", "systemctl is-active/is-failed/show"),
		command("logs.kernel_window", "logs", "read_only", "读取限定时间窗内核日志", "Read kernel logs in a bounded incident window", "journalctl -k --since/--until --no-pager -n"),
		command("logs.service_window", "logs", "read_only", "读取限定服务和时间窗日志", "Read registered service logs in a bounded window", "journalctl -u <registered> --since/--until --no-pager -n"),
		command("bmc.sensor_read", "bmc", "read_only", "读取 BMC 传感器", "Read BMC sensors", "registered BMC read adapter"),
		command("bmc.sel_read", "bmc", "read_only", "读取限定数量 SEL", "Read a bounded number of SEL entries", "registered BMC SEL read adapter"),
		command("diagnostic.dcgm", "diagnostic", "approval_required", "执行 DCGM 诊断", "Run a DCGM diagnostic", "dcgmi diag <approved level>"),
		command("diagnostic.benchmark", "diagnostic", "approval_required", "执行性能、功能或压力验证", "Run performance, functional, or stress validation", "registered benchmark plan"),
		command("maintenance.service_restart", "maintenance", "approval_required", "重启注册服务", "Restart a registered service", "systemctl restart <approved service>"),
		command("maintenance.gpu_reset", "maintenance", "approval_required", "重置指定 GPU", "Reset an approved GPU", "nvidia-smi --gpu-reset"),
		command("maintenance.node_reboot", "maintenance", "approval_required", "重启节点", "Reboot a node", "reboot"),
		command("maintenance.workload", "maintenance", "approval_required", "终止、排空、隔离或操作 GPU 任务", "Terminate, drain, isolate, or change GPU workloads", "external approved workflow"),
	}
}

func command(id, category, approvalClass, zh, en, preview string) CommandDefinition {
	planningStatus := "automatic"
	collectionMode := "default_read_only"
	if approvalClass != "read_only" {
		planningStatus = "approval_required"
		collectionMode = "human_confirmation_required"
	}
	return CommandDefinition{ID: id, Category: category, ApprovalClass: approvalClass, PlanningStatus: planningStatus, CollectionMode: collectionMode, Purpose: Text{ZH: zh, EN: en}, Preview: preview}
}
