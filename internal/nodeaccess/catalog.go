package nodeaccess

func skillCatalog() []SkillDefinition {
	return []SkillDefinition{
		{
			ID: "atlas-node-evidence", Version: "v0.6.3", Class: "evidence", Status: "auditable_retry",
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
		command("node.recovery_context", "node", "read_only", "读取恢复后的启动时间与最近启动历史", "Read boot time and recent boot history after recovery", "uptime -s · who -b · journalctl --list-boots"),
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

func alertEvidencePolicies() []AlertEvidencePolicy {
	return []AlertEvidencePolicy{
		{
			Category: "hardware_fault",
			IssueTypes: []string{
				"row_remap_failure", "uncorrectable_remapped_rows", "uncorrectable_remapped_rows_growth", "correctable_remapped_rows", "correctable_remapped_rows_growth",
				"correctable_remapped_rows_rapid_growth", "gpu_reset_required", "recent_uncorrected_ecc",
				"recent_xid_change", "gpu_temp_high", "gpu_temp_critical", "memory_temp_high",
				"memory_temp_critical", "pcie_replay_growth", "pcie_replay_spike",
				"pcie_link_width_degraded", "high_load_low_sm_clock",
			},
			Semantics: "event", CollectionTrigger: "immediate",
			Purpose: Text{ZH: "GPU 硬件规则事件出现时立即采集节点只读证据", EN: "Collect read-only node evidence immediately for GPU hardware rule events"},
		},
		{
			Category: "availability", IssueTypes: []string{"node_state"},
			Semantics: "condition", CollectionTrigger: "after_recovery",
			Purpose: Text{ZH: "节点 offline 期间等待；恢复为 up 后补采启动与故障时间窗证据", EN: "Wait while a node is offline, then collect boot and incident-window evidence after it returns up"},
		},
		{
			Category: "inventory", IssueTypes: []string{"gpu_state"},
			Semantics: "condition", CollectionTrigger: "observe_only",
			Purpose: Text{ZH: "资产冲突、UUID 未知等先保留状态证据，不自动登录节点", EN: "Retain state evidence for asset conflicts or unknown UUIDs without automatic node login"},
		},
		{
			Category: "data_quality", IssueTypes: []string{"target_health", "health_score_unknown", "metric_continuity"},
			Semantics: "condition", CollectionTrigger: "observe_only",
			Purpose: Text{ZH: "监控链路与数据质量异常先在平台内诊断，避免把采集器故障误当硬件故障", EN: "Diagnose monitoring and data-quality conditions in-platform before treating them as hardware faults"},
		},
		{
			Category: "access", IssueTypes: []string{"node_authentication"},
			Semantics: "condition", CollectionTrigger: "observe_only",
			Purpose: Text{ZH: "访问链路自身失败时不重复触发 SSH 证据采集", EN: "Do not recursively trigger SSH evidence collection when the access path itself has failed"},
		},
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
