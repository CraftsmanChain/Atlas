package gateway

import "testing"

func TestParseFeishuWebhookAlertText(t *testing.T) {
	body := []byte(`{
		"msg_type":"text",
		"content":{
			"text":"[atlas-alert]\nsource=prometheus\nlevel=critical\nhost=gpu-node-01\nlabels=gpu=3,cluster=train-a\nalertname=GPUHighTemperature\nnamespace=training\nmessage=GPU temperature is too high"
		}
	}`)

	event, err := parseFeishuWebhookAlert(body)
	if err != nil {
		t.Fatalf("parseFeishuWebhookAlert returned error: %v", err)
	}

	if event.Source != "prometheus" {
		t.Fatalf("expected source prometheus, got %q", event.Source)
	}
	if event.Level != "critical" {
		t.Fatalf("expected level critical, got %q", event.Level)
	}
	if event.Host != "gpu-node-01" {
		t.Fatalf("expected host gpu-node-01, got %q", event.Host)
	}
	if event.Message != "GPU temperature is too high" {
		t.Fatalf("expected message to be parsed, got %q", event.Message)
	}
	if event.Labels["gpu"] != "3" || event.Labels["cluster"] != "train-a" {
		t.Fatalf("expected inline labels to be parsed, got %#v", event.Labels)
	}
	if event.Labels["alertname"] != "GPUHighTemperature" || event.Labels["namespace"] != "training" {
		t.Fatalf("expected unknown fields to fallback into labels, got %#v", event.Labels)
	}
}

func TestParseFeishuWebhookAlertPost(t *testing.T) {
	body := []byte(`{
		"msg_type":"post",
		"content":{
			"post":{
				"zh_cn":{
					"title":"GPU Alert",
					"content":[
						[
							{"tag":"text","text":"[atlas-alert]"},
							{"tag":"text","text":"source=dcgm"}
						],
						[
							{"tag":"text","text":"message=ECC error count increased"}
						]
					]
				}
			}
		}
	}`)

	event, err := parseFeishuWebhookAlert(body)
	if err != nil {
		t.Fatalf("parseFeishuWebhookAlert returned error: %v", err)
	}

	if event.Source != "dcgm" {
		t.Fatalf("expected source dcgm, got %q", event.Source)
	}
	if event.Message == "" {
		t.Fatal("expected message to be extracted from post payload")
	}
}

func TestParseFeishuWebhookAlertPlainTextFallback(t *testing.T) {
	body := []byte(`{
		"msg_type":"text",
		"content":{
			"text":"GPU 3 temperature warning on gpu-node-01"
		}
	}`)

	event, err := parseFeishuWebhookAlert(body)
	if err != nil {
		t.Fatalf("parseFeishuWebhookAlert returned error: %v", err)
	}

	if event.Source != "feishu" {
		t.Fatalf("expected fallback source feishu, got %q", event.Source)
	}
	if event.Level != "info" {
		t.Fatalf("expected fallback level info, got %q", event.Level)
	}
	if event.Message != "GPU 3 temperature warning on gpu-node-01" {
		t.Fatalf("unexpected fallback message: %q", event.Message)
	}
}

func TestParseFeishuWebhookAlertLegacyChineseNetworkSample(t *testing.T) {
	body := []byte(`{
		"msg_type":"text",
		"content":{
			"text":"网络失活-外网\n级别状态:  次要 [啊？]Triggered\n告警名称: 网络失活-外网\n告警标签:\n\t - app: blackbox-exporter\n\t - device_type: WWXQ\n\t - ext: 10.111.101.4:9115\n\t - instance: 10.111.101.4\n\t - job: blackbox-icmp-wwxq\n\t - module: icmp\n\t - target: www.dingtalk.com\n触发时间: 2026-04-14 17:00:03\n发送时间: 2026-04-14 17:00:46\n触发时值:  1"
		}
	}`)

	event, err := parseFeishuWebhookAlert(body)
	if err != nil {
		t.Fatalf("parseFeishuWebhookAlert returned error: %v", err)
	}

	if event.Source != "alertmanager" {
		t.Fatalf("expected source alertmanager, got %q", event.Source)
	}
	if event.Level != "warning" {
		t.Fatalf("expected level warning, got %q", event.Level)
	}
	if event.Message != "网络失活-外网" {
		t.Fatalf("expected message 网络失活-外网, got %q", event.Message)
	}
	if event.Host != "10.111.101.4" {
		t.Fatalf("expected host 10.111.101.4, got %q", event.Host)
	}
	if event.Labels["target"] != "www.dingtalk.com" || event.Labels["device_type"] != "WWXQ" {
		t.Fatalf("expected labels to be parsed, got %#v", event.Labels)
	}
	if event.Labels["trigger_value"] != "1" {
		t.Fatalf("expected trigger_value 1, got %q", event.Labels["trigger_value"])
	}
}

func TestParseFeishuWebhookAlertLegacyChineseXIDSample(t *testing.T) {
	body := []byte(`{
		"msg_type":"text",
		"content":{
			"text":"XID故障-低优先级\n级别状态:  次要 [啊？]Triggered\n告警名称: XID故障-低优先级\n告警标签:\n\t - DCGM_FI_DRIVER_VERSION: 565.77\n\t - Hostname: 4090GPU-03\n\t - UUID: GPU-78b7fc57-0fe4-8cb9-d802-09c6f1cf4b99\n\t - device: nvidia3\n\t - device_type: RTX4090\n\t - err_code: 43\n\t - err_msg: GPU stopped processing\n\t - gpu: 3\n\t - instance: 10.114.4.23\n\t - modelName: NVIDIA GeForce RTX 4090\n\t - pci_bus_id: 00000000:61:00.0\n\t - suggestion: 尝试自行解决\n触发时间: 2026-03-21 14:18:51\n发送时间: 2026-03-21 14:19:05\n触发时值:  43"
		}
	}`)

	event, err := parseFeishuWebhookAlert(body)
	if err != nil {
		t.Fatalf("parseFeishuWebhookAlert returned error: %v", err)
	}

	if event.Level != "warning" {
		t.Fatalf("expected level warning, got %q", event.Level)
	}
	if event.Message != "XID故障-低优先级" {
		t.Fatalf("expected XID message, got %q", event.Message)
	}
	if event.Host != "4090GPU-03" {
		t.Fatalf("expected host Hostname label to win, got %q", event.Host)
	}
	if event.Labels["err_code"] != "43" || event.Labels["err_msg"] != "GPU stopped processing" {
		t.Fatalf("expected xid labels to be parsed, got %#v", event.Labels)
	}
	if event.Labels["severity_text"] != "次要" {
		t.Fatalf("expected cleaned severity_text 次要, got %q", event.Labels["severity_text"])
	}
}

func TestExtractFeishuHookToken(t *testing.T) {
	token := extractFeishuHookToken("/open-apis/bot/v2/hook/test-token")
	if token != "test-token" {
		t.Fatalf("expected token test-token, got %q", token)
	}
}
