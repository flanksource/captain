package cli

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestPreviewsForByteDataMasksTextAndBinary(t *testing.T) {
	previews := previewsForByteData(map[string][]byte{
		"token":  []byte("sk-ant-api03-ABCDEFGH"),
		"binary": {0xff, 0xfe, 0xfd},
	})

	if len(previews) != 2 {
		t.Fatalf("got %d previews, want 2: %+v", len(previews), previews)
	}
	if previews[0].Key != "binary" || previews[0].Value != "3 bytes" {
		t.Fatalf("binary preview = %+v", previews[0])
	}
	if previews[1].Key != "token" || previews[1].Value != maskKey("sk-ant-api03-ABCDEFGH") {
		t.Fatalf("token preview = %+v", previews[1])
	}
}

func TestConfigMapKeysIncludeBinaryData(t *testing.T) {
	item := corev1.ConfigMap{
		Data:       map[string]string{"workspace": "/repo/workspace"},
		BinaryData: map[string][]byte{"cert": {0x01, 0x02}},
	}

	keys := sortedConfigMapKeys(item)
	if len(keys) != 2 || keys[0] != "cert" || keys[1] != "workspace" {
		t.Fatalf("keys = %+v", keys)
	}

	previews := previewsForConfigMap(item)
	if previews[0].Key != "cert" || previews[0].Value != "2 bytes" {
		t.Fatalf("cert preview = %+v", previews[0])
	}
	if previews[1].Key != "workspace" || previews[1].Value != maskKey("/repo/workspace") {
		t.Fatalf("workspace preview = %+v", previews[1])
	}
}
