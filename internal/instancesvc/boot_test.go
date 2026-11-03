package instancesvc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ocicore/internal/ociclient"
)

func bootTestClient(t *testing.T, attachments string) *ociclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/bootVolumeAttachments"), strings.HasSuffix(r.URL.Path, "/bootVolumeAttachments/"):
			_, _ = w.Write([]byte(attachments))
		case strings.HasSuffix(r.URL.Path, "/bootVolumes/bv-now"):
			_, _ = w.Write([]byte(`{"id":"bv-now","sizeInGBs":50,"vpusPerGB":10,"lifecycleState":"AVAILABLE"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(srv.URL)
	client, err := ociclient.New(&ociclient.Credentials{
		TenancyOCID: "ocid1.tenancy.oc1..t", UserOCID: "ocid1.user.oc1..u",
		Fingerprint: ociclient.FingerprintOf(&key.PublicKey), Region: "eu-frankfurt-1", PrivateKey: key,
	}, ociclient.WithHTTPClient(&http.Client{Transport: redirectTransport{target}}))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestFindBootVolumeUsesAttachedOnly 锁住只认 ATTACHED 的挂载关系。
// OCI 会把分离过的历史关系一并列出，认错了会把别的卷的容量写到这台实例上。
func TestFindBootVolumeUsesAttachedOnly(t *testing.T) {
	client := bootTestClient(t, `[
		{"bootVolumeId":"bv-old","lifecycleState":"DETACHED"},
		{"bootVolumeId":"bv-now","lifecycleState":"ATTACHED"}]`)

	vol, err := findBootVolume(context.Background(), client, "eu-frankfurt-1", "c", "AD-2", "i-1")
	if err != nil {
		t.Fatal(err)
	}
	if vol == nil || vol.ID != "bv-now" || vol.SizeInGBs != 50 || vol.VpusPerGB != 10 {
		t.Fatalf("应取到 bv-now（50 GB / VPU 10），实际 %+v", vol)
	}
}

// 实例刚落定时挂载关系可能还没就绪：返回 nil 让调用方重试，不能当成出错。
func TestFindBootVolumeNotAttachedYet(t *testing.T) {
	client := bootTestClient(t, `[{"bootVolumeId":"bv-now","lifecycleState":"ATTACHING"}]`)

	vol, err := findBootVolume(context.Background(), client, "eu-frankfurt-1", "c", "AD-2", "i-1")
	if err != nil || vol != nil {
		t.Fatalf("还没挂好时应返回 nil, nil，实际 %+v, %v", vol, err)
	}
}
